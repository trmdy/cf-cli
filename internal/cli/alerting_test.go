package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const alertingTestAccountID = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"
const alertingTestPolicyID = "0da2b59ef118439d8097bdfb215203c9"
const alertingTestPolicyName = "origin-errors"
const alertingTestWebhookID = "b115d5ec15c641ee8b7692c449b5227b"
const alertingTestWebhookName = "ops-webhook"

func runAlertingCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token", "--account-id", alertingTestAccountID}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func alertingAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid wanted JSON %q: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("body = %s, want %s", gotJSON, wantJSON)
	}
}

func TestAlertingClientEarlyValidation(t *testing.T) {
	// missing account must fail before any client or net
	root := NewRootCmd()
	root.SetArgs([]string{"alerting", "policy", "list", "--dry-run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("expected early account error, got %v", err)
	}
}

func TestBuildAlertingMechanisms(t *testing.T) {
	m, err := buildAlertingMechanisms([]string{"a@b.com", "c@d.com"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	alertingAssertJSONEqual(t, b, `{"email":[{"id":"a@b.com"},{"id":"c@d.com"}]}`)

	m, err = buildAlertingMechanisms(nil, []string{"0123456789abcdef0123456789abcdef"}, []string{"fedcba9876543210fedcba9876543210"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(m)
	alertingAssertJSONEqual(t, b, `{"pagerduty":[{"id":"fedcba9876543210fedcba9876543210"}],"webhooks":[{"id":"0123456789abcdef0123456789abcdef"}]}`)
}

func TestAlertingPolicyCreateBodyValidation(t *testing.T) {
	// local validation before client
	_, _, err := runAlertingCLI(t, "http://127.0.0.1", "alerting", "policy", "create", "", "--alert-type", "x")
	if err == nil || !strings.Contains(err.Error(), "name cannot be empty") {
		t.Fatalf("expected name validation, got %v", err)
	}
	// missing alert type
	_, _, err = runAlertingCLI(t, "http://127.0.0.1", "alerting", "policy", "create", "n", "--email", "e@e.com")
	if err == nil || !strings.Contains(err.Error(), "alert-type") {
		t.Fatalf("expected alert-type error, got %v", err)
	}
}

func TestAlertingPolicyUpdateNoChange(t *testing.T) {
	_, _, err := runAlertingCLI(t, "http://127.0.0.1", "alerting", "policy", "update", alertingTestPolicyID)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("expected nothing-to-update, got %v", err)
	}
}

func TestAlertingResolveIDShortCircuit(t *testing.T) {
	if !isAlertingID(alertingTestPolicyID) {
		t.Fatal("test id should be valid 32hex")
	}
	if isAlertingID("not-an-id") {
		t.Fatal("name must not be id")
	}
}

func TestAlertingPolicyHTTP(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = readBody(r)
		if strings.Contains(r.URL.Path, "/policies") && r.Method == "GET" {
			// list or get
			if strings.HasSuffix(r.URL.Path, "/policies") {
				_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[{"id":"` + alertingTestPolicyID + `","name":"` + alertingTestPolicyName + `","enabled":true,"alert_type":"http_alert_origin_error"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"` + alertingTestPolicyID + `","name":"` + alertingTestPolicyName + `","enabled":true,"alert_type":"http_alert_origin_error","mechanisms":{}}}`))
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/policies") {
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestPolicyID + `"}}`))
			return
		}
		if r.Method == "PUT" {
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestPolicyID + `"}}`))
			return
		}
		if r.Method == "DELETE" {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/test") {
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	// list
	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "policy", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, alertingTestPolicyName) {
		t.Errorf("list output missing name: %s", stdout)
	}

	// create
	stdout, _, err = runAlertingCLI(t, srv.URL, "alerting", "policy", "create", "newpol", "--alert-type", "http_alert_origin_error", "--email", "a@b.com")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || !strings.HasSuffix(gotPath, "/policies") {
		t.Errorf("create req %s %s", gotMethod, gotPath)
	}
	alertingAssertJSONEqual(t, gotBody, `{"alert_type":"http_alert_origin_error","enabled":true,"mechanisms":{"email":[{"id":"a@b.com"}]},"name":"newpol"}`)

	// get by name (resolve)
	_, _, err = runAlertingCLI(t, srv.URL, "alerting", "policy", "get", alertingTestPolicyName)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || !strings.HasSuffix(gotPath, "/"+alertingTestPolicyID) {
		t.Errorf("get resolved %s %s", gotMethod, gotPath)
	}

	// update (read-merge)
	// server already set to return policy on GET
	_, _, err = runAlertingCLI(t, srv.URL, "alerting", "policy", "update", alertingTestPolicyName, "--enabled=false")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" {
		t.Errorf("update method %s", gotMethod)
	}
	// body must be merged without readonly
	var merged map[string]any
	json.Unmarshal(gotBody, &merged)
	if _, hasID := merged["id"]; hasID {
		t.Errorf("update body must not contain id: %v", merged)
	}
	if en, ok := merged["enabled"].(bool); !ok || en {
		t.Errorf("enabled merge failed: %v", merged)
	}

	// delete
	_, _, err = runAlertingCLI(t, srv.URL, "alerting", "policy", "delete", alertingTestPolicyID, "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("delete %s", gotMethod)
	}

	// test fire
	_, _, err = runAlertingCLI(t, srv.URL, "alerting", "policy", "test", alertingTestPolicyID, "--severity", "2")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || !strings.HasSuffix(gotPath, "/test") {
		t.Errorf("test req %s %s", gotMethod, gotPath)
	}
	alertingAssertJSONEqual(t, gotBody, `{"severity":2}`)
}

func TestAlertingWebhookReadMergeAndDryRun(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		_, _ = readBody(r)
		if r.Method == "GET" {
			if strings.HasSuffix(r.URL.Path, "/webhooks") {
				// list for resolve by name
				_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + alertingTestWebhookID + `","name":"` + alertingTestWebhookName + `","url":"https://old"}]}`))
				return
			}
			// single get
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestWebhookID + `","name":"` + alertingTestWebhookName + `","url":"https://old","created_at":"now"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	// update does read even on dry-run
	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "destination", "webhook", "update", alertingTestWebhookName, "--url", "https://new", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || !strings.Contains(gotPath, "webhooks") {
		t.Errorf("dry-run update must GET first, got %s %s", gotMethod, gotPath)
	}
	var dump struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != "PUT" {
		t.Errorf("dry dump method %s", dump.Method)
	}
	var m map[string]any
	json.Unmarshal(dump.Body, &m)
	if _, ok := m["created_at"]; ok {
		t.Error("dry merged body must strip readonly")
	}
	if u, _ := m["url"].(string); u != "https://new" {
		t.Errorf("url not merged: %v", m)
	}
}

func TestAlertingPagerDutyDeleteConfirm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	// without force should abort before request (non-tty in test)
	_, _, err := runAlertingCLI(t, srv.URL, "alerting", "destination", "pagerduty", "delete")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected abort without --force in non-tty, got %v", err)
	}

	// with force ok
	_, _, err = runAlertingCLI(t, srv.URL, "alerting", "destination", "pagerduty", "delete", "--force")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAlertingAvailableAlertsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"result":{"Origin Monitoring":[{"type":"http_alert_origin_error","display_name":"Origin Error Rate Alert","description":"High 5xx"}]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "available-alerts")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "http_alert_origin_error") || !strings.Contains(stdout, "CATEGORY") {
		t.Errorf("catalog table bad: %s", stdout)
	}
}

func TestAlertingDryRunNoTokenOk(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"--dry-run", "--account-id", alertingTestAccountID, "alerting", "policy", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("dry-run list must not require token: %v", err)
	}
}

// --- additional compact exact request coverage for all 16 leaves (policy 6,
// webhook 5, pagerduty 4, available 1). Includes scalar zero-read (enabled=false,
// severity=0), nested read-before-write for mech, force/destructive dry-run
// (DELETE/PUT dry without confirm side effects), early validates.

func TestAlertingEarlyValidationsAndBlanks(t *testing.T) {
	// alert_type enum before any client/net
	_, _, err := runAlertingCLI(t, "http://127.0.0.1", "alerting", "policy", "create", "p", "--alert-type", "not_a_real_type")
	if err == nil || !strings.Contains(err.Error(), "invalid --alert-type") {
		t.Fatalf("expected early enum error, got %v", err)
	}

	// blank dest in create policy (mech)
	_, _, err = runAlertingCLI(t, "http://127.0.0.1", "alerting", "policy", "create", "p", "--alert-type", "http_alert_origin_error", "--email", "")
	if err == nil || !strings.Contains(err.Error(), "blank email") {
		t.Fatalf("expected blank dest error, got %v", err)
	}

	// now accepts short/non-hex (per spec); reject only blank or >32 codepoints
	// test via direct build (before client) to avoid network
	_, err = buildAlertingMechanisms(nil, []string{"short"}, nil)
	if err != nil {
		t.Fatalf("short non-hex mech ID should be accepted (no invention of 32-hex), got %v", err)
	}
	_, err = buildAlertingMechanisms(nil, nil, []string{"0123456789abcdef0123456789abcde!"})
	if err != nil {
		t.Fatalf("32-char non-hex should accept, got %v", err)
	}

	// >32 runes should reject (even if hex-looking)
	longID := "0123456789abcdef0123456789abcdefX" // 33 chars
	_, err = buildAlertingMechanisms(nil, []string{longID}, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid webhooks destination ID") {
		t.Fatalf("expected >32 codepoint mech id error, got %v", err)
	}

	// multibyte 32/33 boundaries + nonblank (per spec review)
	mb32 := strings.Repeat("é", 32) // 32 code points, multibyte
	if !isValidAlertingMechanismID(mb32) {
		t.Error("32 multibyte runes should be accepted (<=32)")
	}
	mb33 := strings.Repeat("é", 33)
	if isValidAlertingMechanismID(mb33) {
		t.Error("33 multibyte runes should be rejected (>32)")
	}
	if isValidAlertingMechanismID("") || isValidAlertingMechanismID("   ") {
		t.Error("blank must be rejected")
	}
	// short + non-hex acceptance already covered above
}

func TestAlertingPagerDutyLinkTokenValidation(t *testing.T) {
	valid32 := strings.Repeat("é", 32)
	if got, err := validateAlertingIntegrationTokenID(valid32); err != nil || got != valid32 {
		t.Fatalf("32-code-point token = %q, %v; want accepted unchanged", got, err)
	}
	if _, err := validateAlertingIntegrationTokenID(strings.Repeat("é", 33)); err == nil {
		t.Fatal("33-code-point token should be rejected")
	}

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()
	for _, token := range []string{"", strings.Repeat("é", 33)} {
		_, _, err := runAlertingCLI(t, srv.URL, "alerting", "destination", "pagerduty", "link", token)
		if err == nil {
			t.Fatalf("token %q: expected validation error", token)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid PagerDuty tokens made %d requests, want 0", calls)
	}
}

func TestAlertingPolicyMechanismsMalformedNoWrite(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestPolicyID + `","mechanisms":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runAlertingCLI(t, srv.URL, "alerting", "policy", "update", alertingTestPolicyID, "--email", "new@ex.com")
	if err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("expected malformed mechanisms error, got %v", err)
	}
	if len(methods) != 1 || methods[0] != "GET" {
		t.Fatalf("requests = %v, want exactly one GET and no PUT", methods)
	}
}

