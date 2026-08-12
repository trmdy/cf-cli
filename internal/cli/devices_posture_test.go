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

const postureTestAccountID = "account-123"
const postureTestID = "f174e90a-fafe-4643-bbbc-4a0ed4fc8415"

func runPostureCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CF_PROFILE", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	root := NewRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token", "--account-id", postureTestAccountID}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func postureJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON: %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid wanted JSON: %s: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("JSON = %s, want %s", gotJSON, wantJSON)
	}
}

func TestPostureRuleCreateValidatesBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Fatal("invalid local input reached server")
	}))
	defer server.Close()

	_, _, err := runPostureCLI(t, server.URL,
		"devices", "posture", "rules", "create",
		"--name", "bad", "--type", "not-a-rule", "--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "--type must be one of") {
		t.Fatalf("error = %v, want type validation", err)
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}

	_, _, err = runPostureCLI(t, server.URL,
		"devices", "posture", "rules", "update", postureTestID, "--input", "[]",
	)
	if err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("update error = %v, want input validation", err)
	}
	if requests != 0 {
		t.Fatalf("update requests = %d, want 0", requests)
	}
}

func TestPostureRuleCreateRequestAndDryRun(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/accounts/"+postureTestAccountID+"/devices/posture" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		postureJSONEqual(t, body, `{"name":"Signed binary","type":"file","schedule":"5m","input":{"operating_system":"mac","path":"/usr/local/bin/tool"}}`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + postureTestID + `"}}`))
	}))
	defer server.Close()

	_, _, err := runPostureCLI(t, server.URL,
		"devices", "posture", "rules", "create",
		"--name", "Signed binary", "--type", "file", "--schedule", "5m",
		"--input", `{"operating_system":"mac","path":"/usr/local/bin/tool"}`,
	)
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runPostureCLI(t, server.URL,
		"devices", "posture", "rules", "create",
		"--name", "Signed binary", "--type", "file", "--schedule", "5m",
		"--input", `{"operating_system":"mac","path":"/usr/local/bin/tool"}`, "--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != http.MethodPost {
		t.Fatalf("dry-run method = %q", dump.Method)
	}
	postureJSONEqual(t, dump.Body, `{"name":"Signed binary","type":"file","schedule":"5m","input":{"operating_system":"mac","path":"/usr/local/bin/tool"}}`)
}

func TestPostureRuleUpdateReadMergeWriteAndDryRun(t *testing.T) {
	var methods []string
	var putBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + postureTestID + `","enabled":true,"name":"Current","type":"file","schedule":"5m","input":{"path":"/bin/old","operating_system":"mac"},"future_field":{"keep":true}}}`))
		case http.MethodPut:
			putBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	_, _, err := runPostureCLI(t, server.URL,
		"devices", "posture", "rules", "update", postureTestID, "--schedule", "10m",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET,PUT" {
		t.Fatalf("methods = %v, want GET, PUT", methods)
	}
	postureJSONEqual(t, putBody, `{"name":"Current","type":"file","schedule":"10m","input":{"path":"/bin/old","operating_system":"mac"},"future_field":{"keep":true}}`)

	methods = nil
	stdout, _, err := runPostureCLI(t, server.URL,
		"devices", "posture", "rules", "update", postureTestID, "--schedule", "10m", "--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET" {
		t.Fatalf("dry-run methods = %v, want GET only", methods)
	}
	var dump struct {
		Method string          `json:"method"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != http.MethodPut {
		t.Fatalf("dry-run method = %q", dump.Method)
	}
	postureJSONEqual(t, dump.Body, string(putBody))
}

func TestPostureIntegrationCreateAndPatch(t *testing.T) {
	var requests []string
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer server.Close()

	_, _, err := runPostureCLI(t, server.URL,
		"devices", "posture", "integrations", "create",
		"--name", "CrowdStrike", "--type", "crowdstrike_s2s", "--interval", "5m",
		"--config", `{"api_url":"https://api.example.test","customer_id":"customer","client_id":"id","client_secret":"secret"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runPostureCLI(t, server.URL,
		"devices", "posture", "integrations", "update", postureTestID, "--interval", "15m",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(requests, ",") != "POST /accounts/"+postureTestAccountID+"/devices/posture/integration,PATCH /accounts/"+postureTestAccountID+"/devices/posture/integration/"+postureTestID {
		t.Fatalf("requests = %v", requests)
	}
	postureJSONEqual(t, bodies[0], `{"name":"CrowdStrike","type":"crowdstrike_s2s","interval":"5m","config":{"api_url":"https://api.example.test","customer_id":"customer","client_id":"id","client_secret":"secret"}}`)
	postureJSONEqual(t, bodies[1], `{"interval":"15m"}`)
}

func TestPostureRulesListAndGetRequestOutput(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/accounts/" + postureTestAccountID + "/devices/posture":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + postureTestID + `","name":"Signed binary","type":"file","description":"Checks a binary","schedule":"5m"}]}`))
		case "/accounts/" + postureTestAccountID + "/devices/posture/" + postureTestID:
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + postureTestID + `","name":"Signed binary","type":"file"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	stdout, _, err := runPostureCLI(t, server.URL, "devices", "posture", "rules", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "Signed binary") || !strings.Contains(stdout, "Checks a binary") {
		t.Fatalf("table output = %q", stdout)
	}

	stdout, _, err = runPostureCLI(t, server.URL, "--output", "json", "devices", "posture", "rules", "list")
	if err != nil {
		t.Fatal(err)
	}
	postureJSONEqual(t, []byte(stdout), `[{"id":"`+postureTestID+`","name":"Signed binary","type":"file","description":"Checks a binary","schedule":"5m"}]`)

	stdout, _, err = runPostureCLI(t, server.URL, "devices", "posture", "rules", "get", postureTestID)
	if err != nil {
		t.Fatal(err)
	}
	postureJSONEqual(t, []byte(stdout), `{"id":"`+postureTestID+`","name":"Signed binary","type":"file"}`)

	want := []string{
		"GET /accounts/" + postureTestAccountID + "/devices/posture",
		"GET /accounts/" + postureTestAccountID + "/devices/posture",
		"GET /accounts/" + postureTestAccountID + "/devices/posture/" + postureTestID,
	}
	if strings.Join(requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestPostureIntegrationsListAndGetRequestOutput(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/accounts/" + postureTestAccountID + "/devices/posture/integration":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + postureTestID + `","name":"CrowdStrike","type":"crowdstrike_s2s","interval":"5m"}]}`))
		case "/accounts/" + postureTestAccountID + "/devices/posture/integration/" + postureTestID:
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + postureTestID + `","name":"CrowdStrike","type":"crowdstrike_s2s","interval":"5m"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	stdout, _, err := runPostureCLI(t, server.URL, "devices", "posture", "integrations", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "ID") || !strings.Contains(stdout, "CrowdStrike") || !strings.Contains(stdout, "INTERVAL") {
		t.Fatalf("table output = %q", stdout)
	}

	stdout, _, err = runPostureCLI(t, server.URL, "--output", "json", "devices", "posture", "integrations", "list")
	if err != nil {
		t.Fatal(err)
	}
	postureJSONEqual(t, []byte(stdout), `[{"id":"`+postureTestID+`","name":"CrowdStrike","type":"crowdstrike_s2s","interval":"5m"}]`)

	stdout, _, err = runPostureCLI(t, server.URL, "devices", "posture", "integrations", "get", postureTestID)
	if err != nil {
		t.Fatal(err)
	}
	postureJSONEqual(t, []byte(stdout), `{"id":"`+postureTestID+`","name":"CrowdStrike","type":"crowdstrike_s2s","interval":"5m"}`)

	want := []string{
		"GET /accounts/" + postureTestAccountID + "/devices/posture/integration",
		"GET /accounts/" + postureTestAccountID + "/devices/posture/integration",
		"GET /accounts/" + postureTestAccountID + "/devices/posture/integration/" + postureTestID,
	}
	if strings.Join(requests, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestPostureDeletesForceAndDryRun(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		postureJSONEqual(t, body, `{}`)
		requests = append(requests, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer server.Close()

	_, _, err := runPostureCLI(t, server.URL, "devices", "posture", "rules", "delete", postureTestID, "--force")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runPostureCLI(t, server.URL, "devices", "posture", "integrations", "delete", postureTestID, "--force")
	if err != nil {
		t.Fatal(err)
	}

	ruleDryRun, _, err := runPostureCLI(t, server.URL, "--dry-run", "devices", "posture", "rules", "delete", postureTestID)
	if err != nil {
		t.Fatalf("rule dry-run should not prompt: %v", err)
	}
	integrationDryRun, _, err := runPostureCLI(t, server.URL, "--dry-run", "devices", "posture", "integrations", "delete", postureTestID)
	if err != nil {
		t.Fatalf("integration dry-run should not prompt: %v", err)
	}

	want := []string{
		"DELETE /accounts/" + postureTestAccountID + "/devices/posture/" + postureTestID,
		"DELETE /accounts/" + postureTestAccountID + "/devices/posture/integration/" + postureTestID,
	}
	if strings.Join(requests, ",") != strings.Join(want, ",") {
		t.Fatalf("live requests = %v, want %v", requests, want)
	}
	for label, output := range map[string]string{"rule": ruleDryRun, "integration": integrationDryRun} {
		var dump struct {
			Method string          `json:"method"`
			Body   json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal([]byte(output), &dump); err != nil {
			t.Fatalf("%s dry-run JSON: %v", label, err)
		}
		if dump.Method != http.MethodDelete {
			t.Fatalf("%s dry-run method = %q", label, dump.Method)
		}
		postureJSONEqual(t, dump.Body, `{}`)
	}
}

func TestPostureRuleScheduleAndJSONShapes(t *testing.T) {
	if err := postureValidateSchedule("59s"); err == nil {
		t.Fatal("expected 59s to violate the documented 1m minimum")
	}
	if err := postureValidateSchedule("1m"); err != nil {
		t.Fatal(err)
	}
	if err := postureValidateInterval("1s"); err == nil {
		t.Fatal("expected seconds to violate the documented m/h interval format")
	}

	cmd := &cobra.Command{}
	cmd.SetIn(strings.NewReader(`[]`))
	if _, err := postureJSONObject(cmd, "input", "@-"); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("input array error = %v", err)
	}
}
