package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const logpushTestAccountID = "account-test"
const logpushTestZoneID = "0123456789abcdef0123456789abcdef"

func runLogpushCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token"}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestBuildLogpushJobBodyCreate(t *testing.T) {
	cmd := &cobra.Command{}
	options := logpushJobOptions{}
	addLogpushJobFlags(cmd, &options, true)
	for flag, value := range map[string]string{"dataset": "http_requests", "destination": "s3://logs-bucket/http?region=eu-west-1", "name": "example.com", "field": "RayID", "enabled": "true", "sample-rate": "0.25"} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := cmd.Flags().Set("field", "ClientIP"); err != nil {
		t.Fatal(err)
	}
	body, err := buildLogpushJobBody(cmd, options, true)
	if err != nil {
		t.Fatal(err)
	}
	logpushAssertJSONEqual(t, body, `{"dataset":"http_requests","destination_conf":"s3://logs-bucket/http?region=eu-west-1","enabled":true,"name":"example.com","output_options":{"field_names":["RayID","ClientIP"],"sample_rate":0.25}}`)
}

func TestBuildLogpushJobBodyUpdateValidation(t *testing.T) {
	cmd := &cobra.Command{}
	options := logpushJobOptions{}
	addLogpushJobFlags(cmd, &options, false)
	if _, err := buildLogpushJobBody(cmd, options, false); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("expected nothing-to-update error, got %v", err)
	}
	if err := cmd.Flags().Set("output-type", "xml"); err != nil {
		t.Fatal(err)
	}
	options.outputType = "xml"
	if _, err := buildLogpushJobBody(cmd, options, false); err == nil || !strings.Contains(err.Error(), "ndjson or csv") {
		t.Fatalf("expected output type error, got %v", err)
	}
}

func TestBuildLogpushOwnershipBodyValidation(t *testing.T) {
	if _, err := buildLogpushOwnershipBody("", "", false); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("expected destination error, got %v", err)
	}
	if _, err := buildLogpushOwnershipBody("s3://bucket/logs", "", true); err == nil || !strings.Contains(err.Error(), "challenge") {
		t.Fatalf("expected challenge error, got %v", err)
	}
	body, err := buildLogpushOwnershipBody("s3://bucket/logs", "token", true)
	if err != nil {
		t.Fatal(err)
	}
	logpushAssertJSONEqual(t, body, `{"destination_conf":"s3://bucket/logs","ownership_challenge":"token"}`)
}

func TestLogpushJobsListAccountHTTPAndTable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":123,"dataset":"http_requests","destination_conf":"s3://bucket/logs","enabled":true,"name":"example.com"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "jobs", "list")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+logpushTestAccountID+"/logpush/jobs" {
		t.Errorf("path = %s", gotPath)
	}
	for _, want := range []string{"ID", "DATASET", "http_requests", "example.com"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestLogpushJobsCreateZoneDryRun(t *testing.T) {
	stdout, _, err := runLogpushCLI(t, "http://example.invalid",
		"logpush", "jobs", "create",
		"--scope", "zone", "--zone", logpushTestZoneID,
		"--dataset", "http_requests",
		"--destination", "s3://bucket/http?region=eu-west-1",
		"--field", "RayID",
		"--field", "ClientIP",
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/zones/"+logpushTestZoneID+"/logpush/jobs") {
		t.Fatalf("dump = %+v", dump)
	}
	logpushAssertJSONEqual(t, dump.Body, `{"dataset":"http_requests","destination_conf":"s3://bucket/http?region=eu-west-1","output_options":{"field_names":["RayID","ClientIP"]}}`)
}

func TestLogpushJobsUpdateZoneHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":123,"enabled":false}}`))
	}))
	defer srv.Close()

	_, _, err := runLogpushCLI(t, srv.URL,
		"logpush", "jobs", "update", "123",
		"--scope", "zone", "--zone", logpushTestZoneID,
		"--enabled=false",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotPath != "/zones/"+logpushTestZoneID+"/logpush/jobs/123" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	logpushAssertJSONEqual(t, gotBody, `{"enabled":false}`)
}

func TestLogpushDatasetFieldsHTTPAndTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+logpushTestAccountID+"/logpush/datasets/http_requests/fields" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"result":{"RayID":"The request Ray ID","ClientIP":"The client IP"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "datasets", "fields", "http_requests")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FIELD", "DESCRIPTION", "ClientIP", "The request Ray ID"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestLogpushOwnershipValidationDryRun(t *testing.T) {
	stdout, _, err := runLogpushCLI(t, "http://example.invalid",
		"--account-id", logpushTestAccountID,
		"logpush", "ownership", "validate",
		"--destination", "s3://bucket/logs?region=eu-west-1",
		"--challenge", "challenge-token",
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/accounts/"+logpushTestAccountID+"/logpush/ownership/validate") {
		t.Fatalf("dump = %+v", dump)
	}
	logpushAssertJSONEqual(t, dump.Body, `{"destination_conf":"s3://bucket/logs?region=eu-west-1","ownership_challenge":"challenge-token"}`)
}

func TestLogpushScopeAndDeleteSafety(t *testing.T) {
	_, _, err := runLogpushCLI(t, "http://example.invalid", "--account-id", logpushTestAccountID,
		"logpush", "jobs", "list", "--scope", "wrong", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "account or zone") {
		t.Fatalf("expected scope error, got %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	_, _, err = runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "jobs", "delete", "123")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force error, got %v", err)
	}
}

func TestLogpushHelpIncludesExamples(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"logpush", "jobs", "create", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--dataset", "--destination", "--scope", "cf logpush jobs create"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q:\n%s", want, out.String())
		}
	}
}

func logpushAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid expected JSON %s: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}
