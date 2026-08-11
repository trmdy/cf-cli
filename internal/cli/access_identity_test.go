package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const accessIdentityTestAccountID = "0123456789abcdef0123456789abcdef"

func runAccessIdentityCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runAccessIdentityCLIWithStdin(t, serverURL, nil, args...)
}

func runAccessIdentityCLIWithStdin(t *testing.T, serverURL string, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	if stdin != nil {
		root.SetIn(stdin)
	}
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", accessIdentityTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func accessIdentityAssertJSONEqual(t *testing.T, got []byte, want string) {
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

// accessIdentityDump is the --dry-run request representation.
type accessIdentityDump struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func accessIdentityParseDump(t *testing.T, stdout string) accessIdentityDump {
	t.Helper()
	var d accessIdentityDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

// --- identity provider body ------------------------------------------------

func TestBuildAccessIdentityProviderBodyOneTimePin(t *testing.T) {
	body, err := buildAccessIdentityProviderBody(accessIdentityProviderOpts{
		name:    "One-time PIN",
		idpType: "onetimepin",
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"One-time PIN","type":"onetimepin","config":{}}`)
}

func TestBuildAccessIdentityProviderBodyWithConfigAndSCIM(t *testing.T) {
	body, err := buildAccessIdentityProviderBody(accessIdentityProviderOpts{
		name:       "Okta",
		idpType:    "okta",
		config:     `{"client_id":"abc","client_secret":"s3cret","okta_account":"https://example.okta.com"}`,
		scimConfig: `{"enabled":true,"user_deprovision":true}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{
		"name":"Okta","type":"okta",
		"config":{"client_id":"abc","client_secret":"s3cret","okta_account":"https://example.okta.com"},
		"scim_config":{"enabled":true,"user_deprovision":true}
	}`)
}

// The API spells some provider types in camelCase; input is matched
// case-insensitively but the canonical spelling has to reach the wire.
func TestBuildAccessIdentityProviderBodyCanonicalizesType(t *testing.T) {
	cases := []struct{ in, want string }{
		{"azuread", "azureAD"},
		{"AzureAD", "azureAD"},
		{"AZUREAD", "azureAD"},
		{"GOOGLE-APPS", "google-apps"},
		{"SAML", "saml"},
		{"OneTimePin", "onetimepin"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			opts := accessIdentityProviderOpts{name: "IdP", idpType: tc.in, config: `{"a":"b"}`}
			if tc.want == "onetimepin" {
				opts.config = ""
			}
			body, err := buildAccessIdentityProviderBody(opts)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatal(err)
			}
			if got.Type != tc.want {
				t.Fatalf("type = %q, want %q", got.Type, tc.want)
			}
		})
	}
}

func TestBuildAccessIdentityProviderBodyAcceptsEveryDocumentedType(t *testing.T) {
	for _, typ := range accessIdentityProviderTypes {
		config := `{"client_id":"abc"}`
		if typ == "onetimepin" {
			config = ""
		}
		if _, err := buildAccessIdentityProviderBody(accessIdentityProviderOpts{
			name: "IdP", idpType: typ, config: config,
		}); err != nil {
			t.Errorf("type %q rejected: %v", typ, err)
		}
	}
}

func TestBuildAccessIdentityProviderBodyValidation(t *testing.T) {
	cases := []struct {
		name string
		opts accessIdentityProviderOpts
		want string
	}{
		{"empty name", accessIdentityProviderOpts{name: "  ", idpType: "onetimepin"}, "name must not be empty"},
		{"unknown type", accessIdentityProviderOpts{name: "IdP", idpType: "ldap", config: `{"a":1}`}, `unknown --type "ldap"`},
		{"empty type", accessIdentityProviderOpts{name: "IdP"}, "unknown --type"},
		{"missing config", accessIdentityProviderOpts{name: "IdP", idpType: "okta"}, "--config is required for --type okta"},
		{"onetimepin with config", accessIdentityProviderOpts{name: "IdP", idpType: "onetimepin", config: `{"a":1}`}, "onetimepin takes no --config"},
		{"config null", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: "null"}, "--config must be a JSON object"},
		{"config array", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: `[{"a":1}]`}, "--config must be a JSON object"},
		{"config string", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: `"abc"`}, "--config must be a JSON object"},
		{"config number", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: "42"}, "--config must be a JSON object"},
		{"config bool", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: "true"}, "--config must be a JSON object"},
		{"config malformed", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: `{"a":`}, "--config must be a JSON object"},
		{"config trailing data", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: `{"a":1} {"b":2}`}, "trailing data"},
		{"scim null", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: `{"a":1}`, scimConfig: "null"}, "--scim-config must be a JSON object"},
		{"scim array", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: `{"a":1}`, scimConfig: "[]"}, "--scim-config must be a JSON object"},
		{"two stdin readers", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: "@-", scimConfig: "@-"}, "only one flag can read stdin"},
		{"missing config file", accessIdentityProviderOpts{name: "IdP", idpType: "okta", config: "@/nope/missing.json"}, "read --config from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAccessIdentityProviderBody(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// An empty JSON object is a legitimate onetimepin config: the check is on
// content, not on whether the flag was passed.
func TestBuildAccessIdentityProviderBodyOneTimePinAcceptsEmptyObject(t *testing.T) {
	body, err := buildAccessIdentityProviderBody(accessIdentityProviderOpts{
		name: "One-time PIN", idpType: "onetimepin", config: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"One-time PIN","type":"onetimepin","config":{}}`)
}

func TestBuildAccessIdentityProviderBodyPreservesNumberPrecision(t *testing.T) {
	body, err := buildAccessIdentityProviderBody(accessIdentityProviderOpts{
		name: "OIDC", idpType: "oidc", config: `{"clock_skew":10000000000000000001,"ratio":1e-9}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "10000000000000000001") {
		t.Errorf("body lost integer precision: %s", body)
	}
	if !strings.Contains(string(body), "1e-9") {
		t.Errorf("body rewrote exponent form: %s", body)
	}
}

func TestBuildAccessIdentityProviderBodyFromFileAndStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"client_id":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := buildAccessIdentityProviderBody(accessIdentityProviderOpts{
		name: "Okta", idpType: "okta", config: "@" + path,
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"Okta","type":"okta","config":{"client_id":"from-file"}}`)

	body, err = buildAccessIdentityProviderBody(accessIdentityProviderOpts{
		name: "Okta", idpType: "okta", config: "@-",
		stdin: strings.NewReader(`{"client_id":"from-stdin"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"Okta","type":"okta","config":{"client_id":"from-stdin"}}`)
}

// --- Access group body -----------------------------------------------------

func TestBuildAccessIdentityGroupBodyMinimal(t *testing.T) {
	body, err := buildAccessIdentityGroupBody(accessIdentityGroupOpts{
		name:    "Employees",
		include: `[{"email_domain":{"domain":"example.com"}}]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Unset rule lists are omitted so the API's own defaults apply.
	accessIdentityAssertJSONEqual(t, body, `{"name":"Employees","include":[{"email_domain":{"domain":"example.com"}}]}`)
}

func TestBuildAccessIdentityGroupBodyAllRules(t *testing.T) {
	body, err := buildAccessIdentityGroupBody(accessIdentityGroupOpts{
		name:         "Contractors",
		include:      `[{"email_domain":{"domain":"example.com"}},{"everyone":{}}]`,
		exclude:      `[{"email":{"email":"former@example.com"}}]`,
		require:      `[{"ip":{"ip":"198.51.100.4/32"}}]`,
		isDefault:    true,
		isDefaultSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{
		"name":"Contractors",
		"include":[{"email_domain":{"domain":"example.com"}},{"everyone":{}}],
		"exclude":[{"email":{"email":"former@example.com"}}],
		"require":[{"ip":{"ip":"198.51.100.4/32"}}],
		"is_default":true
	}`)
}

// An explicit --is-default=false is not the same as leaving it unset.
func TestBuildAccessIdentityGroupBodyIsDefaultFalseIsSent(t *testing.T) {
	body, err := buildAccessIdentityGroupBody(accessIdentityGroupOpts{
		name:         "Employees",
		include:      `[{"everyone":{}}]`,
		isDefault:    false,
		isDefaultSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"Employees","include":[{"everyone":{}}],"is_default":false}`)
}

// Empty exclude/require lists are meaningful on a replacing update: they say
// "no rules of this kind".
func TestBuildAccessIdentityGroupBodyAcceptsEmptyExcludeAndRequire(t *testing.T) {
	body, err := buildAccessIdentityGroupBody(accessIdentityGroupOpts{
		name:    "Employees",
		include: `[{"everyone":{}}]`,
		exclude: `[]`,
		require: `[]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"Employees","include":[{"everyone":{}}],"exclude":[],"require":[]}`)
}

func TestBuildAccessIdentityGroupBodyValidation(t *testing.T) {
	cases := []struct {
		name string
		opts accessIdentityGroupOpts
		want string
	}{
		{"empty name", accessIdentityGroupOpts{name: "   ", include: `[{"everyone":{}}]`}, "group name must not be empty"},
		{"missing include", accessIdentityGroupOpts{name: "G"}, "--include is required"},
		{"include null", accessIdentityGroupOpts{name: "G", include: "null"}, "--include must be a JSON array of rule objects"},
		{"include object", accessIdentityGroupOpts{name: "G", include: `{"everyone":{}}`}, "--include must be a JSON array of rule objects"},
		{"include string", accessIdentityGroupOpts{name: "G", include: `"everyone"`}, "--include must be a JSON array of rule objects"},
		{"include number", accessIdentityGroupOpts{name: "G", include: "7"}, "--include must be a JSON array of rule objects"},
		{"include malformed", accessIdentityGroupOpts{name: "G", include: `[{"everyone":`}, "--include must be a JSON array of rule objects"},
		{"include empty", accessIdentityGroupOpts{name: "G", include: "[]"}, "--include must contain at least one rule"},
		{"include null element", accessIdentityGroupOpts{name: "G", include: "[null]"}, "--include rule 1 must be a JSON object"},
		{"include scalar element", accessIdentityGroupOpts{name: "G", include: `["everyone"]`}, "--include rule 1 must be a JSON object"},
		{"include nested array", accessIdentityGroupOpts{name: "G", include: `[[]]`}, "--include rule 1 must be a JSON object"},
		{"include empty rule", accessIdentityGroupOpts{name: "G", include: `[{"everyone":{}},{}]`}, "--include rule 2 is empty"},
		{"exclude null", accessIdentityGroupOpts{name: "G", include: `[{"everyone":{}}]`, exclude: "null"}, "--exclude must be a JSON array of rule objects"},
		{"exclude bad element", accessIdentityGroupOpts{name: "G", include: `[{"everyone":{}}]`, exclude: "[1]"}, "--exclude rule 1 must be a JSON object"},
		{"require null", accessIdentityGroupOpts{name: "G", include: `[{"everyone":{}}]`, require: "null"}, "--require must be a JSON array of rule objects"},
		{"require empty rule", accessIdentityGroupOpts{name: "G", include: `[{"everyone":{}}]`, require: "[{}]"}, "--require rule 1 is empty"},
		{"two stdin readers", accessIdentityGroupOpts{name: "G", include: "@-", exclude: "@-"}, "only one flag can read stdin"},
		{"three stdin readers", accessIdentityGroupOpts{name: "G", include: "@-", exclude: "@-", require: "@-"}, "only one flag can read stdin"},
		{"missing include file", accessIdentityGroupOpts{name: "G", include: "@/nope/missing.json"}, "read --include from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAccessIdentityGroupBody(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildAccessIdentityGroupBodyFromStdin(t *testing.T) {
	body, err := buildAccessIdentityGroupBody(accessIdentityGroupOpts{
		name:    "Employees",
		include: "@-",
		stdin:   strings.NewReader(`[{"everyone":{}}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"Employees","include":[{"everyone":{}}]}`)
}

// --- service token body ----------------------------------------------------

func TestBuildAccessIdentityServiceTokenBody(t *testing.T) {
	body, err := buildAccessIdentityServiceTokenBody("ci-deploy", "", false)
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"ci-deploy"}`)

	body, err = buildAccessIdentityServiceTokenBody("  ci-deploy  ", "8760h", true)
	if err != nil {
		t.Fatal(err)
	}
	accessIdentityAssertJSONEqual(t, body, `{"name":"ci-deploy","duration":"8760h"}`)
}

func TestBuildAccessIdentityServiceTokenBodyRejectsEmptyName(t *testing.T) {
	if _, err := buildAccessIdentityServiceTokenBody("   ", "", false); err == nil ||
		!strings.Contains(err.Error(), "service token name must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

// The API documents the duration format as <number><unit> with ms, s, m, or h;
// zero and negative lifetimes are rejected on both sides of the bound.
func TestValidateAccessIdentityDuration(t *testing.T) {
	accepted := []string{"300ms", "1s", "90s", "1m", "30m", "1h", "8760h", "1h30m", "0.5h", "1ms"}
	for _, d := range accepted {
		if err := validateAccessIdentityDuration(d); err != nil {
			t.Errorf("duration %q rejected: %v", d, err)
		}
	}
	rejected := []struct{ in, want string }{
		{"", "invalid --duration"},
		{"0h", "greater than zero"},
		{"0s", "greater than zero"},
		{"0ms", "greater than zero"},
		{"0.0h", "greater than zero"},
		{"-1h", "invalid --duration"},
		{"1d", "invalid --duration"},
		{"1", "invalid --duration"},
		{"h", "invalid --duration"},
		{"1 h", "invalid --duration"},
		{"1us", "invalid --duration"},
		{"1ns", "invalid --duration"},
		{"forever", "invalid --duration"},
	}
	for _, tc := range rejected {
		t.Run(tc.in, func(t *testing.T) {
			err := validateAccessIdentityDuration(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// --- revoke body -----------------------------------------------------------

// devices belongs to the revoke body, not the query string. Unset means "omit
// so the API's default applies"; an explicit false has to survive.
func TestBuildAccessIdentityRevokeBody(t *testing.T) {
	cases := []struct {
		name       string
		devices    bool
		devicesSet bool
		want       string
	}{
		{"devices unset", false, false, `{"email":"jane@example.com"}`},
		{"devices true", true, true, `{"email":"jane@example.com","devices":true}`},
		{"devices false", false, true, `{"email":"jane@example.com","devices":false}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := buildAccessIdentityRevokeBody("  jane@example.com  ", tc.devices, tc.devicesSet)
			if err != nil {
				t.Fatal(err)
			}
			accessIdentityAssertJSONEqual(t, body, tc.want)
		})
	}
}

func TestBuildAccessIdentityRevokeBodyValidatesEmailBeforeDevices(t *testing.T) {
	if _, err := buildAccessIdentityRevokeBody("jane", true, true); err == nil ||
		!strings.Contains(err.Error(), "expected a single address") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateAccessIdentityEmail(t *testing.T) {
	accepted := []string{"jane@example.com", "j+tag@sub.example.co.uk", "a@b"}
	for _, e := range accepted {
		if err := validateAccessIdentityEmail(e); err != nil {
			t.Errorf("email %q rejected: %v", e, err)
		}
	}
	rejected := []struct{ in, want string }{
		{"", "must not be empty"},
		{"   ", "must not be empty"},
		{"jane", "expected a single address"},
		{"@example.com", "expected a single address"},
		{"jane@", "expected a single address"},
		{"jane@a@b.com", "expected a single address"},
		{"jane doe@example.com", "must not contain whitespace"},
	}
	for _, tc := range rejected {
		t.Run(tc.in, func(t *testing.T) {
			err := validateAccessIdentityEmail(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// --- request construction (dry run) ----------------------------------------

func TestAccessIdentityDryRunRequests(t *testing.T) {
	base := "/accounts/" + accessIdentityTestAccountID + "/access"
	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantURL    string
		wantBody   string
	}{
		{
			name:       "provider list",
			args:       []string{"access", "identity", "provider", "list"},
			wantMethod: "GET",
			wantURL:    base + "/identity_providers",
		},
		{
			name:       "provider list scim filter",
			args:       []string{"access", "identity", "provider", "list", "--scim-enabled"},
			wantMethod: "GET",
			wantURL:    base + "/identity_providers?scim_enabled=true",
		},
		{
			name:       "provider list scim filter false",
			args:       []string{"access", "identity", "provider", "list", "--scim-enabled=false"},
			wantMethod: "GET",
			wantURL:    base + "/identity_providers?scim_enabled=false",
		},
		{
			name:       "provider get",
			args:       []string{"access", "identity", "provider", "get", "idp-1"},
			wantMethod: "GET",
			wantURL:    base + "/identity_providers/idp-1",
		},
		{
			name:       "provider create",
			args:       []string{"access", "identity", "provider", "create", "One-time PIN", "--type", "onetimepin"},
			wantMethod: "POST",
			wantURL:    base + "/identity_providers",
			wantBody:   `{"name":"One-time PIN","type":"onetimepin","config":{}}`,
		},
		{
			name: "provider update",
			args: []string{"access", "identity", "provider", "update", "idp-1",
				"--name", "Okta", "--type", "okta", "--config", `{"client_id":"abc"}`},
			wantMethod: "PUT",
			wantURL:    base + "/identity_providers/idp-1",
			wantBody:   `{"name":"Okta","type":"okta","config":{"client_id":"abc"}}`,
		},
		{
			name:       "provider delete",
			args:       []string{"access", "identity", "provider", "delete", "idp-1", "--force"},
			wantMethod: "DELETE",
			wantURL:    base + "/identity_providers/idp-1",
		},
		{
			name:       "group list filters",
			args:       []string{"access", "identity", "group", "list", "--name", "Employees", "--search", "emp"},
			wantMethod: "GET",
			wantURL:    base + "/groups?name=Employees&search=emp",
		},
		{
			name:       "group get",
			args:       []string{"access", "identity", "group", "get", "grp-1"},
			wantMethod: "GET",
			wantURL:    base + "/groups/grp-1",
		},
		{
			name: "group create",
			args: []string{"access", "identity", "group", "create", "Employees",
				"--include", `[{"email_domain":{"domain":"example.com"}}]`},
			wantMethod: "POST",
			wantURL:    base + "/groups",
			wantBody:   `{"name":"Employees","include":[{"email_domain":{"domain":"example.com"}}]}`,
		},
		{
			name: "group update",
			args: []string{"access", "identity", "group", "update", "grp-1",
				"--name", "Employees", "--include", `[{"everyone":{}}]`, "--is-default"},
			wantMethod: "PUT",
			wantURL:    base + "/groups/grp-1",
			wantBody:   `{"name":"Employees","include":[{"everyone":{}}],"is_default":true}`,
		},
		{
			name:       "group delete",
			args:       []string{"access", "identity", "group", "delete", "grp-1", "--force"},
			wantMethod: "DELETE",
			wantURL:    base + "/groups/grp-1",
		},
		{
			name:       "service token list",
			args:       []string{"access", "identity", "service-token", "list", "--search", "ci"},
			wantMethod: "GET",
			wantURL:    base + "/service_tokens?search=ci",
		},
		{
			name:       "service token get",
			args:       []string{"access", "identity", "service-token", "get", "tok-1"},
			wantMethod: "GET",
			wantURL:    base + "/service_tokens/tok-1",
		},
		{
			name:       "service token create",
			args:       []string{"access", "identity", "service-token", "create", "ci-deploy", "--duration", "720h"},
			wantMethod: "POST",
			wantURL:    base + "/service_tokens",
			wantBody:   `{"name":"ci-deploy","duration":"720h"}`,
		},
		{
			name:       "service token update",
			args:       []string{"access", "identity", "service-token", "update", "tok-1", "--name", "ci-deploy"},
			wantMethod: "PUT",
			wantURL:    base + "/service_tokens/tok-1",
			wantBody:   `{"name":"ci-deploy"}`,
		},
		{
			name:       "service token rotate",
			args:       []string{"access", "identity", "service-token", "rotate", "tok-1", "--force"},
			wantMethod: "POST",
			wantURL:    base + "/service_tokens/tok-1/rotate",
		},
		{
			name:       "service token delete",
			args:       []string{"access", "identity", "service-token", "delete", "tok-1", "--force"},
			wantMethod: "DELETE",
			wantURL:    base + "/service_tokens/tok-1",
		},
		{
			name:       "user list filters",
			args:       []string{"access", "identity", "user", "list", "--email", "jane@example.com", "--name", "Jane", "--search", "ex"},
			wantMethod: "GET",
			wantURL:    base + "/users?email=jane%40example.com&name=Jane&search=ex",
		},
		{
			name:       "user revoke sessions",
			args:       []string{"access", "identity", "user", "revoke-sessions", "jane@example.com", "--force"},
			wantMethod: "POST",
			wantURL:    base + "/organizations/revoke_user",
			wantBody:   `{"email":"jane@example.com"}`,
		},
		{
			// devices rides in the body; the URL keeps no query string.
			name:       "user revoke sessions with devices",
			args:       []string{"access", "identity", "user", "revoke-sessions", "jane@example.com", "--devices", "--force"},
			wantMethod: "POST",
			wantURL:    base + "/organizations/revoke_user",
			wantBody:   `{"email":"jane@example.com","devices":true}`,
		},
		{
			name:       "user revoke sessions with devices false",
			args:       []string{"access", "identity", "user", "revoke-sessions", "jane@example.com", "--devices=false", "--force"},
			wantMethod: "POST",
			wantURL:    base + "/organizations/revoke_user",
			wantBody:   `{"email":"jane@example.com","devices":false}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := runAccessIdentityCLI(t, "http://example.invalid", append(tc.args, "--dry-run")...)
			if err != nil {
				t.Fatal(err)
			}
			d := accessIdentityParseDump(t, stdout)
			if d.Method != tc.wantMethod {
				t.Errorf("method = %s, want %s", d.Method, tc.wantMethod)
			}
			if !strings.HasSuffix(d.URL, tc.wantURL) {
				t.Errorf("url = %s, want suffix %s", d.URL, tc.wantURL)
			}
			if tc.wantBody == "" {
				if len(d.Body) > 0 {
					t.Errorf("body = %s, want none", d.Body)
				}
				return
			}
			accessIdentityAssertJSONEqual(t, d.Body, tc.wantBody)
		})
	}
}

func TestAccessIdentityEscapesIDsInPath(t *testing.T) {
	stdout, _, err := runAccessIdentityCLI(t, "http://example.invalid",
		"access", "identity", "group", "get", "weird id/../x", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := accessIdentityParseDump(t, stdout)
	if strings.Contains(d.URL, "/../") {
		t.Errorf("group ID not escaped: %s", d.URL)
	}
}

// --- validation happens before any client or network work -------------------

func TestAccessIdentityValidatesBeforeRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "provider create unknown type",
			args: []string{"access", "identity", "provider", "create", "IdP", "--type", "ldap"},
			want: "unknown --type",
		},
		{
			name: "provider create null config",
			args: []string{"access", "identity", "provider", "create", "IdP", "--type", "okta", "--config", "null"},
			want: "--config must be a JSON object",
		},
		{
			name: "group create null include",
			args: []string{"access", "identity", "group", "create", "G", "--include", "null"},
			want: "--include must be a JSON array of rule objects",
		},
		{
			name: "group create empty include",
			args: []string{"access", "identity", "group", "create", "G", "--include", "[]"},
			want: "--include must contain at least one rule",
		},
		{
			name: "service token create bad duration",
			args: []string{"access", "identity", "service-token", "create", "ci", "--duration", "1d"},
			want: "invalid --duration",
		},
		{
			name: "service token create zero duration",
			args: []string{"access", "identity", "service-token", "create", "ci", "--duration", "0h"},
			want: "greater than zero",
		},
		{
			name: "revoke sessions bad email",
			args: []string{"access", "identity", "user", "revoke-sessions", "jane", "--force"},
			want: "expected a single address",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer srv.Close()

			_, _, err := runAccessIdentityCLI(t, srv.URL, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

// Local input is checked before the account scope is resolved, so a bad flag
// is reported even when no account is configured.
func TestAccessIdentityValidatesBeforeAccountResolution(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t",
		"access", "identity", "group", "create", "G", "--include", "null", "--dry-run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--include must be a JSON array") {
		t.Fatalf("err = %v, want the include shape error", err)
	}
}

func TestAccessIdentityRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")

	for _, args := range [][]string{
		{"access", "identity", "provider", "list", "--dry-run"},
		{"access", "identity", "group", "list", "--dry-run"},
		{"access", "identity", "service-token", "list", "--dry-run"},
		{"access", "identity", "user", "list", "--dry-run"},
	} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			root := NewRootCmd()
			var out, errBuf bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errBuf)
			root.SetArgs(append([]string{"--base-url", "http://example.invalid", "--token", "t"}, args...))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "no account specified") {
				t.Fatalf("err = %v, want the account error", err)
			}
		})
	}
}

// --- destructive commands ---------------------------------------------------

func TestAccessIdentityDestructiveCommandsRequireForceWithoutTTY(t *testing.T) {
	cases := [][]string{
		{"access", "identity", "provider", "delete", "idp-1"},
		{"access", "identity", "group", "delete", "grp-1"},
		{"access", "identity", "service-token", "delete", "tok-1"},
		{"access", "identity", "service-token", "rotate", "tok-1"},
		{"access", "identity", "user", "revoke-sessions", "jane@example.com"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer srv.Close()

			_, _, err := runAccessIdentityCLI(t, srv.URL, args...)
			if err == nil || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("expected force/abort error, got %v", err)
			}
		})
	}
}

// --- real command tree over httptest ---------------------------------------

func TestAccessIdentityProviderListRendersTable(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"idp-1","name":"Okta","type":"okta","scim_config":{"enabled":true}},
			{"id":"idp-2","name":"One-time PIN","type":"onetimepin"}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLI(t, srv.URL, "access", "identity", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+accessIdentityTestAccountID+"/access/identity_providers" {
		t.Errorf("path = %s", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty when no filter is set", gotQuery)
	}
	for _, want := range []string{"ID", "NAME", "TYPE", "SCIM", "idp-1", "Okta", "okta", "true", "One-time PIN", "false"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestAccessIdentityGroupListPaginatesAndRendersTable(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"success":true,"result":[
				{"id":"grp-2","name":"Contractors","is_default":false,"created_at":"2026-02-02T00:00:00Z"}
			],"result_info":{"page":2,"per_page":1,"total_pages":2,"count":1,"total_count":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"grp-1","name":"Employees","is_default":true,"created_at":"2026-01-01T00:00:00Z"}
		],"result_info":{"page":1,"per_page":1,"total_pages":2,"count":1,"total_count":2}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLI(t, srv.URL, "access", "identity", "group", "list")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0] != "" || pages[1] != "2" {
		t.Errorf("pages requested = %v, want the first page then page 2", pages)
	}
	for _, want := range []string{"ID", "NAME", "DEFAULT", "CREATED", "grp-1", "Employees", "true", "grp-2", "Contractors", "false"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestAccessIdentityGroupListHonorsQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"grp-1","name":"Employees"},{"id":"grp-2","name":"Contractors"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLI(t, srv.URL, "access", "identity", "group", "list", "--query", ".[].name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Employees") || !strings.Contains(stdout, "Contractors") {
		t.Errorf("stdout = %s", stdout)
	}
	if strings.Contains(stdout, "DEFAULT") {
		t.Errorf("--query should not render the table: %s", stdout)
	}
}

func TestAccessIdentityGroupCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"grp-9","name":"Employees"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLIWithStdin(t, srv.URL,
		strings.NewReader(`[{"email_domain":{"domain":"example.com"}}]`),
		"access", "identity", "group", "create", "Employees", "--include", "@-")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+accessIdentityTestAccountID+"/access/groups" {
		t.Errorf("path = %s", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	accessIdentityAssertJSONEqual(t, gotBody, `{"name":"Employees","include":[{"email_domain":{"domain":"example.com"}}]}`)
	if !strings.Contains(stdout, "grp-9") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestAccessIdentityProviderUpdateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"idp-1","name":"Entra"}}`))
	}))
	defer srv.Close()

	_, _, err := runAccessIdentityCLI(t, srv.URL,
		"access", "identity", "provider", "update", "idp-1",
		"--name", "Entra", "--type", "azuread",
		"--config", `{"client_id":"abc","directory_id":"dir"}`,
		"--scim-config", `{"enabled":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+accessIdentityTestAccountID+"/access/identity_providers/idp-1" {
		t.Errorf("path = %s", gotPath)
	}
	accessIdentityAssertJSONEqual(t, gotBody, `{
		"name":"Entra","type":"azureAD",
		"config":{"client_id":"abc","directory_id":"dir"},
		"scim_config":{"enabled":true}
	}`)
}

func TestAccessIdentityServiceTokenCreateNotesSecretOnStderr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"tok-1","name":"ci-deploy","client_id":"cid.access","client_secret":"s3cret"}}`))
	}))
	defer srv.Close()

	stdout, stderr, err := runAccessIdentityCLI(t, srv.URL,
		"access", "identity", "service-token", "create", "ci-deploy")
	if err != nil {
		t.Fatal(err)
	}
	// Stdout stays machine-parseable; the warning goes to stderr.
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if result["client_secret"] != "s3cret" {
		t.Errorf("result = %v", result)
	}
	if !strings.Contains(stderr, "shown only once") {
		t.Errorf("stderr = %q, want the secret note", stderr)
	}
}

func TestAccessIdentityServiceTokenCreateDryRunHasNoNote(t *testing.T) {
	_, stderr, err := runAccessIdentityCLI(t, "http://example.invalid",
		"access", "identity", "service-token", "create", "ci-deploy", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr, "shown only once") {
		t.Errorf("dry run should not claim a secret was issued: %q", stderr)
	}
}

func TestAccessIdentityServiceTokenRotateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"tok-1","client_secret":"rotated"}}`))
	}))
	defer srv.Close()

	stdout, stderr, err := runAccessIdentityCLI(t, srv.URL,
		"access", "identity", "service-token", "rotate", "tok-1", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+accessIdentityTestAccountID+"/access/service_tokens/tok-1/rotate" {
		t.Errorf("path = %s", gotPath)
	}
	if len(gotBody) != 0 {
		t.Errorf("body = %q, want empty", gotBody)
	}
	if !strings.Contains(stdout, "rotated") {
		t.Errorf("stdout = %s", stdout)
	}
	if !strings.Contains(stderr, "shown only once") {
		t.Errorf("stderr = %q, want the secret note", stderr)
	}
}

func TestAccessIdentityServiceTokenListRendersTable(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"tok-1","name":"ci-deploy","client_id":"cid.access","expires_at":"2027-01-01T00:00:00Z"}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLI(t, srv.URL,
		"access", "identity", "service-token", "list", "--name", "ci-deploy")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "name=ci-deploy" {
		t.Errorf("query = %q", gotQuery)
	}
	for _, want := range []string{"CLIENT ID", "EXPIRES", "tok-1", "ci-deploy", "cid.access", "2027-01-01T00:00:00Z"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestAccessIdentityUserListRendersTable(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"usr-1","name":"Jane Doe","email":"jane@example.com","active_device_count":2,"last_successful_login":"2026-08-01T10:00:00Z"}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLI(t, srv.URL,
		"access", "identity", "user", "list", "--email", "jane@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+accessIdentityTestAccountID+"/access/users" {
		t.Errorf("path = %s", gotPath)
	}
	if gotQuery != "email=jane%40example.com" {
		t.Errorf("query = %q", gotQuery)
	}
	for _, want := range []string{"EMAIL", "DEVICES", "LAST LOGIN", "usr-1", "Jane Doe", "jane@example.com", "2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestAccessIdentityUserRevokeSessionsHTTPRequest(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantBody string
	}{
		{
			name:     "devices unset",
			args:     []string{"access", "identity", "user", "revoke-sessions", "jane@example.com", "--force"},
			wantBody: `{"email":"jane@example.com"}`,
		},
		{
			name:     "devices true",
			args:     []string{"access", "identity", "user", "revoke-sessions", "jane@example.com", "--devices", "--force"},
			wantBody: `{"email":"jane@example.com","devices":true}`,
		},
		{
			name:     "devices false",
			args:     []string{"access", "identity", "user", "revoke-sessions", "jane@example.com", "--devices=false", "--force"},
			wantBody: `{"email":"jane@example.com","devices":false}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotContentType string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				gotContentType = r.Header.Get("Content-Type")
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":true}`))
			}))
			defer srv.Close()

			if _, _, err := runAccessIdentityCLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if gotMethod != "POST" {
				t.Errorf("method = %s", gotMethod)
			}
			if gotPath != "/accounts/"+accessIdentityTestAccountID+"/access/organizations/revoke_user" {
				t.Errorf("path = %s", gotPath)
			}
			// devices is a body field: the request carries no query string.
			if gotQuery != "" {
				t.Errorf("query = %q, want empty", gotQuery)
			}
			if gotContentType != "application/json" {
				t.Errorf("Content-Type = %q", gotContentType)
			}
			accessIdentityAssertJSONEqual(t, gotBody, tc.wantBody)
		})
	}
}

