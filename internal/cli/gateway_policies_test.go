package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const gatewayPolicyTestAccountID = "0123456789abcdef0123456789abcdef"

const gatewayPolicyTestRuleID = "3a1b2c4d-0000-4000-8000-000000000000"

const gatewayPolicyTestListID = "7f3e1a90-0000-4000-8000-000000000000"

// gatewayPolicyIsolateConfig points config resolution at an empty directory
// and clears the credential environment, so every test depends only on the
// flags it passes.
func gatewayPolicyIsolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CF_PROFILE", "default")
	for _, key := range []string{
		"CLOUDFLARE_ZONE_ID", "CF_ZONE_ID",
		"CLOUDFLARE_ACCOUNT_ID", "CF_ACCOUNT_ID",
		"CLOUDFLARE_API_TOKEN", "CF_API_TOKEN",
	} {
		t.Setenv(key, "")
	}
}

func runGatewayPolicyCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	gatewayPolicyIsolateConfig(t)
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", gatewayPolicyTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// gatewayPolicyNoCallServer fails the test if any request reaches it. Used to
// prove local validation happens before network work.
func gatewayPolicyNoCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type gatewayPolicyDump struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func gatewayPolicyParseDump(t *testing.T, stdout string) gatewayPolicyDump {
	t.Helper()
	var d gatewayPolicyDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

func gatewayPolicyAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func gatewayPolicyEnvelope(result string) string {
	return `{"success":true,"errors":[],"messages":[],"result":` + result + `}`
}

// --- rule create -----------------------------------------------------------

func TestGatewayPolicyRuleCreateDryRun(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "create",
		"Block malware", "--action", "BLOCK", "--filter", "DNS",
		"--traffic", `any(dns.domains[*] in $blocked)`, "--enabled", "--precedence", "42",
		"--description", "from the incident", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if d.Method != "POST" {
		t.Fatalf("method = %s", d.Method)
	}
	want := srv.URL + "/accounts/" + gatewayPolicyTestAccountID + "/gateway/rules"
	if d.URL != want {
		t.Fatalf("url = %s, want %s", d.URL, want)
	}
	// Enum values are canonicalized to their documented spelling.
	gatewayPolicyAssertJSONEqual(t, d.Body, `{
		"name": "Block malware",
		"action": "block",
		"filters": ["dns"],
		"traffic": "any(dns.domains[*] in $blocked)",
		"description": "from the incident",
		"precedence": 42,
		"enabled": true
	}`)
}

func TestGatewayPolicyRuleCreateOmitsUnsetFields(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "create",
		"Allow finance", "--action", "allow", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	gatewayPolicyAssertJSONEqual(t, d.Body, `{"name":"Allow finance","action":"allow"}`)
}

func TestGatewayPolicyRuleCreateRejectsUnknownEnums(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "action",
			args: []string{"--action", "audit_ssh"},
			want: `invalid --action "audit_ssh"`,
		},
		{
			name: "filter",
			args: []string{"--action", "block", "--filter", "tcp"},
			want: `invalid --filter "tcp"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"gateway", "policy", "rule", "create", "A rule"}, tc.args...)
			_, _, err := runGatewayPolicyCLI(t, srv.URL, args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestGatewayPolicyRuleCreateRejectsBlankName(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "create", "  ",
		"--action", "block")
	if err == nil || !strings.Contains(err.Error(), "rule name must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyRuleCreateRequiresAccount(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	gatewayPolicyIsolateConfig(t)
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{
		"--base-url", srv.URL, "--token", "test-token", "--account-id", "",
		"gateway", "policy", "rule", "create", "A rule", "--action", "block",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("err = %v", err)
	}
}

// --- rule list -------------------------------------------------------------

func TestGatewayPolicyRuleListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/accounts/"+gatewayPolicyTestAccountID+"/gateway/rules" {
			t.Errorf("path = %s", got)
		}
		fmt.Fprint(w, gatewayPolicyEnvelope(`[
			{"id":"r1","name":"Block malware","action":"block","filters":["dns"],"precedence":1,"enabled":true},
			{"id":"r2","name":"Isolate webmail","action":"isolate","filters":["http"],"precedence":2,"enabled":false}
		]`))
	}))
	defer srv.Close()

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "PRECEDENCE", "ENABLED", "Block malware", "isolate", "http", "false"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
}

// The Gateway collections report page/per_page/total_count but no total_pages
// and no cursor, so the shared paginator would stop after page one.
func TestGatewayPolicyRuleListFollowsCountPagination(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		switch r.URL.Query().Get("page") {
		case "":
			fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"r1"},{"id":"r2"}],"result_info":{"page":1,"per_page":2,"count":2,"total_count":3}}`)
		case "2":
			fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"r3"}],"result_info":{"page":2,"per_page":2,"count":1,"total_count":3}}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	gatewayPolicyAssertJSONEqual(t, []byte(stdout), `[{"id":"r1"},{"id":"r2"},{"id":"r3"}]`)
	if len(pages) != 2 || pages[0] != "" || pages[1] != "2" {
		t.Fatalf("requested pages = %v", pages)
	}
}

