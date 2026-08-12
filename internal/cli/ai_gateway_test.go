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

const aiGatewayTestAccountID = "0123456789abcdef0123456789abcdef"

func runAIGatewayCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CF_PROFILE", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token", "--account-id", aiGatewayTestAccountID}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func aiGatewayAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func TestAIGatewayCreateDryRun(t *testing.T) {
	stdout, _, err := runAIGatewayCLI(t, "http://example.invalid", "ai-gateway", "create", "production-gateway",
		"--rate-limit-interval", "60", "--rate-limit-limit", "100", "--collect-logs=false", "--cache-ttl", "300", "--cache-invalidate-on-update", "--retry-backoff", "linear", "--retry-delay", "100", "--retry-max-attempts", "3", "--dry-run")
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
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/accounts/"+aiGatewayTestAccountID+"/ai-gateway/gateways") {
		t.Fatalf("dump = %#v", dump)
	}
	aiGatewayAssertJSONEqual(t, dump.Body, `{"id":"production-gateway","rate_limiting_interval":60,"rate_limiting_limit":100,"collect_logs":false,"cache_ttl":300,"cache_invalidate_on_update":true,"retry_backoff":"linear","retry_delay":100,"retry_max_attempts":3}`)
}

func TestAIGatewayValidationBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	_, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "create", "INVALID", "--rate-limit-interval", "0", "--rate-limit-limit", "0", "--collect-logs", "--cache-ttl", "0", "--cache-invalidate-on-update")
	if err == nil || !strings.Contains(err.Error(), "gateway ID") {
		t.Fatalf("err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("validation made %d requests", calls)
	}
}

func TestAIGatewayUpdateReadMergeWrite(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"production-gateway","created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-02T00:00:00Z","is_default":true,"rate_limiting_interval":60,"rate_limiting_limit":100,"collect_logs":true,"cache_ttl":300,"cache_invalidate_on_update":false,"unknown_writable":{"keep":true}}}`)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			aiGatewayAssertJSONEqual(t, body, `{"rate_limiting_interval":60,"rate_limiting_limit":100,"collect_logs":false,"cache_ttl":300,"cache_invalidate_on_update":false,"unknown_writable":{"keep":true}}`)
			_, _ = io.WriteString(w, `{"success":true,"result":{"ok":true}}`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	_, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "update", "production-gateway", "--collect-logs=false")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET,PUT" {
		t.Fatalf("methods = %v", methods)
	}
}

func TestAIGatewayUpdateDryRunReadsOnly(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"production-gateway","rate_limiting_interval":60,"rate_limiting_limit":100,"collect_logs":true,"cache_ttl":300,"cache_invalidate_on_update":false}}`)
	}))
	defer server.Close()
	stdout, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "update", "production-gateway", "--cache-ttl", "600", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET" {
		t.Fatalf("dry run methods = %v", methods)
	}
	if !strings.Contains(stdout, `"method": "PUT"`) || !strings.Contains(stdout, `"cache_ttl": 600`) {
		t.Fatalf("dry run = %s", stdout)
	}
}

func TestAIGatewayLogsListPaginatesAndEncodesFilter(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		if got := r.URL.Query().Get("filters"); got != `{"key":"provider","operator":"eq","value":["openai"]}` {
			t.Fatalf("filters = %q", got)
		}
		if page == "1" {
			logs := make([]map[string]any, aiGatewayLogsPerPage)
			for i := range logs {
				logs[i] = map[string]any{"id": "log-" + strconv.Itoa(i+1), "provider": "openai", "model": "gpt", "success": true, "cached": false}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": logs, "result_info": map[string]any{"page": 1, "per_page": aiGatewayLogsPerPage, "total_count": 51}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{{"id": "log-51", "provider": "openai", "model": "gpt", "success": true, "cached": false}}, "result_info": map[string]any{"page": 2, "per_page": aiGatewayLogsPerPage, "total_count": 51}})
	}))
	defer server.Close()
	stdout, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "logs", "list", "production-gateway", "--filter", `{"key":"provider","operator":"eq","value":["openai"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v", pages)
	}
	if !strings.Contains(stdout, "log-51") {
		t.Fatalf("table missing second page: %s", stdout)
	}
}

func TestAIGatewayLogsListHonorsTotalCountBeforeShortPage(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":     true,
			"result":      []map[string]any{{"id": "log-" + strconv.Itoa(page), "provider": "openai", "model": "gpt", "success": true, "cached": false}},
			"result_info": map[string]any{"page": page, "per_page": aiGatewayLogsPerPage, "total_count": 2},
		})
	}))
	defer server.Close()
	stdout, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "logs", "list", "production-gateway")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v", pages)
	}
	if !strings.Contains(stdout, "log-2") {
		t.Fatalf("table missing second page: %s", stdout)
	}
}

func TestAIGatewayLogsListMalformedFirstResultRendersRaw(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"result":{"unexpected":true}}`)
	}))
	defer server.Close()
	stdout, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "logs", "list", "production-gateway")
	if err != nil {
		t.Fatal(err)
	}
	aiGatewayAssertJSONEqual(t, []byte(stdout), `{"unexpected":true}`)
}

