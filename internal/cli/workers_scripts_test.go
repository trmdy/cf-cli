package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const workersScriptsTestAccountID = "a1b2c3d4e5f6789012345678abcdef01"

func runWorkersScriptsCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runWorkersScriptsCLIStdin(t, serverURL, nil, args...)
}

func runWorkersScriptsCLIStdin(t *testing.T, serverURL string, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
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
		"--account-id", workersScriptsTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

type workersScriptsDump struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Body    json.RawMessage   `json:"body"`
	Headers map[string]string `json:"headers"`
}

func workersScriptsParseDryRun(t *testing.T, stdout string) workersScriptsDump {
	t.Helper()
	var dump workersScriptsDump
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return dump
}

func workersScriptsAssertJSONEqual(t *testing.T, got []byte, want string) {
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

type workersScriptsPart struct {
	Name        string
	FileName    string
	ContentType string
	Disposition string
	Body        string
}

// workersScriptsReadParts decodes a multipart body using the boundary from
// contentType, so tests assert the exact wire format the API receives.
func workersScriptsReadParts(t *testing.T, contentType string, body []byte) []workersScriptsPart {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("Content-Type %q: %v", contentType, err)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatalf("Content-Type %q has no boundary", contentType)
	}
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var parts []workersScriptsPart
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read multipart: %v", err)
		}
		data, err := io.ReadAll(p)
		if err != nil {
			t.Fatalf("read part %q: %v", p.FormName(), err)
		}
		parts = append(parts, workersScriptsPart{
			Name:        p.FormName(),
			FileName:    p.FileName(),
			ContentType: p.Header.Get("Content-Type"),
			Disposition: p.Header.Get("Content-Disposition"),
			Body:        string(data),
		})
		p.Close()
	}
	return parts
}

// workersScriptsUploadParts runs an upload in dry-run mode and returns the
// decoded multipart parts plus the dump.
func workersScriptsUploadParts(t *testing.T, args ...string) ([]workersScriptsPart, workersScriptsDump) {
	t.Helper()
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid", args...)
	if err != nil {
		t.Fatalf("upload dry-run: %v", err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	var body string
	if err := json.Unmarshal(dump.Body, &body); err != nil {
		t.Fatalf("dry-run body is not a JSON string: %v\n%s", err, dump.Body)
	}
	return workersScriptsReadParts(t, dump.Headers["Content-Type"], []byte(body)), dump
}

func workersScriptsWriteFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------------------------------------------------------------------------
// wiring and account scope

func TestWorkersScriptsRegisteredUnderWorkers(t *testing.T) {
	root := NewRootCmd()
	for _, path := range [][]string{
		{"workers", "script", "list"},
		{"workers", "script", "get"},
		{"workers", "script", "upload"},
		{"workers", "script", "download"},
		{"workers", "script", "delete"},
		{"workers", "script", "secret", "put"},
		{"workers", "script", "subdomain", "enable"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if cmd.Name() != path[len(path)-1] {
			t.Fatalf("%v resolved to %q", path, cmd.Name())
		}
	}
}

func TestWorkersScriptsAccountIDRequired(t *testing.T) {
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "test-token",
		"workers", "script", "list", "--dry-run",
	})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "account") {
		t.Fatalf("expected account error, got %v", err)
	}
}

func TestWorkersScriptsNoLocalAccountFlag(t *testing.T) {
	root := NewRootCmd()
	for _, path := range [][]string{
		{"workers", "script", "list"},
		{"workers", "script", "upload"},
		{"workers", "script", "secret", "list"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if cmd.LocalFlags().Lookup("account-id") != nil {
			t.Fatalf("%v must not define a local --account-id flag", path)
		}
	}
}

func TestWorkersScriptsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"workers", "script", "list", "extra", "--dry-run"},
		{"workers", "script", "get", "my-worker", "extra", "--dry-run"},
		{"workers", "script", "delete", "my-worker", "extra", "--force", "--dry-run"},
		{"workers", "script", "download", "my-worker", "extra", "--dry-run"},
		{"workers", "script", "secret", "list", "my-worker", "extra", "--dry-run"},
		{"workers", "script", "secret", "delete", "my-worker", "TOKEN", "extra", "--force", "--dry-run"},
		{"workers", "script", "subdomain", "get", "my-worker", "extra", "--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, _, err := runWorkersScriptsCLI(t, "http://example.invalid", args...); err == nil {
				t.Fatal("expected error for stray positional args")
			}
		})
	}
}

func TestWorkersScriptsNameValidation(t *testing.T) {
	for _, name := range []string{"  ", "a/b", "a?b", "a#b"} {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "get", name, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "script name") && !strings.Contains(err.Error(), "invalid script name") {
			t.Fatalf("name %q: expected validation error, got %v", name, err)
		}
	}
}

