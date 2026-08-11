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
	"time"
)

const d1TestAccountID = "0123456789abcdef0123456789abcdef"
const d1TestDatabaseID = "7f0f5e3d-7d2e-4ef2-9db6-100000000001"

func runD1CLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", d1TestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func d1AssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid received JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid expected JSON %q: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}

func TestBuildD1CreateBody(t *testing.T) {
	body, err := buildD1CreateBody("app-data", " EU ", "", "AUTO")
	if err != nil {
		t.Fatal(err)
	}
	d1AssertJSONEqual(t, body, `{"name":"app-data","jurisdiction":"eu","read_replication":{"mode":"auto"}}`)

	body, err = buildD1CreateBody("regional-data", "", " WEUR ", "")
	if err != nil {
		t.Fatal(err)
	}
	d1AssertJSONEqual(t, body, `{"name":"regional-data","primary_location_hint":"weur"}`)

	if _, err := buildD1CreateBody(" ", "", "", ""); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name error, got %v", err)
	}
	if _, err := buildD1CreateBody("app-data", "", "", "nearby"); err == nil || !strings.Contains(err.Error(), "read-replication") {
		t.Fatalf("expected replication error, got %v", err)
	}
}

func TestBuildD1CreateBodyLocationValidation(t *testing.T) {
	for _, value := range []string{"eu", " FEDRAMP ", "us"} {
		got, err := d1Jurisdiction(value)
		if err != nil {
			t.Errorf("jurisdiction %q: %v", value, err)
			continue
		}
		if got != strings.ToLower(strings.TrimSpace(value)) {
			t.Errorf("jurisdiction %q = %q", value, got)
		}
	}
	for _, value := range []string{"wnam", " ENAM ", "weur", "eeur", "apac", "oc"} {
		got, err := d1PrimaryLocation(value)
		if err != nil {
			t.Errorf("primary location %q: %v", value, err)
			continue
		}
		if got != strings.ToLower(strings.TrimSpace(value)) {
			t.Errorf("primary location %q = %q", value, got)
		}
	}
	for _, value := range []string{"asia", "europe"} {
		if _, err := buildD1CreateBody("app-data", "", value, ""); err == nil || !strings.Contains(err.Error(), "primary-location") {
			t.Errorf("primary location %q error = %v", value, err)
		}
	}
	for _, value := range []string{"apac", "global"} {
		if _, err := buildD1CreateBody("app-data", value, "", ""); err == nil || !strings.Contains(err.Error(), "jurisdiction") {
			t.Errorf("jurisdiction %q error = %v", value, err)
		}
	}
	if _, err := buildD1CreateBody("app-data", "eu", "weur", ""); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("expected conflicting location error, got %v", err)
	}
}

func TestBuildD1UpdateBody(t *testing.T) {
	body, err := buildD1UpdateBody("disabled")
	if err != nil {
		t.Fatal(err)
	}
	d1AssertJSONEqual(t, body, `{"read_replication":{"mode":"disabled"}}`)
}

func TestD1SQLCommandFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schema.sql")
	if err := os.WriteFile(path, []byte("CREATE TABLE users (id INTEGER);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sql, err := d1SQLCommand("@" + path)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "CREATE TABLE users (id INTEGER);\n" {
		t.Errorf("sql = %q", sql)
	}
	if _, err := d1SQLCommand("@"); err == nil || !strings.Contains(err.Error(), "file path") {
		t.Fatalf("expected file path error, got %v", err)
	}
	if _, err := d1SQLCommand(" \n\t "); err == nil || !strings.Contains(err.Error(), "must contain SQL") {
		t.Fatalf("expected whitespace SQL error, got %v", err)
	}
}

func TestBuildD1QueryBody(t *testing.T) {
	body, err := buildD1QueryBody("SELECT * FROM users WHERE id = ?", []string{"42"})
	if err != nil {
		t.Fatal(err)
	}
	d1AssertJSONEqual(t, body, `{"sql":"SELECT * FROM users WHERE id = ?","params":["42"]}`)
}

func TestBuildD1ExportBody(t *testing.T) {
	body, err := buildD1ExportBody(true, false, []string{"users", "sessions"}, "")
	if err != nil {
		t.Fatal(err)
	}
	d1AssertJSONEqual(t, body, `{"output_format":"polling","dump_options":{"no_data":true,"tables":["users","sessions"]}}`)

	body, err = buildD1ExportBody(false, false, nil, "bookmark-1")
	if err != nil {
		t.Fatal(err)
	}
	d1AssertJSONEqual(t, body, `{"output_format":"polling","current_bookmark":"bookmark-1"}`)

	if _, err := buildD1ExportBody(true, true, nil, ""); err == nil || !strings.Contains(err.Error(), "cannot") {
		t.Fatalf("expected conflicting options error, got %v", err)
	}
}

