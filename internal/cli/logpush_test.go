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

func TestLogpushDatasetIsCreateOnly(t *testing.T) {
	root := NewRootCmd()
	root.SetArgs([]string{"--account-id", logpushTestAccountID, "logpush", "jobs", "update", "123", "--dataset", "http_requests", "--dry-run"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --dataset") {
		t.Fatalf("expected create-only dataset flag error, got %v", err)
	}
}

func TestLogpushBatchLimitValidationBoundaries(t *testing.T) {
	modes := []struct {
		name   string
		create bool
	}{
		{"create", true},
		{"update", false},
	}
	cases := []struct {
		flag       string
		min, max   int
		invalidLow int
		invalidHi  int
	}{
		{"max-upload-bytes", 5_000_000, 1_000_000_000, 4_999_999, 1_000_000_001},
		{"max-upload-interval", 30, 300, 29, 301},
		{"max-upload-records", 1_000, 1_000_000, 999, 1_000_001},
	}
	for _, tc := range cases {
		for _, mode := range modes {
			t.Run(mode.name+"/"+tc.flag, func(t *testing.T) {
				for _, value := range []int{0, tc.min, tc.max} {
					cmd, options := newLogpushJobBodyTestCommand(t, mode.create)
					if err := cmd.Flags().Set(tc.flag, strconv.Itoa(value)); err != nil {
						t.Fatal(err)
					}
					if _, err := buildLogpushJobBody(cmd, *options, mode.create); err != nil {
						t.Fatalf("value %d rejected: %v", value, err)
					}
				}
				for _, value := range []int{tc.invalidLow, tc.invalidHi} {
					cmd, options := newLogpushJobBodyTestCommand(t, mode.create)
					if err := cmd.Flags().Set(tc.flag, strconv.Itoa(value)); err != nil {
						t.Fatal(err)
					}
					if _, err := buildLogpushJobBody(cmd, *options, mode.create); err == nil || !strings.Contains(err.Error(), "--"+tc.flag) {
						t.Fatalf("value %d: expected %s range error, got %v", value, tc.flag, err)
					}
				}
			})
		}
	}
}

func newLogpushJobBodyTestCommand(t *testing.T, create bool) (*cobra.Command, *logpushJobOptions) {
	t.Helper()
	cmd := &cobra.Command{}
	options := &logpushJobOptions{}
	addLogpushJobFlags(cmd, options, create)
	if create {
		for flag, value := range map[string]string{"dataset": "http_requests", "destination": "s3://bucket/logs?region=eu-west-1"} {
			if err := cmd.Flags().Set(flag, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	return cmd, options
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

func TestLogpushJobsGetAccountEndpoint(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":123,"dataset":"http_requests"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "jobs", "get", "123")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/accounts/"+logpushTestAccountID+"/logpush/jobs/123" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if !strings.Contains(stdout, `"id": 123`) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestLogpushZoneNameResolution(t *testing.T) {
	var sawLookup, sawJob bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zones":
			sawLookup = true
			if r.URL.Query().Get("name") != "example.com" {
				t.Errorf("lookup name = %q", r.URL.Query().Get("name"))
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + logpushTestZoneID + `","name":"example.com"}]}`))
		case "/zones/" + logpushTestZoneID + "/logpush/jobs/123":
			sawJob = true
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":123}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := runLogpushCLI(t, srv.URL, "logpush", "jobs", "get", "123", "--scope", "zone", "--zone", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !sawLookup || !sawJob {
		t.Fatalf("zone resolution lookup=%v job=%v", sawLookup, sawJob)
	}
}

func TestLogpushJobsListJSONRendering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":123,"dataset":"http_requests"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "--output", "json", "logpush", "jobs", "list")
	if err != nil {
		t.Fatal(err)
	}
	var jobs []struct {
		ID      int64  `json:"id"`
		Dataset string `json:"dataset"`
	}
	if err := json.Unmarshal([]byte(stdout), &jobs); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, stdout)
	}
	if len(jobs) != 1 || jobs[0].ID != 123 || jobs[0].Dataset != "http_requests" {
		t.Fatalf("jobs = %#v", jobs)
	}

	stdout, _, err = runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "--query", ".[0].dataset", "logpush", "jobs", "list")
	if err != nil {
		t.Fatal(err)
	}
	var dataset string
	if err := json.Unmarshal([]byte(stdout), &dataset); err != nil {
		t.Fatalf("query output is not JSON: %v\n%s", err, stdout)
	}
	if dataset != "http_requests" {
		t.Fatalf("dataset = %q", dataset)
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

func TestLogpushDatasetFieldsJSONAndQueryRendering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"result":{"RayID":"The request Ray ID","ClientIP":"The client IP"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "--output", "json", "logpush", "datasets", "fields", "http_requests")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(stdout), &fields); err != nil {
		t.Fatalf("fields output is not JSON: %v\n%s", err, stdout)
	}
	if fields["RayID"] != "The request Ray ID" {
		t.Fatalf("fields = %#v", fields)
	}

	stdout, _, err = runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "--query", ".RayID", "logpush", "datasets", "fields", "http_requests")
	if err != nil {
		t.Fatal(err)
	}
	var description string
	if err := json.Unmarshal([]byte(stdout), &description); err != nil {
		t.Fatalf("query output is not JSON: %v\n%s", err, stdout)
	}
	if description != "The request Ray ID" {
		t.Fatalf("description = %q", description)
	}
}

func TestLogpushJobsDeleteEndpointAndValidation(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":123}}`))
	}))
	defer srv.Close()

	_, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "jobs", "delete", "123", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/accounts/"+logpushTestAccountID+"/logpush/jobs/123" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}

	_, _, err = runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "jobs", "delete", "0", "--force", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("expected job ID error, got %v", err)
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

func TestLogpushOwnershipChallengeEndpoint(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true,"result":{"filename":"challenge.txt"}}`))
	}))
	defer srv.Close()

	_, _, err := runLogpushCLI(t, srv.URL, "--account-id", logpushTestAccountID, "logpush", "ownership", "challenge", "--destination", "s3://bucket/logs?region=eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/accounts/"+logpushTestAccountID+"/logpush/ownership" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	logpushAssertJSONEqual(t, gotBody, `{"destination_conf":"s3://bucket/logs?region=eu-west-1"}`)
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
