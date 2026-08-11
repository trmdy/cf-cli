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

const queuesTestAccountID = "account-test"

func runQueuesCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token", "--account-id", queuesTestAccountID}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func queuesAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("invalid got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("invalid wanted JSON %q: %v", want, err)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("body = %s, want %s", gotJSON, wantJSON)
	}
}

func TestBuildQueueUpdateBody(t *testing.T) {
	cmd := newQueuesUpdateCmd(&globalOpts{})
	if _, err := buildQueueUpdateBody(cmd, "", 0, 0, false); err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("expected no update error, got %v", err)
	}
	if err := cmd.Flags().Set("name", "events-v2"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("delivery-paused", "true"); err != nil {
		t.Fatal(err)
	}
	body, err := buildQueueUpdateBody(cmd, "events-v2", 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	queuesAssertJSONEqual(t, body, `{"queue_name":"events-v2","settings":{"delivery_paused":true}}`)
}

func TestBuildQueueConsumerBody(t *testing.T) {
	worker := newQueuesConsumerAddCmd(&globalOpts{})
	if _, err := buildQueueConsumerBody(worker, "worker", "", "", 0, 0, 0, 0, 0, 0); err == nil || !strings.Contains(err.Error(), "--script") {
		t.Fatalf("expected missing script error, got %v", err)
	}
	if err := worker.Flags().Set("batch-size", "10"); err != nil {
		t.Fatal(err)
	}
	body, err := buildQueueConsumerBody(worker, "worker", "process-events", "", 10, 0, 0, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	queuesAssertJSONEqual(t, body, `{"type":"worker","script_name":"process-events","settings":{"batch_size":10}}`)

	httpPull := newQueuesConsumerAddCmd(&globalOpts{})
	if err := httpPull.Flags().Set("visibility-timeout-ms", "30000"); err != nil {
		t.Fatal(err)
	}
	body, err = buildQueueConsumerBody(httpPull, "http-pull", "", "", 0, 0, 0, 0, 0, 30000)
	if err != nil {
		t.Fatal(err)
	}
	queuesAssertJSONEqual(t, body, `{"type":"http_pull","settings":{"visibility_timeout_ms":30000}}`)

	if _, err := buildQueueConsumerBody(httpPull, "worker", "worker", "", 0, 0, 0, 0, 0, 30000); err == nil || !strings.Contains(err.Error(), "http-pull") {
		t.Fatalf("expected consumer type error, got %v", err)
	}
}

func TestBuildQueueMessageAndAckBodies(t *testing.T) {
	body, err := buildQueueMessageBody(`{"event":"created"}`, true, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	queuesAssertJSONEqual(t, body, `{"body":{"event":"created"},"content_type":"json","delay_seconds":30}`)
	if _, err := buildQueueMessageBody("not-json", true, 0, false); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("expected JSON validation error, got %v", err)
	}
	if _, err := buildQueueMessageBody("null", true, 0, false); err == nil || !strings.Contains(err.Error(), "JSON object") {
		t.Fatalf("expected JSON object error, got %v", err)
	}
	body, err = buildQueueAckBody([]string{"lease-1", "lease-2"})
	if err != nil {
		t.Fatal(err)
	}
	queuesAssertJSONEqual(t, body, `{"acks":[{"lease_id":"lease-1"},{"lease_id":"lease-2"}]}`)
}

func TestQueuesCommandsBuildRequests(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		method   string
		path     string
		wantBody string
		result   string
	}{
		{"list", []string{"queues", "list"}, "GET", "/accounts/account-test/queues", "", `[{"queue_id":"q1","queue_name":"events","consumers_total_count":2,"settings":{"delivery_paused":false}}]`},
		{"get", []string{"queues", "get", "events"}, "GET", "/accounts/account-test/queues/events", "", `{"queue_id":"q1","queue_name":"events"}`},
		{"create", []string{"queues", "create", "events"}, "POST", "/accounts/account-test/queues", `{"queue_name":"events"}`, `{"queue_id":"q1"}`},
		{"update", []string{"queues", "update", "events", "--delivery-delay", "30"}, "PATCH", "/accounts/account-test/queues/events", `{"settings":{"delivery_delay":30}}`, `{"queue_id":"q1"}`},
		{"delete", []string{"queues", "delete", "events", "--force"}, "DELETE", "/accounts/account-test/queues/events", "", `{"queue_id":"q1"}`},
		{"consumer add", []string{"queues", "consumer", "add", "events", "--script", "process-events", "--max-retries", "3"}, "POST", "/accounts/account-test/queues/events/consumers", `{"type":"worker","script_name":"process-events","settings":{"max_retries":3}}`, `{"consumer_id":"c1"}`},
		{"consumer remove", []string{"queues", "consumer", "remove", "events", "c1", "--force"}, "DELETE", "/accounts/account-test/queues/events/consumers/c1", "", `{"consumer_id":"c1"}`},
		{"message send", []string{"queues", "message", "send", "events", "hello", "--delay-seconds", "5"}, "POST", "/accounts/account-test/queues/events/messages", `{"body":"hello","content_type":"text","delay_seconds":5}`, `{"id":"m1"}`},
		{"message pull", []string{"queues", "message", "pull", "events", "--batch-size", "2"}, "POST", "/accounts/account-test/queues/events/messages/pull", `{"batch_size":2}`, `[]`},
		{"message ack", []string{"queues", "message", "ack", "events", "lease-1", "lease-2"}, "POST", "/accounts/account-test/queues/events/messages/ack", `{"acks":[{"lease_id":"lease-1"},{"lease_id":"lease-2"}]}`, `{"acknowledged":2}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":` + tc.result + `}`))
			}))
			defer srv.Close()

			stdout, _, err := runQueuesCLI(t, srv.URL, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.method || gotPath != tc.path {
				t.Errorf("request = %s %s, want %s %s", gotMethod, gotPath, tc.method, tc.path)
			}
			if tc.wantBody == "" {
				if len(gotBody) != 0 {
					t.Errorf("body = %s, want none", gotBody)
				}
			} else {
				queuesAssertJSONEqual(t, gotBody, tc.wantBody)
			}
			if tc.name == "list" {
				if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "events") {
					t.Errorf("list output = %s", stdout)
				}
			} else if stdout == "" {
				t.Error("expected response output")
			}
		})
	}
}

func TestQueuesDryRunAndMissingAccount(t *testing.T) {
	stdout, _, err := runQueuesCLI(t, "http://example.invalid", "queues", "message", "send", "events", "hello", "--dry-run")
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/accounts/account-test/queues/events/messages") {
		t.Errorf("dump = %#v", dump)
	}
	queuesAssertJSONEqual(t, dump.Body, `{"body":"hello","content_type":"text"}`)

	root := NewRootCmd()
	root.SetArgs([]string{"--dry-run", "queues", "list"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "missing account ID") {
		t.Fatalf("expected account error, got %v", err)
	}
}