func TestWorkersScriptsAccountIDIsPathEscaped(t *testing.T) {
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "test-token",
		// A path-sensitive account ID must not escape the /accounts/<id>/
		// path segment.
		"--account-id", "acct/../../zones",
		"workers", "script", "secret", "list", "my-worker", "--dry-run",
	})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, out.String())
	want := "/accounts/acct%2F..%2F..%2Fzones/workers/scripts/my-worker/secrets"
	if !strings.HasSuffix(dump.URL, want) {
		t.Errorf("url = %s, want suffix %s", dump.URL, want)
	}
	if strings.Contains(dump.URL, "/accounts/acct/") {
		t.Errorf("url = %s, account ID leaked extra path segments", dump.URL)
	}
}

// ---------------------------------------------------------------------------
// list

func TestWorkersScriptsListDryRun(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "list", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	if dump.Method != "GET" {
		t.Errorf("method = %s", dump.Method)
	}
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts"
	if !strings.HasSuffix(dump.URL, want) {
		t.Errorf("url = %s, want suffix %s", dump.URL, want)
	}
}

func TestWorkersScriptsListTagGrammar(t *testing.T) {
	// Both documented sides of the tag filter grammar are accepted.
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "list",
		"--tag", "team:yes",
		"--tag", "deprecated:no",
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	if !strings.Contains(dump.URL, "tags=team%3Ayes%2Cdeprecated%3Ano") {
		t.Errorf("url = %s, want comma-joined tags", dump.URL)
	}

	for _, bad := range []string{"team", "team:maybe", ":yes", "team:", "te,am:yes"} {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "list", "--tag", bad, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "--tag") {
			t.Errorf("--tag %q: expected grammar error, got %v", bad, err)
		}
	}
}

func TestWorkersScriptsListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/accounts/"+workersScriptsTestAccountID+"/workers/scripts" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"my-worker","created_on":"2026-01-02T03:04:05Z","modified_on":"2026-02-03T04:05:06Z","usage_model":"standard","handlers":["fetch","scheduled"]}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL, "workers", "script", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "USAGE_MODEL", "my-worker", "standard", "fetch,scheduled"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

// ---------------------------------------------------------------------------
// get

func TestWorkersScriptsGetDryRun(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "get", "my-worker", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/settings"
	if dump.Method != "GET" || !strings.HasSuffix(dump.URL, want) {
		t.Errorf("got %s %s, want GET %s", dump.Method, dump.URL, want)
	}
}

func TestWorkersScriptsGetLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+workersScriptsTestAccountID+"/workers/scripts/my-worker/settings" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"compatibility_date":"2026-01-01","bindings":[{"name":"KV","type":"kv_namespace"}]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL, "workers", "script", "get", "my-worker")
	if err != nil {
		t.Fatal(err)
	}
	workersScriptsAssertJSONEqual(t, []byte(stdout), `{"compatibility_date":"2026-01-01","bindings":[{"name":"KV","type":"kv_namespace"}]}`)
}

// ---------------------------------------------------------------------------
// delete

func TestWorkersScriptsDeleteDryRun(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "delete", "my-worker", "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker"
	if dump.Method != "DELETE" || !strings.HasSuffix(dump.URL, want) {
		t.Errorf("got %s %s, want DELETE %s", dump.Method, dump.URL, want)
	}
	if strings.Contains(dump.URL, "force=") {
		t.Errorf("--force must not send the API force flag: %s", dump.URL)
	}
}

func TestWorkersScriptsDeleteBindingsQuery(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "delete", "my-worker", "--delete-bindings", "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	if !strings.Contains(dump.URL, "force=true") {
		t.Errorf("url = %s, want force=true", dump.URL)
	}
}

func TestWorkersScriptsDeleteRequiresForceWithoutTTY(t *testing.T) {
	_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "delete", "my-worker")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestWorkersScriptsDeleteLive(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotQuery = r.Method, r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":null}`))
	}))
	defer srv.Close()

	if _, _, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "delete", "my-worker", "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/accounts/"+workersScriptsTestAccountID+"/workers/scripts/my-worker" {
		t.Errorf("got %s %s", gotMethod, gotPath)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty", gotQuery)
	}
}

// ---------------------------------------------------------------------------
// upload: wire format