func TestAlertingPolicyScalarUpdateNoRead(t *testing.T) {
	// scalar update (incl zero/false) must send only changed, no GET (even dry)
	// dry-run means no server hits at all for scalar case.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	// use ID so no resolve list; scalar so no mech GET, and dry so no write
	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "policy", "update", alertingTestPolicyID, "--enabled=false", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if callCount != 0 {
		t.Errorf("scalar dry update must perform zero server calls (no GET), got %d", callCount)
	}
	var dump struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	json.Unmarshal([]byte(stdout), &dump)
	if dump.Method != "PUT" {
		t.Errorf("expected PUT in dump")
	}
	alertingAssertJSONEqual(t, dump.Body, `{"enabled":false}`)

	// also test --name scalar
	stdout, _, err = runAlertingCLI(t, srv.URL, "alerting", "policy", "update", alertingTestPolicyID, "--name", "renamed-pol", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(stdout), &dump)
	alertingAssertJSONEqual(t, dump.Body, `{"name":"renamed-pol"}`)
}

func TestAlertingPolicyMechNestedReadMergeDry(t *testing.T) {
	// only when mech family changed: targeted GET (dry ok), merge preserves others
	var methods, paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		_, _ = readBody(r)
		if r.Method == "GET" {
			// return current with mixed mechs
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestPolicyID + `","mechanisms":{"email":[{"id":"old@ex.com"}],"webhooks":[{"id":"0123456789abcdef0123456789abcdef"}]}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "policy", "update", alertingTestPolicyID, "--email", "new@ex.com", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	// should have done GET for mech (ID so no res list), dump shows PUT
	foundGet := false
	for i, m := range methods {
		if m == "GET" && strings.Contains(paths[i], alertingTestPolicyID) {
			foundGet = true
		}
	}
	if !foundGet {
		t.Errorf("expected targeted GET for mech update, got %v", methods)
	}
	var dump struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	json.Unmarshal([]byte(stdout), &dump)
	if dump.Method != "PUT" {
		t.Errorf("expected PUT dump, got %s", dump.Method)
	}
	var merged map[string]any
	json.Unmarshal(dump.Body, &merged)
	mechs := merged["mechanisms"].(map[string]any)
	// email updated
	if em := mechs["email"].([]any); len(em) != 1 || em[0].(map[string]any)["id"] != "new@ex.com" {
		t.Errorf("email not overlaid: %v", mechs)
	}
	// webhook preserved from current
	if wh := mechs["webhooks"].([]any); len(wh) != 1 || wh[0].(map[string]any)["id"] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("webhook not preserved: %v", mechs)
	}
}

func TestAlertingPolicyTestBodyEarlyAndEnforce(t *testing.T) {
	// body build/validate before client+lookup; state_event requires corr; explicit strings sent
	_, _, err := runAlertingCLI(t, "http://127.0.0.1", "alerting", "policy", "test", alertingTestPolicyID, "--state-event", "1")
	if err == nil || !strings.Contains(err.Error(), "state_event requires") {
		t.Fatalf("expected state enforce before net, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = readBody(r)
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	// explicit --source "" should be sent, not omitted
	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "policy", "test", alertingTestPolicyID, "--source", "", "--severity", "0", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct{ Body json.RawMessage }
	json.Unmarshal([]byte(stdout), &dump)
	alertingAssertJSONEqual(t, dump.Body, `{"severity":0,"source":""}`)
}

func TestAlertingWebhookUpdateValidateMerged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestWebhookID + `","name":"old","url":"https://old"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	// dry-run with url change: should validate merged has name/url
	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "destination", "webhook", "update", alertingTestWebhookID, "--url", "https://new", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct{ Body json.RawMessage }
	json.Unmarshal([]byte(stdout), &dump)
	var m map[string]any
	json.Unmarshal(dump.Body, &m)
	if m["name"] == nil || m["url"] == nil {
		t.Errorf("merged must include required name/url: %v", m)
	}
}

func TestAlertingAvailableAlertsSorted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"result":{"Zebra":[{"type":"z","display_name":"Z","description":"z"}],"Alpha":[{"type":"a","display_name":"A","description":"a"}]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "available-alerts")
	if err != nil {
		t.Fatal(err)
	}
	// categories should appear Alpha before Zebra (sorted)
	alphaIdx := strings.Index(stdout, "Alpha")
	zebraIdx := strings.Index(stdout, "Zebra")
	if alphaIdx == -1 || zebraIdx == -1 || alphaIdx > zebraIdx {
		t.Errorf("expected sorted categories Alpha before Zebra in:\n%s", stdout)
	}
}

