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
	webAnalyticsTestAccountID = "abcdef0123456789abcdef0123456789"
	webAnalyticsTestSiteID    = "023e105f4ecef8ad9ca31a8372d0c353"
	webAnalyticsTestRulesetID = "f174e90a-fafe-4643-bbbc-4a0ed4fc8415"
	webAnalyticsTestRuleID    = "a174e90a-fafe-4643-bbbc-4a0ed4fc8415"
	webAnalyticsTestZoneTag   = "023e105f4ecef8ad9ca31a8372d0c353"
)

func runWebAnalyticsCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", webAnalyticsTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func webAnalyticsAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want not JSON: %v\n%s", err, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", gb, wb)
	}
}

func webAnalyticsParseDump(t *testing.T, stdout string) (method, url string, body json.RawMessage) {
	t.Helper()
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dump: %v\n%s", err, stdout)
	}
	return dump.Method, dump.URL, dump.Body
}

// --- body builders ---------------------------------------------------------

func TestBuildWebAnalyticsCreateBodyHostOnly(t *testing.T) {
	body, err := buildWebAnalyticsCreateBody(webAnalyticsCreateOpts{Host: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	webAnalyticsAssertJSONEqual(t, body, `{"host":"example.com"}`)
}

func TestBuildWebAnalyticsCreateBodyZoneAutoInstall(t *testing.T) {
	yes := true
	body, err := buildWebAnalyticsCreateBody(webAnalyticsCreateOpts{
		ZoneTag:     webAnalyticsTestZoneTag,
		AutoInstall: &yes,
	})
	if err != nil {
		t.Fatal(err)
	}
	webAnalyticsAssertJSONEqual(t, body, `{"zone_tag":"023e105f4ecef8ad9ca31a8372d0c353","auto_install":true}`)
}

func TestBuildWebAnalyticsCreateBodyAllFields(t *testing.T) {
	no := false
	body, err := buildWebAnalyticsCreateBody(webAnalyticsCreateOpts{
		Host:        "blog.example.com",
		ZoneTag:     webAnalyticsTestZoneTag,
		AutoInstall: &no,
	})
	if err != nil {
		t.Fatal(err)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"host":"blog.example.com",
		"zone_tag":"023e105f4ecef8ad9ca31a8372d0c353",
		"auto_install":false
	}`)
}

func TestBuildWebAnalyticsCreateBodyValidation(t *testing.T) {
	cases := []struct {
		name    string
		o       webAnalyticsCreateOpts
		wantErr string
	}{
		{"missing host and zone", webAnalyticsCreateOpts{}, "--host and/or --zone-tag"},
		{"empty host", webAnalyticsCreateOpts{Host: "  "}, "--host must not be empty"},
		{"wildcard host", webAnalyticsCreateOpts{Host: "*.example.com"}, "wildcards"},
		{"bad host", webAnalyticsCreateOpts{Host: "not_a_host"}, "not a valid hostname"},
		{"zone too short", webAnalyticsCreateOpts{ZoneTag: "abc"}, "32 hex"},
		{"zone non-hex", webAnalyticsCreateOpts{ZoneTag: strings.Repeat("g", 32)}, "hex zone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildWebAnalyticsCreateBody(tc.o); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestBuildWebAnalyticsUpdateBody(t *testing.T) {
	host := "www.example.com"
	yes, no := true, false
	body, err := buildWebAnalyticsUpdateBody(webAnalyticsUpdateOpts{
		Host:        &host,
		AutoInstall: &yes,
		Enabled:     &yes,
		Lite:        &no,
	})
	if err != nil {
		t.Fatal(err)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"host":"www.example.com",
		"auto_install":true,
		"enabled":true,
		"lite":false
	}`)
}

func TestBuildWebAnalyticsUpdateBodyValidation(t *testing.T) {
	empty := ""
	no := false
	yes := true
	cases := []struct {
		name    string
		o       webAnalyticsUpdateOpts
		wantErr string
	}{
		{"nothing", webAnalyticsUpdateOpts{}, "nothing to update"},
		{"empty host", webAnalyticsUpdateOpts{Host: &empty}, "--host must not be empty"},
		{"enabled without auto_install true", webAnalyticsUpdateOpts{AutoInstall: &no, Enabled: &yes}, "--enabled can only be used when --auto-install is true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildWebAnalyticsUpdateBody(tc.o); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
	// enabled alone is allowed (API checks current auto_install).
	if _, err := buildWebAnalyticsUpdateBody(webAnalyticsUpdateOpts{Enabled: &yes}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildWebAnalyticsRuleBody(t *testing.T) {
	yes, no := true, false
	body, err := buildWebAnalyticsRuleBody(webAnalyticsRuleOpts{
		Host:      "example.com",
		Inclusive: &yes,
		IsPaused:  &no,
		Paths:     []string{"*", "/app/*"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"host":"example.com",
		"inclusive":true,
		"is_paused":false,
		"paths":["*","/app/*"]
	}`)
}

func TestBuildWebAnalyticsRuleBodyCanonicalWireKeys(t *testing.T) {
	// Wire field is is_paused, not paused.
	yes := true
	body, err := buildWebAnalyticsRuleBody(webAnalyticsRuleOpts{IsPaused: &yes}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"is_paused":true`) {
		t.Fatalf("expected is_paused wire key, got %s", body)
	}
	if strings.Contains(string(body), `"paused"`) {
		t.Fatalf("should not send paused key: %s", body)
	}
}

func TestBuildWebAnalyticsRuleApplyBody(t *testing.T) {
	body, err := buildWebAnalyticsRuleApplyBody(
		true, `[{"host":"example.com","paths":["*"],"inclusive":true}]`,
		true, `["`+webAnalyticsTestRuleID+`"]`,
	)
	if err != nil {
		t.Fatal(err)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"rules":[{"host":"example.com","paths":["*"],"inclusive":true}],
		"delete_rules":["a174e90a-fafe-4643-bbbc-4a0ed4fc8415"]
	}`)
}

func TestBuildWebAnalyticsRuleApplyBodyRejectsNullAndWrongShapes(t *testing.T) {
	cases := []struct {
		name      string
		rulesSet  bool
		rules     string
		deleteSet bool
		delete    string
		wantErr   string
	}{
		{"nothing", false, "", false, "", "nothing to apply"},
		{"rules null", true, "null", false, "", "not null"},
		{"rules object", true, `{"host":"x"}`, false, "", "JSON array"},
		{"rules scalar", true, `"x"`, false, "", "JSON array"},
		{"rules element null", true, `[null]`, false, "", "not null"},
		{"rules element scalar", true, `["x"]`, false, "", "object"},
		{"delete null", false, "", true, "null", "not null"},
		{"delete object", false, "", true, `{}`, "JSON array"},
		{"delete empty string element", false, "", true, `[""]`, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildWebAnalyticsRuleApplyBody(tc.rulesSet, tc.rules, tc.deleteSet, tc.delete); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// --- bounds ----------------------------------------------------------------

// webAnalyticsHostOfLen returns a hostname of exactly n characters that
// satisfies webAnalyticsHostPattern when n is large enough for "a." + mid + ".com".
func webAnalyticsHostOfLen(n int) string {
	// "a." + mid + ".com" => 2 + mid + 4 = n => mid = n-6
	if n < 7 {
		return strings.Repeat("a", n)
	}
	mid := n - 6
	return "a." + strings.Repeat("b", mid) + ".com"
}

func TestWebAnalyticsHostBoundaries(t *testing.T) {
	atLimit := webAnalyticsHostOfLen(webAnalyticsMaxHostLen)
	if len(atLimit) != webAnalyticsMaxHostLen {
		t.Fatalf("fixture length = %d, want %d", len(atLimit), webAnalyticsMaxHostLen)
	}
	if !webAnalyticsHostPattern.MatchString(atLimit) {
		t.Fatalf("fixture must match host pattern: %q", atLimit)
	}
	over := atLimit + "x"

	cases := []struct {
		name    string
		host    string
		wantErr bool
	}{
		{"valid short", "example.com", false},
		{"at max length", atLimit, false},
		{"one over max length", over, true},
		{"empty", "", true},
		{"wildcard", "*.example.com", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebAnalyticsHost(tc.host)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate error = %v, wantErr %v", err, tc.wantErr)
			}
			_, cerr := buildWebAnalyticsCreateBody(webAnalyticsCreateOpts{Host: tc.host})
			if tc.host == "" {
				// empty host with no zone fails the require-one check first.
				if cerr == nil {
					t.Fatal("expected create error for empty host")
				}
				return
			}
			if (cerr != nil) != tc.wantErr {
				t.Fatalf("create body error = %v, wantErr %v", cerr, tc.wantErr)
			}
		})
	}
}

func TestWebAnalyticsZoneTagBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		wantErr bool
	}{
		{"exact 32 hex", webAnalyticsTestZoneTag, false},
		{"31 chars", strings.Repeat("a", 31), true},
		{"33 chars", strings.Repeat("a", 33), true},
		{"32 non-hex", strings.Repeat("z", 32), true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWebAnalyticsZoneTag(tc.tag)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestParseWebAnalyticsJSONStringArray(t *testing.T) {
	got, err := parseWebAnalyticsJSONStringArray("paths", `["*","/a"]`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "*,/a" {
		t.Fatalf("got %v", got)
	}
	for _, raw := range []string{"null", `{}`, `"x"`, `1`, `true`, `[1]`, `[{"a":1}]`, "{"} {
		if _, err := parseWebAnalyticsJSONStringArray("paths", raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

// --- validation before client / network ------------------------------------

func TestWebAnalyticsCreateRejectsInvalidBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no host or zone", []string{"web-analytics", "create", "--dry-run"}, "--host and/or --zone-tag"},
		{"bad order-by", []string{"web-analytics", "list", "--order-by", "name", "--dry-run"}, "--order-by must be one of"},
		{"enabled with auto-install false", []string{"web-analytics", "update", webAnalyticsTestSiteID, "--auto-install=false", "--enabled", "--dry-run"}, "--enabled can only be used"},
		{"paths null", []string{"web-analytics", "rule", "create", "--ruleset-id", webAnalyticsTestRulesetID, "--paths", "null", "--dry-run"}, "not null"},
		{"rules null", []string{"web-analytics", "rule", "apply", "--ruleset-id", webAnalyticsTestRulesetID, "--rules", "null", "--dry-run"}, "not null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWebAnalyticsCLI(t, srv.URL, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestWebAnalyticsMissingAccountIDBeforeClientUse(t *testing.T) {
	// Real command tree without --account-id must fail with an actionable message.
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--token", "t", "web-analytics", "list", "--dry-run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "account ID") {
		t.Fatalf("error = %v", err)
	}
}

// --- httptest: exact paths, payloads, output --------------------------------

func TestWebAnalyticsListHTTPRequestAndTable(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{
			"site_tag":"` + webAnalyticsTestSiteID + `",
			"host":"example.com",
			"auto_install":true,
			"created":"2014-01-01T05:20:00.12345Z",
			"ruleset":{"id":"` + webAnalyticsTestRulesetID + `","zone_name":"example.com"}
		}],"result_info":{"page":1,"total_pages":1}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "list", "--order-by", "created")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := "/accounts/" + webAnalyticsTestAccountID + "/rum/site_info/list"
	if gotPath != wantPath {
		t.Fatalf("path = %s, want %s", gotPath, wantPath)
	}
	if !strings.Contains(gotQuery, "order_by=created") || !strings.Contains(gotQuery, "per_page=100") {
		t.Fatalf("query = %s", gotQuery)
	}
	for _, want := range []string{"SITE_TAG", webAnalyticsTestSiteID, "example.com", "true", webAnalyticsTestRulesetID} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestWebAnalyticsListOrderByHostCanonical(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	if _, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "list", "--order-by", "host"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "order_by=host") {
		t.Fatalf("query = %s", gotQuery)
	}
}

func TestWebAnalyticsGetPath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"site_tag":"` + webAnalyticsTestSiteID + `","host":"example.com"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "get", webAnalyticsTestSiteID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" {
		t.Fatalf("method = %s", gotMethod)
	}
	want := "/accounts/" + webAnalyticsTestAccountID + "/rum/site_info/" + webAnalyticsTestSiteID
	if gotPath != want {
		t.Fatalf("path = %s, want %s", gotPath, want)
	}
	if !strings.Contains(stdout, webAnalyticsTestSiteID) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestWebAnalyticsCreateDryRunPathAndBody(t *testing.T) {
	stdout, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "create",
		"--host", "example.com",
		"--zone-tag", webAnalyticsTestZoneTag,
		"--auto-install",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := webAnalyticsParseDump(t, stdout)
	if method != "POST" {
		t.Fatalf("method = %s", method)
	}
	wantSuffix := "/accounts/" + webAnalyticsTestAccountID + "/rum/site_info"
	if !strings.HasSuffix(u, wantSuffix) {
		t.Fatalf("url = %s, want suffix %s", u, wantSuffix)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"host":"example.com",
		"zone_tag":"023e105f4ecef8ad9ca31a8372d0c353",
		"auto_install":true
	}`)
}

func TestWebAnalyticsCreateHTTP(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"site_tag":"` + webAnalyticsTestSiteID + `","site_token":"tok","snippet":"<script/>"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "create", "--host", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/accounts/"+webAnalyticsTestAccountID+"/rum/site_info" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	webAnalyticsAssertJSONEqual(t, gotBody, `{"host":"example.com"}`)
	if !strings.Contains(stdout, "site_tag") || !strings.Contains(stdout, webAnalyticsTestSiteID) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestWebAnalyticsUpdateDryRun(t *testing.T) {
	stdout, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "update", webAnalyticsTestSiteID,
		"--auto-install", "--enabled", "--lite=false",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := webAnalyticsParseDump(t, stdout)
	if method != "PUT" {
		t.Fatalf("method = %s", method)
	}
	wantSuffix := "/accounts/" + webAnalyticsTestAccountID + "/rum/site_info/" + webAnalyticsTestSiteID
	if !strings.HasSuffix(u, wantSuffix) {
		t.Fatalf("url = %s", u)
	}
	webAnalyticsAssertJSONEqual(t, body, `{"auto_install":true,"enabled":true,"lite":false}`)
}

func TestWebAnalyticsDeleteForcePath(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	if _, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "delete", webAnalyticsTestSiteID, "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Fatalf("method = %s", gotMethod)
	}
	want := "/accounts/" + webAnalyticsTestAccountID + "/rum/site_info/" + webAnalyticsTestSiteID
	if gotPath != want {
		t.Fatalf("path = %s, want %s", gotPath, want)
	}
}

func TestWebAnalyticsDeleteWithoutForceAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "delete", webAnalyticsTestSiteID)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("error = %v", err)
	}
}

// --- rules httptest -----------------------------------------------------------

func TestWebAnalyticsRuleListPathAndTable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{
			"rules":[{
				"id":"` + webAnalyticsTestRuleID + `",
				"host":"example.com",
				"inclusive":true,
				"is_paused":false,
				"paths":["*","/app/*"],
				"priority":1000
			}],
			"ruleset":{"id":"` + webAnalyticsTestRulesetID + `","enabled":true}
		}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWebAnalyticsCLI(t, srv.URL, "web-analytics", "rule", "list", "--ruleset-id", webAnalyticsTestRulesetID)
	if err != nil {
		t.Fatal(err)
	}
	want := "/accounts/" + webAnalyticsTestAccountID + "/rum/v2/" + webAnalyticsTestRulesetID + "/rules"
	if gotPath != want {
		t.Fatalf("path = %s, want %s", gotPath, want)
	}
	for _, col := range []string{"ID", "HOST", "INCLUSIVE", "PAUSED", "PATHS", webAnalyticsTestRuleID, "example.com", "true", "false", "*"} {
		if !strings.Contains(stdout, col) {
			t.Fatalf("table missing %q:\n%s", col, stdout)
		}
	}
}

func TestWebAnalyticsRuleCreateDryRun(t *testing.T) {
	stdout, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "rule", "create",
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--host", "example.com",
		"--path", "*",
		"--path", "/blog/*",
		"--inclusive",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := webAnalyticsParseDump(t, stdout)
	if method != "POST" {
		t.Fatalf("method = %s", method)
	}
	wantSuffix := "/accounts/" + webAnalyticsTestAccountID + "/rum/v2/" + webAnalyticsTestRulesetID + "/rule"
	if !strings.HasSuffix(u, wantSuffix) {
		t.Fatalf("url = %s", u)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"host":"example.com",
		"inclusive":true,
		"paths":["*","/blog/*"]
	}`)
}

func TestWebAnalyticsRuleCreatePathsJSON(t *testing.T) {
	stdout, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "rule", "create",
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--paths", `["*"]`,
		"--paused",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := webAnalyticsParseDump(t, stdout)
	webAnalyticsAssertJSONEqual(t, body, `{"is_paused":true,"paths":["*"]}`)
}

func TestWebAnalyticsRuleUpdateHTTP(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + webAnalyticsTestRuleID + `","host":"example.com","is_paused":true}}`))
	}))
	defer srv.Close()

	if _, _, err := runWebAnalyticsCLI(t, srv.URL,
		"web-analytics", "rule", "update", webAnalyticsTestRuleID,
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--paused",
		"--inclusive=false",
	); err != nil {
		t.Fatal(err)
	}
	want := "/accounts/" + webAnalyticsTestAccountID + "/rum/v2/" + webAnalyticsTestRulesetID + "/rule/" + webAnalyticsTestRuleID
	if gotMethod != "PUT" || gotPath != want {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	webAnalyticsAssertJSONEqual(t, gotBody, `{"inclusive":false,"is_paused":true}`)
}