func TestWorkersScriptsUploadWireFormatSingleModule(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "worker.js", "export default { fetch() { return new Response('ok') } }")

	parts, dump := workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", filepath.Join(dir, "worker.js"),
		"--dry-run")

	if dump.Method != "PUT" {
		t.Errorf("method = %s", dump.Method)
	}
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker"
	if !strings.HasSuffix(dump.URL, want) {
		t.Errorf("url = %s, want suffix %s", dump.URL, want)
	}
	if !strings.HasPrefix(dump.Headers["Content-Type"], "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q", dump.Headers["Content-Type"])
	}
	if len(parts) != 2 {
		t.Fatalf("got %d parts, want metadata + 1 module: %+v", len(parts), parts)
	}

	meta := parts[0]
	if meta.Name != "metadata" {
		t.Errorf("first part = %q, want metadata", meta.Name)
	}
	if meta.ContentType != "application/json" {
		t.Errorf("metadata Content-Type = %q", meta.ContentType)
	}
	if meta.FileName != "" {
		t.Errorf("metadata filename = %q, want none", meta.FileName)
	}
	workersScriptsAssertJSONEqual(t, []byte(meta.Body), `{"main_module":"worker.js"}`)

	mod := parts[1]
	if mod.Name != "worker.js" || mod.FileName != "worker.js" {
		t.Errorf("module part name = %q, filename = %q", mod.Name, mod.FileName)
	}
	if mod.ContentType != "application/javascript+module" {
		t.Errorf("module Content-Type = %q, want application/javascript+module", mod.ContentType)
	}
	if mod.Body != "export default { fetch() { return new Response('ok') } }" {
		t.Errorf("module body = %q", mod.Body)
	}
}

func TestWorkersScriptsUploadWireFormatMultipleModules(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "entry.js", "import './lib.cjs'")
	workersScriptsWriteFile(t, dir, "lib.js", "module.exports = {}")
	workersScriptsWriteFile(t, dir, "blob.wasm", "wasm-bytes")
	workersScriptsWriteFile(t, dir, "readme.txt", "hello")

	parts, _ := workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", filepath.Join(dir, "entry.js"),
		"--module", "vendor/lib.cjs="+filepath.Join(dir, "lib.js"),
		"--module", "blob.wasm="+filepath.Join(dir, "blob.wasm"),
		"--module", filepath.Join(dir, "readme.txt"),
		"--main-module", "entry.js",
		"--compatibility-date", "2026-01-15",
		"--compatibility-flag", "nodejs_compat",
		"--compatibility-flag", "streams_enable_constructors",
		"--keep-bindings", "secret_text",
		"--logpush",
		"--bindings", `[{"type":"kv_namespace","name":"KV","namespace_id":"0f2ac74b498b48028cb68387c421e279"}]`,
		"--dry-run")

	if len(parts) != 5 {
		t.Fatalf("got %d parts, want metadata + 4 modules: %+v", len(parts), parts)
	}
	workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{
		"main_module":"entry.js",
		"compatibility_date":"2026-01-15",
		"compatibility_flags":["nodejs_compat","streams_enable_constructors"],
		"keep_bindings":["secret_text"],
		"logpush":true,
		"bindings":[{"type":"kv_namespace","name":"KV","namespace_id":"0f2ac74b498b48028cb68387c421e279"}]
	}`)

	// Modules keep command-line order, are named as declared, and carry the
	// content type the API uses to classify them.
	wantModules := []struct{ name, contentType, body string }{
		{"entry.js", "application/javascript+module", "import './lib.cjs'"},
		{"vendor/lib.cjs", "application/javascript", "module.exports = {}"},
		{"blob.wasm", "application/wasm", "wasm-bytes"},
		{"readme.txt", "text/plain", "hello"},
	}
	for i, want := range wantModules {
		got := parts[i+1]
		// Part.FileName() strips directories, so the nested module name is
		// asserted on the raw Content-Disposition below.
		if got.Name != want.name {
			t.Errorf("part %d: name = %q, want %q", i+1, got.Name, want.name)
		}
		if !strings.Contains(got.Disposition, `filename="`+want.name+`"`) {
			t.Errorf("part %d disposition = %q, want filename %q", i+1, got.Disposition, want.name)
		}
		if got.ContentType != want.contentType {
			t.Errorf("part %d (%s): Content-Type = %q, want %q", i+1, want.name, got.ContentType, want.contentType)
		}
		if got.Body != want.body {
			t.Errorf("part %d (%s): body = %q, want %q", i+1, want.name, got.Body, want.body)
		}
		if !strings.Contains(got.Disposition, `name="`+want.name+`"`) {
			t.Errorf("part %d disposition = %q", i+1, got.Disposition)
		}
	}
}

func TestWorkersScriptsUploadModuleTypeOverrides(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "worker.js", "module.exports = {}")
	workersScriptsWriteFile(t, dir, "data.dat", "raw")

	parts, _ := workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", filepath.Join(dir, "worker.js"),
		"--module", filepath.Join(dir, "data.dat"),
		"--module-type", "worker.js=CommonJS",
		"--module-type", "data.dat=data",
		"--main-module", "worker.js",
		"--dry-run")

	if got := parts[1].ContentType; got != "application/javascript" {
		t.Errorf("overridden js Content-Type = %q, want application/javascript", got)
	}
	if got := parts[2].ContentType; got != "application/octet-stream" {
		t.Errorf("data Content-Type = %q, want application/octet-stream", got)
	}
}

func TestWorkersScriptsUploadModuleTypeCanonicalValues(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "mod", "x")
	cases := map[string]string{
		"esm":                "application/javascript+module",
		"commonjs":           "application/javascript",
		"text":               "text/plain",
		"data":               "application/octet-stream",
		"wasm":               "application/wasm",
		"python":             "text/x-python",
		"python-requirement": "text/x-python-requirement",
		"sourcemap":          "application/source-map",
	}
	for typeName, contentType := range cases {
		t.Run(typeName, func(t *testing.T) {
			parts, _ := workersScriptsUploadParts(t,
				"workers", "script", "upload", "my-worker",
				"--module", filepath.Join(dir, "mod"),
				"--module-type", "mod="+typeName,
				"--dry-run")
			if got := parts[1].ContentType; got != contentType {
				t.Errorf("type %s: Content-Type = %q, want %q", typeName, got, contentType)
			}
		})
	}
}

func TestWorkersScriptsUploadInfersTypeFromExtension(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"a.js":     "application/javascript+module",
		"a.mjs":    "application/javascript+module",
		"a.cjs":    "application/javascript",
		"a.wasm":   "application/wasm",
		"a.txt":    "text/plain",
		"a.html":   "text/plain",
		"a.bin":    "application/octet-stream",
		"a.py":     "text/x-python",
		"a.js.map": "application/source-map",
	}
	for name, contentType := range cases {
		t.Run(name, func(t *testing.T) {
			path := workersScriptsWriteFile(t, dir, name, "x")
			parts, _ := workersScriptsUploadParts(t,
				"workers", "script", "upload", "my-worker",
				"--module", path,
				"--dry-run")
			if got := parts[1].ContentType; got != contentType {
				t.Errorf("%s: Content-Type = %q, want %q", name, got, contentType)
			}
		})
	}
}

func TestWorkersScriptsUploadMetadataBaseAndOverrides(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "worker.js", "x")
	metaPath := workersScriptsWriteFile(t, dir, "metadata.json",
		`{"placement":{"mode":"smart"},"compatibility_date":"2020-01-01","tags":["team"]}`)

	parts, _ := workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", filepath.Join(dir, "worker.js"),
		"--metadata", "@"+metaPath,
		"--compatibility-date", "2026-03-04",
		"--dry-run")

	workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{
		"main_module":"worker.js",
		"placement":{"mode":"smart"},
		"tags":["team"],
		"compatibility_date":"2026-03-04"
	}`)
}

