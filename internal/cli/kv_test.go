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

const (
	kvTestAccountID   = "a1b2c3d4e5f6789012345678abcdef01"
	kvTestNamespaceID = "0f2ac74b498b48028cb68387c421e279"
)

func runKVCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", kvTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func kvAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func kvParseDryRun(t *testing.T, stdout string) (method, urlStr string, body json.RawMessage, headers map[string]string) {
	t.Helper()
	var dump struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Body    json.RawMessage   `json:"body"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return dump.Method, dump.URL, dump.Body, dump.Headers
}

func TestKVAccountIDRequired(t *testing.T) {
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "test-token",
		"kv", "namespace", "list",
		"--dry-run",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("expected account error, got %v", err)
	}
}

func TestKVNamespaceListDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "list",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, _, _ := kvParseDryRun(t, stdout)
	if method != "GET" {
		t.Errorf("method = %s", method)
	}
	wantSuffix := "/accounts/" + kvTestAccountID + "/storage/kv/namespaces"
	if !strings.Contains(u, wantSuffix) {
		t.Errorf("url = %s, want path %s", u, wantSuffix)
	}
	if !strings.Contains(u, "per_page=100") {
		t.Errorf("url missing per_page: %s", u)
	}
}

func TestKVNamespaceListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + kvTestNamespaceID + `","title":"app-config"}],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":1,"total_count":1}}`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL, "kv", "namespace", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "TITLE") {
		t.Errorf("missing table headers: %s", stdout)
	}
	if !strings.Contains(stdout, kvTestNamespaceID) || !strings.Contains(stdout, "app-config") {
		t.Errorf("missing row: %s", stdout)
	}
}

func TestKVNamespaceCreateDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "create", "my-app-config",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body, _ := kvParseDryRun(t, stdout)
	if method != "POST" {
		t.Errorf("method = %s", method)
	}
	if !strings.HasSuffix(strings.Split(u, "?")[0], "/accounts/"+kvTestAccountID+"/storage/kv/namespaces") {
		t.Errorf("url = %s", u)
	}
	kvAssertJSONEqual(t, body, `{"title":"my-app-config"}`)
}

func TestKVNamespaceCreateEmptyTitle(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "create", "   ",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("expected title error, got %v", err)
	}
}

func TestKVNamespaceCreateLive(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + kvTestNamespaceID + `","title":"my-app-config"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL, "kv", "namespace", "create", "my-app-config")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces" {
		t.Errorf("path = %s", gotPath)
	}
	kvAssertJSONEqual(t, gotBody, `{"title":"my-app-config"}`)
	if !strings.Contains(stdout, kvTestNamespaceID) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestKVNamespaceDeleteDryRunForce(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "delete", kvTestNamespaceID,
		"--force",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, _, _ := kvParseDryRun(t, stdout)
	if method != "DELETE" {
		t.Errorf("method = %s", method)
	}
	want := "/accounts/" + kvTestAccountID + "/storage/kv/namespaces/" + kvTestNamespaceID
	if !strings.HasSuffix(strings.Split(u, "?")[0], want) {
		t.Errorf("url = %s", u)
	}
}

func TestKVNamespaceDeleteRequiresForceWithoutTTY(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "delete", kvTestNamespaceID,
	)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestKVNamespaceDeleteLive(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runKVCLI(t, srv.URL,
		"kv", "namespace", "delete", kvTestNamespaceID,
		"--force",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces/"+kvTestNamespaceID {
		t.Errorf("path = %s", gotPath)
	}
}

func TestKVNamespaceRenameDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "rename", kvTestNamespaceID, "production-config",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body, _ := kvParseDryRun(t, stdout)
	if method != "PUT" {
		t.Errorf("method = %s", method)
	}
	want := "/accounts/" + kvTestAccountID + "/storage/kv/namespaces/" + kvTestNamespaceID
	if !strings.HasSuffix(strings.Split(u, "?")[0], want) {
		t.Errorf("url = %s", u)
	}
	kvAssertJSONEqual(t, body, `{"title":"production-config"}`)
}

func TestKVNamespaceRenameEmptyTitle(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "namespace", "rename", kvTestNamespaceID, "  ",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "title") {
		t.Fatalf("expected title error, got %v", err)
	}
}