func TestWebAnalyticsRuleDeleteForce(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + webAnalyticsTestRuleID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runWebAnalyticsCLI(t, srv.URL,
		"web-analytics", "rule", "delete", webAnalyticsTestRuleID,
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--force",
	); err != nil {
		t.Fatal(err)
	}
	want := "/accounts/" + webAnalyticsTestAccountID + "/rum/v2/" + webAnalyticsTestRulesetID + "/rule/" + webAnalyticsTestRuleID
	if gotMethod != "DELETE" || gotPath != want {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
}

func TestWebAnalyticsRuleApplyDryRun(t *testing.T) {
	stdout, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "rule", "apply",
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--rules", `[{"host":"example.com","inclusive":true,"paths":["*"]}]`,
		"--delete-rules", `["`+webAnalyticsTestRuleID+`"]`,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := webAnalyticsParseDump(t, stdout)
	if method != "POST" {
		t.Fatalf("method = %s", method)
	}
	wantSuffix := "/accounts/" + webAnalyticsTestAccountID + "/rum/v2/" + webAnalyticsTestRulesetID + "/rules"
	if !strings.HasSuffix(u, wantSuffix) {
		t.Fatalf("url = %s", u)
	}
	webAnalyticsAssertJSONEqual(t, body, `{
		"rules":[{"host":"example.com","inclusive":true,"paths":["*"]}],
		"delete_rules":["a174e90a-fafe-4643-bbbc-4a0ed4fc8415"]
	}`)
}

func TestWebAnalyticsRulePathAndPathsMutuallyExclusive(t *testing.T) {
	_, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "rule", "create",
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--path", "*",
		"--paths", `["/a"]`,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "either --path or --paths") {
		t.Fatalf("error = %v", err)
	}
}

func TestWebAnalyticsEmptyPathRejected(t *testing.T) {
	_, _, err := runWebAnalyticsCLI(t, "http://example.invalid",
		"web-analytics", "rule", "create",
		"--ruleset-id", webAnalyticsTestRulesetID,
		"--path", "",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v", err)
	}
}
