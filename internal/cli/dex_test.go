package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	dexTestAccountID = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5"
	dexTestID        = "1234567890abcdef1234567890abcdef"     // max 32 per spec
	dexTestResultID  = "1234567890abcdef1234567890abcdef1234" // max 36 per spec
	dexTestDeviceID  = "123e4567-e89b-12d3-a456-426614174000"
)

func runDexCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", dexTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func dexParseDump(t *testing.T, stdout string) (method, u string, body json.RawMessage) {
	t.Helper()
	var d struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("parse dump: %v\n%s", err, stdout)
	}
	return d.Method, d.URL, d.Body
}

// --- local validation (before client) ---

func TestDexValidateKind(t *testing.T) {
	if err := validateDexKind("http"); err != nil {
		t.Fatal(err)
	}
	if err := validateDexKind("traceroute"); err != nil {
		t.Fatal(err)
	}
	if err := validateDexKind("foo"); err == nil || !strings.Contains(err.Error(), "http, traceroute") {
		t.Fatalf("want kind error, got %v", err)
	}
}

func TestDexValidateTimestamp(t *testing.T) {
	cases := []struct {
		v   string
		err bool
	}{
		{"2026-08-12T00:00:00Z", false},
		{"1723420800000", false},
		{"1723420800", false},             // seconds ok-ish for int parse
		{"2023-10-11 00:00:00+00", false}, // pinned example
		{"not-a-time", true},
		{"", false},
	}
	for _, c := range cases {
		err := validateDexTimestamp("from", c.v)
		if (err != nil) != c.err {
			t.Errorf("ts %q: err=%v wantErr=%v", c.v, err, c.err)
		}
	}
}

func TestDexValidateEnum(t *testing.T) {
	allowed := []string{"http", "traceroute"}
	if err := validateDexEnum("kind", "http", allowed); err != nil {
		t.Fatal(err)
	}
	if err := validateDexEnum("kind", "bad", allowed); err == nil {
		t.Fatal("expected error")
	}
}

func TestDexTestsListRejectsBadKindBeforeClient(t *testing.T) {
	// Use invalid URL; should fail validation locally, never reach network.
	_, _, err := runDexCLI(t, "http://127.0.0.1:0", "dex", "tests", "list", "--kind", "foo")
	if err == nil || !strings.Contains(err.Error(), "http, traceroute") {
		t.Fatalf("got err=%v", err)
	}
}

func TestDexFleetDevicesRequiresFromToBeforeClient(t *testing.T) {
	_, _, err := runDexCLI(t, "http://127.0.0.1:0", "dex", "fleet-status", "devices", "--to", "now")
	if err == nil || !strings.Contains(err.Error(), "--from is required") {
		t.Fatalf("got err=%v", err)
	}
	_, _, err = runDexCLI(t, "http://127.0.0.1:0", "dex", "fleet-status", "devices", "--from", "2026-08-01T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "--to is required") {
		t.Fatalf("got err=%v", err)
	}
}

func TestDexMissingAccountIDBeforeNet(t *testing.T) {
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--token", "t", "dex", "tests", "list", "--dry-run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "account ID") {
		t.Fatalf("err=%v", err)
	}
}

func TestDexValidateSinceMinutes(t *testing.T) {
	if err := validateDexSinceMinutes(1); err != nil {
		t.Fatal(err)
	}
	if err := validateDexSinceMinutes(60); err != nil {
		t.Fatal(err)
	}
	if err := validateDexSinceMinutes(0); err == nil || !strings.Contains(err.Error(), "1 and 60") {
		t.Fatalf("want range error for 0, got %v", err)
	}
	if err := validateDexSinceMinutes(61); err == nil || !strings.Contains(err.Error(), "1 and 60") {
		t.Fatalf("want range error for 61, got %v", err)
	}
}

func TestDexFleetLiveSinceMinutesValidateBeforeClient(t *testing.T) {
	_, _, err := runDexCLI(t, "http://127.0.0.1:0", "dex", "fleet-status", "live", "--since-minutes", "0")
	if err == nil || !strings.Contains(err.Error(), "1 and 60") {
		t.Fatalf("got err=%v", err)
	}
	_, _, err = runDexCLI(t, "http://127.0.0.1:0", "dex", "fleet-status", "live", "--since-minutes", "99")
	if err == nil || !strings.Contains(err.Error(), "1 and 60") {
		t.Fatalf("got err=%v", err)
	}
}