func TestWorkersScriptsUploadOptionalMetadataFields(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	// Unset optional flags stay out of the metadata entirely.
	parts, _ := workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", module,
		"--dry-run")
	workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":"worker.js"}`)

	// An explicit false is sent, not dropped.
	parts, _ = workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", module,
		"--logpush=false",
		"--dry-run")
	workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":"worker.js","logpush":false}`)
}

// ---------------------------------------------------------------------------
// upload: main-module rules

func TestWorkersScriptsUploadMainModuleRules(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "a.js", "a")
	workersScriptsWriteFile(t, dir, "b.js", "b")
	metaMain := workersScriptsWriteFile(t, dir, "meta-main.json", `{"main_module":"b.js"}`)
	metaBad := workersScriptsWriteFile(t, dir, "meta-bad.json", `{"main_module":"missing.js"}`)
	metaNull := workersScriptsWriteFile(t, dir, "meta-null-main.json", `{"main_module":null}`)

	t.Run("single module defaults to that module", func(t *testing.T) {
		parts, _ := workersScriptsUploadParts(t,
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--dry-run")
		workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":"a.js"}`)
	})

	t.Run("multiple modules require --main-module", func(t *testing.T) {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--module", filepath.Join(dir, "b.js"),
			"--dry-run")
		if err == nil || !strings.Contains(err.Error(), "--main-module") {
			t.Fatalf("expected main-module error, got %v", err)
		}
	})

	t.Run("--main-module must name an uploaded module", func(t *testing.T) {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--main-module", "c.js",
			"--dry-run")
		if err == nil || !strings.Contains(err.Error(), "does not match an uploaded module") {
			t.Fatalf("expected mismatch error, got %v", err)
		}
	})

	t.Run("metadata main_module is honored", func(t *testing.T) {
		parts, _ := workersScriptsUploadParts(t,
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--module", filepath.Join(dir, "b.js"),
			"--metadata", "@"+metaMain,
			"--dry-run")
		workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":"b.js"}`)
	})

	t.Run("--main-module beats metadata main_module", func(t *testing.T) {
		parts, _ := workersScriptsUploadParts(t,
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--module", filepath.Join(dir, "b.js"),
			"--metadata", "@"+metaMain,
			"--main-module", "a.js",
			"--dry-run")
		workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":"a.js"}`)
	})

	t.Run("metadata main_module must name an uploaded module", func(t *testing.T) {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--metadata", "@"+metaBad,
			"--dry-run")
		if err == nil || !strings.Contains(err.Error(), "does not match an uploaded module") {
			t.Fatalf("expected mismatch error, got %v", err)
		}
	})

	t.Run("metadata main_module must be a string", func(t *testing.T) {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "upload", "my-worker",
			"--module", filepath.Join(dir, "a.js"),
			"--metadata", "@"+metaNull,
			"--dry-run")
		if err == nil || !strings.Contains(err.Error(), "main_module") {
			t.Fatalf("expected main_module type error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// upload: input validation

func TestWorkersScriptsUploadModuleValidation(t *testing.T) {
	dir := t.TempDir()
	good := workersScriptsWriteFile(t, dir, "worker.js", "x")
	workersScriptsWriteFile(t, dir, "other.js", "y")
	subdir := filepath.Join(dir, "sub")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing file", []string{"--module", filepath.Join(dir, "nope.js")}, "read module"},
		{"directory", []string{"--module", "sub=" + subdir}, "is a directory"},
		{"empty name", []string{"--module", "=" + good}, "module name before"},
		{"empty path", []string{"--module", "worker.js="}, "file path after"},
		{"duplicate name", []string{"--module", good, "--module", "worker.js=" + filepath.Join(dir, "other.js")}, "duplicate module name"},
		{"reserved metadata name", []string{"--module", "metadata=" + good}, `cannot be named "metadata"`},
		{"unknown extension", []string{"--module", "mystery=" + good}, "cannot infer the module type"},
		{"unknown type", []string{"--module", good, "--module-type", "worker.js=elf"}, "unknown type"},
		{"type for unknown module", []string{"--module", good, "--module-type", "ghost.js=esm"}, "no module named"},
		{"malformed type", []string{"--module", good, "--module-type", "worker.js"}, "expected <module>=<type>"},
		{"duplicate type", []string{"--module", good, "--module-type", "worker.js=esm", "--module-type", "worker.js=text"}, "more than once"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"workers", "script", "upload", "my-worker"}, tc.args...)
			args = append(args, "--dry-run")
			_, _, err := runWorkersScriptsCLI(t, "http://example.invalid", args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}

// TestWorkersScriptsUploadModuleNameHeaderSafety covers the module names that
// reach a MIME header: quoting characters must survive a parse round-trip,
// and control characters must be refused locally — before a client exists.
func TestWorkersScriptsUploadModuleNameHeaderSafety(t *testing.T) {
	dir := t.TempDir()
	file := workersScriptsWriteFile(t, dir, "src.js", "x")

	t.Run("quoting characters round-trip", func(t *testing.T) {
		for _, name := range []string{
			`he"llo.js`,
			`back\slash.js`,
			"sp ace.js",
			"naïve.js",
			"vendor/nested/lib.js",
			// Quote plus separator: a naive header builder would end the
			// name parameter here and let the rest inject new ones.
			`odd";filename"weird.js`,
		} {
			parts, _ := workersScriptsUploadParts(t,
				"workers", "script", "upload", "my-worker",
				"--module", name+"="+file,
				"--module-type", name+"=esm",
				"--dry-run")
			if len(parts) != 2 {
				t.Fatalf("name %q: got %d parts, want metadata + 1 module: %+v", name, len(parts), parts)
			}
			if parts[0].Name != "metadata" || parts[0].ContentType != "application/json" {
				t.Errorf("name %q: metadata part = %+v", name, parts[0])
			}
			workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":`+mustJSONString(t, name)+`}`)
			if parts[1].Name != name {
				t.Errorf("module part name = %q, want %q", parts[1].Name, name)
			}
			if parts[1].Body != "x" {
				t.Errorf("name %q: module body = %q", name, parts[1].Body)
			}
		}
	})

	t.Run("control characters are rejected before the client is built", func(t *testing.T) {
		for _, name := range []string{
			"a\r\nb.js",
			"a\nContent-Type: text/plain\n\nb.js",
			"a\tb.js",
			"a\x00b.js",
			"a\x7fb.js",
		} {
			// No --token and no --account-id: a name error here proves the
			// check runs before any client or account resolution.
			root := NewRootCmd()
			var out, errBuf bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errBuf)
			root.SetArgs([]string{
				"--base-url", "http://example.invalid",
				"workers", "script", "upload", "my-worker",
				"--module", name + "=" + file,
			})
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "control characters") {
				t.Errorf("name %q: expected control-character rejection, got %v", name, err)
			}
		}
	})

	t.Run("invalid UTF-8 is rejected", func(t *testing.T) {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "upload", "my-worker",
			"--module", "bad\xff.js="+file,
			"--dry-run")
		if err == nil || !strings.Contains(err.Error(), "UTF-8") {
			t.Fatalf("expected UTF-8 rejection, got %v", err)
		}
	})
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWorkersScriptsUploadRequiresModule(t *testing.T) {
	_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "upload", "my-worker", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "module") {
		t.Fatalf("expected module requirement error, got %v", err)
	}
}

