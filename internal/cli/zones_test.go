package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	zonesTestZoneID    = "023e105f4ecef8ad9ca31a8372d0c353"
	zonesTestAccountID = "9a7806061c88ada191ed06f989cc3dac"
)

// runZonesCLI drives the real command tree against a test server, with a
// throwaway config dir and no ambient Cloudflare env vars, so results depend
// only on the arguments.
func runZonesCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CF_PROFILE", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_ZONE_ID", "")
	t.Setenv("CF_ZONE_ID", "")
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

type zonesDump struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body"`
}

func zonesDecodeDump(t *testing.T, stdout string) zonesDump {
	t.Helper()
	var d zonesDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not a request dump: %v\n%s", err, stdout)
	}
	return d
}

func zonesDecodeDumps(t *testing.T, stdout string) []zonesDump {
	t.Helper()
	var d []zonesDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not a request dump array: %v\n%s", err, stdout)
	}
	return d
}

func zonesAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want invalid JSON %s: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("json = %s, want %s", gb, wb)
	}
}

func zonesHelp(t *testing.T, args ...string) string {
	t.Helper()
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(append(args, "--help"))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// --- unit tests for body/flag construction ---------------------------------

func TestBuildZoneCreateBody(t *testing.T) {
	body, err := buildZoneCreateBody("example.com", zonesTestAccountID, "full")
	if err != nil {
		t.Fatal(err)
	}
	zonesAssertJSONEqual(t, body, `{"name":"example.com","account":{"id":"`+zonesTestAccountID+`"},"type":"full"}`)

	body, err = buildZoneCreateBody("example.com", zonesTestAccountID, "PARTIAL")
	if err != nil {
		t.Fatal(err)
	}
	zonesAssertJSONEqual(t, body, `{"name":"example.com","account":{"id":"`+zonesTestAccountID+`"},"type":"partial"}`)
}

func TestBuildZoneCreateBodyValidation(t *testing.T) {
	if _, err := buildZoneCreateBody("  ", zonesTestAccountID, "full"); err == nil || !strings.Contains(err.Error(), "zone name is empty") {
		t.Fatalf("expected empty name error, got %v", err)
	}
	_, err := buildZoneCreateBody("example.com", "", "full")
	if err == nil || !strings.Contains(err.Error(), "--account-id") {
		t.Fatalf("expected account error, got %v", err)
	}
	if !strings.Contains(err.Error(), "CLOUDFLARE_ACCOUNT_ID") {
		t.Errorf("account error should name the env var: %v", err)
	}
	if _, err := buildZoneCreateBody("example.com", zonesTestAccountID, "halfway"); err == nil || !strings.Contains(err.Error(), "full, partial, secondary, internal") {
		t.Fatalf("expected type error listing options, got %v", err)
	}
}

func TestResolveZoneSettingDefs(t *testing.T) {
	defs, err := resolveZoneSettingDefs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != len(zoneSettingDefs) {
		t.Fatalf("default settings = %d, want %d", len(defs), len(zoneSettingDefs))
	}

	// API spelling, CLI spelling, mixed case and duplicates all normalize.
	defs, err = resolveZoneSettingDefs([]string{"always_use_https", "SSL", "always-use-https"})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 || defs[0].ID != "always_use_https" || defs[1].ID != "ssl" {
		t.Fatalf("defs = %+v", defs)
	}

	if _, err := resolveZoneSettingDefs([]string{"minify"}); err == nil ||
		!strings.Contains(err.Error(), "development-mode, ssl, always-use-https") {
		t.Fatalf("expected unknown setting error listing options, got %v", err)
	}
}

func TestValidateZoneChoice(t *testing.T) {
	v, err := validateZoneChoice("--ssl", " Full ", []string{"off", "flexible", "full", "strict"})
	if err != nil {
		t.Fatal(err)
	}
	if v != "full" {
		t.Fatalf("value = %q", v)
	}
	if _, err := validateZoneChoice("--ssl", "", []string{"off", "full"}); err == nil || !strings.Contains(err.Error(), "--ssl is empty") {
		t.Fatalf("expected empty value error, got %v", err)
	}
	if _, err := validateZoneChoice("--ssl", "sorta", []string{"off", "full"}); err == nil || !strings.Contains(err.Error(), `"sorta"`) {
		t.Fatalf("expected invalid value error, got %v", err)
	}
}

// --- list ------------------------------------------------------------------

func TestZonesListDryRun(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "list",
		"--name", "example.com",
		"--status", "active",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := zonesDecodeDump(t, stdout)
	if dump.Method != "GET" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.Contains(dump.URL, "/zones?") {
		t.Errorf("url = %s", dump.URL)
	}
	for _, want := range []string{"name=example.com", "status=active", "per_page=100"} {
		if !strings.Contains(dump.URL, want) {
			t.Errorf("url %s missing %q", dump.URL, want)
		}
	}
}

func TestZonesListRejectsInvalidStatus(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "list", "--status", "sleeping", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "invalid --status value") {
		t.Fatalf("expected status error, got %v", err)
	}
	if !strings.Contains(err.Error(), "initializing, pending, active, moved") {
		t.Errorf("status error should list valid values: %v", err)
	}
}