func TestAlertingDestructiveDryRunNoConfirm(t *testing.T) {
	// dry-run on delete should dump without hitting confirm path
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()

	stdout, _, err := runAlertingCLI(t, srv.URL, "alerting", "policy", "delete", alertingTestPolicyID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct{ Method string }
	json.Unmarshal([]byte(stdout), &dump)
	if dump.Method != "DELETE" {
		t.Errorf("dry delete should dump DELETE: %s", stdout)
	}
	// no prompt text leaked
	if strings.Contains(stdout, "Delete") && strings.Contains(stdout, "?") {
		t.Error("dry-run delete must not trigger confirm")
	}
}

// TestAlertingExactRequestMatrix provides explicit coverage for all 16 leaves
// with exact method/path/body and read/write counts (GETs=reads, others=writes).
// Covers required: webhook list/get/create/delete, pagerduty list/connect/link/delete,
// forced live deletes, destructive dry-run (zero writes).
func TestAlertingExactRequestMatrix(t *testing.T) {
	type call struct {
		method string
		path   string
		query  string
		body   string
	}
	type testCase struct {
		name           string
		args           []string
		forceLive      bool
		wantCalls      []call // exact expected calls (for cases that do network even in dry)
		wantReads      int
		wantWrites     int
		wantDumpMethod string
		wantDumpPath   string // substring match for URL
	}
	policiesPath := "/accounts/" + alertingTestAccountID + "/alerting/v3/policies"
	policyPath := policiesPath + "/" + alertingTestPolicyID
	webhooksPath := "/accounts/" + alertingTestAccountID + "/alerting/v3/destinations/webhooks"
	webhookPath := webhooksPath + "/" + alertingTestWebhookID
	pagerDutyPath := "/accounts/" + alertingTestAccountID + "/alerting/v3/destinations/pagerduty"
	wantDumps := map[string]call{
		"policy-list":                          {"GET", policiesPath, "", ""},
		"policy-get":                           {"GET", policyPath, "", ""},
		"policy-create":                        {"POST", policiesPath, "", `{"alert_type":"http_alert_origin_error","enabled":true,"mechanisms":{"email":[{"id":"a@b.com"}]},"name":"m"}`},
		"policy-update-scalar":                 {"PUT", policyPath, "", `{"enabled":false}`},
		"policy-update-mech":                   {"PUT", policyPath, "", `{"mechanisms":{"email":[{"id":"new@ex.com"}],"webhooks":[{"id":"existing-webhook"}]}}`},
		"policy-update-scalar-name-resolution": {"PUT", policyPath, "", `{"enabled":false}`},
		"policy-delete-dry-zero":               {"DELETE", policyPath, "", ""},
		"policy-test":                          {"POST", policyPath + "/test", "", ""},
		"webhook-list":                         {"GET", webhooksPath, "", ""},
		"webhook-get":                          {"GET", webhookPath, "", ""},
		"webhook-create":                       {"POST", webhooksPath, "", `{"name":"w","url":"https://ex.com/h"}`},
		"webhook-update":                       {"PUT", webhookPath, "", `{"name":"old","url":"https://new.ex"}`},
		"webhook-delete-dry-zero":              {"DELETE", webhookPath, "", ""},
		"pagerduty-list":                       {"GET", pagerDutyPath, "", ""},
		"pagerduty-connect":                    {"POST", pagerDutyPath + "/connect", "", ""},
		"pagerduty-link":                       {"GET", pagerDutyPath + "/connect/0123456789abcdef0123456789abcdef", "", ""},
		"pagerduty-delete-dry-zero":            {"DELETE", pagerDutyPath, "", ""},
		"available-alerts":                     {"GET", "/accounts/" + alertingTestAccountID + "/alerting/v3/available_alerts", "", ""},
	}

	cases := []testCase{
		// policy leaves
		{"policy-list", []string{"alerting", "policy", "list"}, false, nil, 0, 0, "GET", "/alerting/v3/policies"},
		{"policy-get", []string{"alerting", "policy", "get", alertingTestPolicyID}, false, nil, 0, 0, "GET", "/alerting/v3/policies/" + alertingTestPolicyID},
		{"policy-create", []string{"alerting", "policy", "create", "m", "--alert-type", "http_alert_origin_error", "--email", "a@b.com"}, false, nil, 0, 0, "POST", "/alerting/v3/policies"},
		{"policy-update-scalar", []string{"alerting", "policy", "update", alertingTestPolicyID, "--enabled=false"}, false, nil, 0, 0, "PUT", "/alerting/v3/policies/" + alertingTestPolicyID},                                                                                                                 // scalar zero, no read
		{"policy-update-mech", []string{"alerting", "policy", "update", alertingTestPolicyID, "--email", "new@ex.com"}, false, []call{{"GET", "/accounts/" + alertingTestAccountID + "/alerting/v3/policies/" + alertingTestPolicyID, "", ""}}, 1, 0, "PUT", "/alerting/v3/policies/" + alertingTestPolicyID}, // nested read-before-write + dump write
		{"policy-update-scalar-name-resolution", []string{"alerting", "policy", "update", alertingTestPolicyName, "--enabled=false"}, false, []call{{"GET", policiesPath, "", ""}}, 1, 0, "PUT", "/alerting/v3/policies/" + alertingTestPolicyID},
		{"policy-delete-dry-zero", []string{"alerting", "policy", "delete", alertingTestPolicyID}, false, nil, 0, 0, "DELETE", "/alerting/v3/policies/" + alertingTestPolicyID},
		{"policy-delete-forced", []string{"alerting", "policy", "delete", alertingTestPolicyID, "--force"}, true, []call{{"DELETE", "/accounts/" + alertingTestAccountID + "/alerting/v3/policies/" + alertingTestPolicyID, "", ""}}, 0, 1, "", ""},
		{"policy-test", []string{"alerting", "policy", "test", alertingTestPolicyID}, false, nil, 0, 0, "POST", "/alerting/v3/policies/" + alertingTestPolicyID + "/test"},

		// webhook leaves (explicitly required)
		{"webhook-list", []string{"alerting", "destination", "webhook", "list"}, false, nil, 0, 0, "GET", "/alerting/v3/destinations/webhooks"},
		{"webhook-get", []string{"alerting", "destination", "webhook", "get", alertingTestWebhookID}, false, nil, 0, 0, "GET", "/alerting/v3/destinations/webhooks/" + alertingTestWebhookID},
		{"webhook-create", []string{"alerting", "destination", "webhook", "create", "w", "--url", "https://ex.com/h"}, false, nil, 0, 0, "POST", "/alerting/v3/destinations/webhooks"},
		{"webhook-delete-dry-zero", []string{"alerting", "destination", "webhook", "delete", alertingTestWebhookID}, false, nil, 0, 0, "DELETE", "/alerting/v3/destinations/webhooks/" + alertingTestWebhookID}, // explicit zero-write dry-run
		{"webhook-delete-forced-live", []string{"alerting", "destination", "webhook", "delete", alertingTestWebhookID, "--force"}, true, []call{{"DELETE", "/accounts/" + alertingTestAccountID + "/alerting/v3/destinations/webhooks/" + alertingTestWebhookID, "", ""}}, 0, 1, "", ""},
		{"webhook-update", []string{"alerting", "destination", "webhook", "update", alertingTestWebhookID, "--url", "https://new.ex"}, false, []call{{"GET", "/accounts/" + alertingTestAccountID + "/alerting/v3/destinations/webhooks/" + alertingTestWebhookID, "", ""}}, 1, 0, "PUT", "/alerting/v3/destinations/webhooks/" + alertingTestWebhookID},

		// PagerDuty leaves (explicitly required)
		{"pagerduty-list", []string{"alerting", "destination", "pagerduty", "list"}, false, nil, 0, 0, "GET", "/alerting/v3/destinations/pagerduty"},
		{"pagerduty-connect", []string{"alerting", "destination", "pagerduty", "connect"}, false, nil, 0, 0, "POST", "/alerting/v3/destinations/pagerduty/connect"},
		{"pagerduty-link", []string{"alerting", "destination", "pagerduty", "link", "0123456789abcdef0123456789abcdef"}, false, nil, 0, 0, "GET", "/alerting/v3/destinations/pagerduty/connect/0123456789abcdef0123456789abcdef"},
		{"pagerduty-delete-dry-zero", []string{"alerting", "destination", "pagerduty", "delete"}, false, nil, 0, 0, "DELETE", "/alerting/v3/destinations/pagerduty"}, // explicit zero-write dry-run
		{"pagerduty-delete-forced-live", []string{"alerting", "destination", "pagerduty", "delete", "--force"}, true, []call{{"DELETE", "/accounts/" + alertingTestAccountID + "/alerting/v3/destinations/pagerduty", "", ""}}, 0, 1, "", ""},

		// available
		{"available-alerts", []string{"alerting", "available-alerts"}, false, nil, 0, 0, "GET", "/alerting/v3/available_alerts"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []call
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := readBody(r)
				q := r.URL.RawQuery
				calls = append(calls, call{r.Method, r.URL.Path, q, string(b)})
				if r.Method == "GET" {
					switch r.URL.Path {
					case policiesPath:
						_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + alertingTestPolicyID + `","name":"` + alertingTestPolicyName + `"}]}`))
					case policyPath:
						_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestPolicyID + `","mechanisms":{"email":[{"id":"old@ex.com"}],"webhooks":[{"id":"existing-webhook"}]}}}`))
					case webhooksPath:
						_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + alertingTestWebhookID + `","name":"` + alertingTestWebhookName + `","url":"https://old"}]}`))
					case webhookPath:
						_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + alertingTestWebhookID + `","name":"old","url":"https://old"}}`))
					case pagerDutyPath:
						_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
					default:
						_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
					}
					return
				}
				_, _ = w.Write([]byte(`{"success":true}`))
			}))
			defer srv.Close()

			root := NewRootCmd()
			var out, errb bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errb)
			baseArgs := []string{"--base-url", srv.URL, "--token", "t", "--account-id", alertingTestAccountID}
			cmdArgs := append(baseArgs, tc.args...)
			if !tc.forceLive {
				hasDry := false
				for _, a := range cmdArgs {
					if a == "--dry-run" {
						hasDry = true
						break
					}
				}
				if !hasDry {
					cmdArgs = append(cmdArgs, "--dry-run")
				}
			}
			root.SetArgs(cmdArgs)
			if err := root.Execute(); err != nil {
				t.Fatalf("%s: root.Execute failed: %v\nstdout: %s\nstderr: %s", tc.name, err, out.String(), errb.String())
			}

			reads, writes := 0, 0
			for _, c := range calls {
				if c.method == "GET" {
					reads++
				} else if c.method == "POST" || c.method == "PUT" || c.method == "DELETE" {
					writes++
				}
			}
			if reads != tc.wantReads {
				t.Errorf("reads=%d want=%d calls=%+v", reads, tc.wantReads, calls)
			}
			if writes != tc.wantWrites {
				t.Errorf("writes=%d want=%d", writes, tc.wantWrites)
			}
			if len(calls) != len(tc.wantCalls) {
				t.Fatalf("call count = %d, want %d; calls=%+v", len(calls), len(tc.wantCalls), calls)
			}
			for i, want := range tc.wantCalls {
				if got := calls[i]; got != want {
					t.Errorf("call %d = %+v, want %+v", i, got, want)
				}
			}

			if !tc.forceLive {
				want, ok := wantDumps[tc.name]
				if !ok {
					t.Fatalf("missing exact dry-run dump expectation")
				}
				var dump struct {
					Method string          `json:"method"`
					URL    string          `json:"url"`
					Body   json.RawMessage `json:"body"`
				}
				dumpStr := out.String()
				if err := json.Unmarshal([]byte(dumpStr), &dump); err != nil {
					t.Fatalf("%s: failed decode dry dump: %v\nout: %s", tc.name, err, dumpStr)
				}
				gotURL, err := url.Parse(dump.URL)
				if err != nil {
					t.Fatalf("%s: parse dry dump URL %q: %v", tc.name, dump.URL, err)
				}
				wantURL := srv.URL + want.path
				if want.query != "" {
					wantURL += "?" + want.query
				}
				if dump.Method != want.method || dump.URL != wantURL || gotURL.Path != want.path || gotURL.RawQuery != want.query {
					t.Errorf("%s: dry dump = method=%q url=%q path=%q query=%q, want method=%q url=%q path=%q query=%q", tc.name, dump.Method, dump.URL, gotURL.Path, gotURL.RawQuery, want.method, wantURL, want.path, want.query)
				}
				if want.body == "" {
					if len(dump.Body) != 0 {
						t.Errorf("%s: dry dump body = %s, want absent", tc.name, dump.Body)
					}
				} else {
					alertingAssertJSONEqual(t, dump.Body, want.body)
				}
			}
		})
	}
}

// helper to read body (dupe minimal from other tests)
func readBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	return buf[:n], nil
}