func TestWorkersScriptsUploadMetadataValidation(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	cases := []struct {
		name string
		meta string
		want string
	}{
		{"null", "null", "must be a JSON object"},
		{"array", `[{"main_module":"worker.js"}]`, "must be a JSON object"},
		{"scalar", `"worker"`, "must be a JSON object"},
		{"malformed", "{not json", "must be a JSON object"},
		{"service worker body_part", `{"body_part":"script"}`, "body_part"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
				"workers", "script", "upload", "my-worker",
				"--module", module,
				"--metadata", tc.meta,
				"--dry-run")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("metadata %s: expected %q, got %v", tc.meta, tc.want, err)
			}
		})
	}

	_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "upload", "my-worker",
		"--module", module,
		"--metadata", "@"+filepath.Join(dir, "absent.json"),
		"--dry-run")
	if err == nil || !strings.Contains(err.Error(), "read --metadata file") {
		t.Fatalf("expected metadata file error, got %v", err)
	}
}

func TestWorkersScriptsUploadBindingsValidation(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	cases := []struct {
		name     string
		bindings string
		want     string
	}{
		{"null", "null", "must be a JSON array"},
		{"object", `{"name":"KV","type":"kv_namespace"}`, "must be a JSON array"},
		{"malformed", "[", "must be a JSON array"},
		{"null item", `[null]`, "item 0 must be a JSON object"},
		{"scalar item", `["KV"]`, "item 0 must be a JSON object"},
		{"missing name", `[{"type":"kv_namespace"}]`, `missing non-empty string "name"`},
		{"missing type", `[{"name":"KV"}]`, `missing non-empty string "type"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
				"workers", "script", "upload", "my-worker",
				"--module", module,
				"--bindings", tc.bindings,
				"--dry-run")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("bindings %s: expected %q, got %v", tc.bindings, tc.want, err)
			}
		})
	}

	// An empty array is a legitimate "remove every binding" upload.
	parts, _ := workersScriptsUploadParts(t,
		"workers", "script", "upload", "my-worker",
		"--module", module,
		"--bindings", "[]",
		"--dry-run")
	workersScriptsAssertJSONEqual(t, []byte(parts[0].Body), `{"main_module":"worker.js","bindings":[]}`)
}

func TestWorkersScriptsUploadCompatibilityDateBounds(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	// Both edges of a valid calendar date are accepted.
	for _, date := range []string{"2024-02-29", "2026-12-31", "2021-01-01"} {
		parts, _ := workersScriptsUploadParts(t,
			"workers", "script", "upload", "my-worker",
			"--module", module,
			"--compatibility-date", date,
			"--dry-run")
		workersScriptsAssertJSONEqual(t, []byte(parts[0].Body),
			`{"main_module":"worker.js","compatibility_date":"`+date+`"}`)
	}

	for _, date := range []string{"2023-02-29", "2026-13-01", "2026-01-32", "2026-1-5", "01-15-2026", "yesterday"} {
		_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
			"workers", "script", "upload", "my-worker",
			"--module", module,
			"--compatibility-date", date,
			"--dry-run")
		if err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
			t.Errorf("date %q: expected format error, got %v", date, err)
		}
	}
}

func TestWorkersScriptsUploadEmptyListValues(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "upload", "my-worker",
		"--module", module,
		"--compatibility-flag", "  ",
		"--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--compatibility-flag") {
		t.Fatalf("expected empty flag error, got %v", err)
	}

	_, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "upload", "my-worker",
		"--module", module,
		"--keep-bindings", "",
		"--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--keep-bindings") {
		t.Fatalf("expected empty keep-bindings error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// upload: live streaming

func TestWorkersScriptsUploadLive(t *testing.T) {
	dir := t.TempDir()
	workersScriptsWriteFile(t, dir, "worker.js", "export default {}")
	wasmPath := filepath.Join(dir, "lib.wasm")
	// Binary bytes prove the streamed body is not mangled.
	if err := os.WriteFile(wasmPath, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00}, 0o600); err != nil {
		t.Fatal(err)
	}

	var gotMethod, gotPath, gotAuth string
	var gotParts []workersScriptsPart
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		gotParts = workersScriptsReadParts(t, r.Header.Get("Content-Type"), body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"my-worker","etag":"abc123"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "upload", "my-worker",
		"--module", filepath.Join(dir, "worker.js"),
		"--module", "lib.wasm="+wasmPath,
		"--main-module", "worker.js")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotPath != "/accounts/"+workersScriptsTestAccountID+"/workers/scripts/my-worker" {
		t.Errorf("got %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(gotParts) != 3 {
		t.Fatalf("got %d parts, want 3: %+v", len(gotParts), gotParts)
	}
	workersScriptsAssertJSONEqual(t, []byte(gotParts[0].Body), `{"main_module":"worker.js"}`)
	if gotParts[2].Name != "lib.wasm" || gotParts[2].ContentType != "application/wasm" {
		t.Errorf("wasm part = %+v", gotParts[2])
	}
	if gotParts[2].Body != string([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00}) {
		t.Errorf("wasm bytes = %q", gotParts[2].Body)
	}
	if !strings.Contains(stdout, "abc123") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestWorkersScriptsUploadAPIError(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10021,"message":"Uncaught SyntaxError"}],"result":null}`))
	}))
	defer srv.Close()

	_, _, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "upload", "my-worker", "--module", module)
	if err == nil || !strings.Contains(err.Error(), "Uncaught SyntaxError") {
		t.Fatalf("expected API error, got %v", err)
	}
}