func TestZonesListTableOutput(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"` + zonesTestZoneID + `","name":"example.com","status":"active","paused":false,"plan":{"name":"Free Website"}},
			{"id":"1111111111111111aaaaaaaaaaaaaaaa","name":"paused.example","status":"active","paused":true,"plan":{"name":"Pro Website"}}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL, "zones", "list")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/zones" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(gotQuery, "per_page=100") {
		t.Errorf("query = %s", gotQuery)
	}
	for _, want := range []string{"ID", "NAME", "STATUS", "PLAN", "PAUSED", "example.com", "Free Website", "true", "false"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestZonesListJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + zonesTestZoneID + `","name":"example.com"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL, "zones", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var zones []map[string]any
	if err := json.Unmarshal([]byte(stdout), &zones); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if len(zones) != 1 || zones[0]["name"] != "example.com" {
		t.Fatalf("zones = %v", zones)
	}
}

// --- get -------------------------------------------------------------------

func TestZonesGetHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + zonesTestZoneID + `","name":"example.com","status":"active"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL, "zones", "get", "--zone", zonesTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/zones/"+zonesTestZoneID {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, `"example.com"`) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestZonesGetResolvesZoneName(t *testing.T) {
	var sawLookup bool
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			sawLookup = true
			if got := r.URL.Query().Get("name"); got != "example.com" {
				t.Errorf("lookup name = %q", got)
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + zonesTestZoneID + `","name":"example.com"}]}`))
		case r.Method == "GET" && r.URL.Path == "/zones/"+zonesTestZoneID:
			gotPath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + zonesTestZoneID + `"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, _, err := runZonesCLI(t, srv.URL, "zones", "get", "--zone", "example.com"); err != nil {
		t.Fatal(err)
	}
	if !sawLookup {
		t.Error("expected a zone name lookup")
	}
	if gotPath != "/zones/"+zonesTestZoneID {
		t.Errorf("path = %s", gotPath)
	}
}

func TestZonesGetWithoutZoneIsActionable(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid", "zones", "get", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("expected missing zone error, got %v", err)
	}
	if !strings.Contains(err.Error(), "--zone") {
		t.Errorf("missing zone error should name --zone: %v", err)
	}
}

// --- create ----------------------------------------------------------------

func TestZonesCreateDryRun(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "create", "example.com",
		"--account-id", zonesTestAccountID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := zonesDecodeDump(t, stdout)
	if dump.Method != "POST" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/zones") {
		t.Errorf("url = %s", dump.URL)
	}
	zonesAssertJSONEqual(t, dump.Body, `{"name":"example.com","account":{"id":"`+zonesTestAccountID+`"},"type":"full"}`)
}

func TestZonesCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + zonesTestZoneID + `","name":"example.com","status":"pending"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL,
		"zones", "create", "example.com",
		"--type", "partial",
		"--account-id", zonesTestAccountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/zones" {
		t.Errorf("request = %s %s", gotMethod, gotPath)
	}
	zonesAssertJSONEqual(t, gotBody, `{"name":"example.com","account":{"id":"`+zonesTestAccountID+`"},"type":"partial"}`)
	if !strings.Contains(stdout, "pending") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestZonesCreateRequiresAccountID(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid", "zones", "create", "example.com", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no account ID") {
		t.Fatalf("expected account error, got %v", err)
	}
}

func TestZonesCreateRejectsInvalidType(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "create", "example.com",
		"--account-id", zonesTestAccountID,
		"--type", "halfway",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "invalid --type value") {
		t.Fatalf("expected type error, got %v", err)
	}
}

func TestZonesCreateRequiresName(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "create", "--account-id", zonesTestAccountID, "--dry-run")
	if err == nil {
		t.Fatal("expected error when no zone name is given")
	}
}

// --- delete ----------------------------------------------------------------

func TestZonesDeleteHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + zonesTestZoneID + `"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL, "zones", "delete", "--zone", zonesTestZoneID, "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/zones/"+zonesTestZoneID {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, zonesTestZoneID) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestZonesDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runZonesCLI(t, srv.URL, "zones", "delete", "--zone", zonesTestZoneID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected abort error mentioning --force, got %v", err)
	}
}

func TestZonesDeleteDryRunSkipsConfirmation(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "delete", "--zone", zonesTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := zonesDecodeDump(t, stdout)
	if dump.Method != "DELETE" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/zones/"+zonesTestZoneID) {
		t.Errorf("url = %s", dump.URL)
	}
}

// --- pause / resume --------------------------------------------------------

func TestZonesPauseHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + zonesTestZoneID + `","paused":true}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL, "zones", "pause", "--zone", zonesTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PATCH" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/zones/"+zonesTestZoneID {
		t.Errorf("path = %s", gotPath)
	}
	zonesAssertJSONEqual(t, gotBody, `{"paused":true}`)
	if !strings.Contains(stdout, "true") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestZonesResumeDryRun(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "resume", "--zone", zonesTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := zonesDecodeDump(t, stdout)
	if dump.Method != "PATCH" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/zones/"+zonesTestZoneID) {
		t.Errorf("url = %s", dump.URL)
	}
	zonesAssertJSONEqual(t, dump.Body, `{"paused":false}`)
}

// --- settings --------------------------------------------------------------

func TestZonesSettingsGetDefaultsToCommonToggles(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "settings", "get", "--zone", zonesTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dumps := zonesDecodeDumps(t, stdout)
	want := []string{"development_mode", "ssl", "always_use_https"}
	if len(dumps) != len(want) {
		t.Fatalf("dumps = %d, want %d\n%s", len(dumps), len(want), stdout)
	}
	for i, setting := range want {
		if dumps[i].Method != "GET" {
			t.Errorf("dump %d method = %s", i, dumps[i].Method)
		}
		if !strings.HasSuffix(dumps[i].URL, "/zones/"+zonesTestZoneID+"/settings/"+setting) {
			t.Errorf("dump %d url = %s", i, dumps[i].URL)
		}
		if len(dumps[i].Body) != 0 {
			t.Errorf("dump %d has a body: %s", i, dumps[i].Body)
		}
	}
}

func TestZonesSettingsGetSelectedSettings(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "settings", "get",
		"--zone", zonesTestZoneID,
		"--setting", "always_use_https",
		"--setting", "ssl",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dumps := zonesDecodeDumps(t, stdout)
	if len(dumps) != 2 {
		t.Fatalf("dumps = %d\n%s", len(dumps), stdout)
	}
	if !strings.HasSuffix(dumps[0].URL, "/settings/always_use_https") {
		t.Errorf("dump 0 url = %s", dumps[0].URL)
	}
	if !strings.HasSuffix(dumps[1].URL, "/settings/ssl") {
		t.Errorf("dump 1 url = %s", dumps[1].URL)
	}
}

func TestZonesSettingsGetRejectsUnknownSetting(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "settings", "get", "--zone", zonesTestZoneID, "--setting", "minify", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), `unknown setting "minify"`) {
		t.Fatalf("expected unknown setting error, got %v", err)
	}
}

