package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	accessAppsTestAccountID = "account-test"
	accessAppsTestZoneID    = "0123456789abcdef0123456789abcdef"
	accessAppsTestAppID     = "f174e90a-fafe-4643-bbbc-4a0ed4fc8415"
	accessAppsTestPolicyID  = "699d98642c564d2e855e9661899b7252"
)

func runAccessAppsCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runAccessAppsCLIWithStdin(t, serverURL, nil, args...)
}

func runAccessAppsCLIWithStdin(t *testing.T, serverURL string, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runAccessAppsCLIRaw(t, stdin, append([]string{"--base-url", serverURL, "--token", "test-token"}, args...)...)
}

// runAccessAppsCLIRaw drives the real command tree with exactly the arguments
// given — no token, no base URL — for checks about what happens before a
// client is built.
func runAccessAppsCLIRaw(t *testing.T, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	if stdin != nil {
		root.SetIn(stdin)
	}
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// accessAppsIsolateConfig points config resolution at an empty directory and
// clears the credential environment, so scope resolution in a test depends
// only on the flags that test passes.
func accessAppsIsolateConfig(t *testing.T) {
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

func accessAppsAssertJSONEqual(t *testing.T, got []byte, want string) {
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

// accessAppsFlag is one --flag=value pair; slices keep the order that
// repeatable flags depend on.
type accessAppsFlag struct{ name, value string }

func accessAppsSetFlags(t *testing.T, cmd *cobra.Command, flags []accessAppsFlag) {
	t.Helper()
	for _, f := range flags {
		if err := cmd.Flags().Set(f.name, f.value); err != nil {
			t.Fatalf("set --%s=%s: %v", f.name, f.value, err)
		}
	}
}

func newAccessAppTestCmd(t *testing.T, create bool, flags ...accessAppsFlag) (*cobra.Command, *accessAppOptions) {
	t.Helper()
	cmd := &cobra.Command{}
	o := &accessAppOptions{}
	addAccessAppFlags(cmd, o, create)
	accessAppsSetFlags(t, cmd, flags)
	return cmd, o
}

func newAccessPolicyTestCmd(t *testing.T, flags ...accessAppsFlag) (*cobra.Command, *accessPolicyOptions) {
	t.Helper()
	cmd := &cobra.Command{}
	o := &accessPolicyOptions{}
	addAccessPolicyFlags(cmd, o)
	accessAppsSetFlags(t, cmd, flags)
	return cmd, o
}

// --- application create body ------------------------------------------------

func TestBuildAccessAppCreateBody(t *testing.T) {
	cmd, o := newAccessAppTestCmd(t, true, []accessAppsFlag{
		{"domain", "wiki.example.com"},
		{"session-duration", "2h45m"},
		{"allowed-idp", "699d98642c564d2e855e9661899b7252"},
		{"allowed-idp", "aa0a4aab-672b-4bdb-bc33-a59f1130a11f"},
		{"tag", "engineering"},
		{"logo-url", "https://cdn.example.com/logo.png"},
		{"custom-deny-message", "Ask #access for help"},
		{"custom-deny-url", "https://example.com/denied"},
		{"app-launcher-visible", "false"},
		{"auto-redirect-to-identity", "true"},
		{"enable-binding-cookie", "true"},
		{"http-only-cookie-attribute", "false"},
		{"skip-interstitial", "true"},
	}...)
	body, err := buildAccessAppCreateBody(cmd, "Internal wiki", *o)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{
		"name": "Internal wiki",
		"type": "self_hosted",
		"domain": "wiki.example.com",
		"session_duration": "2h45m",
		"allowed_idps": ["699d98642c564d2e855e9661899b7252","aa0a4aab-672b-4bdb-bc33-a59f1130a11f"],
		"tags": ["engineering"],
		"logo_url": "https://cdn.example.com/logo.png",
		"custom_deny_message": "Ask #access for help",
		"custom_deny_url": "https://example.com/denied",
		"app_launcher_visible": false,
		"auto_redirect_to_identity": true,
		"enable_binding_cookie": true,
		"http_only_cookie_attribute": false,
		"skip_interstitial": true
	}`)
}

func TestBuildAccessAppCreateBodyOnlySendsWhatWasPassed(t *testing.T) {
	cmd, o := newAccessAppTestCmd(t, true, accessAppsFlag{"domain", "wiki.example.com"})
	body, err := buildAccessAppCreateBody(cmd, "Internal wiki", *o)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{"name":"Internal wiki","type":"self_hosted","domain":"wiki.example.com"}`)
}

func TestBuildAccessAppCreateBodyBookmarkAndAssignedDomainTypes(t *testing.T) {
	cmd, o := newAccessAppTestCmd(t, true,
		accessAppsFlag{"type", "bookmark"},
		accessAppsFlag{"domain", "https://runbooks.example.com"},
	)
	body, err := buildAccessAppCreateBody(cmd, "Runbooks", *o)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{"name":"Runbooks","type":"bookmark","domain":"https://runbooks.example.com"}`)

	// app_launcher gets its domain from Cloudflare, so none is sent.
	cmd, o = newAccessAppTestCmd(t, true, accessAppsFlag{"type", "app_launcher"})
	body, err = buildAccessAppCreateBody(cmd, "App Launcher", *o)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{"name":"App Launcher","type":"app_launcher"}`)

	// mcp_portal and proxy_endpoint accept a domain without requiring one.
	for _, appType := range []string{"mcp_portal", "proxy_endpoint"} {
		cmd, o = newAccessAppTestCmd(t, true, accessAppsFlag{"type", appType})
		if _, err := buildAccessAppCreateBody(cmd, "Portal", *o); err != nil {
			t.Fatalf("%s without a domain: %v", appType, err)
		}
		cmd, o = newAccessAppTestCmd(t, true,
			accessAppsFlag{"type", appType},
			accessAppsFlag{"domain", "portal.example.com"},
		)
		body, err = buildAccessAppCreateBody(cmd, "Portal", *o)
		if err != nil {
			t.Fatalf("%s with a domain: %v", appType, err)
		}
		accessAppsAssertJSONEqual(t, body, `{"name":"Portal","type":"`+appType+`","domain":"portal.example.com"}`)
	}
}

func TestBuildAccessAppCreateBodyValidation(t *testing.T) {
	cases := []struct {
		name  string
		flags []accessAppsFlag
		app   string
		want  string
	}{
		{"empty name", []accessAppsFlag{{"domain", "wiki.example.com"}}, "  ", "name must not be empty"},
		{"unknown type", []accessAppsFlag{{"type", "kubernetes"}}, "App", "unknown --type"},
		{"saas needs config", []accessAppsFlag{{"type", "saas"}}, "Okta", "saas_app configuration"},
		{"infrastructure needs targets", []accessAppsFlag{{"type", "infrastructure"}}, "Fleet", "target_criteria"},
		{"rdp needs targets", []accessAppsFlag{{"type", "rdp"}}, "Desktop", "target_criteria"},
		{"self hosted needs domain", nil, "Wiki", "--domain is required for self_hosted"},
		{"ssh needs domain", []accessAppsFlag{{"type", "ssh"}}, "Bastion", "--domain is required for ssh"},
		{"required domain empty", []accessAppsFlag{{"domain", " "}}, "Wiki", "--domain must not be empty"},
		{"warp rejects domain", []accessAppsFlag{{"type", "warp"}, {"domain", "x.example.com"}}, "Enrollment", "not supported for warp"},
		{"bad session duration", []accessAppsFlag{{"domain", "w.example.com"}, {"session-duration", "45"}}, "Wiki", "--session-duration must be a duration"},
		{"empty allowed idp", []accessAppsFlag{{"domain", "w.example.com"}, {"allowed-idp", " "}}, "Wiki", "--allowed-idp value at position 1 is empty"},
		{"empty tag", []accessAppsFlag{{"domain", "w.example.com"}, {"tag", ""}}, "Wiki", "--tag value at position 1 is empty"},
		{"relative logo url", []accessAppsFlag{{"domain", "w.example.com"}, {"logo-url", "/logo.png"}}, "Wiki", "--logo-url must be an absolute http or https URL"},
		{"non http deny url", []accessAppsFlag{{"domain", "w.example.com"}, {"custom-deny-url", "ftp://example.com/denied"}}, "Wiki", "--custom-deny-url must be an absolute http or https URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, o := newAccessAppTestCmd(t, true, tc.flags...)
			_, err := buildAccessAppCreateBody(cmd, tc.app, *o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// Clearing an optional field is different from never passing it: bookmark
// domains and deny messages may legitimately be set to the empty string.
func TestBuildAccessAppCreateBodyKeepsExplicitEmptyValues(t *testing.T) {
	cmd, o := newAccessAppTestCmd(t, true,
		accessAppsFlag{"type", "bookmark"},
		accessAppsFlag{"domain", ""},
		accessAppsFlag{"custom-deny-message", ""},
		accessAppsFlag{"logo-url", ""},
	)
	body, err := buildAccessAppCreateBody(cmd, "Runbooks", *o)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{"name":"Runbooks","type":"bookmark","domain":"","custom_deny_message":"","logo_url":""}`)
}

func TestValidateAccessAppsDurationBounds(t *testing.T) {
	for _, valid := range []string{"300ms", "2h45m", "24h", "730h", "1.5h", "1ns"} {
		if err := validateAccessAppsDuration("session-duration", valid); err != nil {
			t.Errorf("%q rejected: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "  ", "45", "1d", "0s", "0", "-1h", "+3h", "2 h"} {
		if err := validateAccessAppsDuration("session-duration", invalid); err == nil {
			t.Errorf("%q accepted", invalid)
		}
	}
}

func TestAccessAppsIDValidation(t *testing.T) {
	if _, err := accessAppsID("application", "  "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty ID error = %v", err)
	}
	if _, err := accessAppsID("policy", "apps/../zones"); err == nil || !strings.Contains(err.Error(), "invalid policy ID") {
		t.Fatalf("path-like ID error = %v", err)
	}
	got, err := accessAppsID("application", "  "+accessAppsTestAppID+"  ")
	if err != nil || got != accessAppsTestAppID {
		t.Fatalf("id = %q, err = %v", got, err)
	}
}

// --- policy body ------------------------------------------------------------

func TestAccessPolicyFieldChangesRuleWireFormat(t *testing.T) {
	cmd, o := newAccessPolicyTestCmd(t,
		accessAppsFlag{"name", "Engineers"},
		accessAppsFlag{"decision", "allow"},
		accessAppsFlag{"include", `[{"ip":{"ip":"198.51.100.0/24"}}]`},
		accessAppsFlag{"include-everyone", "true"},
		accessAppsFlag{"include-email", "oncall@example.com"},
		accessAppsFlag{"include-email-domain", "example.com"},
		accessAppsFlag{"include-group", "aa0a4aab-672b-4bdb-bc33-a59f1130a11f"},
		accessAppsFlag{"exclude", `[{"email":{"email":"contractor@example.com"}}]`},
		accessAppsFlag{"require", `[{"any_valid_service_token":{}}]`},
		accessAppsFlag{"precedence", "2"},
		accessAppsFlag{"session-duration", "30m"},
		accessAppsFlag{"purpose-justification-required", "true"},
		accessAppsFlag{"purpose-justification-prompt", "Why do you need this?"},
		accessAppsFlag{"approval-required", "true"},
		accessAppsFlag{"isolation-required", "false"},
	)
	changes, err := accessPolicyFieldChanges(cmd, *o)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{
		"name": "Engineers",
		"decision": "allow",
		"include": [
			{"ip":{"ip":"198.51.100.0/24"}},
			{"everyone":{}},
			{"email":{"email":"oncall@example.com"}},
			{"email_domain":{"domain":"example.com"}},
			{"group":{"id":"aa0a4aab-672b-4bdb-bc33-a59f1130a11f"}}
		],
		"exclude": [{"email":{"email":"contractor@example.com"}}],
		"require": [{"any_valid_service_token":{}}],
		"precedence": 2,
		"session_duration": "30m",
		"purpose_justification_required": true,
		"purpose_justification_prompt": "Why do you need this?",
		"approval_required": true,
		"isolation_required": false
	}`)

	// The include list has to keep the documented order: JSON rules, then
	// everyone, email, email-domain, group.
	if !strings.Contains(string(body), `[{"ip":{"ip":"198.51.100.0/24"}},{"everyone":{}},{"email":{"email":"oncall@example.com"}}`) {
		t.Fatalf("include order changed: %s", body)
	}
}

func TestAccessPolicyFieldChangesEmptyListsClearExcludeAndRequire(t *testing.T) {
	cmd, o := newAccessPolicyTestCmd(t,
		accessAppsFlag{"exclude", "[]"},
		accessAppsFlag{"require", "[]"},
	)
	changes, err := accessPolicyFieldChanges(cmd, *o)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{"exclude":[],"require":[]}`)
}

func TestAccessPolicyFieldChangesValidation(t *testing.T) {
	cases := []struct {
		name  string
		flags []accessAppsFlag
		want  string
	}{
		{"empty name", []accessAppsFlag{{"name", "  "}}, "--name must not be empty"},
		{"unknown decision", []accessAppsFlag{{"decision", "maybe"}}, "unknown --decision"},
		{"null include", []accessAppsFlag{{"include", "null"}}, "--include must be a JSON array of rule objects"},
		{"object include", []accessAppsFlag{{"include", `{"everyone":{}}`}}, "--include must be a JSON array of rule objects"},
		{"string include", []accessAppsFlag{{"include", `"everyone"`}}, "--include must be a JSON array of rule objects"},
		{"invalid JSON include", []accessAppsFlag{{"include", "[{"}}, "--include must be a JSON array of rule objects"},
		{"null rule element", []accessAppsFlag{{"include", "[null]"}}, "--include rule 1 must be a non-empty JSON object"},
		{"scalar rule element", []accessAppsFlag{{"include", `["everyone"]`}}, "--include rule 1 must be a non-empty JSON object"},
		{"empty rule element", []accessAppsFlag{{"include", `[{"everyone":{}},{}]`}}, "--include rule 2 must be a non-empty JSON object"},
		{"empty include", []accessAppsFlag{{"include", "[]"}}, "--include must contain at least one rule"},
		{"null exclude", []accessAppsFlag{{"exclude", "null"}}, "--exclude must be a JSON array of rule objects"},
		{"null require", []accessAppsFlag{{"require", "null"}}, "--require must be a JSON array of rule objects"},
		{"empty include flag", []accessAppsFlag{{"include", ""}}, "--include must not be empty"},
		{"empty email", []accessAppsFlag{{"include-email", " "}}, "--include-email value at position 1 is empty"},
		{"email without user", []accessAppsFlag{{"include-email", "@example.com"}}, "is not an email address"},
		{"email without domain", []accessAppsFlag{{"include-email", "oncall@"}}, "is not an email address"},
		{"domain given as email", []accessAppsFlag{{"include-email-domain", "oncall@example.com"}}, "pass just the domain"},
		{"group with whitespace", []accessAppsFlag{{"include-group", "aa0a4aab 672b"}}, "must not contain whitespace"},
		{"precedence below minimum", []accessAppsFlag{{"include-everyone", "true"}, {"precedence", "0"}}, "--precedence must be 1 or greater"},
		{"negative precedence", []accessAppsFlag{{"include-everyone", "true"}, {"precedence", "-1"}}, "--precedence must be 1 or greater"},
		{"bad session duration", []accessAppsFlag{{"session-duration", "forever"}}, "--session-duration must be a duration"},
		{"two stdin rule flags", []accessAppsFlag{{"include", "@-"}, {"exclude", "@-"}}, "cannot all read stdin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, o := newAccessPolicyTestCmd(t, tc.flags...)
			if _, err := accessPolicyFieldChanges(cmd, *o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestAccessPolicyPrecedenceAcceptsMinimum(t *testing.T) {
	cmd, o := newAccessPolicyTestCmd(t,
		accessAppsFlag{"include-everyone", "true"},
		accessAppsFlag{"precedence", strconv.Itoa(accessAppsMinPrecedence)},
	)
	changes, err := accessPolicyFieldChanges(cmd, *o)
	if err != nil {
		t.Fatal(err)
	}
	if changes["precedence"] != accessAppsMinPrecedence {
		t.Fatalf("precedence = %v", changes["precedence"])
	}
}

// --include-everyone=false is not a rule; it must not turn into an empty
// include list that would wipe a policy.
func TestAccessPolicyIncludeEveryoneFalseAddsNothing(t *testing.T) {
	cmd, o := newAccessPolicyTestCmd(t, accessAppsFlag{"include-everyone", "false"})
	changes, err := accessPolicyFieldChanges(cmd, *o)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := changes["include"]; ok {
		t.Fatalf("include set from --include-everyone=false: %v", changes)
	}
}

func TestAccessPolicyRulesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "include.json")
	if err := os.WriteFile(path, []byte(`[{"email_domain":{"domain":"example.com"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, o := newAccessPolicyTestCmd(t, accessAppsFlag{"include", "@" + path})
	changes, err := accessPolicyFieldChanges(cmd, *o)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{"include":[{"email_domain":{"domain":"example.com"}}]}`)

	cmd, o = newAccessPolicyTestCmd(t, accessAppsFlag{"include", "@" + path + ".missing"})
	if _, err := accessPolicyFieldChanges(cmd, *o); err == nil || !strings.Contains(err.Error(), "read --include from") {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestAccessAppsReplacementBodyDropsReadOnlyFields(t *testing.T) {
	current := map[string]any{
		"id":         accessAppsTestAppID,
		"aud":        "737646a56ab1df6ec9bddc7e5ca84eaf3b0768850f3ffb5d74f1534911fe3893",
		"created_at": "2024-01-01T00:00:00Z",
		"updated_at": "2024-01-02T00:00:00Z",
		"policies":   []any{map[string]any{"id": accessAppsTestPolicyID}},
		"name":       "Internal wiki",
		"type":       "self_hosted",
		"domain":     "wiki.example.com",
		"tags":       []any{"engineering"},
	}
	body, err := accessAppsReplacementBody(current, accessAppReadOnlyFields, map[string]any{"session_duration": "12h"})
	if err != nil {
		t.Fatal(err)
	}
	accessAppsAssertJSONEqual(t, body, `{
		"name": "Internal wiki",
		"type": "self_hosted",
		"domain": "wiki.example.com",
		"tags": ["engineering"],
		"session_duration": "12h"
	}`)
}

// --- command tree over httptest --------------------------------------------

func TestAccessAppsListAccountHTTPAndTable(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + accessAppsTestAppID + `","name":"Internal wiki","type":"self_hosted","domain":"wiki.example.com","aud":"737646a5"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "list", "--name", "Internal wiki", "--exact")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+accessAppsTestAccountID+"/access/apps" {
		t.Errorf("path = %s", gotPath)
	}
	if gotQuery != "exact=true&name=Internal+wiki" {
		t.Errorf("query = %s", gotQuery)
	}
	for _, want := range []string{"ID", "NAME", "TYPE", "DOMAIN", "AUD", "Internal wiki", "wiki.example.com"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestAccessAppsListJSONAndQueryRendering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + accessAppsTestAppID + `","name":"Internal wiki"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID, "--output", "json",
		"access", "app", "list")
	if err != nil {
		t.Fatal(err)
	}
	var apps []accessApp
	if err := json.Unmarshal([]byte(stdout), &apps); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, stdout)
	}
	if len(apps) != 1 || apps[0].Name != "Internal wiki" {
		t.Fatalf("apps = %#v", apps)
	}

	stdout, _, err = runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID, "--query", ".[0].id",
		"access", "app", "list")
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal([]byte(stdout), &id); err != nil {
		t.Fatalf("query output is not JSON: %v\n%s", err, stdout)
	}
	if id != accessAppsTestAppID {
		t.Fatalf("id = %q", id)
	}
}

func TestAccessAppsZoneScopeResolvesZoneName(t *testing.T) {
	accessAppsIsolateConfig(t)
	var sawLookup, sawApp bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zones":
			sawLookup = true
			if got := r.URL.Query().Get("name"); got != "example.com" {
				t.Errorf("lookup name = %q", got)
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + accessAppsTestZoneID + `","name":"example.com"}]}`))
		case "/zones/" + accessAppsTestZoneID + "/access/apps/" + accessAppsTestAppID:
			sawApp = true
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestAppID + `"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "access", "app", "get", accessAppsTestAppID,
		"--scope", "zone", "--zone", "example.com"); err != nil {
		t.Fatal(err)
	}
	if !sawLookup || !sawApp {
		t.Fatalf("zone resolution lookup=%v app=%v", sawLookup, sawApp)
	}
}

// The zone application list endpoint takes no query parameters, so every
// account-only filter has to be rejected locally — before the client is built
// and before the zone name would be looked up.
func TestAccessAppsListRejectsAccountOnlyFiltersUnderZoneScope(t *testing.T) {
	cases := []struct {
		flag string
		args []string
	}{
		{"name", []string{"--name", "Internal wiki"}},
		{"domain", []string{"--domain", "wiki.example.com"}},
		{"aud", []string{"--aud", "737646a5"}},
		{"search", []string{"--search", "wiki"}},
		{"exact", []string{"--exact"}},
		{"name", []string{"--name", "Internal wiki", "--exact"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			accessAppsIsolateConfig(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer srv.Close()

			args := append([]string{"access", "app", "list", "--scope", "zone", "--zone", "example.com"}, tc.args...)
			_, _, err := runAccessAppsCLI(t, srv.URL, args...)
			if err == nil {
				t.Fatalf("%v: expected a rejection", tc.args)
			}
			for _, want := range []string{"--" + tc.flag, "--scope zone"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want it to mention %q", err, want)
				}
			}
		})
	}
}

// The same filters stay available for account scope, where the endpoint
// documents them.
func TestAccessAppsListSendsEveryFilterUnderAccountScope(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "list",
		"--name", "Internal wiki", "--domain", "wiki.example.com",
		"--aud", "737646a5", "--search", "wiki", "--exact=false"); err != nil {
		t.Fatal(err)
	}
	want := "aud=737646a5&domain=wiki.example.com&exact=false&name=Internal+wiki&search=wiki"
	if gotQuery != want {
		t.Fatalf("query = %s, want %s", gotQuery, want)
	}
}

// The scope flags are checked on their own, before a client is built: with no
// token available these still report the scope problem rather than the
// missing credential.
func TestAccessAppsScopeValidatedBeforeClient(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown scope", []string{"access", "app", "list", "--scope", "organization"}, "--scope must be account or zone"},
		{"zone flag under account scope", []string{"access", "app", "list", "--zone", "example.com"}, "--zone requires --scope zone"},
		{"unknown scope on a policy command", []string{"access", "app", "policy", "list", accessAppsTestAppID, "--scope", "organization"}, "--scope must be account or zone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accessAppsIsolateConfig(t)
			_, _, err := runAccessAppsCLIRaw(t, nil, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "no API token") {
				t.Fatalf("client was built before the scope was validated: %v", err)
			}
		})
	}
}

// A zone ID given directly, or one already configured, is used as-is: no zone
// lookup and no picker.
func TestAccessAppsZoneScopeSkipsLookupForKnownZoneID(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"zone flag holds an ID", []string{"access", "app", "get", accessAppsTestAppID, "--scope", "zone", "--zone", accessAppsTestZoneID}},
		{"configured zone", []string{"--zone-id", accessAppsTestZoneID, "access", "app", "get", accessAppsTestAppID, "--scope", "zone"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accessAppsIsolateConfig(t)
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestAppID + `"}}`))
			}))
			defer srv.Close()

			if _, _, err := runAccessAppsCLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			want := "/zones/" + accessAppsTestZoneID + "/access/apps/" + accessAppsTestAppID
			if len(paths) != 1 || paths[0] != want {
				t.Fatalf("requests = %v, want just %s", paths, want)
			}
		})
	}
}

// With no --zone and no configured zone there is nothing to resolve; on a
// dry run (and off a terminal) that has to fail with advice instead of
// prompting or guessing.
func TestAccessAppsZoneScopeWithoutAnyZoneFailsBeforeRequest(t *testing.T) {
	accessAppsIsolateConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runAccessAppsCLI(t, srv.URL, "--dry-run", "access", "app", "list", "--scope", "zone")
	if err == nil {
		t.Fatal("expected a missing-zone error")
	}
	for _, want := range []string{"no zone specified", "--zone"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to mention %q", err, want)
		}
	}
}