func TestKVNamespaceResolvesTitle(t *testing.T) {
	var deletePath string
	var sawList bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/accounts/"+kvTestAccountID+"/storage/kv/namespaces":
			sawList = true
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + kvTestNamespaceID + `","title":"my-app-config"}],"result_info":{"page":1,"per_page":100,"total_pages":1,"count":1,"total_count":1}}`))
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/accounts/"):
			deletePath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := runKVCLI(t, srv.URL,
		"kv", "namespace", "delete", "my-app-config",
		"--force",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sawList {
		t.Error("expected namespace title lookup")
	}
	if deletePath != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces/"+kvTestNamespaceID {
		t.Errorf("delete path = %s", deletePath)
	}
}

func TestKVKeyListDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "list",
		"--namespace", kvTestNamespaceID,
		"--prefix", "user:",
		"--limit", "100",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, _, _ := kvParseDryRun(t, stdout)
	if method != "GET" {
		t.Errorf("method = %s", method)
	}
	wantPath := "/accounts/" + kvTestAccountID + "/storage/kv/namespaces/" + kvTestNamespaceID + "/keys"
	if !strings.Contains(u, wantPath) {
		t.Errorf("url = %s", u)
	}
	if !strings.Contains(u, "prefix=user") {
		t.Errorf("missing prefix query: %s", u)
	}
	if !strings.Contains(u, "limit=100") {
		t.Errorf("missing limit query: %s", u)
	}
}

func TestKVKeyListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces/"+kvTestNamespaceID+"/keys" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"greeting","expiration":1700000000,"metadata":{"lang":"en"}}],"result_info":{"count":1}}`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "list",
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "greeting", "1700000000"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q: %s", want, stdout)
		}
	}
}

func TestKVKeyListRequiresNamespace(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "list",
		"--dry-run",
	)
	if err == nil {
		t.Fatal("expected required --namespace error")
	}
	if !strings.Contains(err.Error(), "namespace") && !strings.Contains(err.Error(), "required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestKVKeyListLimitRange(t *testing.T) {
	// Below Cloudflare minimum.
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "list",
		"--namespace", kvTestNamespaceID,
		"--limit", "1",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "10") || !strings.Contains(err.Error(), "1000") {
		t.Fatalf("expected limit range error for 1, got %v", err)
	}

	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "list",
		"--namespace", kvTestNamespaceID,
		"--limit", "9",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "between 10 and 1000") {
		t.Fatalf("expected limit range error for 9, got %v", err)
	}

	// Above Cloudflare maximum.
	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "list",
		"--namespace", kvTestNamespaceID,
		"--limit", "1001",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "between 10 and 1000") {
		t.Fatalf("expected limit range error for 1001, got %v", err)
	}

	// Lower and upper boundaries accepted.
	for _, lim := range []string{"10", "1000"} {
		stdout, _, err := runKVCLI(t, "http://example.invalid",
			"kv", "key", "list",
			"--namespace", kvTestNamespaceID,
			"--limit", lim,
			"--dry-run",
		)
		if err != nil {
			t.Fatalf("limit %s: %v", lim, err)
		}
		_, u, _, _ := kvParseDryRun(t, stdout)
		if !strings.Contains(u, "limit="+lim) {
			t.Errorf("limit %s missing from url: %s", lim, u)
		}
	}
}

func TestKVKeyGetDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "get", "session:abc",
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, _, _ := kvParseDryRun(t, stdout)
	if method != "GET" {
		t.Errorf("method = %s", method)
	}
	// PathEscape keeps ":" unescaped in Go's PathEscape for some chars;
	// session:abc uses PathEscape which encodes special chars.
	if !strings.Contains(u, "/values/") {
		t.Errorf("url = %s", u)
	}
	if !strings.Contains(u, kvTestNamespaceID) {
		t.Errorf("url missing namespace: %s", u)
	}
	if !strings.Contains(u, "session") {
		t.Errorf("url missing key: %s", u)
	}
}

func TestKVKeyGetLiveRawBytes(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`hello-world`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "get", "greeting",
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces/"+kvTestNamespaceID+"/values/greeting" {
		t.Errorf("path = %s", gotPath)
	}
	// Default: raw bytes, not JSON-quoted.
	if stdout != "hello-world" {
		t.Errorf("stdout = %q, want raw hello-world", stdout)
	}
}

func TestKVKeyGetOutputJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(`hello-world`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL,
		"--output", "json",
		"kv", "key", "get", "greeting",
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("expected JSON string, got %q: %v", stdout, err)
	}
	if s != "hello-world" {
		t.Errorf("decoded = %q", s)
	}
}

func TestKVKeyGetJSONValuePassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":1}`))
	}))
	defer srv.Close()

	// Default raw still writes exact bytes.
	stdout, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "get", "cfg",
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != `{"a":1}` {
		t.Errorf("raw stdout = %q", stdout)
	}

	stdout, _, err = runKVCLI(t, srv.URL,
		"--output", "json",
		"kv", "key", "get", "cfg",
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	kvAssertJSONEqual(t, []byte(stdout), `{"a":1}`)
}

func TestKVKeyPutLiveDoRaw(t *testing.T) {
	var gotMethod, gotPath, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "put", "greeting",
		"--namespace", kvTestNamespaceID,
		"--value", "hello",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" {
		t.Errorf("method = %s", gotMethod)
	}
	if !strings.HasSuffix(gotPath, "/values/greeting") {
		t.Errorf("path = %s", gotPath)
	}
	if gotCT != "text/plain" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	if string(gotBody) != "hello" {
		t.Errorf("body = %q", gotBody)
	}
}

func TestKVKeyGetAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10007,"message":"key not found"}],"result":null}`))
	}))
	defer srv.Close()

	_, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "get", "missing",
		"--namespace", kvTestNamespaceID,
	)
	if err == nil || !strings.Contains(err.Error(), "key not found") {
		t.Fatalf("expected API error, got %v", err)
	}
}

func TestKVKeyGetQueryRequiresOutputJSON(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"--query", ".foo",
		"kv", "key", "get", "cfg",
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "--output json") {
		t.Fatalf("expected --query without --output json error, got %v", err)
	}
}

func TestKVKeyGetQueryWithOutputJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"foo":"bar","n":1}`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL,
		"--output", "json",
		"--query", ".foo",
		"kv", "key", "get", "cfg",
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("stdout = %q: %v", stdout, err)
	}
	if s != "bar" {
		t.Errorf("queried value = %q, want bar", s)
	}
}

func TestKVKeyPutDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "put", "greeting",
		"--namespace", kvTestNamespaceID,
		"--value", "hello",
		"--ttl", "3600",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body, headers := kvParseDryRun(t, stdout)
	if method != "PUT" {
		t.Errorf("method = %s", method)
	}
	if !strings.Contains(u, "/values/greeting") {
		t.Errorf("url = %s", u)
	}
	if !strings.Contains(u, "expiration_ttl=3600") {
		t.Errorf("missing ttl query: %s", u)
	}
	if headers["Content-Type"] != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", headers["Content-Type"])
	}
	// Non-JSON body is quoted in dry-run dump.
	var bodyStr string
	if err := json.Unmarshal(body, &bodyStr); err != nil {
		// Might already be raw JSON string content
		if string(body) != `"hello"` && !strings.Contains(string(body), "hello") {
			t.Fatalf("body = %s", body)
		}
	} else if bodyStr != "hello" {
		t.Errorf("body string = %q", bodyStr)
	}
}

func TestKVKeyPutFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "value.txt")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotBody []byte
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "put", "config",
		"--namespace", kvTestNamespaceID,
		"--value", "@"+path,
	)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBody) != "from-file" {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.HasSuffix(gotPath, "/values/config") {
		t.Errorf("path = %s", gotPath)
	}
}

func TestKVKeyPutValidation(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "put", "k",
		"--namespace", kvTestNamespaceID,
		"--value", "v",
		"--ttl", "30",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "60") {
		t.Fatalf("expected ttl min error, got %v", err)
	}

	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "put", "k",
		"--namespace", kvTestNamespaceID,
		"--value", "v",
		"--ttl", "60",
		"--expiration", "1700000000",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}

	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "put", "k",
		"--namespace", kvTestNamespaceID,
		"--value", "v",
		"--expiration", "0",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "expiration") {
		t.Fatalf("expected expiration error, got %v", err)
	}
}

func TestKVKeyPutExpirationQuery(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "put", "k",
		"--namespace", kvTestNamespaceID,
		"--value", "v",
		"--expiration", "1700000000",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, u, _, _ := kvParseDryRun(t, stdout)
	if !strings.Contains(u, "expiration=1700000000") {
		t.Errorf("url = %s", u)
	}
	if strings.Contains(u, "expiration_ttl") {
		t.Errorf("unexpected ttl in url: %s", u)
	}
}