// A list result the table decoder cannot read falls back to JSON instead of
// failing the command.
func TestAccessIdentityListFallsBackToJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"unexpected":"shape"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAccessIdentityCLI(t, srv.URL, "access", "identity", "provider", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "unexpected") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestAccessIdentityAPIErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":12109,"message":"access.api.error.group_not_found"}],"result":null}`))
	}))
	defer srv.Close()

	_, _, err := runAccessIdentityCLI(t, srv.URL, "access", "identity", "group", "get", "grp-missing")
	if err == nil || !strings.Contains(err.Error(), "group_not_found") {
		t.Fatalf("err = %v, want the API error", err)
	}
}

// --- command tree shape -----------------------------------------------------

func TestAccessIdentityRegisteredUnderAccess(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"access", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "identity") {
		t.Errorf("cf access help does not list identity:\n%s", out.String())
	}
}

func TestAccessIdentityHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"access", "identity", "provider", "create", "--help"},
			[]string{"cf access identity provider create", "--type", "--config", "--scim-config"}},
		{[]string{"access", "identity", "provider", "update", "--help"},
			[]string{"cf access identity provider update", "replaces the whole provider"}},
		{[]string{"access", "identity", "provider", "delete", "--help"},
			[]string{"cf access identity provider delete", "--force"}},
		{[]string{"access", "identity", "group", "create", "--help"},
			[]string{"cf access identity group create", "email_domain", "--include", "--exclude"}},
		{[]string{"access", "identity", "group", "update", "--help"},
			[]string{"cf access identity group update", "replaces the whole group"}},
		{[]string{"access", "identity", "service-token", "create", "--help"},
			[]string{"cf access identity service-token create", "--duration", "never shows it again", "8760h"}},
		{[]string{"access", "identity", "service-token", "rotate", "--help"},
			[]string{"cf access identity service-token rotate", "--force", "stops working"}},
		{[]string{"access", "identity", "user", "revoke-sessions", "--help"},
			[]string{"cf access identity user revoke-sessions", "--devices", "--force"}},
		{[]string{"access", "identity", "user", "list", "--help"},
			[]string{"cf access identity user list", "--email", "--search"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			root := NewRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			help := out.String()
			for _, want := range tc.want {
				if !strings.Contains(help, want) {
					t.Errorf("help missing %q\n%s", want, help)
				}
			}
		})
	}
}