func TestDexTestIDMax32BeforeClient(t *testing.T) {
	long := strings.Repeat("a", 33)
	_, _, err := runDexCLI(t, "http://127.0.0.1:0", "dex", "tests", "get", long)
	if err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("got err=%v", err)
	}
}

func TestDexTracerouteResultIDMax36BeforeClient(t *testing.T) {
	long := strings.Repeat("b", 37)
	_, _, err := runDexCLI(t, "http://127.0.0.1:0", "dex", "traceroute", "results", long)
	if err == nil || !strings.Contains(err.Error(), "36") {
		t.Fatalf("got err=%v", err)
	}
}

// Multibyte (UTF-8) exact/over bounds per pinned JSON Schema maxLength (code points, not bytes).
// Over-limit must fail validation with zero requests (real command tree + bad URL).
func TestDexTestIDMultibyteExactOverBoundRealCommandTree(t *testing.T) {
	// é is 2 bytes in UTF-8, 1 rune. Exact 32 runes (64 bytes) must be accepted by validator.
	exact32 := strings.Repeat("é", 32)
	// Use dry-run + invalid host: must pass validation (no length error), reach dump attempt.
	_, _, err := runDexCLI(t, "http://example.invalid", "dex", "tests", "get", exact32, "--dry-run")
	if err != nil {
		t.Fatalf("exact 32-rune multibyte must pass validation: %v", err)
	}

	// 33 runes multibyte must fail before client/request
	over33 := strings.Repeat("é", 33)
	_, _, err = runDexCLI(t, "http://127.0.0.1:0", "dex", "tests", "get", over33)
	if err == nil || !strings.Contains(err.Error(), "33 characters") || !strings.Contains(err.Error(), "at most 32") {
		t.Fatalf("over 33-rune multibyte must fail pre-request with rune count: %v", err)
	}
}

func TestDexTracerouteResultIDMultibyteExactOverBoundRealCommandTree(t *testing.T) {
	// Exact 36 runes multibyte (e.g. with é)
	exact36 := strings.Repeat("é", 36)
	_, _, err := runDexCLI(t, "http://example.invalid", "dex", "traceroute", "results", exact36, "--dry-run")
	if err != nil {
		t.Fatalf("exact 36-rune multibyte must pass validation: %v", err)
	}

	over37 := strings.Repeat("é", 37)
	_, _, err = runDexCLI(t, "http://127.0.0.1:0", "dex", "traceroute", "results", over37)
	if err == nil || !strings.Contains(err.Error(), "37 characters") || !strings.Contains(err.Error(), "at most 36") {
		t.Fatalf("over 37-rune multibyte must fail pre-request: %v", err)
	}
}

// --- HTTP behavior + table/dry-run ---

func TestDexTestsListPathQueryAndTable(t *testing.T) {
	var gotPath, gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQ = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"` + dexTestID + `","name":"login","kind":"http","enabled":true,"description":"check login"}
		],"result_info":{"page":1,"total_pages":1}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDexCLI(t, srv.URL, "dex", "tests", "list", "--kind", "http", "--test-name", "login")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/accounts/" + dexTestAccountID + "/dex/devices/dex_tests"
	if gotPath != wantPath {
		t.Fatalf("path=%s want=%s", gotPath, wantPath)
	}
	if !strings.Contains(gotQ, "kind=http") || !strings.Contains(gotQ, "testName=login") {
		t.Fatalf("query=%s", gotQ)
	}
	for _, w := range []string{"ID", "NAME", "KIND", "ENABLED", dexTestID, "login", "http", "true"} {
		if !strings.Contains(stdout, w) {
			t.Fatalf("table missing %q:\n%s", w, stdout)
		}
	}
}