func TestKVKeyDeleteDryRun(t *testing.T) {
	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "delete", "session:abc",
		"--namespace", kvTestNamespaceID,
		"--force",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, _, _ := kvParseDryRun(t, stdout)
	if method != "DELETE" {
		t.Errorf("method = %s", method)
	}
	if !strings.Contains(u, "/values/") {
		t.Errorf("url = %s", u)
	}
}

func TestKVKeyDeleteRequiresForceWithoutTTY(t *testing.T) {
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "delete", "k",
		"--namespace", kvTestNamespaceID,
	)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestKVKeyBulkPutDryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pairs.json")
	content := `[{"key":"a","value":"1"},{"key":"b","value":"2","expiration_ttl":120}]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-put", "@"+path,
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body, _ := kvParseDryRun(t, stdout)
	if method != "PUT" {
		t.Errorf("method = %s", method)
	}
	wantPath := "/accounts/" + kvTestAccountID + "/storage/kv/namespaces/" + kvTestNamespaceID + "/bulk"
	if !strings.HasSuffix(strings.Split(u, "?")[0], wantPath) {
		t.Errorf("url = %s", u)
	}
	kvAssertJSONEqual(t, body, content)
}

func TestKVKeyBulkPutLive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pairs.json")
	content := `[{"key":"a","value":"1"}]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"successful_key_count":1,"unsuccessful_keys":[]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "bulk-put", "@"+path,
		"--namespace", kvTestNamespaceID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces/"+kvTestNamespaceID+"/bulk" {
		t.Errorf("path = %s", gotPath)
	}
	kvAssertJSONEqual(t, gotBody, content)
	if !strings.Contains(stdout, "successful_key_count") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestKVKeyBulkPutValidation(t *testing.T) {
	dir := t.TempDir()

	// Missing @ prefix
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-put", "pairs.json",
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "@file") {
		t.Fatalf("expected @file error, got %v", err)
	}

	// Invalid JSON
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-put", "@"+bad,
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("expected JSON error, got %v", err)
	}

	// Missing value field
	missing := filepath.Join(dir, "missing.json")
	if err := os.WriteFile(missing, []byte(`[{"key":"a"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-put", "@"+missing,
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("expected value error, got %v", err)
	}

	// Empty array
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-put", "@"+empty,
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("expected empty array error, got %v", err)
	}

	// Non-string value
	num := filepath.Join(dir, "num.json")
	if err := os.WriteFile(num, []byte(`[{"key":"a","value":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-put", "@"+num,
		"--namespace", kvTestNamespaceID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "string") {
		t.Fatalf("expected string value error, got %v", err)
	}
}

func TestKVKeyBulkDeleteDryRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	content := `["a","b","c"]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-delete", "@"+path,
		"--namespace", kvTestNamespaceID,
		"--force",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	method, u, body, _ := kvParseDryRun(t, stdout)
	if method != "POST" {
		t.Errorf("method = %s", method)
	}
	wantPath := "/accounts/" + kvTestAccountID + "/storage/kv/namespaces/" + kvTestNamespaceID + "/bulk/delete"
	if !strings.HasSuffix(strings.Split(u, "?")[0], wantPath) {
		t.Errorf("url = %s", u)
	}
	kvAssertJSONEqual(t, body, content)
}

func TestKVKeyBulkDeleteRequiresForceWithoutTTY(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	if err := os.WriteFile(path, []byte(`["a"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-delete", "@"+path,
		"--namespace", kvTestNamespaceID,
	)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestKVKeyBulkDeleteLive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	content := `["old-1","old-2"]`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"successful_key_count":2,"unsuccessful_keys":[]}}`))
	}))
	defer srv.Close()

	_, _, err := runKVCLI(t, srv.URL,
		"kv", "key", "bulk-delete", "@"+path,
		"--namespace", kvTestNamespaceID,
		"--force",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+kvTestAccountID+"/storage/kv/namespaces/"+kvTestNamespaceID+"/bulk/delete" {
		t.Errorf("path = %s", gotPath)
	}
	kvAssertJSONEqual(t, gotBody, content)
}

func TestKVKeyBulkDeleteValidation(t *testing.T) {
	dir := t.TempDir()

	// Object instead of string array
	obj := filepath.Join(dir, "obj.json")
	if err := os.WriteFile(obj, []byte(`{"keys":["a"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-delete", "@"+obj,
		"--namespace", kvTestNamespaceID,
		"--force",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "array") {
		t.Fatalf("expected array error, got %v", err)
	}

	// Empty key in array
	emptyKey := filepath.Join(dir, "empty-key.json")
	if err := os.WriteFile(emptyKey, []byte(`["ok",""]`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = runKVCLI(t, "http://example.invalid",
		"kv", "key", "bulk-delete", "@"+emptyKey,
		"--namespace", kvTestNamespaceID,
		"--force",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty key error, got %v", err)
	}
}

func TestKVValidateHelpers(t *testing.T) {
	if err := kvValidateBulkPutBody([]byte(`[{"key":"a","value":"1"}]`)); err != nil {
		t.Fatal(err)
	}
	keys, err := kvValidateBulkDeleteBody([]byte(`["x"]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "x" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestKVReadValue(t *testing.T) {
	got, err := kvReadValue("plain", nil)
	if err != nil || string(got) != "plain" {
		t.Fatalf("plain: %q %v", got, err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "v")
	if err := os.WriteFile(path, []byte("file-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = kvReadValue("@"+path, nil)
	if err != nil || string(got) != "file-data" {
		t.Fatalf("file: %q %v", got, err)
	}
	got, err = kvReadValue("@-", strings.NewReader("stdin-data"))
	if err != nil || string(got) != "stdin-data" {
		t.Fatalf("stdin: %q %v", got, err)
	}
}

func TestKVCommandsRejectStrayArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"namespace-list", []string{"kv", "namespace", "list", "extra", "--dry-run"}},
		{"namespace-create", []string{"kv", "namespace", "create", "title", "extra", "--dry-run"}},
		{"namespace-delete", []string{"kv", "namespace", "delete", kvTestNamespaceID, "extra", "--force", "--dry-run"}},
		{"namespace-rename", []string{"kv", "namespace", "rename", kvTestNamespaceID, "new", "extra", "--dry-run"}},
		{"key-list", []string{"kv", "key", "list", "extra", "--namespace", kvTestNamespaceID, "--dry-run"}},
		{"key-get", []string{"kv", "key", "get", "k", "extra", "--namespace", kvTestNamespaceID, "--dry-run"}},
		{"key-put", []string{"kv", "key", "put", "k", "extra", "--namespace", kvTestNamespaceID, "--value", "v", "--dry-run"}},
		{"key-delete", []string{"kv", "key", "delete", "k", "extra", "--namespace", kvTestNamespaceID, "--force", "--dry-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runKVCLI(t, "http://example.invalid", tc.args...)
			if err == nil {
				t.Fatal("expected error for stray positional args")
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "arg") && !strings.Contains(msg, "unknown") && !strings.Contains(msg, "accepts") {
				// cobra often: "accepts 1 arg(s), received 2"
				if !strings.Contains(err.Error(), "extra") {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestKVHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{
			args: []string{"kv", "namespace", "create", "--help"},
			want: []string{"cf kv namespace create", "title"},
		},
		{
			args: []string{"kv", "namespace", "delete", "--help"},
			want: []string{"--force", "cf kv namespace delete"},
		},
		{
			args: []string{"kv", "key", "put", "--help"},
			want: []string{"--value", "--namespace", "cf kv key put", "--ttl", "metadata", "bulk-put"},
		},
		{
			args: []string{"kv", "key", "get", "--help"},
			want: []string{"raw", "--output json", "cf kv key get"},
		},
		{
			args: []string{"kv", "key", "bulk-put", "--help"},
			want: []string{"@file", "cf kv key bulk-put", "--namespace", "metadata"},
		},
		{
			args: []string{"kv", "key", "bulk-delete", "--help"},
			want: []string{"@file", "--force", "cf kv key bulk-delete"},
		},
		{
			args: []string{"kv", "key", "list", "--help"},
			want: []string{"--prefix", "--namespace", "cf kv key list"},
		},
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
			for _, w := range tc.want {
				if !strings.Contains(help, w) {
					t.Errorf("help missing %q\n%s", w, help)
				}
			}
		})
	}
}

func TestKVNoDuplicateAccountFlag(t *testing.T) {
	// Per-command account-id must not exist; global flag is the contract.
	root := NewRootCmd()
	cmd, _, err := root.Find([]string{"kv", "namespace", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Flags().Lookup("account-id") != nil {
		t.Fatal("kv namespace list must not define a local --account-id flag")
	}
	cmd, _, err = root.Find([]string{"kv", "key", "put"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd.LocalFlags().Lookup("account-id") != nil {
		t.Fatal("kv key put must not define a local --account-id flag")
	}
}