func TestWorkersScriptsUploadUnsuccessfulEnvelope(t *testing.T) {
	dir := t.TempDir()
	module := workersScriptsWriteFile(t, dir, "worker.js", "x")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"workers.api.error"}],"result":null}`))
	}))
	defer srv.Close()

	_, _, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "upload", "my-worker", "--module", module)
	if err == nil || !strings.Contains(err.Error(), "workers.api.error") {
		t.Fatalf("expected upload failure, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// download

func TestWorkersScriptsDownloadDryRun(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "download", "my-worker", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/content/v2"
	if dump.Method != "GET" || !strings.HasSuffix(dump.URL, want) {
		t.Errorf("got %s %s, want GET %s", dump.Method, dump.URL, want)
	}
}

func TestWorkersScriptsDownloadRawBytes(t *testing.T) {
	body := "export default { fetch() {} }\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+workersScriptsTestAccountID+"/workers/scripts/my-worker/content/v2" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/javascript+module")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL, "workers", "script", "download", "my-worker")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != body {
		t.Errorf("stdout = %q, want exact bytes %q", stdout, body)
	}
}

func TestWorkersScriptsDownloadToFile(t *testing.T) {
	bundle := "--boundary\r\nContent-Disposition: form-data; name=\"worker.js\"\r\n\r\nexport default {}\r\n--boundary--\r\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "multipart/form-data; boundary=boundary")
		_, _ = w.Write([]byte(bundle))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bundle.txt")
	stdout, stderr, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "download", "my-worker", "--file", dest)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty when writing a file", stdout)
	}
	if !strings.Contains(stderr, dest) {
		t.Errorf("stderr = %q, want a note naming %s", stderr, dest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != bundle {
		t.Errorf("file = %q, want %q", got, bundle)
	}
}

func TestWorkersScriptsDownloadRejectsJSONFlags(t *testing.T) {
	_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"--query", ".foo", "workers", "script", "download", "my-worker", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--query") {
		t.Fatalf("expected --query rejection, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("code"))
	}))
	defer srv.Close()

	_, _, err = runWorkersScriptsCLI(t, srv.URL,
		"--output", "json", "workers", "script", "download", "my-worker")
	if err == nil || !strings.Contains(err.Error(), "--output") {
		t.Fatalf("expected --output rejection, got %v", err)
	}

	_, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "download", "my-worker", "--file", "  ", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("expected empty --file error, got %v", err)
	}
}

func TestWorkersScriptsDownloadAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10007,"message":"workers.api.error.script_not_found"}]}`))
	}))
	defer srv.Close()

	_, _, err := runWorkersScriptsCLI(t, srv.URL, "workers", "script", "download", "missing")
	if err == nil || !strings.Contains(err.Error(), "script_not_found") {
		t.Fatalf("expected API error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// secrets

func TestWorkersScriptsSecretListDryRun(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "list", "my-worker", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/secrets"
	if dump.Method != "GET" || !strings.HasSuffix(dump.URL, want) {
		t.Errorf("got %s %s, want GET %s", dump.Method, dump.URL, want)
	}
}

func TestWorkersScriptsSecretListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"API_TOKEN","type":"secret_text"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL, "workers", "script", "secret", "list", "my-worker")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "TYPE", "API_TOKEN", "secret_text"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestWorkersScriptsSecretPutDryRun(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN",
		"--value", "s3cret", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/secrets"
	if dump.Method != "PUT" || !strings.HasSuffix(strings.Split(dump.URL, "?")[0], want) {
		t.Errorf("got %s %s, want PUT %s", dump.Method, dump.URL, want)
	}
	workersScriptsAssertJSONEqual(t, dump.Body, `{"name":"API_TOKEN","text":"s3cret","type":"secret_text"}`)
}

func TestWorkersScriptsSecretPutValueSources(t *testing.T) {
	dir := t.TempDir()
	path := workersScriptsWriteFile(t, dir, "token.txt", "file-secret\n")

	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN",
		"--value", "@"+path, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	workersScriptsAssertJSONEqual(t, dump.Body, `{"name":"API_TOKEN","text":"file-secret","type":"secret_text"}`)

	stdout, _, err = runWorkersScriptsCLIStdin(t, "http://example.invalid",
		strings.NewReader("stdin-secret\n"),
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN",
		"--value", "@-", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump = workersScriptsParseDryRun(t, stdout)
	workersScriptsAssertJSONEqual(t, dump.Body, `{"name":"API_TOKEN","text":"stdin-secret","type":"secret_text"}`)
}

func TestWorkersScriptsSecretPutValidation(t *testing.T) {
	_, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN",
		"--value", "", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty value error, got %v", err)
	}

	_, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "put", "my-worker", "  ",
		"--value", "v", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "secret name") {
		t.Fatalf("expected secret name error, got %v", err)
	}

	_, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "value") {
		t.Fatalf("expected missing value error, got %v", err)
	}

	_, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN",
		"--value", "@"+filepath.Join(t.TempDir(), "absent"), "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "read --value file") {
		t.Fatalf("expected value file error, got %v", err)
	}
}