func TestDexTestsGetPath(t *testing.T) {
	var gotPath, gotM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotM, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + dexTestID + `","name":"login","kind":"http"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDexCLI(t, srv.URL, "dex", "tests", "get", dexTestID)
	if err != nil {
		t.Fatal(err)
	}
	if gotM != "GET" {
		t.Fatalf("method=%s", gotM)
	}
	want := "/accounts/" + dexTestAccountID + "/dex/devices/dex_tests/" + dexTestID
	if gotPath != want {
		t.Fatalf("path=%s want=%s", gotPath, want)
	}
	if !strings.Contains(stdout, dexTestID) {
		t.Fatalf("stdout=%s", stdout)
	}
}

func TestDexTestsListDryRun(t *testing.T) {
	stdout, _, err := runDexCLI(t, "http://example.invalid", "dex", "tests", "list", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	m, u, _ := dexParseDump(t, stdout)
	if m != "GET" || !strings.Contains(u, "/dex/devices/dex_tests") {
		t.Fatalf("dry-run dump wrong: %s %s", m, u)
	}
}

func TestDexFleetStatusLivePathAndDefault(t *testing.T) {
	var gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"total_devices":42}}`))
	}))
	defer srv.Close()

	_, _, err := runDexCLI(t, srv.URL, "dex", "fleet-status", "live")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQ, "since_minutes=60") {
		t.Fatalf("query=%s want since_minutes default", gotQ)
	}
}

func TestDexFleetStatusDevicesDryRunUsesPage1(t *testing.T) {
	stdout, _, err := runDexCLI(t, "http://example.invalid",
		"dex", "fleet-status", "devices",
		"--from", "2026-08-01T00:00:00Z", "--to", "2026-08-02T00:00:00Z",
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	m, u, _ := dexParseDump(t, stdout)
	if m != "GET" {
		t.Fatalf("method %s", m)
	}
	if !strings.Contains(u, "page=1") || !strings.Contains(u, "per_page=50") || !strings.Contains(u, "from=2026-08-01T00%3A00%3A00Z") {
		t.Fatalf("dry run query must pin page=1 per_page + times: %s", u)
	}
}

func TestDexFleetStatusDevicesLocalPaginationAndTable(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = w.Write([]byte(`{"success":true,"result":[
				{"device_id":"` + dexTestDeviceID + `","timestamp":"2026-08-11T12:00:00Z","colo":"SJC","platform":"mac","status":"healthy","version":"1.0"}
			],"result_info":{"page":1,"per_page":50,"total_pages":1}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	stdout, _, err := runDexCLI(t, srv.URL, "dex", "fleet-status", "devices",
		"--from", "2026-08-01T00:00:00Z", "--to", "2026-08-12T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if call < 1 {
		t.Fatal("expected at least one call")
	}
	for _, w := range []string{"DEVICE_ID", "TIMESTAMP", "COLO", dexTestDeviceID, "SJC", "healthy"} {
		if !strings.Contains(stdout, w) {
			t.Fatalf("table missing %q:\n%s", w, stdout)
		}
	}
}

func TestDexFleetDevicesNonArrayFirstPageRendersRawNotEmptyTable(t *testing.T) {
	// simulate first page returning success envelope but result is not array (object); must render raw (the inner result), not empty table
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"unexpected":"object not array"},"result_info":{"page":1}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDexCLI(t, srv.URL, "dex", "fleet-status", "devices",
		"--from", "2026-08-01T00:00:00Z", "--to", "2026-08-12T00:00:00Z", "--output", "table")
	if err != nil {
		t.Fatal(err)
	}
	// should not be a table header, should contain the raw inner result json
	if strings.Contains(stdout, "DEVICE_ID") || strings.Contains(stdout, "COLO") {
		t.Fatalf("should not render table headers for non-array: %s", stdout)
	}
	if !strings.Contains(stdout, "unexpected") || !strings.Contains(stdout, "object not array") {
		t.Fatalf("expected raw json result in output: %s", stdout)
	}
}

func TestDexTracerouteResultsPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"test_result_id":"` + dexTestResultID + `","hops":[{"ttl":1,"ip":"1.1.1.1"}]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDexCLI(t, srv.URL, "dex", "traceroute", "results", dexTestResultID)
	if err != nil {
		t.Fatal(err)
	}
	want := "/accounts/" + dexTestAccountID + "/dex/traceroute-test-results/" + dexTestResultID + "/network-path"
	if gotPath != want {
		t.Fatalf("path=%s want=%s", gotPath, want)
	}
	if !strings.Contains(stdout, dexTestResultID) {
		t.Fatalf("stdout missing id: %s", stdout)
	}
}

func TestDexTracerouteResultsDryRun(t *testing.T) {
	stdout, _, err := runDexCLI(t, "http://example.invalid", "dex", "traceroute", "results", dexTestResultID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	m, u, _ := dexParseDump(t, stdout)
	if m != "GET" || !strings.Contains(u, "/traceroute-test-results/") || !strings.Contains(u, dexTestResultID) {
		t.Fatalf("bad dry run: %s %s", m, u)
	}
}
