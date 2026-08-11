package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trmdy/cf-cli/internal/config"
)

const pagesTestAccountID = "abcdef0123456789abcdef0123456789"

// runPagesCLI executes the root command against a stub server with an explicit
// account so resolution never depends on the developer's environment.
func runPagesCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", pagesTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

type pagesDump struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body"`
}

func pagesDryRun(t *testing.T, args ...string) pagesDump {
	t.Helper()
	stdout, _, err := runPagesCLI(t, "http://example.invalid", append(args, "--dry-run")...)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	var d pagesDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

func pagesAssertJSONEqual(t *testing.T, got []byte, want string) {
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

// --- pure helpers -----------------------------------------------------------

func TestPagesAccountID(t *testing.T) {
	id, err := pagesAccountID(config.Resolved{AccountID: pagesTestAccountID})
	if err != nil {
		t.Fatal(err)
	}
	if id != pagesTestAccountID {
		t.Fatalf("account = %q", id)
	}
	if _, err := pagesAccountID(config.Resolved{AccountID: "   "}); err == nil || !strings.Contains(err.Error(), "--account-id") {
		t.Fatalf("expected actionable account error, got %v", err)
	}
}

func TestPagesPaths(t *testing.T) {
	if got := pagesProjectsPath(pagesTestAccountID); got != "/accounts/"+pagesTestAccountID+"/pages/projects" {
		t.Errorf("projects path = %s", got)
	}
	if got := pagesProjectPath(pagesTestAccountID, "my-site"); !strings.HasSuffix(got, "/pages/projects/my-site") {
		t.Errorf("project path = %s", got)
	}
	if got := pagesDeploymentPath(pagesTestAccountID, "my-site", "dep-1"); !strings.HasSuffix(got, "/projects/my-site/deployments/dep-1") {
		t.Errorf("deployment path = %s", got)
	}
	if got := pagesDomainPath(pagesTestAccountID, "my-site", "www.example.com"); !strings.HasSuffix(got, "/projects/my-site/domains/www.example.com") {
		t.Errorf("domain path = %s", got)
	}
	// Path segments must be escaped so odd names cannot alter the route.
	if got := pagesProjectPath(pagesTestAccountID, "a/b c"); !strings.HasSuffix(got, "/pages/projects/a%2Fb%20c") {
		t.Errorf("escaped project path = %s", got)
	}
}

func TestPagesArg(t *testing.T) {
	v, err := pagesArg("project name", "  my-site  ")
	if err != nil {
		t.Fatal(err)
	}
	if v != "my-site" {
		t.Fatalf("value = %q", v)
	}
	if _, err := pagesArg("project name", "   "); err == nil || !strings.Contains(err.Error(), "project name is required") {
		t.Fatalf("expected required error, got %v", err)
	}
}

func TestPagesTime(t *testing.T) {
	cases := map[string]string{
		"2021-03-09T00:55:03.923456Z": "2021-03-09 00:55:03",
		"2021-03-09T00:58:59.045655":  "2021-03-09 00:58:59",
		"2021-03-09T02:55:03+02:00":   "2021-03-09 00:55:03",
		"":                            "",
		"whenever":                    "whenever",
	}
	for in, want := range cases {
		if got := pagesTime(in); got != want {
			t.Errorf("pagesTime(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildPagesProjectBodyMinimal(t *testing.T) {
	body, err := buildPagesProjectBody("my-site", "main", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pagesAssertJSONEqual(t, body, `{"name":"my-site","production_branch":"main"}`)
}

func TestBuildPagesProjectBodyWithBuildConfig(t *testing.T) {
	body, err := buildPagesProjectBody(" my-site ", "release", "npm run build", "dist", "app")
	if err != nil {
		t.Fatal(err)
	}
	pagesAssertJSONEqual(t, body, `{
		"name":"my-site",
		"production_branch":"release",
		"build_config":{"build_command":"npm run build","destination_dir":"dist","root_dir":"app"}
	}`)
}

func TestBuildPagesProjectBodyPartialBuildConfig(t *testing.T) {
	body, err := buildPagesProjectBody("my-site", "main", "", "dist", "")
	if err != nil {
		t.Fatal(err)
	}
	pagesAssertJSONEqual(t, body, `{"name":"my-site","production_branch":"main","build_config":{"destination_dir":"dist"}}`)
}

func TestBuildPagesProjectBodyRequiresName(t *testing.T) {
	if _, err := buildPagesProjectBody("  ", "main", "", "", ""); err == nil || !strings.Contains(err.Error(), "project name is required") {
		t.Fatalf("expected name error, got %v", err)
	}
}

func TestBuildPagesDomainBody(t *testing.T) {
	body, err := buildPagesDomainBody(" www.example.com ")
	if err != nil {
		t.Fatal(err)
	}
	pagesAssertJSONEqual(t, body, `{"name":"www.example.com"}`)
	if _, err := buildPagesDomainBody(""); err == nil || !strings.Contains(err.Error(), "domain is required") {
		t.Fatalf("expected domain error, got %v", err)
	}
}

func TestPagesDeploymentListQuery(t *testing.T) {
	q, err := pagesDeploymentListQuery("")
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 0 {
		t.Errorf("query = %v, want empty", q)
	}
	q, err = pagesDeploymentListQuery(" Production ")
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("env") != "production" {
		t.Errorf("env = %q", q.Get("env"))
	}
	if _, err := pagesDeploymentListQuery("staging"); err == nil || !strings.Contains(err.Error(), "production or preview") {
		t.Fatalf("expected env error, got %v", err)
	}
}

// --- request construction (dry run) ----------------------------------------

func TestPagesDryRunRequests(t *testing.T) {
	base := "/accounts/" + pagesTestAccountID + "/pages/projects"
	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantURL    string
		wantBody   string
	}{
		{
			name:       "project list",
			args:       []string{"pages", "project", "list"},
			wantMethod: "GET",
			wantURL:    base,
		},
		{
			name:       "project get",
			args:       []string{"pages", "project", "get", "my-site"},
			wantMethod: "GET",
			wantURL:    base + "/my-site",
		},
		{
			name:       "project create",
			args:       []string{"pages", "project", "create", "my-site", "--production-branch", "main"},
			wantMethod: "POST",
			wantURL:    base,
			wantBody:   `{"name":"my-site","production_branch":"main"}`,
		},
		{
			name:       "project delete",
			args:       []string{"pages", "project", "delete", "my-site"},
			wantMethod: "DELETE",
			wantURL:    base + "/my-site",
		},
		{
			name:       "deployment list",
			args:       []string{"pages", "deployment", "list", "my-site"},
			wantMethod: "GET",
			wantURL:    base + "/my-site/deployments",
		},
		{
			name:       "deployment list filtered",
			args:       []string{"pages", "deployment", "list", "my-site", "--env", "preview"},
			wantMethod: "GET",
			wantURL:    base + "/my-site/deployments?env=preview",
		},
		{
			name:       "deployment get",
			args:       []string{"pages", "deployment", "get", "my-site", "dep-1"},
			wantMethod: "GET",
			wantURL:    base + "/my-site/deployments/dep-1",
		},
		{
			name:       "deployment rollback",
			args:       []string{"pages", "deployment", "rollback", "my-site", "dep-1"},
			wantMethod: "POST",
			wantURL:    base + "/my-site/deployments/dep-1/rollback",
		},
		{
			name:       "domain add",
			args:       []string{"pages", "domain", "add", "my-site", "www.example.com"},
			wantMethod: "POST",
			wantURL:    base + "/my-site/domains",
			wantBody:   `{"name":"www.example.com"}`,
		},
		{
			name:       "domain remove",
			args:       []string{"pages", "domain", "remove", "my-site", "www.example.com"},
			wantMethod: "DELETE",
			wantURL:    base + "/my-site/domains/www.example.com",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := pagesDryRun(t, tc.args...)
			if d.Method != tc.wantMethod {
				t.Errorf("method = %s, want %s", d.Method, tc.wantMethod)
			}
			if !strings.HasSuffix(d.URL, tc.wantURL) {
				t.Errorf("url = %s, want suffix %s", d.URL, tc.wantURL)
			}
			if tc.wantBody == "" {
				if len(d.Body) != 0 {
					t.Errorf("body = %s, want none", d.Body)
				}
				return
			}
			pagesAssertJSONEqual(t, d.Body, tc.wantBody)
		})
	}
}

func TestPagesDryRunSkipsConfirmation(t *testing.T) {
	// Destructive commands must stay usable under --dry-run without --force.
	for _, args := range [][]string{
		{"pages", "project", "delete", "my-site"},
		{"pages", "domain", "remove", "my-site", "www.example.com"},
		{"pages", "deployment", "rollback", "my-site", "dep-1"},
	} {
		if d := pagesDryRun(t, args...); d.URL == "" {
			t.Errorf("%v: empty dry-run url", args)
		}
	}
}

func TestPagesRejectsEmptyArgs(t *testing.T) {
	cases := [][]string{
		{"pages", "project", "get", " "},
		{"pages", "deployment", "get", "my-site", ""},
		{"pages", "domain", "add", "my-site", ""},
		{"pages", "domain", "remove", "", "www.example.com"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := runPagesCLI(t, "http://example.invalid", append(args, "--dry-run")...)
			if err == nil || !strings.Contains(err.Error(), "is required") {
				t.Fatalf("expected required-arg error, got %v", err)
			}
		})
	}
}

func TestPagesDeploymentListRejectsBadEnv(t *testing.T) {
	_, _, err := runPagesCLI(t, "http://example.invalid",
		"pages", "deployment", "list", "my-site", "--env", "staging", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "production or preview") {
		t.Fatalf("expected env error, got %v", err)
	}
}

func TestPagesRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t", "--dry-run", "pages", "project", "list"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("expected account error, got %v", err)
	}
}

// --- HTTP behavior ----------------------------------------------------------

func TestPagesProjectListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+pagesTestAccountID+"/pages/projects" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"p1","name":"my-site","subdomain":"my-site.pages.dev","production_branch":"main",
			 "domains":["example.com","www.example.com"],"created_on":"2021-03-09T00:55:03.923456Z"}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runPagesCLI(t, srv.URL, "pages", "project", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "SUBDOMAIN", "BRANCH", "DOMAINS", "CREATED", "my-site", "example.com,www.example.com", "2021-03-09 00:55:03"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestPagesProjectListPaginates(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "", "1":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"a"},{"name":"b"}],
				"result_info":{"page":1,"per_page":2,"count":2,"total_count":3,"total_pages":2}}`))
		case "2":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"c"}],
				"result_info":{"page":2,"per_page":2,"count":1,"total_count":3,"total_pages":2}}`))
		default:
			t.Errorf("unexpected page %q", page)
		}
	}))
	defer srv.Close()

	stdout, _, err := runPagesCLI(t, srv.URL, "pages", "project", "list")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("requested pages = %v, want two requests", pages)
	}
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output missing project %q\n%s", want, stdout)
		}
	}
}

func TestPagesProjectListJSONAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"p1","name":"my-site"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runPagesCLI(t, srv.URL, "pages", "project", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var projects []map[string]any
	if err := json.Unmarshal([]byte(stdout), &projects); err != nil {
		t.Fatalf("json output not an array: %v\n%s", err, stdout)
	}

	stdout, _, err = runPagesCLI(t, srv.URL, "pages", "project", "list", "--query", ".[0].name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "my-site") || strings.Contains(stdout, "p1") {
		t.Errorf("query output = %s", stdout)
	}
}

func TestPagesDeploymentListTable(t *testing.T) {
	var gotEnv string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+pagesTestAccountID+"/pages/projects/my-site/deployments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotEnv = r.URL.Query().Get("env")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"dep-1","environment":"production","url":"https://dep-1.my-site.pages.dev",
			 "created_on":"2021-03-09T00:55:03.923456Z",
			 "latest_stage":{"name":"deploy","status":"success"},
			 "deployment_trigger":{"metadata":{"branch":"main","commit_hash":"abc"}}}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runPagesCLI(t, srv.URL, "pages", "deployment", "list", "my-site", "--env", "production")
	if err != nil {
		t.Fatal(err)
	}
	if gotEnv != "production" {
		t.Errorf("env query = %q", gotEnv)
	}
	for _, want := range []string{"ID", "ENVIRONMENT", "STATUS", "STAGE", "BRANCH", "URL", "dep-1", "success", "deploy", "main", "https://dep-1.my-site.pages.dev"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestPagesDeploymentRollbackHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"dep-1","environment":"production"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runPagesCLI(t, srv.URL, "pages", "deployment", "rollback", "my-site", "dep-1", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+pagesTestAccountID+"/pages/projects/my-site/deployments/dep-1/rollback" {
		t.Errorf("path = %s", gotPath)
	}
	if len(gotBody) != 0 {
		t.Errorf("body = %s, want empty", gotBody)
	}
	if !strings.Contains(stdout, "dep-1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestPagesDomainAddHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"d1","name":"www.example.com","status":"pending"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runPagesCLI(t, srv.URL, "pages", "domain", "add", "my-site", "www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+pagesTestAccountID+"/pages/projects/my-site/domains" {
		t.Errorf("path = %s", gotPath)
	}
	pagesAssertJSONEqual(t, gotBody, `{"name":"www.example.com"}`)
	if !strings.Contains(stdout, "pending") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestPagesProjectCreateHTTPRequest(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"p1","name":"my-site"}}`))
	}))
	defer srv.Close()

	_, _, err := runPagesCLI(t, srv.URL, "pages", "project", "create", "my-site",
		"--build-command", "npm run build", "--destination-dir", "dist")
	if err != nil {
		t.Fatal(err)
	}
	pagesAssertJSONEqual(t, gotBody, `{"name":"my-site","production_branch":"main","build_config":{"build_command":"npm run build","destination_dir":"dist"}}`)
}

func TestPagesAPIErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":8000007,"message":"Project not found"}],"result":null}`))
	}))
	defer srv.Close()

	_, _, err := runPagesCLI(t, srv.URL, "pages", "project", "get", "missing")
	if err == nil || !strings.Contains(err.Error(), "Project not found") {
		t.Fatalf("expected API error, got %v", err)
	}
}

func TestPagesDestructiveCommandsRequireForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cases := [][]string{
		{"pages", "project", "delete", "my-site"},
		{"pages", "domain", "remove", "my-site", "www.example.com"},
		{"pages", "deployment", "rollback", "my-site", "dep-1"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := runPagesCLI(t, srv.URL, args...)
			if err == nil || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("expected force/abort error, got %v", err)
			}
		})
	}
}

func TestPagesCommandsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"pages", "project", "list", "extra"},
		{"pages", "project", "get", "my-site", "extra"},
		{"pages", "deployment", "list", "my-site", "extra"},
		{"pages", "domain", "add", "my-site", "www.example.com", "extra"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := runPagesCLI(t, "http://example.invalid", append(args, "--dry-run")...)
			if err == nil {
				t.Fatal("expected error for stray positional args")
			}
		})
	}
}

func TestPagesHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"pages", "project", "create", "--help"}, []string{"cf pages project create my-site", "--production-branch", "--build-command"}},
		{[]string{"pages", "deployment", "rollback", "--help"}, []string{"cf pages deployment rollback my-site", "--force"}},
		{[]string{"pages", "domain", "add", "--help"}, []string{"cf pages domain add my-site www.example.com"}},
		{[]string{"pages", "domain", "remove", "--help"}, []string{"cf pages domain remove my-site www.example.com", "--force"}},
		{[]string{"pages", "deployment", "list", "--help"}, []string{"cf pages deployment list my-site", "--env"}},
		{[]string{"pages", "project", "delete", "--help"}, []string{"cf pages project delete my-site", "--force"}},
		{[]string{"pages", "project", "list", "--help"}, []string{"cf pages project list"}},
		{[]string{"pages", "project", "get", "--help"}, []string{"cf pages project get my-site"}},
		{[]string{"pages", "deployment", "get", "--help"}, []string{"cf pages deployment get my-site"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
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
