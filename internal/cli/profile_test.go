package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trmdy/cf-cli/internal/config"
)

func runCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	if stdin != "" {
		root.SetIn(strings.NewReader(stdin))
	}
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func isolatedConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CF_CONFIG_DIR", dir)
	t.Setenv("CF_PROFILE", "")
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CF_API_TOKEN", "")
	return dir
}

func TestProfileSetListUseDelete(t *testing.T) {
	isolatedConfig(t)

	for _, args := range [][]string{
		{"profile", "set", "work", "api-token", "tok-work-abcdef"},
		{"profile", "set", "work", "account-id", "acct-1"},
		{"profile", "set", "personal", "api_token", "tok-personal-xyz"},
	} {
		if _, _, err := runCLI(t, "", args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	stdout, _, err := runCLI(t, "", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "work") || !strings.Contains(stdout, "personal") {
		t.Errorf("list missing profiles: %s", stdout)
	}
	if strings.Contains(stdout, "tok-work-abcdef") || strings.Contains(stdout, "tok-personal-xyz") {
		t.Errorf("token leaked in list output: %s", stdout)
	}

	// first-created profile became default; switch it
	if _, _, err := runCLI(t, "", "profile", "use", "personal"); err != nil {
		t.Fatal(err)
	}
	f, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.DefaultProfile != "personal" {
		t.Errorf("default = %q", f.DefaultProfile)
	}

	// json output also masks
	stdout, _, err = runCLI(t, "", "profile", "show", "work", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var v profileView
	if err := json.Unmarshal([]byte(stdout), &v); err != nil {
		t.Fatalf("show output not json: %v\n%s", err, stdout)
	}
	if strings.Contains(v.APIToken, "work-abcdef"[:6]) || v.AccountID != "acct-1" {
		t.Errorf("unexpected view: %+v", v)
	}

	if _, _, err := runCLI(t, "", "profile", "delete", "work", "--force"); err != nil {
		t.Fatal(err)
	}
	f, _ = config.Load()
	if _, ok := f.Profiles["work"]; ok {
		t.Error("work profile not deleted")
	}
}

func TestProfileUseUnknownFails(t *testing.T) {
	isolatedConfig(t)
	if _, _, err := runCLI(t, "", "profile", "use", "nope"); err == nil {
		t.Error("expected error for unknown profile")
	}
	if _, _, err := runCLI(t, "", "profile", "set", "p", "bogus-key", "v"); err == nil {
		t.Error("expected error for unknown key")
	}
}

func TestProfileRename(t *testing.T) {
	isolatedConfig(t)
	if _, _, err := runCLI(t, "", "profile", "set", "old", "zone-id", "z1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "", "profile", "rename", "old", "new"); err != nil {
		t.Fatal(err)
	}
	f, _ := config.Load()
	if f.Profiles["new"].ZoneID != "z1" || f.DefaultProfile != "new" {
		t.Errorf("rename failed: %+v", f)
	}
}

func TestAuthLoginPipedVerifies(t *testing.T) {
	dir := isolatedConfig(t)
	verified := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/tokens/verify" {
			verified = true
			fmt.Fprint(w, `{"success":true,"result":{"id":"t1","status":"active"}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// piped token: cobra InOrStdin carries it; os.Stdin stat won't look like
	// a pipe under `go test`, so pass --token to exercise the verify path.
	_, _, err := runCLI(t, "", "--base-url", srv.URL, "--token", "tok123", "auth", "login")
	if err != nil {
		t.Fatal(err)
	}
	if !verified {
		t.Error("login did not verify the token")
	}
	f, err := config.LoadFrom(filepath.Join(dir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Profiles["default"].APIToken != "tok123" {
		t.Errorf("token not saved: %+v", f)
	}
}

func TestAuthLoginVerifyFailureRejects(t *testing.T) {
	isolatedConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`)
	}))
	defer srv.Close()

	_, _, err := runCLI(t, "", "--base-url", srv.URL, "--token", "bad", "auth", "login")
	if err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("expected verification failure, got %v", err)
	}
	f, _ := config.Load()
	if len(f.Profiles) != 0 {
		t.Error("bad token should not be saved")
	}
	// --no-verify overrides
	if _, _, err := runCLI(t, "", "--base-url", srv.URL, "--token", "bad", "auth", "login", "--no-verify"); err != nil {
		t.Fatal(err)
	}
}

func newBufReader(s string) *bufio.Reader { return bufio.NewReader(strings.NewReader(s)) }

func TestPrompterSelectOption(t *testing.T) {
	var out bytes.Buffer
	p := &prompter{in: newBufReader("2\n"), out: &out}
	idx, err := p.selectOption("pick", []string{"a", "b", "c"}, false)
	if err != nil || idx != 1 {
		t.Errorf("idx=%d err=%v", idx, err)
	}

	p = &prompter{in: newBufReader("\n"), out: &out}
	idx, err = p.selectOption("pick", []string{"a"}, true)
	if err != nil || idx != -1 {
		t.Errorf("skip: idx=%d err=%v", idx, err)
	}

	p = &prompter{in: newBufReader("9\nx\n3\n"), out: &out}
	idx, err = p.selectOption("pick", []string{"a", "b", "c"}, false)
	if err != nil || idx != 2 {
		t.Errorf("retry: idx=%d err=%v", idx, err)
	}
}
