package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const rulesetsTestAccountID = "account-test"
const rulesetsTestZoneID = "0123456789abcdef0123456789abcdef"
const rulesetsTestID = "2f2feab2026849078ba485f918791bdc"
const rulesetsTestRuleID = "3a03d665bac047339bb530ecb439a90d"

func runRulesetsCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token"}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestRulesetsCreateBodyUsesCanonicalWireValues(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.Flags().String("description", "", "")
	cmd.Flags().String("rules", "", "")
	if err := cmd.Flags().Set("description", ""); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("rules", `[{"action":"block","expression":"ip.src eq 192.0.2.1"}]`); err != nil {
		t.Fatal(err)
	}
	body, err := buildRulesetsCreateBody(cmd, rulesetsCreateOptions{
		name:        "custom firewall",
		kind:        "root",
		phase:       "http_request_firewall_custom",
		description: "",
		rules:       `[{"action":"block","expression":"ip.src eq 192.0.2.1"}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	rulesetsAssertJSONEqual(t, body, `{"name":"custom firewall","kind":"root","phase":"http_request_firewall_custom","description":"","rules":[{"action":"block","expression":"ip.src eq 192.0.2.1"}]}`)

	for _, tc := range []struct {
		flag, value string
	}{
		{"kind", "ROOT"},
		{"phase", "HTTP_REQUEST_FIREWALL_CUSTOM"},
	} {
		options := rulesetsCreateOptions{name: "name", kind: "root", phase: "http_request_firewall_custom"}
		if tc.flag == "kind" {
			options.kind = tc.value
		} else {
			options.phase = tc.value
		}
		if _, err := buildRulesetsCreateBody(&cobra.Command{}, options); err == nil || !strings.Contains(err.Error(), "--"+tc.flag) {
			t.Fatalf("%s=%q: expected canonical value error, got %v", tc.flag, tc.value, err)
		}
	}
}

func TestRulesetsJSONShapeValidation(t *testing.T) {
	cmd := &cobra.Command{}
	for _, raw := range []string{"null", "[]", `"rule"`, "false"} {
		if _, err := parseRulesetsJSONObject(cmd, "rule", raw); err == nil || !strings.Contains(err.Error(), "--rule must be a JSON object") {
			t.Errorf("object %s: got %v", raw, err)
		}
	}
	for _, raw := range []string{"null", "{}", `"rules"`, "false"} {
		if _, err := parseRulesetsJSONArray(cmd, "rules", raw); err == nil || !strings.Contains(err.Error(), "--rules must be a JSON array") {
			t.Errorf("array %s: got %v", raw, err)
		}
	}
	if _, err := buildRulesetsRuleBody(cmd, "{}"); err == nil || !strings.Contains(err.Error(), "must not be an empty") {
		t.Fatalf("expected empty rule error, got %v", err)
	}
	for _, raw := range []string{
		`[{"action":"block"},null]`,
		`[{"position":[]}]`,
		`[{"position":{"index":0}}]`,
	} {
		rules, err := parseRulesetsJSONArray(cmd, "rules", raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRulesetsRules(rules); err == nil {
			t.Errorf("rules %s: expected validation error", raw)
		}
	}
	for _, index := range []string{"1", "2"} {
		rules, err := parseRulesetsJSONArray(cmd, "rules", `[{"position":{"index":`+index+`}}]`)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateRulesetsRules(rules); err != nil {
			t.Errorf("position index %s rejected: %v", index, err)
		}
	}
}

func TestRulesetsRejectInvalidInputBeforeHTTP(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cases := [][]string{
		{"--account-id", rulesetsTestAccountID, "rulesets", "create", "--name", "test", "--kind", "root", "--phase", "http_request_firewall_custom", "--rules", "null"},
		{"--account-id", rulesetsTestAccountID, "rulesets", "rule", "add", rulesetsTestID, "--rule", "[]"},
		{"--account-id", rulesetsTestAccountID, "rulesets", "entrypoint", "update", "http_request_firewall_custom", "--rules", "{}"},
		{"--account-id", rulesetsTestAccountID, "rulesets", "get", "not-an-id"},
		{"--account-id", rulesetsTestAccountID, "rulesets", "list", "--scope", "wrong"},
	}
	for _, args := range cases {
		_, _, err := runRulesetsCLI(t, srv.URL, args...)
		if err == nil {
			t.Errorf("args %q: expected error", args)
		}
	}
	if requests != 0 {
		t.Fatalf("made %d unexpected requests", requests)
	}
}

func TestRulesetsListPerPageBoundsAndAccountTable(t *testing.T) {
	for _, value := range []string{"0", "51"} {
		_, _, err := runRulesetsCLI(t, "http://example.invalid", "--account-id", rulesetsTestAccountID,
			"rulesets", "list", "--per-page", value, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "--per-page must be between 1 and 50") {
			t.Errorf("per-page %s: expected bounds error, got %v", value, err)
		}
	}
	for _, value := range []string{"1", "50"} {
		stdout, _, err := runRulesetsCLI(t, "http://example.invalid", "--account-id", rulesetsTestAccountID,
			"rulesets", "list", "--per-page", value, "--dry-run")
		if err != nil {
			t.Fatalf("per-page %s: %v", value, err)
		}
		if !strings.Contains(stdout, "per_page="+value) {
			t.Errorf("per-page %s not serialized in %s", value, stdout)
		}
	}

	var gotPath, gotPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotPerPage = r.URL.Path, r.URL.Query().Get("per_page")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + rulesetsTestID + `","name":"custom firewall","kind":"root","phase":"http_request_firewall_custom","version":"1"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID, "rulesets", "list")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+rulesetsTestAccountID+"/rulesets" || gotPerPage != "50" {
		t.Fatalf("request = %s?per_page=%s", gotPath, gotPerPage)
	}
	for _, want := range []string{"ID", "NAME", "KIND", "custom firewall", "http_request_firewall_custom"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestRulesetsListJSONRendering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + rulesetsTestID + `","name":"custom firewall"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID, "--output", "json", "rulesets", "list")
	if err != nil {
		t.Fatal(err)
	}
	var rulesets []rulesetSummary
	if err := json.Unmarshal([]byte(stdout), &rulesets); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, stdout)
	}
	if len(rulesets) != 1 || rulesets[0].ID != rulesetsTestID {
		t.Fatalf("rulesets = %#v", rulesets)
	}
}

func TestRulesetsCreateZoneDryRun(t *testing.T) {
	stdout, _, err := runRulesetsCLI(t, "http://example.invalid",
		"rulesets", "create", "--scope", "zone", "--zone", rulesetsTestZoneID,
		"--name", "redirects", "--kind", "zone", "--phase", "http_request_dynamic_redirect",
		"--rules", `[{"action":"redirect","expression":"true"}]`, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/zones/"+rulesetsTestZoneID+"/rulesets") {
		t.Fatalf("dump = %+v", dump)
	}
	rulesetsAssertJSONEqual(t, dump.Body, `{"name":"redirects","kind":"zone","phase":"http_request_dynamic_redirect","rules":[{"action":"redirect","expression":"true"}]}`)
}

func TestRulesetsZoneNameResolutionAndRuleEditEndpoint(t *testing.T) {
	var sawLookup, sawEdit bool
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zones":
			sawLookup = true
			if r.URL.Query().Get("name") != "example.com" {
				t.Errorf("lookup name = %q", r.URL.Query().Get("name"))
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + rulesetsTestZoneID + `","name":"example.com"}]}`))
		case "/zones/" + rulesetsTestZoneID + "/rulesets/" + rulesetsTestID + "/rules/" + rulesetsTestRuleID:
			sawEdit = true
			if r.Method != "PATCH" {
				t.Errorf("method = %s", r.Method)
			}
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + rulesetsTestID + `","version":"2"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := runRulesetsCLI(t, srv.URL, "rulesets", "rule", "edit", rulesetsTestID, rulesetsTestRuleID,
		"--scope", "zone", "--zone", "example.com", "--rule", `{"enabled":false}`)
	if err != nil {
		t.Fatal(err)
	}
	if !sawLookup || !sawEdit {
		t.Fatalf("lookup=%v edit=%v", sawLookup, sawEdit)
	}
	rulesetsAssertJSONEqual(t, gotBody, `{"enabled":false}`)
}

func TestRulesetsZoneScopeUsesInteractiveResolverWhenNoZoneIsConfigured(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runRulesetsCLI(t, srv.URL, "rulesets", "list", "--scope", "zone", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "run interactively to pick one") {
		t.Fatalf("expected interactive zone resolver error, got %v", err)
	}
	if requests != 0 {
		t.Fatalf("made %d unexpected requests", requests)
	}
}

func TestRulesetsRuleAddDeleteAndDeleteSafety(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + rulesetsTestID + `","version":"2"}}`))
	}))
	defer srv.Close()

	_, _, err := runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID,
		"rulesets", "rule", "add", rulesetsTestID, "--rule", `{"action":"block","expression":"true"}`)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID,
		"rulesets", "rule", "delete", rulesetsTestID, rulesetsTestRuleID, "--force")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(requests, ";"), "POST /accounts/"+rulesetsTestAccountID+"/rulesets/"+rulesetsTestID+"/rules;DELETE /accounts/"+rulesetsTestAccountID+"/rulesets/"+rulesetsTestID+"/rules/"+rulesetsTestRuleID; got != want {
		t.Fatalf("requests = %s, want %s", got, want)
	}

	requests = nil
	_, _, err = runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID,
		"rulesets", "delete", rulesetsTestID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force error, got %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("delete without force made request %v", requests)
	}
}

func TestRulesetsEntrypointGetAndUpdateEndpoints(t *testing.T) {
	var methods, bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + rulesetsTestID + `","phase":"http_request_firewall_custom","version":"2"}}`))
	}))
	defer srv.Close()

	_, _, err := runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID,
		"rulesets", "entrypoint", "get", "http_request_firewall_custom")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runRulesetsCLI(t, srv.URL, "--account-id", rulesetsTestAccountID,
		"rulesets", "entrypoint", "update", "http_request_firewall_custom", "--description", "updated", "--rules", `[]`)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/accounts/" + rulesetsTestAccountID + "/rulesets/phases/http_request_firewall_custom/entrypoint"
	if got, want := strings.Join(methods, ";"), "GET "+wantPath+";PUT "+wantPath; got != want {
		t.Fatalf("requests = %s, want %s", got, want)
	}
	rulesetsAssertJSONEqual(t, []byte(bodies[1]), `{"description":"updated","rules":[]}`)
}

func rulesetsAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid expected JSON %s: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}
