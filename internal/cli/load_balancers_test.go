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

const (
	lbTestZoneID    = "0123456789abcdef0123456789abcdef"
	lbTestAccountID = "fedcba9876543210fedcba9876543210"
)

// runLBCLI drives the real command tree, so every assertion below covers flag
// parsing, validation and request building exactly as a user would hit them.
func runLBCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token"}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// lbIsolateConfig points config resolution at an empty directory so the
// developer's real profile can't leak account/zone defaults into a test.
func lbIsolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_ZONE_ID", "")
	t.Setenv("CF_ZONE_ID", "")
}

type lbDump struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body"`
}

func lbDryRun(t *testing.T, args ...string) lbDump {
	t.Helper()
	stdout, _, err := runLBCLI(t, "http://example.invalid", append(args, "--dry-run")...)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	var d lbDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

func lbAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func lbAssertPath(t *testing.T, url, wantSuffix string) {
	t.Helper()
	if !strings.HasSuffix(url, wantSuffix) {
		t.Errorf("url = %s, want suffix %s", url, wantSuffix)
	}
}

// lbCapture records the single request a command makes and replies with the
// given envelope body.
type lbCapture struct {
	method string
	path   string
	query  string
	body   []byte
}

func lbServer(t *testing.T, result string) (*httptest.Server, *lbCapture) {
	t.Helper()
	got := &lbCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":` + result + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

// --------------------------------------------------------------------------
// Load balancers
// --------------------------------------------------------------------------

func TestLBListRequestAndTable(t *testing.T) {
	srv, got := lbServer(t, `[{"id":"lb1","name":"www.example.com","enabled":true,"proxied":false,"steering_policy":"geo","default_pools":["p1","p2"],"fallback_pool":"p2"}]`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "list", "--zone", lbTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" {
		t.Errorf("method = %s", got.method)
	}
	if got.path != "/zones/"+lbTestZoneID+"/load_balancers" {
		t.Errorf("path = %s", got.path)
	}
	for _, want := range []string{"ID", "NAME", "STEERING", "FALLBACK POOL", "lb1", "www.example.com", "geo", "p1,p2"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestLBListJSONOutputBypassesTable(t *testing.T) {
	srv, _ := lbServer(t, `[{"id":"lb1","name":"www.example.com"}]`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "list", "--zone", lbTestZoneID, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "FALLBACK POOL") {
		t.Errorf("expected JSON, got table:\n%s", stdout)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(stdout), &items); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, stdout)
	}
}

func TestLBListDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "list", "--zone", lbTestZoneID)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/zones/"+lbTestZoneID+"/load_balancers")
}

func TestLBGetDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "get", "lb1", "--zone", lbTestZoneID)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/zones/"+lbTestZoneID+"/load_balancers/lb1")
}

func TestLBCreateBuildsRequest(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "create", "www.example.com",
		"--zone", lbTestZoneID,
		"--default-pool", "p1", "--default-pool", "p2",
		"--fallback-pool", "p2",
		"--description", "prod www",
		"--ttl", "60",
		"--proxied",
		"--enabled=false",
		"--steering-policy", "dynamic-latency",
		"--session-affinity", "ip-cookie",
	)
	if d.Method != "POST" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/zones/"+lbTestZoneID+"/load_balancers")
	lbAssertJSONEqual(t, d.Body, `{
		"name":"www.example.com",
		"default_pools":["p1","p2"],
		"fallback_pool":"p2",
		"description":"prod www",
		"ttl":60,
		"proxied":true,
		"enabled":false,
		"steering_policy":"dynamic_latency",
		"session_affinity":"ip_cookie"
	}`)
}

func TestLBCreateOmitsUnsetOptionalFields(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "create", "www.example.com",
		"--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1")
	lbAssertJSONEqual(t, d.Body, `{"name":"www.example.com","default_pools":["p1"],"fallback_pool":"p1"}`)
}

