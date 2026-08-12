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

const aiTestAccountID = "023e105f4ecef8ad9ca31a8372d0c353"

func runAICLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runAICLIWithStdin(t, serverURL, nil, args...)
}

func runAICLIWithStdin(t *testing.T, serverURL string, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
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
		"--account-id", aiTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func aiAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("got invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("want invalid JSON %s: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}

func TestAIRunDryRunWithFields(t *testing.T) {
	stdout, _, err := runAICLI(t, "http://example.invalid",
		"ai", "run", "@cf/meta/llama-3-8b-instruct",
		"-f", "prompt=Hello from test",
		"-f", "max_tokens=50",
		"--dry-run",
	)
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/ai/run/@cf%2Fmeta%2Fllama-3-8b-instruct") {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	aiAssertJSONEqual(t, dump.Body, `{"prompt":"Hello from test","max_tokens":50}`)
}

func TestAIRunDryRunWithData(t *testing.T) {
	stdout, _, err := runAICLI(t, "http://example.invalid",
		"ai", "run", "@cf/baai/bge-base-en-v1.5",
		"--data", `{"text":"embed this text"}`,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	aiAssertJSONEqual(t, dump.Body, `{"text":"embed this text"}`)
}

func TestAIRunRejectsDataAndFieldTogether(t *testing.T) {
	_, _, err := runAICLI(t, "http://example.invalid",
		"ai", "run", "m",
		"--data", `{"a":1}`,
		"-f", "b=2",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "--data and --field") {
		t.Fatalf("err = %v", err)
	}
}

func TestAIRunValidatesModelNotEmpty(t *testing.T) {
	_, _, err := runAICLI(t, "http://example.invalid", "ai", "run", "   ", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "cannot be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestAIRunReadableResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/ai/run/@cf/test/model") {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"response":"This is the readable output."}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAICLI(t, srv.URL, "ai", "run", "@cf/test/model", "-f", "prompt=hi")
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "This is the readable output.\n" {
		t.Fatalf("readable stdout = %q", stdout)
	}
}

func TestAIRunJSONOutputFullResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"response":"hi","usage":{"tokens":3}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runAICLI(t, srv.URL, "--output", "json", "ai", "run", "@cf/x", "-f", "prompt=p")
	if err != nil {
		t.Fatal(err)
	}
	// should contain the full structure
	if !strings.Contains(stdout, `"response"`) || !strings.Contains(stdout, `"usage"`) {
		t.Fatalf("json output = %s", stdout)
	}
}

func TestAIModelsListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/accounts/"+aiTestAccountID+"/ai/models/search" {
			t.Errorf("got %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page=%s", r.URL.Query().Get("per_page"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"@cf/a/1","description":"first model","task":{"id":"text-generation","name":"Text Generation"}},{"id":"@cf/b/2","description":"second","task":{"id":"embedding"}}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAICLI(t, srv.URL, "ai", "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "TASK") || !strings.Contains(stdout, "DESCRIPTION") {
		t.Fatalf("table header missing: %s", stdout)
	}
	if !strings.Contains(stdout, "@cf/a/1") || !strings.Contains(stdout, "Text Generation") {
		t.Fatalf("table rows missing: %s", stdout)
	}
}

func TestAIModelsListWithFiltersDryRun(t *testing.T) {
	stdout, _, err := runAICLI(t, "http://example.invalid",
		"ai", "models", "list",
		"--search", "llama",
		"--author", "Meta",
		"--task", "Text Generation",
		"--hide-experimental",
		"--dry-run",
	)
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
	if dump.Method != "GET" || !strings.Contains(dump.URL, "search=") || !strings.Contains(dump.URL, "author=Meta") {
		t.Fatalf("dry run url = %s", dump.URL)
	}
	if !strings.Contains(dump.URL, "hide_experimental=true") {
		t.Fatalf("hide flag missing: %s", dump.URL)
	}
}

func TestAIModelsListJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"m1"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runAICLI(t, srv.URL, "--output", "json", "ai", "models", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"id": "m1"`) {
		t.Fatalf("json list = %s", stdout)
	}
}

func TestAIRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t", "--dry-run", "ai", "models", "list"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("expected account error, got %v", err)
	}
}

func TestAIRunRequiresAccountBeforeNetButInputFirst(t *testing.T) {
	// Input validation (mutual exclusive) happens before client/account.
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "t",
		"--dry-run",
		"ai", "run", "m",
		"--data", `{"p":1}`,
		"-f", "x=y",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--data and --field") {
		t.Fatalf("expected data/field err before account, got %v", err)
	}
}

func TestAIRunHTTPAndBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"response":"ok"}}`))
	}))
	defer srv.Close()

	_, _, err := runAICLI(t, srv.URL, "ai", "run", "@cf/foo/bar", "--data", `{"prompt":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || !strings.HasSuffix(gotPath, "/ai/run/@cf/foo/bar") {
		t.Fatalf("http %s %s", gotMethod, gotPath)
	}
	aiAssertJSONEqual(t, gotBody, `{"prompt":"x"}`)
}

func TestAIModelsListBadFormat(t *testing.T) {
	_, _, err := runAICLI(t, "http://example.invalid", "ai", "models", "list", "--format", "foo", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "openrouter") {
		t.Fatalf("err=%v", err)
	}
}

func TestAICommandsInHelp(t *testing.T) {
	stdout, _, err := runAICLI(t, "http://example.invalid", "ai", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "run ") || !strings.Contains(stdout, "models") {
		t.Fatalf("help missing subcmds: %s", stdout)
	}
	stdout, _, err = runAICLI(t, "http://example.invalid", "ai", "run", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "-f") || !strings.Contains(stdout, "--data") || !strings.Contains(stdout, "@cf/") {
		t.Fatalf("run help missing usage: %s", stdout)
	}
}

func TestAIRunRequiresInputBody(t *testing.T) {
	_, _, err := runAICLI(t, "http://example.invalid", "ai", "run", "@cf/m", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no input provided") || !strings.Contains(err.Error(), "--data") {
		t.Fatalf("err = %v", err)
	}
}

func TestAIRunInputBodyRequiredBeforeClient(t *testing.T) {
	// Full local input contract (require body via --data or -f) must fail before client construction or account resolution.
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "t",
		"--dry-run",
		"ai", "run", "m",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no input provided") {
		t.Fatalf("expected input body err before account, got %v", err)
	}
}

func TestAIRunDataMustBeNonNullObject(t *testing.T) {
	cases := []struct {
		name string
		data string
		want string
	}{
		{"null", "null", "non-null JSON object"},
		{"array", "[]", "non-null JSON object"},
		{"string", `"foo"`, "non-null JSON object"},
		{"number", "42", "non-null JSON object"},
		{"true", "true", "non-null JSON object"},
		{"false", "false", "non-null JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runAICLI(t, "http://example.invalid", "ai", "run", "@cf/m", "--data", tc.data, "--dry-run")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAIRunDataObjectBeforeClient(t *testing.T) {
	// Invalid --data shapes must fail before client/account (like missing body).
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "t",
		"--dry-run",
		"ai", "run", "m",
		"--data", "null",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "non-null JSON object") {
		t.Fatalf("expected data shape err before account, got %v", err)
	}
}

func TestAIRunDataValidObject(t *testing.T) {
	stdout, _, err := runAICLI(t, "http://example.invalid",
		"ai", "run", "@cf/m", "--data", `{"prompt":"hi"}`, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	aiAssertJSONEqual(t, dump.Body, `{"prompt":"hi"}`)
}