func TestWorkersScriptsSecretPutLive(t *testing.T) {
	var gotMethod, gotPath, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"name":"API_TOKEN","type":"secret_text"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "secret", "put", "my-worker", "API_TOKEN", "--value", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotPath != "/accounts/"+workersScriptsTestAccountID+"/workers/scripts/my-worker/secrets" {
		t.Errorf("got %s %s", gotMethod, gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type = %q", gotCT)
	}
	workersScriptsAssertJSONEqual(t, gotBody, `{"name":"API_TOKEN","text":"s3cret","type":"secret_text"}`)
	if !strings.Contains(stdout, "API_TOKEN") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestWorkersScriptsSecretDelete(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "delete", "my-worker", "API_TOKEN", "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/secrets/API_TOKEN"
	if dump.Method != "DELETE" || !strings.HasSuffix(dump.URL, want) {
		t.Errorf("got %s %s, want DELETE %s", dump.Method, dump.URL, want)
	}

	_, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "secret", "delete", "my-worker", "API_TOKEN")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// subdomain

func TestWorkersScriptsSubdomainGet(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "subdomain", "get", "my-worker", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/subdomain"
	if dump.Method != "GET" || !strings.HasSuffix(dump.URL, want) {
		t.Errorf("got %s %s, want GET %s", dump.Method, dump.URL, want)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"enabled":true,"previews_enabled":false}}`))
	}))
	defer srv.Close()

	stdout, _, err = runWorkersScriptsCLI(t, srv.URL, "workers", "script", "subdomain", "get", "my-worker")
	if err != nil {
		t.Fatal(err)
	}
	workersScriptsAssertJSONEqual(t, []byte(stdout), `{"enabled":true,"previews_enabled":false}`)
}

func TestWorkersScriptsSubdomainEnableBody(t *testing.T) {
	stdout, _, err := runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "subdomain", "enable", "my-worker", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := workersScriptsParseDryRun(t, stdout)
	want := "/accounts/" + workersScriptsTestAccountID + "/workers/scripts/my-worker/subdomain"
	if dump.Method != "POST" || !strings.HasSuffix(strings.Split(dump.URL, "?")[0], want) {
		t.Errorf("got %s %s, want POST %s", dump.Method, dump.URL, want)
	}
	workersScriptsAssertJSONEqual(t, dump.Body, `{"enabled":true,"previews_enabled":true}`)

	stdout, _, err = runWorkersScriptsCLI(t, "http://example.invalid",
		"workers", "script", "subdomain", "enable", "my-worker", "--previews=false", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump = workersScriptsParseDryRun(t, stdout)
	workersScriptsAssertJSONEqual(t, dump.Body, `{"enabled":true,"previews_enabled":false}`)
}

func TestWorkersScriptsSubdomainEnableLive(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"enabled":true,"previews_enabled":true}}`))
	}))
	defer srv.Close()

	stdout, _, err := runWorkersScriptsCLI(t, srv.URL,
		"workers", "script", "subdomain", "enable", "my-worker")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	workersScriptsAssertJSONEqual(t, gotBody, `{"enabled":true,"previews_enabled":true}`)
	if !strings.Contains(stdout, "previews_enabled") {
		t.Errorf("stdout = %s", stdout)
	}
}

// ---------------------------------------------------------------------------
// help

func TestWorkersScriptsHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"workers", "script", "list", "--help"}, []string{"cf workers script list", "--tag"}},
		{[]string{"workers", "script", "get", "--help"}, []string{"cf workers script get", "download"}},
		{[]string{"workers", "script", "upload", "--help"}, []string{"cf workers script upload", "--module", "--main-module", "multipart/form-data", "keep-bindings"}},
		{[]string{"workers", "script", "download", "--help"}, []string{"cf workers script download", "--file", "raw bytes"}},
		{[]string{"workers", "script", "delete", "--help"}, []string{"cf workers script delete", "--force", "--delete-bindings"}},
		{[]string{"workers", "script", "secret", "put", "--help"}, []string{"cf workers script secret put", "@-", "secret_text"}},
		{[]string{"workers", "script", "secret", "delete", "--help"}, []string{"cf workers script secret delete", "--force"}},
		{[]string{"workers", "script", "subdomain", "enable", "--help"}, []string{"cf workers script subdomain enable", "workers.dev", "--previews"}},
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