func TestZonesSettingsGetTableOutput(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("method = %s", r.Method)
		}
		paths = append(paths, r.URL.Path)
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		value := "on"
		if id == "ssl" {
			value = "full"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + id + `","value":"` + value +
			`","editable":true,"modified_on":"2024-01-02T03:04:05.000000Z"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL, "zones", "settings", "get", "--zone", zonesTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{
		"/zones/" + zonesTestZoneID + "/settings/development_mode",
		"/zones/" + zonesTestZoneID + "/settings/ssl",
		"/zones/" + zonesTestZoneID + "/settings/always_use_https",
	}
	if strings.Join(paths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	for _, want := range []string{"SETTING", "VALUE", "EDITABLE", "MODIFIED", "development_mode", "always_use_https", "full", "2024-01-02"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestZonesSettingsGetQueryAppliesToArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + id + `","value":"full"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL,
		"zones", "settings", "get", "--zone", zonesTestZoneID, "--setting", "ssl", "--query", ".[0].value")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != `"full"` {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestZonesSettingsSetHTTPRequests(t *testing.T) {
	type call struct {
		path string
		body string
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("method = %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, call{path: r.URL.Path, body: string(body)})
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + id + `","value":"set"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runZonesCLI(t, srv.URL,
		"zones", "settings", "set",
		"--zone", zonesTestZoneID,
		"--always-use-https", "on",
		"--ssl", "STRICT",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	// Applied in the documented order, not the order the flags were given.
	if calls[0].path != "/zones/"+zonesTestZoneID+"/settings/ssl" {
		t.Errorf("call 0 path = %s", calls[0].path)
	}
	zonesAssertJSONEqual(t, []byte(calls[0].body), `{"value":"strict"}`)
	if calls[1].path != "/zones/"+zonesTestZoneID+"/settings/always_use_https" {
		t.Errorf("call 1 path = %s", calls[1].path)
	}
	zonesAssertJSONEqual(t, []byte(calls[1].body), `{"value":"on"}`)

	var results []map[string]any
	if err := json.Unmarshal([]byte(stdout), &results); err != nil {
		t.Fatalf("stdout not a JSON array: %v\n%s", err, stdout)
	}
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
}

func TestZonesSettingsSetDevelopmentModeDryRun(t *testing.T) {
	stdout, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "settings", "set",
		"--zone", zonesTestZoneID,
		"--development-mode", "off",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dumps := zonesDecodeDumps(t, stdout)
	if len(dumps) != 1 {
		t.Fatalf("dumps = %d\n%s", len(dumps), stdout)
	}
	if dumps[0].Method != "PATCH" {
		t.Errorf("method = %s", dumps[0].Method)
	}
	if !strings.HasSuffix(dumps[0].URL, "/zones/"+zonesTestZoneID+"/settings/development_mode") {
		t.Errorf("url = %s", dumps[0].URL)
	}
	zonesAssertJSONEqual(t, dumps[0].Body, `{"value":"off"}`)
}

func TestZonesSettingsSetRequiresAtLeastOneFlag(t *testing.T) {
	_, _, err := runZonesCLI(t, "http://example.invalid",
		"zones", "settings", "set", "--zone", zonesTestZoneID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing to set") {
		t.Fatalf("expected nothing-to-set error, got %v", err)
	}
	for _, want := range []string{"--development-mode", "--ssl", "--always-use-https"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s: %v", want, err)
		}
	}
}

func TestZonesSettingsSetRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"ssl", []string{"--ssl", "maximum"}, "invalid --ssl value"},
		{"development-mode", []string{"--development-mode", "yes"}, "invalid --development-mode value"},
		{"always-use-https", []string{"--always-use-https", ""}, "--always-use-https is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"zones", "settings", "set", "--zone", zonesTestZoneID, "--dry-run"}, tc.args...)
			_, _, err := runZonesCLI(t, "http://example.invalid", args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

func TestZonesSettingsSetReportsFailingSetting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1004,"message":"invalid value"}]}`))
	}))
	defer srv.Close()

	_, _, err := runZonesCLI(t, srv.URL,
		"zones", "settings", "set", "--zone", zonesTestZoneID, "--ssl", "strict")
	if err == nil || !strings.Contains(err.Error(), "setting ssl:") {
		t.Fatalf("expected error naming the setting, got %v", err)
	}
}

// --- command tree ----------------------------------------------------------

func TestZonesCommandsRejectStrayArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"list", []string{"zones", "list", "extra"}},
		{"get", []string{"zones", "get", "example.com", "--zone", zonesTestZoneID}},
		{"delete", []string{"zones", "delete", "example.com", "--zone", zonesTestZoneID, "--force"}},
		{"pause", []string{"zones", "pause", "example.com", "--zone", zonesTestZoneID}},
		{"resume", []string{"zones", "resume", "example.com", "--zone", zonesTestZoneID}},
		{"settings-get", []string{"zones", "settings", "get", "extra", "--zone", zonesTestZoneID}},
		{"settings-set", []string{"zones", "settings", "set", "extra", "--zone", zonesTestZoneID, "--ssl", "full"}},
		{"create", []string{"zones", "create", "example.com", "extra", "--account-id", zonesTestAccountID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append(append([]string{}, tc.args...), "--dry-run")
			_, _, err := runZonesCLI(t, "http://example.invalid", args...)
			if err == nil {
				t.Fatal("expected error for stray positional args")
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "arg") && !strings.Contains(msg, "unknown command") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestZonesHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"zones", "list"}, []string{"cf zones list", "--status", "--name"}},
		{[]string{"zones", "get"}, []string{"cf zones get", "--zone"}},
		{[]string{"zones", "create"}, []string{"cf zones create example.com", "--type", "--account-id"}},
		{[]string{"zones", "delete"}, []string{"cf zones delete", "--force", "cannot be undone"}},
		{[]string{"zones", "pause"}, []string{"cf zones pause", "cf zones resume"}},
		{[]string{"zones", "resume"}, []string{"cf zones resume", "--zone"}},
		{[]string{"zones", "settings", "get"}, []string{"cf zones settings get", "--setting"}},
		{[]string{"zones", "settings", "set"}, []string{"cf zones settings set", "--development-mode", "--ssl", "--always-use-https"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			help := zonesHelp(t, tc.args...)
			for _, want := range tc.want {
				if !strings.Contains(help, want) {
					t.Errorf("help missing %q\n%s", want, help)
				}
			}
		})
	}
}

func TestZonesCommandWiredIntoRoot(t *testing.T) {
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "zones" {
			return
		}
	}
	t.Fatal("zones command is not wired into the root command")
}