// If the endpoint ignores ?page the loop must stop instead of re-reading the
// same page until total_count is reached.
func TestGatewayPolicyRuleListStopsWhenPageIgnored(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `{"success":true,"errors":[],"messages":[],"result":[{"id":"r1"},{"id":"r2"}],"result_info":{"page":1,"per_page":2,"count":2,"total_count":100}}`)
	}))
	defer srv.Close()

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	// The repeated page is discarded rather than merged in twice.
	gatewayPolicyAssertJSONEqual(t, []byte(stdout), `[{"id":"r1"},{"id":"r2"}]`)
}

func TestGatewayPolicyRuleListDryRunSendsNothing(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "list", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if d.Method != "GET" || !strings.HasSuffix(d.URL, "/gateway/rules") {
		t.Fatalf("dump = %+v", d)
	}
}

// --- rule update (read-merge-write) ----------------------------------------

const gatewayPolicyStoredRule = `{
	"id": "` + gatewayPolicyTestRuleID + `",
	"name": "Block malware",
	"action": "block",
	"description": "old",
	"enabled": true,
	"precedence": 10,
	"filters": ["dns"],
	"traffic": "any(dns.domains[*] in $blocked)",
	"identity": "",
	"device_posture": "",
	"rule_settings": {"block_page_enabled": true},
	"expiration": {"expires_at": "2026-09-01T00:00:00Z", "duration": 10, "expired": false},
	"future_field": {"kept": true},
	"created_at": "2026-01-01T00:00:00Z",
	"updated_at": "2026-02-01T00:00:00Z",
	"deleted_at": null,
	"version": 3,
	"read_only": false,
	"sharable": true,
	"source_account": "acct",
	"warning_status": "none"
}`

func TestGatewayPolicyRuleUpdateMergesStoredRule(t *testing.T) {
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, gatewayPolicyEnvelope(gatewayPolicyStoredRule))
		case http.MethodPut:
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(r.Body)
			putBody = buf.Bytes()
			fmt.Fprint(w, gatewayPolicyEnvelope(`{"id":"`+gatewayPolicyTestRuleID+`"}`))
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	}))
	defer srv.Close()

	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "update",
		gatewayPolicyTestRuleID, "--precedence", "50", "--description", "reviewed")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatalf("PUT body not JSON: %v", err)
	}
	for _, gone := range []string{"id", "created_at", "updated_at", "deleted_at", "version", "read_only", "sharable", "source_account", "warning_status"} {
		if _, ok := body[gone]; ok {
			t.Fatalf("read-only field %q was sent back", gone)
		}
	}
	if body["precedence"] != float64(50) {
		t.Fatalf("precedence = %v", body["precedence"])
	}
	if body["description"] != "reviewed" {
		t.Fatalf("description = %v", body["description"])
	}
	// Unmodeled and unchanged writable fields survive the round trip.
	if _, ok := body["future_field"]; !ok {
		t.Fatalf("future_field dropped: %v", body)
	}
	settings, ok := body["rule_settings"].(map[string]any)
	if !ok || settings["block_page_enabled"] != true {
		t.Fatalf("rule_settings = %v", body["rule_settings"])
	}
	if body["name"] != "Block malware" || body["action"] != "block" {
		t.Fatalf("required fields lost: %v", body)
	}
	// The read-only marker nested in expiration is stripped.
	expiration, ok := body["expiration"].(map[string]any)
	if !ok {
		t.Fatalf("expiration = %v", body["expiration"])
	}
	if _, ok := expiration["expired"]; ok {
		t.Fatalf("expiration.expired was sent back: %v", expiration)
	}
	if expiration["expires_at"] != "2026-09-01T00:00:00Z" {
		t.Fatalf("expiration lost fields: %v", expiration)
	}
}