func TestAccessAppsScopeValidation(t *testing.T) {
	accessAppsIsolateConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "list", "--scope", "organization"); err == nil || !strings.Contains(err.Error(), "--scope must be account or zone") {
		t.Fatalf("scope error = %v", err)
	}
	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "list", "--zone", "example.com"); err == nil || !strings.Contains(err.Error(), "--zone requires --scope zone") {
		t.Fatalf("zone-without-scope error = %v", err)
	}
	if _, _, err := runAccessAppsCLI(t, srv.URL, "access", "app", "list"); err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("missing account error = %v", err)
	}
	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "list", "--exact"); err == nil || !strings.Contains(err.Error(), "--exact filters") {
		t.Fatalf("exact error = %v", err)
	}
}

// Invalid input must fail before any request is made, including the zone
// lookup a zone-scoped command would otherwise perform.
func TestAccessAppsValidationHappensBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cases := [][]string{
		{"access", "app", "create", "Wiki", "--type", "kubernetes", "--scope", "zone", "--zone", "example.com"},
		{"access", "app", "create", "Wiki", "--scope", "zone", "--zone", "example.com"},
		{"access", "app", "policy", "create", accessAppsTestAppID, "--name", "P", "--decision", "allow", "--include", "null", "--scope", "zone", "--zone", "example.com"},
		{"access", "app", "policy", "create", accessAppsTestAppID, "--name", "P", "--decision", "allow", "--scope", "zone", "--zone", "example.com"},
		{"access", "app", "policy", "get", "app id", accessAppsTestPolicyID, "--scope", "zone", "--zone", "example.com"},
	}
	for _, args := range cases {
		if _, _, err := runAccessAppsCLI(t, srv.URL, args...); err == nil {
			t.Fatalf("%v: expected a validation error", args)
		}
	}
}