func TestAIGatewayLogsListRejectsMalformedItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"ok"},42]}`)
	}))
	defer server.Close()
	_, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "logs", "list", "production-gateway")
	if err == nil || !strings.Contains(err.Error(), "item 2") {
		t.Fatalf("err = %v", err)
	}
}

func TestAIGatewayAllLeavesRealCommandTreeRequests(t *testing.T) {
	type requestCase struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		response   string
		wantBody   string
	}
	base := "/accounts/" + aiGatewayTestAccountID + "/ai-gateway/gateways"
	cases := []requestCase{
		{"list", []string{"ai-gateway", "list"}, http.MethodGet, base, `{"success":true,"result":[]}`, ""},
		{"get", []string{"ai-gateway", "get", "production-gateway"}, http.MethodGet, base + "/production-gateway", `{"success":true,"result":{"id":"production-gateway"}}`, ""},
		{"create", []string{"ai-gateway", "create", "production-gateway", "--rate-limit-interval", "0", "--rate-limit-limit", "0", "--collect-logs", "--cache-ttl", "0", "--cache-invalidate-on-update"}, http.MethodPost, base, `{"success":true,"result":{}}`, `{"id":"production-gateway","rate_limiting_interval":0,"rate_limiting_limit":0,"collect_logs":true,"cache_ttl":0,"cache_invalidate_on_update":true}`},
		{"update", []string{"ai-gateway", "update", "production-gateway", "--cache-ttl", "600"}, http.MethodPut, base + "/production-gateway", `{"success":true,"result":{}}`, `{"rate_limiting_interval":0,"rate_limiting_limit":0,"collect_logs":true,"cache_ttl":600,"cache_invalidate_on_update":false}`},
		{"delete force", []string{"ai-gateway", "delete", "production-gateway", "--force"}, http.MethodDelete, base + "/production-gateway", `{"success":true,"result":{}}`, ""},
		{"logs list", []string{"ai-gateway", "logs", "list", "production-gateway"}, http.MethodGet, base + "/production-gateway/logs", `{"success":true,"result":[]}`, ""},
		{"logs get", []string{"ai-gateway", "logs", "get", "production-gateway", "log_123"}, http.MethodGet, base + "/production-gateway/logs/log_123", `{"success":true,"result":{"id":"log_123"}}`, ""},
		{"dataset list", []string{"ai-gateway", "dataset", "list", "production-gateway"}, http.MethodGet, base + "/production-gateway/datasets", `{"success":true,"result":[]}`, ""},
		{"dataset get", []string{"ai-gateway", "dataset", "get", "production-gateway", "ds_123"}, http.MethodGet, base + "/production-gateway/datasets/ds_123", `{"success":true,"result":{"id":"ds_123"}}`, ""},
		{"dataset create", []string{"ai-gateway", "dataset", "create", "production-gateway", "openai", "--enabled"}, http.MethodPost, base + "/production-gateway/datasets", `{"success":true,"result":{}}`, `{"name":"openai","enable":true,"filters":[]}`},
		{"dataset update", []string{"ai-gateway", "dataset", "update", "production-gateway", "ds_123", "--enabled=false"}, http.MethodPut, base + "/production-gateway/datasets/ds_123", `{"success":true,"result":{}}`, `{"name":"openai","enable":false,"filters":[]}`},
		{"dataset delete force", []string{"ai-gateway", "dataset", "delete", "production-gateway", "ds_123", "--force"}, http.MethodDelete, base + "/production-gateway/datasets/ds_123", `{"success":true,"result":{}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if tc.name == "update" {
					if requests == 1 {
						if r.URL.Path != tc.wantPath || r.Method != http.MethodGet {
							t.Fatalf("read request = %s %s", r.Method, r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"success":true,"result":{"id":"production-gateway","rate_limiting_interval":0,"rate_limiting_limit":0,"collect_logs":true,"cache_ttl":0,"cache_invalidate_on_update":false}}`)
						return
					}
					if r.URL.Path != tc.wantPath || r.Method != tc.wantMethod {
						t.Fatalf("write request = %s %s, want %s %s", r.Method, r.URL.Path, tc.wantMethod, tc.wantPath)
					}
					body, _ := io.ReadAll(r.Body)
					aiGatewayAssertJSONEqual(t, body, tc.wantBody)
					_, _ = io.WriteString(w, tc.response)
					return
				}
				if tc.name == "dataset update" {
					if requests == 1 {
						if r.URL.Path != tc.wantPath || r.Method != http.MethodGet {
							t.Fatalf("read request = %s %s", r.Method, r.URL.Path)
						}
						_, _ = io.WriteString(w, `{"success":true,"result":{"id":"ds_123","gateway_id":"production-gateway","name":"openai","enable":true,"filters":[]}}`)
						return
					}
					if r.URL.Path != tc.wantPath || r.Method != tc.wantMethod {
						t.Fatalf("write request = %s %s, want %s %s", r.Method, r.URL.Path, tc.wantMethod, tc.wantPath)
					}
					body, _ := io.ReadAll(r.Body)
					aiGatewayAssertJSONEqual(t, body, tc.wantBody)
					_, _ = io.WriteString(w, tc.response)
					return
				}
				if r.URL.Path != tc.wantPath || r.Method != tc.wantMethod {
					t.Fatalf("request = %s %s, want %s %s", r.Method, r.URL.Path, tc.wantMethod, tc.wantPath)
				}
				if tc.wantBody != "" {
					body, _ := io.ReadAll(r.Body)
					aiGatewayAssertJSONEqual(t, body, tc.wantBody)
				}
				_, _ = io.WriteString(w, tc.response)
			}))
			defer server.Close()
			_, _, err := runAIGatewayCLI(t, server.URL, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			wantRequests := 1
			if tc.name == "update" || tc.name == "dataset update" {
				wantRequests = 2
			}
			if requests != wantRequests {
				t.Fatalf("requests = %d, want %d", requests, wantRequests)
			}
		})
	}
}

func TestAIGatewayDeleteDryRunsDoNotSendRequests(t *testing.T) {
	for _, args := range [][]string{
		{"ai-gateway", "delete", "production-gateway", "--dry-run"},
		{"ai-gateway", "dataset", "delete", "production-gateway", "ds_123", "--dry-run"},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
		stdout, _, err := runAIGatewayCLI(t, server.URL, args...)
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if calls != 0 || !strings.Contains(stdout, `"method": "DELETE"`) {
			t.Fatalf("calls = %d, stdout = %s", calls, stdout)
		}
	}
}

func TestAIGatewayDatasetUpdateReadMergeWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"ds_123","gateway_id":"production-gateway","created_at":"2026-01-01T00:00:00Z","modified_at":"2026-01-02T00:00:00Z","name":"old","enable":true,"filters":[{"key":"provider","operator":"eq","value":["openai"]}],"unknown_writable":"keep"}}`)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			aiGatewayAssertJSONEqual(t, body, `{"name":"old","enable":false,"filters":[{"key":"provider","operator":"eq","value":["openai"]}],"unknown_writable":"keep"}`)
			_, _ = io.WriteString(w, `{"success":true,"result":{"ok":true}}`)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	_, _, err := runAIGatewayCLI(t, server.URL, "ai-gateway", "dataset", "update", "production-gateway", "ds_123", "--enabled=false")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAIGatewayDatasetCreateAndFilterValidation(t *testing.T) {
	stdout, _, err := runAIGatewayCLI(t, "http://example.invalid", "ai-gateway", "dataset", "create", "production-gateway", "openai", "--enabled", "--filter", `{"key":"provider","operator":"eq","value":["openai"]}`, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"name": "openai"`) || !strings.Contains(stdout, `"enable": true`) {
		t.Fatalf("dry run = %s", stdout)
	}
	_, _, err = runAIGatewayCLI(t, "http://example.invalid", "ai-gateway", "dataset", "create", "production-gateway", "bad", "--enabled", "--filter", `{"key":"id","operator":"eq","value":["x"]}`, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "filter.key") {
		t.Fatalf("err = %v", err)
	}
}