func TestGatewayPolicyRuleUpdateDryRunReadsThenDumpsPut(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method != http.MethodGet {
			t.Errorf("dry-run sent %s", r.Method)
		}
		fmt.Fprint(w, gatewayPolicyEnvelope(gatewayPolicyStoredRule))
	}))
	defer srv.Close()

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "update",
		gatewayPolicyTestRuleID, "--action", "ALLOW", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0] != http.MethodGet {
		t.Fatalf("requests = %v, want one GET", methods)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if d.Method != "PUT" {
		t.Fatalf("method = %s", d.Method)
	}
	var body map[string]any
	if err := json.Unmarshal(d.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["action"] != "allow" {
		t.Fatalf("action = %v", body["action"])
	}
}

func TestGatewayPolicyRuleUpdateRequiresAChange(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "update", gatewayPolicyTestRuleID)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyRuleUpdateValidatesBeforeReading(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "update",
		gatewayPolicyTestRuleID, "--filter", "smtp")
	if err == nil || !strings.Contains(err.Error(), `invalid --filter "smtp"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyRuleUpdateRejectsBlankName(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "update",
		gatewayPolicyTestRuleID, "--name", "   ")
	if err == nil || !strings.Contains(err.Error(), "--name must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyRuleUpdateRejectsRuleWithoutRequiredFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s after an incomplete read", r.Method)
		}
		fmt.Fprint(w, gatewayPolicyEnvelope(`{"id":"x","name":"Rule"}`))
	}))
	defer srv.Close()

	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "update",
		gatewayPolicyTestRuleID, "--precedence", "5")
	if err == nil || !strings.Contains(err.Error(), "no action") {
		t.Fatalf("err = %v", err)
	}
}

// --- rule enable / disable / delete ----------------------------------------

func TestGatewayPolicyRuleToggle(t *testing.T) {
	for _, tc := range []struct {
		verb string
		want string
	}{
		{"enable", `{"enabled":true}`},
		{"disable", `{"enabled":false}`},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			srv := gatewayPolicyNoCallServer(t)
			stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", tc.verb,
				gatewayPolicyTestRuleID, "--dry-run")
			if err != nil {
				t.Fatal(err)
			}
			d := gatewayPolicyParseDump(t, stdout)
			if d.Method != "PATCH" {
				t.Fatalf("method = %s", d.Method)
			}
			wantURL := srv.URL + "/accounts/" + gatewayPolicyTestAccountID + "/gateway/rules/" + gatewayPolicyTestRuleID
			if d.URL != wantURL {
				t.Fatalf("url = %s, want %s", d.URL, wantURL)
			}
			gatewayPolicyAssertJSONEqual(t, d.Body, tc.want)
		})
	}
}

func TestGatewayPolicyRuleDeleteNeedsForceWithoutTTY(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "delete", gatewayPolicyTestRuleID)
	if err == nil || !strings.Contains(err.Error(), "aborted (pass --force") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyRuleDeleteWithForce(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		fmt.Fprint(w, gatewayPolicyEnvelope(`{"id":"`+gatewayPolicyTestRuleID+`"}`))
	}))
	defer srv.Close()

	if _, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "delete",
		gatewayPolicyTestRuleID, "--force"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodDelete {
		t.Fatalf("method = %s", method)
	}
	if want := "/accounts/" + gatewayPolicyTestAccountID + "/gateway/rules/" + gatewayPolicyTestRuleID; path != want {
		t.Fatalf("path = %s, want %s", path, want)
	}
}

// --- Zero Trust lists ------------------------------------------------------

func TestGatewayPolicyListListFiltersByType(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "list",
		"--type", "domain", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if !strings.HasSuffix(d.URL, "/gateway/lists?type=DOMAIN") {
		t.Fatalf("url = %s", d.URL)
	}
}

func TestGatewayPolicyListListRejectsUnknownType(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "list", "--type", "hostname")
	if err == nil || !strings.Contains(err.Error(), `invalid --type "hostname"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyListListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, gatewayPolicyEnvelope(`[
			{"id":"l1","name":"blocked_domains","type":"DOMAIN","count":42,"description":"bad stuff"}
		]`))
	}))
	defer srv.Close()

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ITEMS", "blocked_domains", "DOMAIN", "42", "bad stuff"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestGatewayPolicyListCreateWithItems(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "create",
		"blocked_domains", "--type", "domain", "--description", "Known-bad",
		"--item", "evil.example", "--item", "worse.example", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if d.Method != "POST" || !strings.HasSuffix(d.URL, "/gateway/lists") {
		t.Fatalf("dump = %+v", d)
	}
	gatewayPolicyAssertJSONEqual(t, d.Body, `{
		"name": "blocked_domains",
		"type": "DOMAIN",
		"description": "Known-bad",
		"items": [{"value":"evil.example"},{"value":"worse.example"}]
	}`)
}