func TestAccessAppsCreateDryRunAndHTTPRequest(t *testing.T) {
	stdout, _, err := runAccessAppsCLI(t, "http://example.invalid", "--account-id", accessAppsTestAccountID,
		"access", "app", "create", "Internal wiki", "--domain", "wiki.example.com", "--session-duration", "24h", "--dry-run")
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/accounts/"+accessAppsTestAccountID+"/access/apps") {
		t.Fatalf("dump = %+v", dump)
	}
	accessAppsAssertJSONEqual(t, dump.Body, `{"name":"Internal wiki","type":"self_hosted","domain":"wiki.example.com","session_duration":"24h"}`)

	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestAppID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "create", "Internal wiki", "--domain", "wiki.example.com"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/accounts/"+accessAppsTestAccountID+"/access/apps" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	accessAppsAssertJSONEqual(t, gotBody, `{"name":"Internal wiki","type":"self_hosted","domain":"wiki.example.com"}`)
}

func TestAccessAppsUpdateReadsThenReplaces(t *testing.T) {
	var gotMethods []string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+accessAppsTestAccountID+"/access/apps/"+accessAppsTestAppID {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotMethods = append(gotMethods, r.Method)
		if r.Method == "PUT" {
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestAppID + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{
			"id":"` + accessAppsTestAppID + `",
			"aud":"737646a5",
			"created_at":"2024-01-01T00:00:00Z",
			"updated_at":"2024-01-02T00:00:00Z",
			"policies":[{"id":"` + accessAppsTestPolicyID + `"}],
			"name":"Internal wiki",
			"type":"self_hosted",
			"domain":"wiki.example.com",
			"session_duration":"24h",
			"app_launcher_visible":true
		}}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "update", accessAppsTestAppID, "--session-duration", "12h", "--app-launcher-visible=false"); err != nil {
		t.Fatal(err)
	}
	if len(gotMethods) != 2 || gotMethods[0] != "GET" || gotMethods[1] != "PUT" {
		t.Fatalf("methods = %v", gotMethods)
	}
	accessAppsAssertJSONEqual(t, gotBody, `{
		"name":"Internal wiki",
		"type":"self_hosted",
		"domain":"wiki.example.com",
		"session_duration":"12h",
		"app_launcher_visible":false
	}`)
}

