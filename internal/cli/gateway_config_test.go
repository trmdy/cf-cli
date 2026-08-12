package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const gatewayConfigTestAccountID = "0123456789abcdef0123456789abcdef"

func runGatewayConfigCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", gatewayConfigTestAccountID,
	}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestGatewayConfigCommandsSendExpectedRequests(t *testing.T) {
	type request struct {
		method string
		path   string
		body   []byte
	}
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, request{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/locations"):
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"location-id","name":"office","client_default":true,"ecs_support":false,"max_ttl":{"mode":"inherit"}}]}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/locations/location-id"):
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"location-id","name":"office","created_at":"2026-01-01T00:00:00Z","unknown_writable":"keep","max_ttl":{"mode":"inherit"}}}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/configuration"):
			_, _ = w.Write([]byte(`{"success":true,"result":{"created_at":"2026-01-01T00:00:00Z","settings":{"tls_decrypt":{"enabled":false,"unknown_writable":"keep"},"block_page":{"enabled":true,"read_only":true,"source_account":"source","version":2}}}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		}
	}))
	defer srv.Close()

	commands := [][]string{
		{"gateway", "config", "locations", "list"},
		{"gateway", "config", "locations", "get", "location-id"},
		{"gateway", "config", "locations", "create", "branch", "--network", "192.0.2.0/24", "--ecs-support", "--max-ttl-mode", "override", "--max-ttl-secs", "3600"},
		{"gateway", "config", "locations", "update", "location-id", "--client-default=false", "--max-ttl-mode", "disabled"},
		{"gateway", "config", "locations", "delete", "location-id", "--force"},
		{"gateway", "config", "settings", "get"},
		{"gateway", "config", "settings", "set", "--tls-decrypt", "--block-page=false", "--antivirus-download"},
		{"gateway", "config", "certificates", "list"},
		{"gateway", "config", "certificates", "activate", "certificate-id"},
	}
	for _, args := range commands {
		if _, _, err := runGatewayConfigCLI(t, srv.URL, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	if len(requests) != 11 { // location/settings updates each read before writing.
		t.Fatalf("requests = %d, want 11: %#v", len(requests), requests)
	}
	base := "/accounts/" + gatewayConfigTestAccountID + "/gateway"
	wantMethodsAndPaths := [][2]string{
		{"GET", base + "/locations"},
		{"GET", base + "/locations/location-id"},
		{"POST", base + "/locations"},
		{"GET", base + "/locations/location-id"},
		{"PUT", base + "/locations/location-id"},
		{"DELETE", base + "/locations/location-id"},
		{"GET", base + "/configuration"},
		{"GET", base + "/configuration"},
		{"PUT", base + "/configuration"},
		{"GET", base + "/certificates"},
		{"POST", base + "/certificates/certificate-id/activate"},
	}
	for i, want := range wantMethodsAndPaths {
		if got := requests[i]; got.method != want[0] || got.path != want[1] {
			t.Errorf("request %d = %#v, want %s %s", i, got, want[0], want[1])
		}
	}
	gatewayConfigAssertJSONEqual(t, requests[2].body, `{"name":"branch","networks":[{"network":"192.0.2.0/24"}],"ecs_support":true,"max_ttl":{"mode":"override","ttl_secs":3600}}`)
	gatewayConfigAssertJSONEqual(t, requests[4].body, `{"name":"office","unknown_writable":"keep","client_default":false,"max_ttl":{"mode":"disabled"}}`)
	gatewayConfigAssertJSONEqual(t, requests[5].body, `{}`)
	gatewayConfigAssertJSONEqual(t, requests[8].body, `{"settings":{"tls_decrypt":{"enabled":true,"unknown_writable":"keep"},"block_page":{"enabled":false},"antivirus":{"enabled_download_phase":true}}}`)
	gatewayConfigAssertJSONEqual(t, requests[10].body, `{}`)
}

func TestGatewayConfigSettingsSetDryRunReadsThenDumpsPut(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/configuration") {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"settings":{"activity_log":{"enabled":false,"unknown_writable":"keep"}}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runGatewayConfigCLI(t, srv.URL, "gateway", "config", "settings", "set", "--activity-log", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want one read", requests)
	}
	var dump struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dry-run output: %v\n%s", err, stdout)
	}
	if dump.Method != "PUT" {
		t.Fatalf("method = %q, want PUT", dump.Method)
	}
	gatewayConfigAssertJSONEqual(t, dump.Body, `{"settings":{"activity_log":{"enabled":true,"unknown_writable":"keep"}}}`)
}

func TestGatewayConfigSettingsBrowserIsolationAndClearRequests(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		current  string
		wantBody string
	}{
		{
			name:     "browser isolation true",
			args:     []string{"gateway", "config", "settings", "set", "--browser-isolation"},
			current:  `{"settings":{"browser_isolation":{"url_browser_isolation_enabled":false,"unknown_writable":"keep"}}}`,
			wantBody: `{"settings":{"browser_isolation":{"url_browser_isolation_enabled":true,"unknown_writable":"keep"}}}`,
		},
		{
			name:     "browser isolation false",
			args:     []string{"gateway", "config", "settings", "set", "--browser-isolation=false"},
			current:  `{"settings":{"browser_isolation":{"url_browser_isolation_enabled":true,"unknown_writable":"keep"}}}`,
			wantBody: `{"settings":{"browser_isolation":{"url_browser_isolation_enabled":false,"unknown_writable":"keep"}}}`,
		},
		{
			name:     "clear max ttl",
			args:     []string{"gateway", "config", "settings", "set", "--clear-max-ttl"},
			current:  `{"settings":{"max_ttl_secs":3600}}`,
			wantBody: `{"settings":{"max_ttl_secs":null}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var putBody []byte
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				switch r.Method {
				case "GET":
					if !strings.HasSuffix(r.URL.Path, "/configuration") {
						t.Fatalf("GET path = %s", r.URL.Path)
					}
					_, _ = w.Write([]byte(`{"success":true,"result":` + tc.current + `}`))
				case "PUT":
					var err error
					putBody, err = io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
				default:
					t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				}
			}))
			defer srv.Close()

			if _, _, err := runGatewayConfigCLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want GET then PUT", requests)
			}
			gatewayConfigAssertJSONEqual(t, putBody, tc.wantBody)
		})
	}
}