func TestGatewayPolicyListCreateOmitsEmptyItems(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "create",
		"serials", "--type", "SERIAL", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	gatewayPolicyAssertJSONEqual(t, gatewayPolicyParseDump(t, stdout).Body,
		`{"name":"serials","type":"SERIAL"}`)
}

func TestGatewayPolicyListCreateRejectsBlankItem(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "create",
		"blocked", "--type", "DOMAIN", "--item", " ")
	if err == nil || !strings.Contains(err.Error(), "item values must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyListUpdateMergesStoredList(t *testing.T) {
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			fmt.Fprint(w, gatewayPolicyEnvelope(`{
				"id": "`+gatewayPolicyTestListID+`",
				"name": "blocked",
				"description": "old",
				"type": "DOMAIN",
				"count": 3,
				"items": [{"value":"evil.example"}],
				"future_field": "kept",
				"created_at": "2026-01-01T00:00:00Z",
				"updated_at": "2026-02-01T00:00:00Z"
			}`))
		case http.MethodPut:
			buf := new(bytes.Buffer)
			_, _ = buf.ReadFrom(r.Body)
			putBody = buf.Bytes()
			fmt.Fprint(w, gatewayPolicyEnvelope(`{"id":"`+gatewayPolicyTestListID+`"}`))
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	}))
	defer srv.Close()

	if _, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "update",
		gatewayPolicyTestListID, "--description", "Reviewed"); err != nil {
		t.Fatal(err)
	}
	// name is required by the write schema and comes from the stored list;
	// type is create-only and items have their own endpoint, so neither is
	// echoed back.
	gatewayPolicyAssertJSONEqual(t, putBody,
		`{"name":"blocked","description":"Reviewed","future_field":"kept"}`)
}

func TestGatewayPolicyListUpdateRequiresAChange(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "update", gatewayPolicyTestListID)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyListDeleteNeedsForceWithoutTTY(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "delete", gatewayPolicyTestListID)
	if err == nil || !strings.Contains(err.Error(), "aborted (pass --force") {
		t.Fatalf("err = %v", err)
	}
}

// --- Zero Trust list items -------------------------------------------------