func TestD1ListHTTPRequest(t *testing.T) {
	var gotPath, gotName, gotPerPage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotName = r.URL.Query().Get("name")
		gotPerPage = r.URL.Query().Get("per_page")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"uuid":"db-1","name":"app-data","jurisdiction":"eu","version":"production","created_at":"2026-01-01T00:00:00Z"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runD1CLI(t, srv.URL, "d1", "list", "--name", "app-data")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+d1TestAccountID+"/d1/database" {
		t.Errorf("path = %s", gotPath)
	}
	if gotName != "app-data" || gotPerPage != "100" {
		t.Errorf("query name=%q per_page=%q", gotName, gotPerPage)
	}
	if !strings.Contains(stdout, "app-data") || !strings.Contains(stdout, "db-1") {
		t.Errorf("table output = %s", stdout)
	}
}

func TestD1CreateAndUpdateHTTPRequest(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{
			name:       "create",
			args:       []string{"d1", "create", "app-data", "--jurisdiction", "eu", "--read-replication", "auto"},
			wantMethod: "POST",
			wantPath:   "/accounts/" + d1TestAccountID + "/d1/database",
			wantBody:   `{"name":"app-data","jurisdiction":"eu","read_replication":{"mode":"auto"}}`,
		},
		{
			name:       "update",
			args:       []string{"d1", "update", d1TestDatabaseID, "--read-replication", "disabled"},
			wantMethod: "PATCH",
			wantPath:   "/accounts/" + d1TestAccountID + "/d1/database/" + d1TestDatabaseID,
			wantBody:   `{"read_replication":{"mode":"disabled"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":{"uuid":"db-1"}}`))
			}))
			defer srv.Close()

			if _, _, err := runD1CLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.wantMethod || gotPath != tc.wantPath {
				t.Errorf("request = %s %s, want %s %s", gotMethod, gotPath, tc.wantMethod, tc.wantPath)
			}
			d1AssertJSONEqual(t, gotBody, tc.wantBody)
		})
	}
}

func TestD1GetAndDeleteHTTPRequest(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantMethod string
	}{
		{"get", []string{"d1", "get", d1TestDatabaseID}, "GET"},
		{"delete", []string{"d1", "delete", d1TestDatabaseID, "--force"}, "DELETE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":{"uuid":"db-1"}}`))
			}))
			defer srv.Close()

			if _, _, err := runD1CLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.wantMethod || gotPath != "/accounts/"+d1TestAccountID+"/d1/database/"+d1TestDatabaseID {
				t.Errorf("request = %s %s", gotMethod, gotPath)
			}
		})
	}
}

func TestD1QueryHTTPRequestFromCommandAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.sql")
	if err := os.WriteFile(path, []byte("SELECT id FROM users;"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		command  string
		params   []string
		wantBody string
	}{
		{"inline", "SELECT * FROM users WHERE id = ?", []string{"42"}, `{"sql":"SELECT * FROM users WHERE id = ?","params":["42"]}`},
		{"file", "@" + path, nil, `{"sql":"SELECT id FROM users;"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":[{"results":[{"id":42}],"success":true}]}`))
			}))
			defer srv.Close()

			args := []string{"d1", "query", d1TestDatabaseID, "--command", tc.command}
			for _, param := range tc.params {
				args = append(args, "--param", param)
			}
			if _, _, err := runD1CLI(t, srv.URL, args...); err != nil {
				t.Fatal(err)
			}
			if gotMethod != "POST" || gotPath != "/accounts/"+d1TestAccountID+"/d1/database/"+d1TestDatabaseID+"/query" {
				t.Errorf("request = %s %s", gotMethod, gotPath)
			}
			d1AssertJSONEqual(t, gotBody, tc.wantBody)
		})
	}
}

func TestD1ExportPollsUntilComplete(t *testing.T) {
	oldInterval := d1ExportPollInterval
	d1ExportPollInterval = time.Millisecond
	defer func() { d1ExportPollInterval = oldInterval }()

	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"success":true,"result":{"status":"in_progress","at_bookmark":"bookmark-1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"status":"complete","result":{"filename":"backup.sql","signed_url":"https://example.invalid/backup.sql"}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runD1CLI(t, srv.URL, "d1", "export", d1TestDatabaseID, "--no-data", "--table", "users")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	d1AssertJSONEqual(t, bodies[0], `{"output_format":"polling","dump_options":{"no_data":true,"tables":["users"]}}`)
	d1AssertJSONEqual(t, bodies[1], `{"output_format":"polling","current_bookmark":"bookmark-1"}`)
	if !strings.Contains(stdout, "backup.sql") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestD1DeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runD1CLI(t, srv.URL, "d1", "delete", d1TestDatabaseID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force error, got %v", err)
	}
}

func TestD1DryRunQuery(t *testing.T) {
	stdout, _, err := runD1CLI(t, "http://example.invalid", "d1", "query", d1TestDatabaseID, "--command", "SELECT 1", "--dry-run")
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/d1/database/"+d1TestDatabaseID+"/query") {
		t.Errorf("dump = %#v", dump)
	}
	d1AssertJSONEqual(t, dump.Body, `{"sql":"SELECT 1"}`)
}
