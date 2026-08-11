package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const workersPlatformTestAccountID = "0123456789abcdef0123456789abcdef"

func runWorkersPlatformCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", workersPlatformTestAccountID,
	}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func workersPlatformAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func TestWorkersPlatformSchedulesBody(t *testing.T) {
	body, err := workersPlatformSchedulesBody(`[{"cron":"0 6 * * 1-5"}]`)
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, body, `[{"cron":"0 6 * * 1-5"}]`)

	body, err = workersPlatformSchedulesBody(`[]`)
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, body, `[]`)
}

func TestWorkersPlatformSchedulesBodyRejectsWrongJSONShapes(t *testing.T) {
	for _, raw := range []string{
		"null",
		"{}",
		`"cron"`,
		"1",
		"true",
		"[null]",
		"[[]]",
		`[{"cron":null}]`,
		`[{"cron":1}]`,
		`[{"cron":""}]`,
		`[{"cron":"* * * * *","created_on":"2026-01-01"}]`,
	} {
		if _, err := workersPlatformSchedulesBody(raw); err == nil {
			t.Errorf("raw %s: expected validation error", raw)
		}
	}
}

func TestWorkersPlatformUsageQueryBounds(t *testing.T) {
	query, err := workersPlatformUsageQuery("0", "1")
	if err != nil {
		t.Fatalf("minimum range: %v", err)
	}
	if got := query.Encode(); got != "from=0&to=1" {
		t.Errorf("query = %q", got)
	}

	max := workersPlatformUsageMaxRangeMS
	query, err = workersPlatformUsageQuery("1000", ""+strconv.FormatInt(1000+max, 10))
	if err != nil {
		t.Fatalf("maximum range: %v", err)
	}
	if got := query.Get("to"); got != strconv.FormatInt(1000+max, 10) {
		t.Errorf("to = %q", got)
	}

	if _, err := workersPlatformUsageQuery("1000", strconv.FormatInt(1000+max+1, 10)); err == nil || !strings.Contains(err.Error(), "90 days") {
		t.Fatalf("expected over-range error, got %v", err)
	}
	for _, input := range [][2]string{{"2", "2"}, {"3", "2"}, {"", "2"}, {"-1", "2"}, {"a", "2"}} {
		if _, err := workersPlatformUsageQuery(input[0], input[1]); err == nil {
			t.Errorf("range %q..%q: expected validation error", input[0], input[1])
		}
	}
}

func TestWorkersPlatformCronGetCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		want := "/accounts/" + workersPlatformTestAccountID + "/workers/scripts/daily-report/schedules"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		_, _ = io.WriteString(w, `{"success":true,"result":{"schedules":[{"cron":"0 6 * * 1-5","created_on":"2026-01-01","modified_on":"2026-01-02"}]}}`)
	}))
	defer srv.Close()

	stdout, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "cron", "get", "daily-report", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, []byte(stdout), `{"schedules":[{"cron":"0 6 * * 1-5","created_on":"2026-01-01","modified_on":"2026-01-02"}]}`)
}

func TestWorkersPlatformCronSetCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s", r.Method)
		}
		want := "/accounts/" + workersPlatformTestAccountID + "/workers/scripts/daily-report/schedules"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		workersPlatformAssertJSONEqual(t, body, `[{"cron":"0 6 * * 1-5"}]`)
		_, _ = io.WriteString(w, `{"success":true,"result":{"schedules":[{"cron":"0 6 * * 1-5"}]}}`)
	}))
	defer srv.Close()

	stdout, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "cron", "set", "daily-report", "--schedules", `[{"cron":"0 6 * * 1-5"}]`)
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, []byte(stdout), `{"schedules":[{"cron":"0 6 * * 1-5"}]}`)
}

func TestWorkersPlatformLocalValidationPrecedesNetwork(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "cron", "set", "daily-report", "--schedules", "null")
	if err == nil || !strings.Contains(err.Error(), "--schedules") {
		t.Fatalf("expected schedules error, got %v", err)
	}
	if called {
		t.Fatal("invalid local input made a network request")
	}
	_, _, err = runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "usage", "--from", "0", "--to", strconv.FormatInt(workersPlatformUsageMaxRangeMS+1, 10))
	if err == nil || !strings.Contains(err.Error(), "90 days") {
		t.Fatalf("expected usage range error, got %v", err)
	}
	if called {
		t.Fatal("invalid local usage range made a network request")
	}
	_, _, err = runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "domain", "add", " ", "app-worker", "--zone", "example.com")
	if err == nil || !strings.Contains(err.Error(), "hostname") {
		t.Fatalf("expected hostname error, got %v", err)
	}
	if called {
		t.Fatal("invalid custom-domain input made a name-resolution request")
	}
}

func TestWorkersPlatformDomainListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		want := "/accounts/" + workersPlatformTestAccountID + "/workers/domains"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.URL.Query().Get("service"); got != "app-worker" {
			t.Errorf("service = %q", got)
		}
		if got := r.URL.Query().Get("hostname"); got != "app.example.com" {
			t.Errorf("hostname = %q", got)
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"domain-1","cert_id":"cert-1","hostname":"app.example.com","service":"app-worker","zone_id":"zone-1","zone_name":"example.com"}]}`)
	}))
	defer srv.Close()

	stdout, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "domain", "list", "--service", "app-worker", "--hostname", "app.example.com", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, []byte(stdout), `[{"id":"domain-1","cert_id":"cert-1","hostname":"app.example.com","service":"app-worker","zone_id":"zone-1","zone_name":"example.com"}]`)
}

func TestWorkersPlatformDomainAddCommandResolvesZoneAndUsesCanonicalWireValues(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/zones":
			if got := r.URL.Query().Get("name"); got != "example.com" {
				t.Errorf("zone name = %q", got)
			}
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"zone-1","name":"example.com"}]}`)
		case "/accounts/" + workersPlatformTestAccountID + "/workers/domains":
			if r.Method != http.MethodPut {
				t.Errorf("method = %s", r.Method)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			workersPlatformAssertJSONEqual(t, body, `{"hostname":"app.example.com","service":"app-worker","zone_id":"zone-1","zone_name":"example.com"}`)
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"domain-1"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "domain", "add", "app.example.com", "app-worker", "--zone", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestWorkersPlatformDomainRemoveCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		want := "/accounts/" + workersPlatformTestAccountID + "/workers/domains/domain-1"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		_, _ = io.WriteString(w, `{"success":true,"result":{}}`)
	}))
	defer srv.Close()

	_, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "domain", "remove", "domain-1", "--force")
	if err != nil {
		t.Fatal(err)
	}
}

func TestWorkersPlatformDeploymentListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		want := "/accounts/" + workersPlatformTestAccountID + "/workers/scripts/app-worker/deployments"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		_, _ = io.WriteString(w, `{"success":true,"result":{"deployments":[{"id":"dep-1","created_on":"2026-01-01T00:00:00Z","source":"wrangler","strategy":"percentage","versions":[{"percentage":100,"version_id":"version-1"}]}]}}`)
	}))
	defer srv.Close()

	stdout, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "deployment", "list", "app-worker", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, []byte(stdout), `{"deployments":[{"id":"dep-1","created_on":"2026-01-01T00:00:00Z","source":"wrangler","strategy":"percentage","versions":[{"percentage":100,"version_id":"version-1"}]}]}`)
}

func TestWorkersPlatformUsageCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		want := "/accounts/" + workersPlatformTestAccountID + "/workers/observability/usage"
		if r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		if got := r.URL.RawQuery; got != "from=0&to=7776000000" {
			t.Errorf("query = %q", got)
		}
		_, _ = io.WriteString(w, `{"success":true,"errors":[],"messages":[],"result":{"events":12,"breakdown":[{"bin":"2026-01-01T00:00:00Z","dataset":"workers","service":"app-worker","count":12}]}}`)
	}))
	defer srv.Close()

	stdout, _, err := runWorkersPlatformCLI(t, srv.URL, "workers", "platform", "usage", "--from", "000", "--to", "7776000000", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	workersPlatformAssertJSONEqual(t, []byte(stdout), `{"events":12,"breakdown":[{"bin":"2026-01-01T00:00:00Z","dataset":"workers","service":"app-worker","count":12}]}`)
}

func TestWorkersPlatformDryRun(t *testing.T) {
	stdout, _, err := runWorkersPlatformCLI(t, "http://example.invalid", "--dry-run", "workers", "platform", "cron", "set", "daily-report", "--schedules", `[{"cron":"0 6 * * 1-5"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"method": "PUT"`) || !strings.Contains(stdout, "/workers/scripts/daily-report/schedules") {
		t.Errorf("unexpected dry run: %s", stdout)
	}
}