func TestGatewayPolicyListItemAdd(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "item", "add",
		gatewayPolicyTestListID, "evil.example", "worse.example",
		"--description", "2026-08 incident", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if d.Method != "PATCH" {
		t.Fatalf("method = %s", d.Method)
	}
	want := srv.URL + "/accounts/" + gatewayPolicyTestAccountID + "/gateway/lists/" + gatewayPolicyTestListID
	if d.URL != want {
		t.Fatalf("url = %s, want %s", d.URL, want)
	}
	gatewayPolicyAssertJSONEqual(t, d.Body, `{"append":[
		{"value":"evil.example","description":"2026-08 incident"},
		{"value":"worse.example","description":"2026-08 incident"}
	]}`)
}

func TestGatewayPolicyListItemAddRejectsDuplicates(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "item", "add",
		gatewayPolicyTestListID, "evil.example", "evil.example")
	if err == nil || !strings.Contains(err.Error(), `duplicate entry value "evil.example"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyListItemAddNeedsAValue(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "item", "add",
		gatewayPolicyTestListID)
	if err == nil {
		t.Fatal("expected an argument error")
	}
}

func TestGatewayPolicyListItemRemove(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "item", "remove",
		gatewayPolicyTestListID, "evil.example", "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := gatewayPolicyParseDump(t, stdout)
	if d.Method != "PATCH" {
		t.Fatalf("method = %s", d.Method)
	}
	gatewayPolicyAssertJSONEqual(t, d.Body, `{"remove":["evil.example"]}`)
}

func TestGatewayPolicyListItemRemoveNeedsForceWithoutTTY(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "item", "remove",
		gatewayPolicyTestListID, "evil.example")
	if err == nil || !strings.Contains(err.Error(), "aborted (pass --force") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatewayPolicyListItemListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/accounts/" + gatewayPolicyTestAccountID + "/gateway/lists/" + gatewayPolicyTestListID + "/items"
		if r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		fmt.Fprint(w, gatewayPolicyEnvelope(`[
			{"value":"evil.example","description":"incident","created_at":"2026-08-01T00:00:00Z"}
		]`))
	}))
	defer srv.Close()

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "item", "list",
		gatewayPolicyTestListID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"VALUE", "DESCRIPTION", "CREATED", "evil.example", "incident"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
}

// --- identifier length bound -----------------------------------------------

// The spec bounds both rule_id and list_id at maxLength 36 (they resolve to
// the zero-trust-gateway UUID schema). The bound is checked before the client
// is built, so an over-long identifier costs no request.

// gatewayPolicyID36 is exactly 36 code points, the longest identifier the spec
// allows.
const gatewayPolicyID36 = "f174e90a-fafe-4643-bbbc-4a0ed4fc8415"

// gatewayPolicyID37 is one code point over the bound.
const gatewayPolicyID37 = "f174e90a-fafe-4643-bbbc-4a0ed4fc8415e"

// gatewayPolicyLeaf is one command whose first positional argument is a rule
// or list identifier.
type gatewayPolicyLeaf struct {
	name string
	args func(id string) []string
}

// gatewayPolicyIDLeaves are representative leaves across all three identifier
// shapes: a rule ID, a list ID, and a list ID followed by item values.
var gatewayPolicyIDLeaves = []gatewayPolicyLeaf{
	{"rule-get", func(id string) []string { return []string{"gateway", "policy", "rule", "get", id} }},
	{"list-get", func(id string) []string { return []string{"gateway", "policy", "list", "get", id} }},
	{"list-item-add", func(id string) []string {
		return []string{"gateway", "policy", "list", "item", "add", id, "evil.example"}
	}},
}

func TestGatewayPolicyIDAtMaxLengthIsAccepted(t *testing.T) {
	if got := len([]rune(gatewayPolicyID36)); got != 36 {
		t.Fatalf("fixture is %d code points, want 36", got)
	}
	for _, leaf := range gatewayPolicyIDLeaves {
		t.Run(leaf.name, func(t *testing.T) {
			srv := gatewayPolicyNoCallServer(t)
			args := append(leaf.args(gatewayPolicyID36), "--dry-run")
			stdout, _, err := runGatewayPolicyCLI(t, srv.URL, args...)
			if err != nil {
				t.Fatalf("36 code points rejected: %v", err)
			}
			if !strings.Contains(gatewayPolicyParseDump(t, stdout).URL, gatewayPolicyID36) {
				t.Fatalf("identifier missing from URL:\n%s", stdout)
			}
		})
	}
}

func TestGatewayPolicyIDOverMaxLengthIsRejectedWithoutRequest(t *testing.T) {
	if got := len([]rune(gatewayPolicyID37)); got != 37 {
		t.Fatalf("fixture is %d code points, want 37", got)
	}
	for _, leaf := range gatewayPolicyIDLeaves {
		t.Run(leaf.name, func(t *testing.T) {
			// Any request at all fails the test: the bound is enforced
			// before the client is constructed.
			srv := gatewayPolicyNoCallServer(t)
			_, _, err := runGatewayPolicyCLI(t, srv.URL, leaf.args(gatewayPolicyID37)...)
			if err == nil {
				t.Fatal("37 code points accepted")
			}
			if !strings.Contains(err.Error(), "must be at most 36 characters, got 37") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The bound counts code points, not UTF-8 bytes, so a 36-rune multi-byte
// identifier is not rejected for its encoded size.
func TestGatewayPolicyIDLengthCountsCodePointsNotBytes(t *testing.T) {
	id := strings.Repeat("é", 36)
	if len([]rune(id)) != 36 || len(id) != 72 {
		t.Fatalf("fixture is %d code points / %d bytes", len([]rune(id)), len(id))
	}
	srv := gatewayPolicyNoCallServer(t)
	if _, _, err := runGatewayPolicyCLI(t, srv.URL,
		"gateway", "policy", "rule", "get", id, "--dry-run"); err != nil {
		t.Fatalf("36 multi-byte code points rejected: %v", err)
	}
}

func TestGatewayPolicyIDBlankIsRejectedWithoutRequest(t *testing.T) {
	srv := gatewayPolicyNoCallServer(t)
	_, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "get", "   ")
	if err == nil || !strings.Contains(err.Error(), "list ID must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

// --- exact live requests ---------------------------------------------------

// gatewayPolicyRecordedRequest is what a leaf actually put on the wire.
type gatewayPolicyRecordedRequest struct {
	method   string
	path     string
	rawQuery string
	body     string
	calls    int
}

// gatewayPolicyRecordingServer records every request and replies with a
// minimal success envelope.
func gatewayPolicyRecordingServer(t *testing.T, rec *gatewayPolicyRecordedRequest, result string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		rec.calls++
		rec.method, rec.path, rec.rawQuery, rec.body = r.Method, r.URL.Path, r.URL.RawQuery, buf.String()
		fmt.Fprint(w, gatewayPolicyEnvelope(result))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGatewayPolicyRuleGetSendsExactRequest(t *testing.T) {
	var rec gatewayPolicyRecordedRequest
	srv := gatewayPolicyRecordingServer(t, &rec, `{"id":"`+gatewayPolicyTestRuleID+`","name":"Block malware"}`)

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "rule", "get", gatewayPolicyTestRuleID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want 1", rec.calls)
	}
	if rec.method != http.MethodGet {
		t.Fatalf("method = %s", rec.method)
	}
	if want := "/accounts/" + gatewayPolicyTestAccountID + "/gateway/rules/" + gatewayPolicyTestRuleID; rec.path != want {
		t.Fatalf("path = %s, want %s", rec.path, want)
	}
	if rec.rawQuery != "" {
		t.Fatalf("query = %q, want none", rec.rawQuery)
	}
	if rec.body != "" {
		t.Fatalf("body = %q, want none", rec.body)
	}
	// get renders the result as JSON by default.
	gatewayPolicyAssertJSONEqual(t, []byte(stdout), `{"id":"`+gatewayPolicyTestRuleID+`","name":"Block malware"}`)
}

func TestGatewayPolicyListGetSendsExactRequest(t *testing.T) {
	var rec gatewayPolicyRecordedRequest
	srv := gatewayPolicyRecordingServer(t, &rec, `{"id":"`+gatewayPolicyTestListID+`","name":"blocked","type":"DOMAIN"}`)

	stdout, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "get", gatewayPolicyTestListID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want 1", rec.calls)
	}
	if rec.method != http.MethodGet {
		t.Fatalf("method = %s", rec.method)
	}
	if want := "/accounts/" + gatewayPolicyTestAccountID + "/gateway/lists/" + gatewayPolicyTestListID; rec.path != want {
		t.Fatalf("path = %s, want %s", rec.path, want)
	}
	if rec.rawQuery != "" {
		t.Fatalf("query = %q, want none", rec.rawQuery)
	}
	if rec.body != "" {
		t.Fatalf("body = %q, want none", rec.body)
	}
	gatewayPolicyAssertJSONEqual(t, []byte(stdout),
		`{"id":"`+gatewayPolicyTestListID+`","name":"blocked","type":"DOMAIN"}`)
}

// --force skips the confirmation and sends the real DELETE.
func TestGatewayPolicyListDeleteWithForceSendsExactRequest(t *testing.T) {
	var rec gatewayPolicyRecordedRequest
	srv := gatewayPolicyRecordingServer(t, &rec, `{"id":"`+gatewayPolicyTestListID+`"}`)

	if _, _, err := runGatewayPolicyCLI(t, srv.URL, "gateway", "policy", "list", "delete",
		gatewayPolicyTestListID, "--force"); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 1 {
		t.Fatalf("calls = %d, want 1", rec.calls)
	}
	if rec.method != http.MethodDelete {
		t.Fatalf("method = %s", rec.method)
	}
	if want := "/accounts/" + gatewayPolicyTestAccountID + "/gateway/lists/" + gatewayPolicyTestListID; rec.path != want {
		t.Fatalf("path = %s, want %s", rec.path, want)
	}
	if rec.rawQuery != "" {
		t.Fatalf("query = %q, want none", rec.rawQuery)
	}
	if rec.body != "" {
		t.Fatalf("body = %q, want none", rec.body)
	}
}

// --- unit-level helpers ----------------------------------------------------

func TestGatewayPolicyCanonicalAcceptsEveryDocumentedValue(t *testing.T) {
	for _, v := range gatewayPolicyActions {
		got, err := gatewayPolicyCanonical("action", strings.ToUpper(v), gatewayPolicyActions)
		if err != nil || got != v {
			t.Fatalf("action %q -> %q, %v", v, got, err)
		}
	}
	for _, v := range gatewayPolicyFilters {
		got, err := gatewayPolicyCanonical("filter", strings.ToUpper(v), gatewayPolicyFilters)
		if err != nil || got != v {
			t.Fatalf("filter %q -> %q, %v", v, got, err)
		}
	}
	for _, v := range gatewayPolicyListTypes {
		got, err := gatewayPolicyCanonical("type", strings.ToLower(v), gatewayPolicyListTypes)
		if err != nil || got != v {
			t.Fatalf("type %q -> %q, %v", v, got, err)
		}
	}
}

func TestGatewayPolicyRequireIDBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		id      string
		wantErr string
	}{
		{"empty", "", "rule ID must not be empty"},
		{"blank", "\t ", "rule ID must not be empty"},
		{"one", "x", ""},
		{"at bound", strings.Repeat("a", 36), ""},
		{"over bound", strings.Repeat("a", 37), "must be at most 36 characters, got 37"},
		{"multi-byte at bound", strings.Repeat("é", 36), ""},
		{"multi-byte over bound", strings.Repeat("é", 37), "must be at most 36 characters, got 37"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := gatewayPolicyRequireID("rule ID", tc.id)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %s", err, tc.wantErr)
			}
		})
	}
}

func TestGatewayPolicyReplacementBodyKeepsUnknownFields(t *testing.T) {
	body, err := gatewayPolicyReplacementBody(
		map[string]any{"id": "x", "name": "n", "unmodeled": 1},
		gatewayPolicyRuleServerFields,
		map[string]any{"name": "new"},
	)
	if err != nil {
		t.Fatal(err)
	}
	gatewayPolicyAssertJSONEqual(t, body, `{"name":"new","unmodeled":1}`)
}