func TestLBCreateHTTPRequest(t *testing.T) {
	srv, got := lbServer(t, `{"id":"lb1","name":"www.example.com"}`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "create", "www.example.com",
		"--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/zones/"+lbTestZoneID+"/load_balancers" {
		t.Errorf("%s %s", got.method, got.path)
	}
	lbAssertJSONEqual(t, got.body, `{"name":"www.example.com","default_pools":["p1"],"fallback_pool":"p1"}`)
	if !strings.Contains(stdout, "lb1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestLBCreateRequiresPools(t *testing.T) {
	lbIsolateConfig(t)
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no default pool",
			args: []string{"load-balancers", "create", "www.example.com", "--zone", lbTestZoneID, "--fallback-pool", "p1"},
			want: "default-pool",
		},
		{
			name: "no fallback pool",
			args: []string{"load-balancers", "create", "www.example.com", "--zone", lbTestZoneID, "--default-pool", "p1"},
			want: "fallback-pool",
		},
		{
			name: "blank default pool",
			args: []string{"load-balancers", "create", "www.example.com", "--zone", lbTestZoneID, "--default-pool", "  ", "--fallback-pool", "p1"},
			want: "--default-pool value at position 1 is empty",
		},
		{
			name: "blank fallback pool",
			args: []string{"load-balancers", "create", "www.example.com", "--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", " "},
			want: "--fallback-pool is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", append(tc.args, "--dry-run")...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestLBCreateRejectsUnknownSteeringPolicy(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "create", "www.example.com",
		"--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1",
		"--steering-policy", "sideways", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--steering-policy must be one of") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "dynamic_latency") {
		t.Errorf("error should list the valid values: %v", err)
	}
}

func TestLBCreateRejectsUnknownSessionAffinity(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "create", "www.example.com",
		"--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1",
		"--session-affinity", "sticky", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--session-affinity must be one of") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBCreateResolvesZoneName(t *testing.T) {
	var createPath string
	var sawLookup bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			sawLookup = true
			if name := r.URL.Query().Get("name"); name != "example.com" {
				t.Errorf("lookup name = %q", name)
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + lbTestZoneID + `","name":"example.com"}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/load_balancers"):
			createPath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"lb1"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, _, err := runLBCLI(t, srv.URL, "load-balancers", "create", "www.example.com",
		"--zone", "example.com", "--default-pool", "p1", "--fallback-pool", "p1"); err != nil {
		t.Fatal(err)
	}
	if !sawLookup {
		t.Error("expected a zone name lookup")
	}
	if createPath != "/zones/"+lbTestZoneID+"/load_balancers" {
		t.Errorf("create path = %s", createPath)
	}
}

func TestLBCreateRequiresZone(t *testing.T) {
	lbIsolateConfig(t)
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "create", "www.example.com",
		"--default-pool", "p1", "--fallback-pool", "p1", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBUpdateSendsOnlyChangedFields(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "update", "lb1", "--zone", lbTestZoneID,
		"--enabled=false", "--steering-policy", "geo")
	if d.Method != "PATCH" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/zones/"+lbTestZoneID+"/load_balancers/lb1")
	lbAssertJSONEqual(t, d.Body, `{"enabled":false,"steering_policy":"geo"}`)
}

func TestLBUpdateReplacesDefaultPools(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "update", "lb1", "--zone", lbTestZoneID,
		"--default-pool", "eu", "--default-pool", "us")
	lbAssertJSONEqual(t, d.Body, `{"default_pools":["eu","us"]}`)
}

func TestLBUpdateRequiresAField(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "update", "lb1",
		"--zone", lbTestZoneID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "--steering-policy") {
		t.Errorf("error should list the updatable flags: %v", err)
	}
}

func TestLBDeleteDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "delete", "lb1", "--zone", lbTestZoneID)
	if d.Method != "DELETE" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/zones/"+lbTestZoneID+"/load_balancers/lb1")
}

func TestLBDeleteHTTPRequestWithForce(t *testing.T) {
	srv, got := lbServer(t, `{"id":"lb1"}`)

	if _, _, err := runLBCLI(t, srv.URL, "load-balancers", "delete", "lb1",
		"--zone", lbTestZoneID, "--force"); err != nil {
		t.Fatal(err)
	}
	if got.method != "DELETE" || got.path != "/zones/"+lbTestZoneID+"/load_balancers/lb1" {
		t.Errorf("%s %s", got.method, got.path)
	}
}

// --------------------------------------------------------------------------
// Pools
// --------------------------------------------------------------------------

func TestLBPoolListRequestAndTable(t *testing.T) {
	srv, got := lbServer(t, `[{"id":"pool1","name":"eu-west","enabled":true,"healthy":false,"monitor":"mon1","origins":[{"name":"web1","address":"203.0.113.1"},{"name":"web2","address":"203.0.113.2"}]}]`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "list",
		"--account-id", lbTestAccountID, "--monitor", "mon1")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" {
		t.Errorf("method = %s", got.method)
	}
	if got.path != "/accounts/"+lbTestAccountID+"/load_balancers/pools" {
		t.Errorf("path = %s", got.path)
	}
	if !strings.Contains(got.query, "monitor=mon1") {
		t.Errorf("query = %s", got.query)
	}
	for _, want := range []string{"ORIGINS", "HEALTHY", "pool1", "eu-west", "false", "2", "mon1"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestLBPoolListOmitsMonitorFilterWhenUnset(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "list", "--account-id", lbTestAccountID)
	if strings.Contains(d.URL, "monitor=") {
		t.Errorf("url = %s", d.URL)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/pools")
}

func TestLBPoolGetDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "get", "pool1", "--account-id", lbTestAccountID)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/pools/pool1")
}

func TestLBPoolCreateBuildsRequest(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "create", "eu-west",
		"--account-id", lbTestAccountID,
		"--origin", "203.0.113.1",
		"--origin", "name=web2,address=203.0.113.2,weight=0.3,enabled=false",
		"--monitor", "mon1",
		"--description", "eu origins",
		"--notification-email", "ops@example.com",
		"--check-region", "WEU", "--check-region", "ENAM",
		"--minimum-origins", "2",
		"--enabled=false",
	)
	if d.Method != "POST" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/pools")
	lbAssertJSONEqual(t, d.Body, `{
		"name":"eu-west",
		"origins":[
			{"name":"203.0.113.1","address":"203.0.113.1"},
			{"name":"web2","address":"203.0.113.2","weight":0.3,"enabled":false}
		],
		"monitor":"mon1",
		"description":"eu origins",
		"notification_email":"ops@example.com",
		"check_regions":["WEU","ENAM"],
		"minimum_origins":2,
		"enabled":false
	}`)
}

func TestLBPoolCreateOmitsUnsetOptionalFields(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "create", "eu-west",
		"--account-id", lbTestAccountID, "--origin", "203.0.113.1")
	lbAssertJSONEqual(t, d.Body, `{"name":"eu-west","origins":[{"name":"203.0.113.1","address":"203.0.113.1"}]}`)
}

func TestLBPoolCreateHTTPRequest(t *testing.T) {
	srv, got := lbServer(t, `{"id":"pool1","name":"eu-west"}`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "create", "eu-west",
		"--account-id", lbTestAccountID, "--origin", "name=web1,address=203.0.113.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/accounts/"+lbTestAccountID+"/load_balancers/pools" {
		t.Errorf("%s %s", got.method, got.path)
	}
	lbAssertJSONEqual(t, got.body, `{"name":"eu-west","origins":[{"name":"web1","address":"203.0.113.1"}]}`)
	if !strings.Contains(stdout, "pool1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestLBPoolCreateRequiresOrigin(t *testing.T) {
	lbIsolateConfig(t)
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "create", "eu-west",
		"--account-id", lbTestAccountID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBPoolCreateRejectsBadOrigins(t *testing.T) {
	cases := []struct {
		origin string
		want   string
	}{
		{"name=web1,weight=0.5", "address is required"},
		{"name=web1,address=203.0.113.1,weight=7", "must be a number between 0 and 1"},
		{"name=web1,address=203.0.113.1,enabled=maybe", "must be true or false"},
		{"name=web1,address=203.0.113.1,colour=red", `unknown key "colour"`},
		{"name=web1,address", "is not key=value"},
	}
	for _, tc := range cases {
		t.Run(tc.origin, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "create", "eu-west",
				"--account-id", lbTestAccountID, "--origin", tc.origin, "--dry-run")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestLBPoolCommandsRequireAccount(t *testing.T) {
	lbIsolateConfig(t)
	cases := [][]string{
		{"load-balancers", "pool", "list"},
		{"load-balancers", "pool", "get", "pool1"},
		{"load-balancers", "pool", "create", "eu-west", "--origin", "203.0.113.1"},
		{"load-balancers", "pool", "update", "pool1", "--enabled=false"},
		{"load-balancers", "pool", "delete", "pool1", "--force"},
		{"load-balancers", "pool", "health", "pool1"},
		{"load-balancers", "monitor", "list"},
		{"load-balancers", "monitor", "create", "--type", "http"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args[1:3], "-"), func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", append(args, "--dry-run")...)
			if err == nil || !strings.Contains(err.Error(), "no account specified") {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(err.Error(), "--account-id") {
				t.Errorf("error should say how to provide the account: %v", err)
			}
		})
	}
}

func TestLBPoolUpdateSendsOnlyChangedFields(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "update", "pool1",
		"--account-id", lbTestAccountID, "--enabled=false", "--monitor", "mon2")
	if d.Method != "PATCH" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/pools/pool1")
	lbAssertJSONEqual(t, d.Body, `{"enabled":false,"monitor":"mon2"}`)
}

func TestLBPoolUpdateReplacesOrigins(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "update", "pool1",
		"--account-id", lbTestAccountID,
		"--origin", "name=web1,address=203.0.113.1",
		"--origin", "203.0.113.3")
	lbAssertJSONEqual(t, d.Body, `{"origins":[
		{"name":"web1","address":"203.0.113.1"},
		{"name":"203.0.113.3","address":"203.0.113.3"}
	]}`)
}

func TestLBPoolUpdateRequiresAField(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "update", "pool1",
		"--account-id", lbTestAccountID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "--origin") {
		t.Errorf("error should list the updatable flags: %v", err)
	}
}

func TestLBPoolDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "delete", "pool1",
		"--account-id", lbTestAccountID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBPoolDeleteDryRunSkipsConfirmation(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "delete", "pool1", "--account-id", lbTestAccountID)
	if d.Method != "DELETE" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/pools/pool1")
}

func TestLBDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runLBCLI(t, srv.URL, "load-balancers", "delete", "lb1", "--zone", lbTestZoneID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBMonitorDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runLBCLI(t, srv.URL, "load-balancers", "monitor", "delete", "mon1",
		"--account-id", lbTestAccountID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v", err)
	}
}

// --------------------------------------------------------------------------
// Pool health
// --------------------------------------------------------------------------

// lbHealthResult mirrors the documented pool health response: one pop_health
// object with the pool's overall health and a list of single-key origin maps.
const lbHealthResult = `{
	"pool_id":"pool1",
	"pop_health":{
		"healthy":false,
		"origins":[
			{"203.0.113.2":{"healthy":false,"rtt":"0ms","failure_reason":"HTTP timeout occurred"}},
			{"203.0.113.1":{"healthy":true,"rtt":"9.2ms","failure_reason":"No failures","response_code":200}}
		]
	}
}`

func TestLBPoolHealthRequestAndTable(t *testing.T) {
	srv, got := lbServer(t, lbHealthResult)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "health", "pool1",
		"--account-id", lbTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" {
		t.Errorf("method = %s", got.method)
	}
	if got.path != "/accounts/"+lbTestAccountID+"/load_balancers/pools/pool1/health" {
		t.Errorf("path = %s", got.path)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("want header + 2 origin rows, got %d lines:\n%s", len(lines), stdout)
	}
	// The pool verdict is result data, so it stays in stdout on every row.
	for _, want := range []string{"POOL HEALTHY", "ORIGIN", "ORIGIN HEALTHY", "FAILURE REASON"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q missing %q", lines[0], want)
		}
	}
	// One pop_health object means no per-PoP dimension to report.
	if strings.Contains(lines[0], "POP\t") || strings.HasPrefix(lines[0], "POP ") {
		t.Errorf("header should not claim per-PoP data: %q", lines[0])
	}
	for i, line := range lines[1:] {
		if !strings.HasPrefix(line, "false") {
			t.Errorf("row %d = %q, want the pool verdict first", i, line)
		}
	}
	// Origins are sorted by address so the view is stable between runs.
	if !strings.Contains(lines[1], "203.0.113.1") || !strings.Contains(lines[2], "203.0.113.2") {
		t.Errorf("origins not sorted by address:\n%s", stdout)
	}
	if !strings.Contains(lines[1], "9.2ms") || !strings.Contains(lines[1], "200") {
		t.Errorf("origin detail missing from %q", lines[1])
	}
	if !strings.Contains(lines[2], "HTTP timeout occurred") {
		t.Errorf("failure reason missing from %q", lines[2])
	}
}

func TestLBPoolHealthReportsHealthyPool(t *testing.T) {
	srv, _ := lbServer(t, `{"pool_id":"pool1","pop_health":{"healthy":true,"origins":[{"203.0.113.1":{"healthy":true,"rtt":"12ms","failure_reason":"No failures","response_code":200}}]}}`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "health", "pool1",
		"--account-id", lbTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 {
		t.Fatalf("want header + 1 row, got:\n%s", stdout)
	}
	if !strings.HasPrefix(lines[1], "true") {
		t.Errorf("row = %q, want the pool verdict first", lines[1])
	}
	if !strings.Contains(lines[1], "203.0.113.1") {
		t.Errorf("row = %q", lines[1])
	}
}

func TestLBPoolHealthJSONOutput(t *testing.T) {
	srv, _ := lbServer(t, lbHealthResult)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "health", "pool1",
		"--account-id", lbTestAccountID, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, stdout)
	}
	if parsed["pool_id"] != "pool1" {
		t.Errorf("pool_id = %v", parsed["pool_id"])
	}
}

// A pool with no origin results still has a verdict worth printing, so the
// table keeps one row rather than collapsing to just a header.
func TestLBPoolHealthEmptyOriginsStillReportsVerdict(t *testing.T) {
	var health lbPoolHealth
	if err := json.Unmarshal([]byte(`{"pool_id":"p","pop_health":{"healthy":true,"origins":[]}}`), &health); err != nil {
		t.Fatal(err)
	}
	rows := lbPoolHealthRows(health)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want one verdict row", rows)
	}
	if rows[0][0] != "true" || rows[0][1] != "" {
		t.Errorf("row = %v", rows[0])
	}

	srv, _ := lbServer(t, `{"pool_id":"pool1","pop_health":{"healthy":false,"origins":[]}}`)
	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "pool", "health", "pool1",
		"--account-id", lbTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "false") {
		t.Errorf("want a header plus one verdict row, got:\n%s", stdout)
	}
}

func TestLBPoolHealthDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "health", "pool1", "--account-id", lbTestAccountID)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/pools/pool1/health")
}

// --------------------------------------------------------------------------
// Monitors
// --------------------------------------------------------------------------

func TestLBMonitorListRequestAndTable(t *testing.T) {
	srv, got := lbServer(t, `[{"id":"mon1","type":"https","method":"GET","path":"/health","port":443,"interval":60,"timeout":5,"description":"prod probe"}]`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "monitor", "list", "--account-id", lbTestAccountID)
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "GET" || got.path != "/accounts/"+lbTestAccountID+"/load_balancers/monitors" {
		t.Errorf("%s %s", got.method, got.path)
	}
	for _, want := range []string{"TYPE", "INTERVAL", "mon1", "https", "/health", "443", "prod probe"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestLBMonitorGetDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "get", "mon1", "--account-id", lbTestAccountID)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/monitors/mon1")
}

func TestLBMonitorCreateBuildsRequest(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID,
		"--type", "https",
		"--method", "head",
		"--path", "/health",
		"--expected-codes", "200",
		"--expected-body", "ok",
		"--description", "prod probe",
		"--header", "Host: www.example.com",
		"--header", "Host: alt.example.com",
		"--header", "X-Auth=secret",
		"--port", "8443",
		"--interval", "30",
		"--timeout", "5",
		"--retries", "2",
		"--follow-redirects",
		"--allow-insecure",
	)
	if d.Method != "POST" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/monitors")
	lbAssertJSONEqual(t, d.Body, `{
		"type":"https",
		"method":"HEAD",
		"path":"/health",
		"expected_codes":"200",
		"expected_body":"ok",
		"description":"prod probe",
		"header":{"Host":["www.example.com","alt.example.com"],"X-Auth":["secret"]},
		"port":8443,
		"interval":30,
		"timeout":5,
		"retries":2,
		"follow_redirects":true,
		"allow_insecure":true
	}`)
}

func TestLBMonitorCreateDefaultsExpectedCodesForHTTP(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "create", "--account-id", lbTestAccountID, "--type", "http")
	lbAssertJSONEqual(t, d.Body, `{"type":"http","expected_codes":"2xx"}`)
}

func TestLBMonitorCreateOmitsExpectedCodesForNonHTTP(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID, "--type", "tcp", "--port", "5432")
	lbAssertJSONEqual(t, d.Body, `{"type":"tcp","port":5432}`)
}

func TestLBMonitorCreateNormalizesType(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID, "--type", "UDP-ICMP", "--port", "53")
	lbAssertJSONEqual(t, d.Body, `{"type":"udp_icmp","port":53}`)
}

// TestLBMonitorCreatePortRequirement covers the protocol matrix: only the
// types with no default port must be given one.
func TestLBMonitorCreatePortRequirement(t *testing.T) {
	cases := []struct {
		mtype       string
		needsPort   bool
		wantNoPort  string
		bodyNoPort  string
		bodyWith443 string
	}{
		{mtype: "http", needsPort: false, bodyNoPort: `{"type":"http","expected_codes":"2xx"}`},
		{mtype: "https", needsPort: false, bodyNoPort: `{"type":"https","expected_codes":"2xx"}`},
		{mtype: "icmp_ping", needsPort: false, bodyNoPort: `{"type":"icmp_ping"}`},
		{mtype: "tcp", needsPort: true, wantNoPort: "--port is required for --type tcp"},
		{mtype: "udp_icmp", needsPort: true, wantNoPort: "--port is required for --type udp_icmp"},
		{mtype: "smtp", needsPort: true, wantNoPort: "--port is required for --type smtp"},
	}
	for _, tc := range cases {
		t.Run(tc.mtype, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "monitor", "create",
				"--account-id", lbTestAccountID, "--type", tc.mtype, "--dry-run")
			if tc.needsPort {
				if err == nil || !strings.Contains(err.Error(), tc.wantNoPort) {
					t.Fatalf("err = %v, want mention of %q", err, tc.wantNoPort)
				}
				// With an explicit port it goes through.
				d := lbDryRun(t, "load-balancers", "monitor", "create",
					"--account-id", lbTestAccountID, "--type", tc.mtype, "--port", "443")
				lbAssertJSONEqual(t, d.Body, `{"type":"`+tc.mtype+`","port":443}`)
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			d := lbDryRun(t, "load-balancers", "monitor", "create",
				"--account-id", lbTestAccountID, "--type", tc.mtype)
			lbAssertJSONEqual(t, d.Body, tc.bodyNoPort)
		})
	}
}

// TestLBMonitorCreateRejectsHTTPOnlyFlags covers finding 2: the probe details
// Cloudflare only honors for http/https must not be sent for other types.
func TestLBMonitorCreateRejectsHTTPOnlyFlags(t *testing.T) {
	httpOnly := [][]string{
		{"--path", "/health"},
		{"--expected-codes", "200"},
		{"--expected-body", "ok"},
		{"--header", "Host: www.example.com"},
		{"--follow-redirects"},
		{"--allow-insecure"},
	}
	for _, mtype := range []string{"tcp", "udp_icmp", "icmp_ping", "smtp"} {
		for _, flag := range httpOnly {
			t.Run(mtype+flag[0], func(t *testing.T) {
				args := []string{"load-balancers", "monitor", "create",
					"--account-id", lbTestAccountID, "--type", mtype, "--port", "443"}
				args = append(append(args, flag...), "--dry-run")
				_, _, err := runLBCLI(t, "http://example.invalid", args...)
				if err == nil {
					t.Fatalf("%s should be rejected for --type %s", flag[0], mtype)
				}
				if !strings.Contains(err.Error(), flag[0]) ||
					!strings.Contains(err.Error(), "only valid for http and https monitors") ||
					!strings.Contains(err.Error(), "--type "+mtype) {
					t.Fatalf("err = %v", err)
				}
			})
		}
	}
}

func TestLBMonitorCreateReportsEveryIncompatibleFlag(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID, "--type", "tcp", "--port", "5432",
		"--path", "/health", "--allow-insecure", "--dry-run")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"--path", "--allow-insecure"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %s", err, want)
		}
	}
}

func TestLBMonitorCreateAllowsHTTPOnlyFlagsForHTTPTypes(t *testing.T) {
	for _, mtype := range []string{"http", "https"} {
		t.Run(mtype, func(t *testing.T) {
			d := lbDryRun(t, "load-balancers", "monitor", "create",
				"--account-id", lbTestAccountID, "--type", mtype,
				"--path", "/health", "--allow-insecure")
			lbAssertJSONEqual(t, d.Body,
				`{"type":"`+mtype+`","path":"/health","expected_codes":"2xx","allow_insecure":true}`)
		})
	}
}

// TestLBMonitorUpdateRejectsHTTPOnlyFlagsWithType covers the update half of
// finding 2: the incompatibility is enforced only when --type is explicit.
func TestLBMonitorUpdateRejectsHTTPOnlyFlagsWithType(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "monitor", "update", "mon1",
		"--account-id", lbTestAccountID, "--type", "tcp", "--path", "/health", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "only valid for http and https monitors") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBMonitorUpdateAllowsHTTPOnlyFlagsWithoutType(t *testing.T) {
	// Without --type the monitor's current type is unknown here, so the
	// server stays authoritative.
	d := lbDryRun(t, "load-balancers", "monitor", "update", "mon1",
		"--account-id", lbTestAccountID, "--path", "/healthz")
	lbAssertJSONEqual(t, d.Body, `{"path":"/healthz"}`)
}

func TestLBMonitorUpdateDoesNotRequirePort(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "update", "mon1",
		"--account-id", lbTestAccountID, "--type", "tcp")
	lbAssertJSONEqual(t, d.Body, `{"type":"tcp"}`)
}

func TestLBMonitorRejectsZeroPort(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID, "--type", "tcp", "--port", "0", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--port must be between 1 and 65535") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBMonitorCreateHTTPRequest(t *testing.T) {
	srv, got := lbServer(t, `{"id":"mon1","type":"https"}`)

	stdout, _, err := runLBCLI(t, srv.URL, "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID, "--type", "https", "--path", "/health")
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/accounts/"+lbTestAccountID+"/load_balancers/monitors" {
		t.Errorf("%s %s", got.method, got.path)
	}
	lbAssertJSONEqual(t, got.body, `{"type":"https","path":"/health","expected_codes":"2xx"}`)
	if !strings.Contains(stdout, "mon1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestLBMonitorCreateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "unknown type",
			args: []string{"--type", "carrier-pigeon"},
			want: "--type must be one of",
		},
		{
			name: "empty expected codes",
			args: []string{"--type", "https", "--expected-codes", " "},
			want: "--expected-codes must not be empty",
		},
		{
			name: "negative interval",
			args: []string{"--type", "https", "--interval", "-1"},
			want: "--interval must be zero or greater",
		},
		{
			name: "port out of range",
			args: []string{"--type", "tcp", "--port", "70000"},
			want: "--port must be between 1 and 65535",
		},
		{
			name: "malformed header",
			args: []string{"--type", "https", "--header", "nonsense"},
			want: `--header "nonsense" must be in`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"load-balancers", "monitor", "create", "--account-id", lbTestAccountID}, tc.args...)
			_, _, err := runLBCLI(t, "http://example.invalid", append(args, "--dry-run")...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want mention of %q", err, tc.want)
			}
		})
	}
}

func TestLBMonitorCreateRequiresType(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "monitor", "create",
		"--account-id", lbTestAccountID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBMonitorUpdateSendsOnlyChangedFields(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "update", "mon1",
		"--account-id", lbTestAccountID, "--interval", "15", "--path", "/healthz")
	if d.Method != "PATCH" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/monitors/mon1")
	lbAssertJSONEqual(t, d.Body, `{"interval":15,"path":"/healthz"}`)
}

func TestLBMonitorUpdateDoesNotSendDefaultExpectedCodes(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "update", "mon1",
		"--account-id", lbTestAccountID, "--type", "https")
	lbAssertJSONEqual(t, d.Body, `{"type":"https"}`)
}

func TestLBMonitorUpdateRequiresAField(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "monitor", "update", "mon1",
		"--account-id", lbTestAccountID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "--interval") {
		t.Errorf("error should list the updatable flags: %v", err)
	}
}

func TestLBMonitorDeleteDryRun(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "monitor", "delete", "mon1", "--account-id", lbTestAccountID)
	if d.Method != "DELETE" {
		t.Errorf("method = %s", d.Method)
	}
	lbAssertPath(t, d.URL, "/accounts/"+lbTestAccountID+"/load_balancers/monitors/mon1")
}

// --------------------------------------------------------------------------
// Argument validation, help and pure helpers
// --------------------------------------------------------------------------

func TestLBCommandsRejectStrayArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"list", []string{"load-balancers", "list", "extra", "--zone", lbTestZoneID}},
		{"get", []string{"load-balancers", "get", "lb1", "extra", "--zone", lbTestZoneID}},
		{"create", []string{"load-balancers", "create", "www.example.com", "extra", "--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1"}},
		{"update", []string{"load-balancers", "update", "lb1", "extra", "--zone", lbTestZoneID, "--enabled=false"}},
		{"delete", []string{"load-balancers", "delete", "lb1", "extra", "--zone", lbTestZoneID, "--force"}},
		{"pool-list", []string{"load-balancers", "pool", "list", "extra", "--account-id", lbTestAccountID}},
		{"pool-get", []string{"load-balancers", "pool", "get", "pool1", "extra", "--account-id", lbTestAccountID}},
		{"pool-create", []string{"load-balancers", "pool", "create", "eu-west", "extra", "--account-id", lbTestAccountID, "--origin", "203.0.113.1"}},
		{"pool-update", []string{"load-balancers", "pool", "update", "pool1", "extra", "--account-id", lbTestAccountID, "--enabled=false"}},
		{"pool-delete", []string{"load-balancers", "pool", "delete", "pool1", "extra", "--account-id", lbTestAccountID, "--force"}},
		{"pool-health", []string{"load-balancers", "pool", "health", "pool1", "extra", "--account-id", lbTestAccountID}},
		{"monitor-list", []string{"load-balancers", "monitor", "list", "extra", "--account-id", lbTestAccountID}},
		{"monitor-get", []string{"load-balancers", "monitor", "get", "mon1", "extra", "--account-id", lbTestAccountID}},
		{"monitor-create", []string{"load-balancers", "monitor", "create", "extra", "--account-id", lbTestAccountID, "--type", "https"}},
		{"monitor-update", []string{"load-balancers", "monitor", "update", "mon1", "extra", "--account-id", lbTestAccountID, "--interval", "10"}},
		{"monitor-delete", []string{"load-balancers", "monitor", "delete", "mon1", "extra", "--account-id", lbTestAccountID, "--force"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", append(tc.args, "--dry-run")...)
			if err == nil {
				t.Fatal("expected an error for a stray positional argument")
			}
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "arg") && !strings.Contains(msg, "unknown command") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLBHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"load-balancers", "--help"}, []string{"cf load-balancers monitor create", "cf load-balancers pool create", "cf load-balancers create"}},
		{[]string{"load-balancers", "create", "--help"}, []string{"cf load-balancers create www.example.com", "--default-pool", "--fallback-pool"}},
		{[]string{"load-balancers", "update", "--help"}, []string{"cf load-balancers update", "--steering-policy"}},
		{[]string{"load-balancers", "delete", "--help"}, []string{"cf load-balancers delete", "--force"}},
		{[]string{"load-balancers", "list", "--help"}, []string{"cf load-balancers list", "--zone"}},
		{[]string{"load-balancers", "get", "--help"}, []string{"cf load-balancers get"}},
		{[]string{"load-balancers", "pool", "create", "--help"}, []string{"cf load-balancers pool create", "name=web1,address=203.0.113.1", "--monitor"}},
		{[]string{"load-balancers", "pool", "update", "--help"}, []string{"cf load-balancers pool update", "--origin"}},
		{[]string{"load-balancers", "pool", "delete", "--help"}, []string{"cf load-balancers pool delete", "--force"}},
		{[]string{"load-balancers", "pool", "list", "--help"}, []string{"cf load-balancers pool list"}},
		{[]string{"load-balancers", "pool", "get", "--help"}, []string{"cf load-balancers pool get"}},
		{[]string{"load-balancers", "pool", "health", "--help"}, []string{"cf load-balancers pool health"}},
		{[]string{"load-balancers", "monitor", "create", "--help"}, []string{"cf load-balancers monitor create", "--expected-codes", "Host: www.example.com"}},
		{[]string{"load-balancers", "monitor", "update", "--help"}, []string{"cf load-balancers monitor update"}},
		{[]string{"load-balancers", "monitor", "delete", "--help"}, []string{"cf load-balancers monitor delete", "--force"}},
		{[]string{"load-balancers", "monitor", "list", "--help"}, []string{"cf load-balancers monitor list"}},
		{[]string{"load-balancers", "monitor", "get", "--help"}, []string{"cf load-balancers monitor get"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args[:len(tc.args)-1], "-"), func(t *testing.T) {
			root := NewRootCmd()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(tc.args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			help := out.String()
			for _, want := range tc.want {
				if !strings.Contains(help, want) {
					t.Errorf("help missing %q\n%s", want, help)
				}
			}
		})
	}
}

func TestLBAliasResolves(t *testing.T) {
	d := lbDryRun(t, "lb", "list", "--zone", lbTestZoneID)
	lbAssertPath(t, d.URL, "/zones/"+lbTestZoneID+"/load_balancers")
}

// TestLBCheckRegionAccepted covers finding 3: documented regions are accepted
// and normalized to the API's upper-case underscore form.
func TestLBCheckRegionAccepted(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"WEU", "WEU"},
		{"weu", "WEU"},
		{"  enam  ", "ENAM"},
		{"ALL_REGIONS", "ALL_REGIONS"},
		{"all-regions", "ALL_REGIONS"},
		{"seas", "SEAS"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			d := lbDryRun(t, "load-balancers", "pool", "create", "eu-west",
				"--account-id", lbTestAccountID, "--origin", "203.0.113.1", "--check-region", tc.in)
			lbAssertJSONEqual(t, d.Body,
				`{"name":"eu-west","origins":[{"name":"203.0.113.1","address":"203.0.113.1"}],"check_regions":["`+tc.want+`"]}`)
		})
	}
}

func TestLBCheckRegionRejected(t *testing.T) {
	for _, bad := range []string{"EU", "westeurope", "WEU2", "  "} {
		t.Run(bad, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "create", "eu-west",
				"--account-id", lbTestAccountID, "--origin", "203.0.113.1", "--check-region", bad, "--dry-run")
			if err == nil || !strings.Contains(err.Error(), "check-region") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestLBPoolUpdateNormalizesCheckRegions(t *testing.T) {
	d := lbDryRun(t, "load-balancers", "pool", "update", "pool1",
		"--account-id", lbTestAccountID, "--check-region", "weu", "--check-region", "enam")
	lbAssertJSONEqual(t, d.Body, `{"check_regions":["WEU","ENAM"]}`)
}

func TestLBPoolUpdateRejectsUnknownCheckRegion(t *testing.T) {
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "update", "pool1",
		"--account-id", lbTestAccountID, "--check-region", "atlantis", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "not a known region") {
		t.Fatalf("err = %v", err)
	}
}

// TestLBSessionAffinityHeaderRejected covers finding 4: `header` affinity
// needs attributes this porcelain cannot express, so it must not be sent.
func TestLBSessionAffinityHeaderRejected(t *testing.T) {
	cases := []struct {
		args []string
		// The pointer must name the plumbing command for the caller's verb,
		// not always the update one.
		wantOp string
	}{
		{
			args:   []string{"load-balancers", "create", "www.example.com", "--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1"},
			wantOp: "cf api load-balancers create-zone",
		},
		{
			args:   []string{"load-balancers", "update", "lb1", "--zone", lbTestZoneID},
			wantOp: "cf api load-balancers update-zone",
		},
	}
	for _, tc := range cases {
		t.Run(tc.args[1], func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid",
				append(tc.args, "--session-affinity", "header", "--dry-run")...)
			if err == nil {
				t.Fatal("expected header session affinity to be rejected")
			}
			if !strings.Contains(err.Error(), "session_affinity_attributes.headers") {
				t.Errorf("err = %v, want it to explain the missing attributes", err)
			}
			if !strings.Contains(err.Error(), tc.wantOp) {
				t.Errorf("err = %v, want it to point at %q", err, tc.wantOp)
			}
		})
	}
}

func TestLBSessionAffinitySupportedValues(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"none", "none"},
		{"cookie", "cookie"},
		{"ip_cookie", "ip_cookie"},
		{"ip-cookie", "ip_cookie"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			d := lbDryRun(t, "load-balancers", "update", "lb1", "--zone", lbTestZoneID,
				"--session-affinity", tc.in)
			lbAssertJSONEqual(t, d.Body, `{"session_affinity":"`+tc.want+`"}`)
		})
	}
}

// TestLBRejectsBlankResourceIDs covers finding 5. A blank ID would build a
// collection path with a trailing slash, so the destructive commands must
// never reach the network with one.
func TestLBRejectsBlankResourceIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("blank ID reached the API: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cases := []struct {
		name  string
		args  []string
		label string
	}{
		{"lb-get", []string{"load-balancers", "get", "  ", "--zone", lbTestZoneID}, "<load-balancer-id>"},
		{"lb-update", []string{"load-balancers", "update", "", "--zone", lbTestZoneID, "--enabled=false"}, "<load-balancer-id>"},
		{"lb-delete", []string{"load-balancers", "delete", "  ", "--zone", lbTestZoneID, "--force"}, "<load-balancer-id>"},
		{"pool-get", []string{"load-balancers", "pool", "get", "  ", "--account-id", lbTestAccountID}, "<pool-id>"},
		{"pool-update", []string{"load-balancers", "pool", "update", "", "--account-id", lbTestAccountID, "--enabled=false"}, "<pool-id>"},
		{"pool-delete", []string{"load-balancers", "pool", "delete", "  ", "--account-id", lbTestAccountID, "--force"}, "<pool-id>"},
		{"pool-health", []string{"load-balancers", "pool", "health", "  ", "--account-id", lbTestAccountID}, "<pool-id>"},
		{"monitor-get", []string{"load-balancers", "monitor", "get", "  ", "--account-id", lbTestAccountID}, "<monitor-id>"},
		{"monitor-update", []string{"load-balancers", "monitor", "update", "", "--account-id", lbTestAccountID, "--interval", "10"}, "<monitor-id>"},
		{"monitor-delete", []string{"load-balancers", "monitor", "delete", "  ", "--account-id", lbTestAccountID, "--force"}, "<monitor-id>"},
		{"lb-create-name", []string{"load-balancers", "create", "  ", "--zone", lbTestZoneID, "--default-pool", "p1", "--fallback-pool", "p1"}, "<name>"},
		{"pool-create-name", []string{"load-balancers", "pool", "create", "  ", "--account-id", lbTestAccountID, "--origin", "203.0.113.1"}, "<name>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No --dry-run: a blank ID must be refused before any request.
			_, _, err := runLBCLI(t, srv.URL, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.label+" must not be empty") {
				t.Fatalf("err = %v, want %q must not be empty", err, tc.label)
			}
		})
	}
}

func TestLBUpdateRejectsBlankNameAndFallbackPool(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"name", []string{"load-balancers", "update", "lb1", "--zone", lbTestZoneID, "--name", "  "}, "--name must not be empty"},
		{"fallback-pool", []string{"load-balancers", "update", "lb1", "--zone", lbTestZoneID, "--fallback-pool", " "}, "--fallback-pool must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", append(tc.args, "--dry-run")...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestLBPoolNameValidation(t *testing.T) {
	for _, ok := range []string{"eu-west", "eu_west", "pool1", "A1"} {
		t.Run("accept-"+ok, func(t *testing.T) {
			d := lbDryRun(t, "load-balancers", "pool", "create", ok,
				"--account-id", lbTestAccountID, "--origin", "203.0.113.1")
			lbAssertJSONEqual(t, d.Body,
				`{"name":"`+ok+`","origins":[{"name":"203.0.113.1","address":"203.0.113.1"}]}`)
		})
	}
	for _, bad := range []string{"eu west", "eu.west", "eu/west", "eu:west"} {
		t.Run("reject-"+bad, func(t *testing.T) {
			_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "create", bad,
				"--account-id", lbTestAccountID, "--origin", "203.0.113.1", "--dry-run")
			if err == nil || !strings.Contains(err.Error(), "is invalid") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// The same contract applies to a rename.
	if _, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "update", "pool1",
		"--account-id", lbTestAccountID, "--name", "eu west", "--dry-run"); err == nil ||
		!strings.Contains(err.Error(), "is invalid") {
		t.Fatalf("rename err = %v", err)
	}
}

// TestLBOriginWeightHundredths covers finding 6: the API documents weight as
// a multiple of 0.01, so finer precision must be refused rather than rounded.
func TestLBOriginWeightHundredths(t *testing.T) {
	for _, ok := range []string{"0", "0.01", "0.1", "0.29", "0.5", "0.55", "0.99", "1"} {
		t.Run("accept-"+ok, func(t *testing.T) {
			if _, err := lbParseOrigin("address=203.0.113.1,weight=" + ok); err != nil {
				t.Fatalf("weight %s rejected: %v", ok, err)
			}
		})
	}
	for _, bad := range []string{"0.555", "0.123", "0.001", "0.9999"} {
		t.Run("reject-"+bad, func(t *testing.T) {
			_, err := lbParseOrigin("address=203.0.113.1,weight=" + bad)
			if err == nil || !strings.Contains(err.Error(), "multiple of 0.01") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// And through the real command tree.
	_, _, err := runLBCLI(t, "http://example.invalid", "load-balancers", "pool", "create", "eu-west",
		"--account-id", lbTestAccountID, "--origin", "name=web1,address=203.0.113.1,weight=0.555", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "multiple of 0.01") {
		t.Fatalf("err = %v", err)
	}
	d := lbDryRun(t, "load-balancers", "pool", "create", "eu-west",
		"--account-id", lbTestAccountID, "--origin", "name=web1,address=203.0.113.1,weight=0.55")
	lbAssertJSONEqual(t, d.Body,
		`{"name":"eu-west","origins":[{"name":"web1","address":"203.0.113.1","weight":0.55}]}`)
}

func TestLBParseOrigin(t *testing.T) {
	o, err := lbParseOrigin("  203.0.113.1 ")
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "203.0.113.1" || o.Address != "203.0.113.1" || o.Weight != nil || o.Enabled != nil {
		t.Fatalf("origin = %+v", o)
	}

	o, err = lbParseOrigin("name=web1, address=203.0.113.1 ,weight=0, enabled=true")
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "web1" || o.Address != "203.0.113.1" {
		t.Fatalf("origin = %+v", o)
	}
	if o.Weight == nil || *o.Weight != 0 {
		t.Fatalf("weight = %v", o.Weight)
	}
	if o.Enabled == nil || !*o.Enabled {
		t.Fatalf("enabled = %v", o.Enabled)
	}

	// Name defaults to the address in the key=value form too.
	o, err = lbParseOrigin("address=203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "203.0.113.9" {
		t.Fatalf("name = %q", o.Name)
	}

	if _, err := lbParseOrigin("   "); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBParseHeaders(t *testing.T) {
	h, err := lbParseHeaders([]string{"Host: www.example.com", "Host: alt.example.com", "X-Auth=secret"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h["Host"]) != 2 || h["Host"][0] != "www.example.com" || h["Host"][1] != "alt.example.com" {
		t.Fatalf("Host = %v", h["Host"])
	}
	if len(h["X-Auth"]) != 1 || h["X-Auth"][0] != "secret" {
		t.Fatalf("X-Auth = %v", h["X-Auth"])
	}
	if _, err := lbParseHeaders([]string{": novalue"}); err == nil {
		t.Fatal("expected an error for a header with no name")
	}
}

func TestLBNormalizeEnum(t *testing.T) {
	v, err := lbNormalizeEnum("steering-policy", " Dynamic-Latency ", lbSteeringPolicies)
	if err != nil {
		t.Fatal(err)
	}
	if v != "dynamic_latency" {
		t.Fatalf("v = %q", v)
	}
	if _, err := lbNormalizeEnum("type", "gopher", lbMonitorTypes); err == nil ||
		!strings.Contains(err.Error(), "icmp_ping") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBAccountID(t *testing.T) {
	id, err := lbAccountID("  " + lbTestAccountID + " ")
	if err != nil {
		t.Fatal(err)
	}
	if id != lbTestAccountID {
		t.Fatalf("id = %q", id)
	}
	if _, err := lbAccountID("   "); err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("err = %v", err)
	}
}

func TestLBRequireNonEmpty(t *testing.T) {
	if err := lbRequireNonEmpty("default-pool", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if err := lbRequireNonEmpty("default-pool", nil); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("err = %v", err)
	}
	if err := lbRequireNonEmpty("default-pool", []string{"a", " "}); err == nil ||
		!strings.Contains(err.Error(), "position 2") {
		t.Fatalf("err = %v", err)
	}
}