func TestGatewayConfigClearMaxTTLExplicitFalseDoesNotClear(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("explicit false must fail locally before request construction: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runGatewayConfigCLI(t, srv.URL, "gateway", "config", "settings", "set", "--clear-max-ttl=false")
	if err == nil || !strings.Contains(err.Error(), "provide at least one setting") {
		t.Fatalf("expected local nothing-to-change error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func TestGatewayConfigValidationPreventsNetworkWork(t *testing.T) {
	for _, args := range [][]string{
		{"gateway", "config", "locations", "create", "office", "--network", "2001:db8::/32", "--dry-run"},
		{"gateway", "config", "locations", "create", "office", "--max-ttl-mode", "override", "--max-ttl-secs", "59", "--dry-run"},
		{"gateway", "config", "locations", "update", "location-id", "--dry-run"},
		{"gateway", "config", "settings", "set", "--inspection-mode", "invalid", "--dry-run"},
		{"gateway", "config", "settings", "set", "--max-ttl-secs", "36001", "--dry-run"},
		{"gateway", "config", "settings", "set", "--max-ttl-secs", "3600", "--clear-max-ttl", "--dry-run"},
	} {
		_, _, err := runGatewayConfigCLI(t, "http://example.invalid", args...)
		if err == nil {
			t.Fatalf("%v: expected validation error", args)
		}
	}
}

func TestGatewayConfigLocationAndSettingsValidation(t *testing.T) {
	for _, value := range []string{"192.0.2.0/23", "not-an-ip", ""} {
		if _, err := gatewayConfigNetworks([]string{value}); err == nil {
			t.Errorf("gatewayConfigNetworks(%q) succeeded", value)
		}
	}
	if got, err := gatewayConfigNetworks([]string{"192.0.2.1", "198.51.100.0/24"}); err != nil || !reflect.DeepEqual(got, []map[string]string{{"network": "192.0.2.1"}, {"network": "198.51.100.0/24"}}) {
		t.Errorf("gatewayConfigNetworks = %#v, %v", got, err)
	}
	if _, err := gatewayConfigMaxTTL("override", 60, true, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode       string
		seconds    int
		modeSet    bool
		secondsSet bool
	}{
		{"override", 0, true, false},
		{"inherit", 60, true, true},
		{"invalid", 0, true, false},
		{"", 3600, false, true},
	} {
		if _, err := gatewayConfigMaxTTL(tc.mode, tc.seconds, tc.modeSet, tc.secondsSet); err == nil {
			t.Errorf("gatewayConfigMaxTTL(%+v) succeeded", tc)
		}
	}
}

func gatewayConfigAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid want JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}