func TestAccessIdentityRequiredFlags(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"access", "identity", "provider", "create", "IdP"}, "type"},
		{[]string{"access", "identity", "provider", "update", "idp-1", "--type", "okta", "--config", "{}"}, "name"},
		{[]string{"access", "identity", "provider", "update", "idp-1", "--name", "IdP"}, "type"},
		{[]string{"access", "identity", "group", "create", "G"}, "include"},
		{[]string{"access", "identity", "group", "update", "grp-1", "--include", `[{"everyone":{}}]`}, "name"},
		{[]string{"access", "identity", "group", "update", "grp-1", "--name", "G"}, "include"},
		{[]string{"access", "identity", "service-token", "update", "tok-1"}, "name"},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
			_, _, err := runAccessIdentityCLI(t, "http://example.invalid", append(tc.args, "--dry-run")...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a required-flag error naming %q", err, tc.want)
			}
		})
	}
}

func TestAccessIdentityCommandsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"access", "identity", "provider", "list", "extra"},
		{"access", "identity", "provider", "get", "idp-1", "extra"},
		{"access", "identity", "provider", "create", "IdP", "extra", "--type", "onetimepin"},
		{"access", "identity", "group", "list", "extra"},
		{"access", "identity", "group", "create", "G", "extra", "--include", `[{"everyone":{}}]`},
		{"access", "identity", "service-token", "list", "extra"},
		{"access", "identity", "service-token", "rotate", "tok-1", "extra", "--force"},
		{"access", "identity", "user", "list", "extra"},
		{"access", "identity", "user", "revoke-sessions", "jane@example.com", "extra", "--force"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			if _, _, err := runAccessIdentityCLI(t, "http://example.invalid", append(args, "--dry-run")...); err == nil {
				t.Fatal("expected error for stray positional args")
			}
		})
	}
}
