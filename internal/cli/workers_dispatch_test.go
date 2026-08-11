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

const workersDispatchTestAccountID = "023e105f4ecef8ad9ca31a8372d0c353"

func runWorkersDispatchCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", workersDispatchTestAccountID,
	}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func workersDispatchAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid wanted JSON %s: %v", want, err)
	}
	gotCanonical, _ := json.Marshal(gotValue)
	wantCanonical, _ := json.Marshal(wantValue)
	if string(gotCanonical) != string(wantCanonical) {
		t.Fatalf("JSON = %s, want %s", gotCanonical, wantCanonical)
	}
}

func TestBuildWorkersDispatchNamespaceCreateBody(t *testing.T) {
	body, err := buildWorkersDispatchNamespaceCreateBody(" customer-workers ")
	if err != nil {
		t.Fatal(err)
	}
	workersDispatchAssertJSONEqual(t, body, `{"name":"customer-workers"}`)
	if _, err := buildWorkersDispatchNamespaceCreateBody(" \t "); err == nil || !strings.Contains(err.Error(), "namespace name cannot be empty") {
		t.Fatalf("expected namespace error, got %v", err)
	}
}

func TestBuildWorkersDispatchUploadCanonicalMetadata(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(file, []byte("export default { fetch() {} }"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload, err := buildWorkersDispatchUpload(file, `{"compatibility_date":"2025-01-01","main_module":"worker.js"}`, "STRICT", true)
	if err != nil {
		t.Fatal(err)
	}
	if upload.BindingsInherit != "strict" {
		t.Fatalf("bindings_inherit = %q", upload.BindingsInherit)
	}
	workersDispatchAssertJSONEqual(t, upload.Metadata, `{"compatibility_date":"2025-01-01","main_module":"worker.js"}`)
	if query := workersDispatchUploadQuery(upload.BindingsInherit); query.Get("bindings_inherit") != "strict" {
		t.Fatalf("bindings_inherit query = %q", query.Get("bindings_inherit"))
	}
}

func TestBuildWorkersDispatchUploadValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(file, []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		file        string
		metadata    string
		metadataSet bool
		inherit     string
		want        string
	}{
		{name: "missing file", metadata: `{"main_module":"worker.js"}`, metadataSet: true, want: "missing --file"},
		{name: "missing metadata", file: file, want: "missing --metadata"},
		{name: "directory", file: dir, metadata: `{"main_module":"worker.js"}`, metadataSet: true, want: "is a directory"},
		{name: "missing file path", file: filepath.Join(dir, "missing.js"), metadata: `{"main_module":"missing.js"}`, metadataSet: true, want: "read --file"},
		{name: "null metadata", file: file, metadata: "null", metadataSet: true, want: "JSON object"},
		{name: "array metadata", file: file, metadata: "[]", metadataSet: true, want: "JSON object"},
		{name: "string metadata", file: file, metadata: `"x"`, metadataSet: true, want: "JSON object"},
		{name: "number metadata", file: file, metadata: "1", metadataSet: true, want: "JSON object"},
		{name: "boolean metadata", file: file, metadata: "true", metadataSet: true, want: "JSON object"},
		{name: "malformed metadata", file: file, metadata: "{", metadataSet: true, want: "JSON object"},
		{name: "missing main module", file: file, metadata: `{}`, metadataSet: true, want: "main_module"},
		{name: "wrong main module type", file: file, metadata: `{"main_module":1}`, metadataSet: true, want: "main_module"},
		{name: "mismatched main module", file: file, metadata: `{"main_module":"other.js"}`, metadataSet: true, want: "must equal"},
		{name: "bad inherit mode", file: file, metadata: `{"main_module":"worker.js"}`, metadataSet: true, inherit: "relaxed", want: "must be strict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildWorkersDispatchUpload(tc.file, tc.metadata, tc.inherit, tc.metadataSet)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestWorkersDispatchNamespaceHTTPCommands(t *testing.T) {
	var requests []struct {
		method string
		path   string
		body   []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, struct {
			method string
			path   string
			body   []byte
		}{r.Method, r.URL.Path, body})
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			if strings.HasSuffix(r.URL.Path, "/namespaces") {
				_, _ = w.Write([]byte(`{"success":true,"result":[{"namespace_id":"id-1","namespace_name":"customer-workers","script_count":2,"trusted_workers":false,"modified_on":"2026-01-01T00:00:00Z"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":{"namespace_name":"customer-workers"}}`))
		case "POST":
			_, _ = w.Write([]byte(`{"success":true,"result":{"namespace_name":"customer-workers"}}`))
		case "DELETE":
			_, _ = w.Write([]byte(`{"success":true,"result":null}`))
		}
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"workers", "dispatch", "namespace", "list"},
		{"workers", "dispatch", "namespace", "get", "customer-workers"},
		{"workers", "dispatch", "namespace", "create", "customer-workers"},
		{"workers", "dispatch", "namespace", "delete", "customer-workers", "--force"},
	} {
		if _, _, err := runWorkersDispatchCLI(t, server.URL, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(requests) != 4 {
		t.Fatalf("request count = %d", len(requests))
	}
	base := "/accounts/" + workersDispatchTestAccountID + "/workers/dispatch/namespaces"
	if requests[0].method != "GET" || requests[0].path != base {
		t.Errorf("list = %s %s", requests[0].method, requests[0].path)
	}
	if requests[1].method != "GET" || requests[1].path != base+"/customer-workers" {
		t.Errorf("get = %s %s", requests[1].method, requests[1].path)
	}
	if requests[2].method != "POST" || requests[2].path != base {
		t.Errorf("create = %s %s", requests[2].method, requests[2].path)
	}
	workersDispatchAssertJSONEqual(t, requests[2].body, `{"name":"customer-workers"}`)
	if requests[3].method != "DELETE" || requests[3].path != base+"/customer-workers" {
		t.Errorf("delete = %s %s", requests[3].method, requests[3].path)
	}
}

func TestWorkersDispatchScriptHTTPCommands(t *testing.T) {
	var requests []struct {
		method string
		path   string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, struct {
			method string
			path   string
		}{r.Method, r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/scripts") {
			_, _ = w.Write([]byte(`{"success":true,"result":[{"modified_on":"2026-01-01T00:00:00Z","script":{"id":"checkout","compatibility_date":"2025-01-01"}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"script":{"id":"checkout"}}}`))
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"workers", "dispatch", "script", "list", "customer-workers"},
		{"workers", "dispatch", "script", "get", "customer-workers", "checkout"},
		{"workers", "dispatch", "script", "delete", "customer-workers", "checkout", "--force"},
	} {
		if _, _, err := runWorkersDispatchCLI(t, server.URL, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	base := "/accounts/" + workersDispatchTestAccountID + "/workers/dispatch/namespaces/customer-workers/scripts"
	if len(requests) != 3 {
		t.Fatalf("request count = %d", len(requests))
	}
	if requests[0].method != "GET" || requests[0].path != base {
		t.Errorf("list = %s %s", requests[0].method, requests[0].path)
	}
	if requests[1].method != "GET" || requests[1].path != base+"/checkout" {
		t.Errorf("get = %s %s", requests[1].method, requests[1].path)
	}
	if requests[2].method != "DELETE" || requests[2].path != base+"/checkout" {
		t.Errorf("delete = %s %s", requests[2].method, requests[2].path)
	}
}

func TestWorkersDispatchScriptUploadHTTPMultipartStream(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(file, []byte("export default { fetch() { return new Response('ok') } }"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotMetadata, gotModule, gotModuleContentType, gotPath, gotInherit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotInherit = r.URL.Query().Get("bindings_inherit")
		if r.Method != "PUT" {
			t.Errorf("method = %s", r.Method)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, err = %v", r.Header.Get("Content-Type"), err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			switch part.FormName() {
			case "metadata":
				gotMetadata = string(body)
			case "worker.js":
				gotModule = string(body)
				gotModuleContentType = part.Header.Get("Content-Type")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"checkout","compatibility_date":"2025-01-01"}}`))
	}))
	defer server.Close()

	stdout, _, err := runWorkersDispatchCLI(t, server.URL,
		"workers", "dispatch", "script", "upload", "customer-workers", "checkout",
		"--file", file,
		"--metadata", `{"compatibility_date":"2025-01-01","main_module":"worker.js"}`,
		"--bindings-inherit", "STRICT",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+workersDispatchTestAccountID+"/workers/dispatch/namespaces/customer-workers/scripts/checkout" {
		t.Errorf("path = %s", gotPath)
	}
	if gotInherit != "strict" {
		t.Errorf("bindings_inherit = %q", gotInherit)
	}
	workersDispatchAssertJSONEqual(t, []byte(gotMetadata), `{"compatibility_date":"2025-01-01","main_module":"worker.js"}`)
	if !strings.Contains(gotModule, "new Response('ok')") {
		t.Errorf("module = %q", gotModule)
	}
	if gotModuleContentType != "application/javascript+module" {
		t.Errorf("module Content-Type = %q", gotModuleContentType)
	}
	if !strings.Contains(stdout, "checkout") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestWorkersDispatchScriptUploadDryRun(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(file, []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runWorkersDispatchCLI(t, "http://example.invalid",
		"workers", "dispatch", "script", "upload", "customer-workers", "checkout",
		"--file", file,
		"--metadata", `{"main_module":"worker.js"}`,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("parse dry run: %v\n%s", err, stdout)
	}
	if dump.Method != "PUT" || !strings.HasSuffix(dump.URL, "/workers/dispatch/namespaces/customer-workers/scripts/checkout") {
		t.Errorf("dump = %#v", dump)
	}
	mediaType, params, err := mime.ParseMediaType(dump.Headers["Content-Type"])
	if err != nil || mediaType != "multipart/form-data" {
		t.Errorf("Content-Type = %q, err = %v", dump.Headers["Content-Type"], err)
	}
	if params["boundary"] != "cf-cli-workers-dispatch-dry-run" {
		t.Errorf("dry-run boundary = %q", params["boundary"])
	}
	var dumpedBody string
	if err := json.Unmarshal(dump.Body, &dumpedBody); err != nil {
		t.Fatalf("parse dry-run multipart body: %v", err)
	}
	reader := multipart.NewReader(strings.NewReader(dumpedBody), params["boundary"])
	parts := map[string]struct {
		body        string
		contentType string
	}{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		parts[part.FormName()] = struct {
			body        string
			contentType string
		}{string(body), part.Header.Get("Content-Type")}
	}
	workersDispatchAssertJSONEqual(t, []byte(parts["metadata"].body), `{"main_module":"worker.js"}`)
	if parts["metadata"].contentType != "application/json" {
		t.Errorf("metadata Content-Type = %q", parts["metadata"].contentType)
	}
	if parts["worker.js"].body != "export default {}" {
		t.Errorf("module body = %q", parts["worker.js"].body)
	}
	if parts["worker.js"].contentType != "application/javascript+module" {
		t.Errorf("module Content-Type = %q", parts["worker.js"].contentType)
	}
}

func TestWorkersDispatchScriptUploadRejectsInvalidMetadataBeforeNetwork(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "worker.js")
	if err := os.WriteFile(file, []byte("export default {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Error("invalid local input must not make an HTTP request")
	}))
	defer server.Close()
	_, _, err := runWorkersDispatchCLI(t, server.URL,
		"workers", "dispatch", "script", "upload", "customer-workers", "checkout",
		"--file", file,
		"--metadata", "null",
	)
	if err == nil || !strings.Contains(err.Error(), "--metadata must be a JSON object") {
		t.Fatalf("error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("HTTP calls = %d", calls)
	}
}