// The domain rules apply to the stored application's type, which is only
// known after the read.
func TestAccessAppsUpdateRejectsDomainForAssignedDomainType(t *testing.T) {
	var sawWrite bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			sawWrite = true
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestAppID + `","type":"app_launcher","name":"App Launcher"}}`))
	}))
	defer srv.Close()

	_, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "update", accessAppsTestAppID, "--domain", "launcher.example.com")
	if err == nil || !strings.Contains(err.Error(), "not supported for app_launcher") {
		t.Fatalf("error = %v", err)
	}
	if sawWrite {
		t.Fatal("update wrote after rejecting the domain")
	}
}

func TestAccessAppsUpdateNothingToUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "update", accessAppsTestAppID); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("error = %v", err)
	}
	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "update", accessAppsTestAppID, accessAppsTestPolicyID); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("policy error = %v", err)
	}
}

func TestAccessAppsDeleteEndpointAndForce(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestAppID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "delete", accessAppsTestAppID, "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/accounts/"+accessAppsTestAccountID+"/access/apps/"+accessAppsTestAppID {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}

	// --dry-run prints the request without confirming or sending anything.
	stdout, _, err := runAccessAppsCLI(t, "http://example.invalid", "--account-id", accessAppsTestAccountID,
		"access", "app", "delete", accessAppsTestAppID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string `json:"method"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != "DELETE" || !strings.HasSuffix(dump.URL, "/access/apps/"+accessAppsTestAppID) {
		t.Fatalf("dump = %+v", dump)
	}
}

func TestAccessAppsPolicyListHTTPAndTable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + accessAppsTestPolicyID + `","name":"Engineers","decision":"allow","precedence":1,"session_duration":"30m"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "list", accessAppsTestAppID)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+accessAppsTestAccountID+"/access/apps/"+accessAppsTestAppID+"/policies" {
		t.Errorf("path = %s", gotPath)
	}
	for _, want := range []string{"ID", "DECISION", "PRECEDENCE", "SESSION", "Engineers", "allow", "30m"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestAccessAppsPolicyCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestPolicyID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "create", accessAppsTestAppID,
		"--name", "Engineers", "--decision", "allow",
		"--include-email-domain", "example.com", "--precedence", "1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/accounts/"+accessAppsTestAccountID+"/access/apps/"+accessAppsTestAppID+"/policies" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	accessAppsAssertJSONEqual(t, gotBody, `{"name":"Engineers","decision":"allow","include":[{"email_domain":{"domain":"example.com"}}],"precedence":1}`)
}

func TestAccessAppsPolicyCreateRequiresIncludeAndFlags(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "create", accessAppsTestAppID, "--name", "Engineers", "--decision", "allow")
	if err == nil || !strings.Contains(err.Error(), "at least one include rule") {
		t.Fatalf("include error = %v", err)
	}
	_, _, err = runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "create", accessAppsTestAppID, "--include-everyone")
	if err == nil || !strings.Contains(err.Error(), `required flag(s) "decision", "name" not set`) {
		t.Fatalf("required flag error = %v", err)
	}
}

func TestAccessAppsPolicyRulesFromStdin(t *testing.T) {
	stdin := strings.NewReader(`[{"group":{"id":"aa0a4aab-672b-4bdb-bc33-a59f1130a11f"}}]`)
	stdout, _, err := runAccessAppsCLIWithStdin(t, "http://example.invalid", stdin,
		"--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "create", accessAppsTestAppID,
		"--name", "Engineers", "--decision", "allow", "--include", "@-", "--dry-run")
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
	if dump.Method != "POST" {
		t.Fatalf("dump = %+v", dump)
	}
	accessAppsAssertJSONEqual(t, dump.Body, `{"name":"Engineers","decision":"allow","include":[{"group":{"id":"aa0a4aab-672b-4bdb-bc33-a59f1130a11f"}}]}`)
}

func TestAccessAppsPolicyUpdateReadsThenReplaces(t *testing.T) {
	var gotMethods []string
	var gotBody []byte
	path := "/accounts/" + accessAppsTestAccountID + "/access/apps/" + accessAppsTestAppID + "/policies/" + accessAppsTestPolicyID
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Fatalf("path = %s", r.URL.Path)
		}
		gotMethods = append(gotMethods, r.Method)
		if r.Method == "PUT" {
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestPolicyID + `"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{
			"id":"` + accessAppsTestPolicyID + `",
			"created_at":"2024-01-01T00:00:00Z",
			"updated_at":"2024-01-02T00:00:00Z",
			"name":"Engineers",
			"decision":"allow",
			"precedence":1,
			"include":[{"email_domain":{"domain":"example.com"}}],
			"exclude":[],
			"require":[]
		}}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "update", accessAppsTestAppID, accessAppsTestPolicyID,
		"--decision", "deny", "--include-email", "oncall@example.com"); err != nil {
		t.Fatal(err)
	}
	if len(gotMethods) != 2 || gotMethods[0] != "GET" || gotMethods[1] != "PUT" {
		t.Fatalf("methods = %v", gotMethods)
	}
	accessAppsAssertJSONEqual(t, gotBody, `{
		"name":"Engineers",
		"decision":"deny",
		"precedence":1,
		"include":[{"email":{"email":"oncall@example.com"}}],
		"exclude":[],
		"require":[]
	}`)
}

func TestAccessAppsPolicyDeleteEndpoint(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + accessAppsTestPolicyID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runAccessAppsCLI(t, srv.URL, "--account-id", accessAppsTestAccountID,
		"access", "app", "policy", "delete", accessAppsTestAppID, accessAppsTestPolicyID, "--force"); err != nil {
		t.Fatal(err)
	}
	want := "/accounts/" + accessAppsTestAccountID + "/access/apps/" + accessAppsTestAppID + "/policies/" + accessAppsTestPolicyID
	if gotMethod != "DELETE" || gotPath != want {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
}

func TestAccessAppsRegisteredUnderAccessGroup(t *testing.T) {
	root := NewRootCmd()
	cmd, _, err := root.Find([]string{"access", "app", "policy", "create"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Name() != "create" || cmd.Parent().Name() != "policy" {
		t.Fatalf("resolved %s under %s", cmd.Name(), cmd.Parent().Name())
	}
}
