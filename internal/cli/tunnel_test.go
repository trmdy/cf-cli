package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	tunnelTestAccountID = "abcdef0123456789abcdef0123456789"
	tunnelTestTunnelID  = "6d6b1e0a-4c7d-4e2a-9f0c-1a2b3c4d5e6f"
	tunnelTestRouteID   = "3f1d7b02-9a4c-4c6c-bb2a-70d2a0c1f0e5"
	tunnelTestVNetID    = "5f2c1a80-1c9e-4f77-8b4d-2f8a37f3b0d1"
)

func runTunnelCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runTunnelCLIWithStdin(t, serverURL, nil, args...)
}

func runTunnelCLIWithStdin(t *testing.T, serverURL string, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
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
		"--account-id", tunnelTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func tunnelAssertJSONEqual(t *testing.T, got []byte, want string) {
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

type tunnelRequestDump struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body"`
}

func decodeTunnelDump(t *testing.T, stdout string) tunnelRequestDump {
	t.Helper()
	var d tunnelRequestDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

// --- pure helpers -----------------------------------------------------------

func TestIsTunnelUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{tunnelTestTunnelID, true},
		{strings.ToUpper(tunnelTestTunnelID), true},
		{"6d6b1e0a4c7d4e2a9f0c1a2b3c4d5e6f", true},
		{"prod-tunnel", false},
		{"", false},
		{"6d6b1e0a-4c7d-4e2a-9f0c-1a2b3c4d5e6g", false},
		{"6d6b1e0a4c7d-4e2a-9f0c-1a2b3c4d5e6f", false},
		{"6d6b1e0a4c7d4e2a9f0c1a2b3c4d5e6", false},
	}
	for _, tc := range cases {
		if got := isTunnelUUID(tc.in); got != tc.want {
			t.Errorf("isTunnelUUID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBuildTunnelListQuery(t *testing.T) {
	q, err := buildTunnelListQuery("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("is_deleted") != "false" {
		t.Errorf("is_deleted = %q, want false by default", q.Get("is_deleted"))
	}
	if q.Get("per_page") != "100" {
		t.Errorf("per_page = %q", q.Get("per_page"))
	}

	q, err = buildTunnelListQuery("prod", "HEALTHY", true)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("name") != "prod" || q.Get("status") != "healthy" {
		t.Errorf("query = %v", q)
	}
	if q.Has("is_deleted") {
		t.Errorf("--include-deleted should drop is_deleted, got %v", q)
	}

	if _, err := buildTunnelListQuery("", "broken", false); err == nil || !strings.Contains(err.Error(), "--status") {
		t.Fatalf("expected status validation error, got %v", err)
	}
}

func TestBuildTunnelCreateBody(t *testing.T) {
	body, err := buildTunnelCreateBody("prod-tunnel", "", "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"name":"prod-tunnel","config_src":"cloudflare"}`)

	body, err = buildTunnelCreateBody("prod-tunnel", "", "LOCAL")
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"name":"prod-tunnel","config_src":"local"}`)

	secret := "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=" // 32 bytes
	body, err = buildTunnelCreateBody("prod-tunnel", secret, "cloudflare")
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"name":"prod-tunnel","config_src":"cloudflare","tunnel_secret":"`+secret+`"}`)
}

// The API contract for tunnel_secret is a minimum of 32 bytes with no upper
// bound, so longer secrets must be accepted.
func TestBuildTunnelCreateBodyAcceptsLongSecret(t *testing.T) {
	for _, size := range []int{32, 64, 65, 128} {
		secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, size))
		body, err := buildTunnelCreateBody("prod-tunnel", secret, "cloudflare")
		if err != nil {
			t.Fatalf("%d-byte secret rejected: %v", size, err)
		}
		if !strings.Contains(string(body), secret) {
			t.Errorf("%d-byte secret missing from body %s", size, body)
		}
	}
	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, 31))
	if _, err := buildTunnelCreateBody("prod-tunnel", short, "cloudflare"); err == nil ||
		!strings.Contains(err.Error(), "at least 32 bytes") {
		t.Fatalf("expected minimum-length error, got %v", err)
	}
}

func TestBuildTunnelCreateBodyValidation(t *testing.T) {
	cases := []struct {
		name    string
		tunnel  string
		secret  string
		src     string
		wantSub string
	}{
		{"empty name", "  ", "", "cloudflare", "must not be empty"},
		{"bad config-src", "prod", "", "remote", "--config-src"},
		{"secret not base64", "prod", "not base64!!", "cloudflare", "base64-encoded"},
		{"secret too short", "prod", "AQIDBAUGBwgJCgsM", "cloudflare", "at least 32 bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildTunnelCreateBody(tc.tunnel, tc.secret, tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

func TestBuildTunnelConfigBody(t *testing.T) {
	bare := `{"ingress":[{"hostname":"app.example.com","service":"http://localhost:8000"},{"service":"http_status:404"}]}`
	body, err := buildTunnelConfigBody([]byte(bare))
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"config":`+bare+`}`)

	// A document from `cf tunnel config get` round-trips: the wrapper and the
	// read-only sibling fields are dropped.
	wrapped := `{"tunnel_id":"` + tunnelTestTunnelID + `","version":3,"config":` + bare + `}`
	body, err = buildTunnelConfigBody([]byte(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"config":`+bare+`}`)

	// An empty config object is legal: it clears the ingress rules.
	body, err = buildTunnelConfigBody([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"config":{}}`)
}

func TestBuildTunnelConfigBodyValidation(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantSub string
	}{
		{"empty", "   \n", "empty"},
		{"not json", "ingress: []", "JSON object"},
		{"array", `[{"service":"http_status:404"}]`, "JSON object"},
		{"null", `null`, "not null"},
		{"config null", `{"config":null}`, "not null"},
		{"config array", `{"config":[1,2]}`, "JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildTunnelConfigBody([]byte(tc.in))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

func TestReadTunnelConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"ingress":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := readTunnelConfigFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ingress":[]}` {
		t.Errorf("file contents = %s", raw)
	}

	raw, err = readTunnelConfigFile("-", strings.NewReader(`{"ingress":[1]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"ingress":[1]}` {
		t.Errorf("stdin contents = %s", raw)
	}

	if _, err := readTunnelConfigFile("", nil); err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("expected missing file error, got %v", err)
	}
	if _, err := readTunnelConfigFile(filepath.Join(t.TempDir(), "missing.json"), nil); err == nil ||
		!strings.Contains(err.Error(), "read configuration file") {
		t.Fatalf("expected read error, got %v", err)
	}
}

func TestNormalizeTunnelNetwork(t *testing.T) {
	for _, in := range []string{"10.0.0.0/8", "192.168.4.0/24", "2001:db8::/32"} {
		got, err := normalizeTunnelNetwork(in)
		if err != nil {
			t.Fatalf("normalizeTunnelNetwork(%q): %v", in, err)
		}
		if got != in {
			t.Errorf("normalizeTunnelNetwork(%q) = %q", in, got)
		}
	}
	if _, err := normalizeTunnelNetwork("10.0.0.1"); err == nil || !strings.Contains(err.Error(), "CIDR notation") {
		t.Fatalf("expected CIDR error, got %v", err)
	}
	if _, err := normalizeTunnelNetwork("10.0.0.5/8"); err == nil || !strings.Contains(err.Error(), "10.0.0.0/8") {
		t.Fatalf("expected host-bits error suggesting the network, got %v", err)
	}
}

func TestBuildTunnelRouteBody(t *testing.T) {
	body, err := buildTunnelRouteBody("10.0.0.0/8", tunnelTestTunnelID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"network":"10.0.0.0/8","tunnel_id":"`+tunnelTestTunnelID+`"}`)

	body, err = buildTunnelRouteBody("10.0.0.0/8", tunnelTestTunnelID, "office lan", tunnelTestVNetID)
	if err != nil {
		t.Fatal(err)
	}
	tunnelAssertJSONEqual(t, body, `{"network":"10.0.0.0/8","tunnel_id":"`+tunnelTestTunnelID+
		`","comment":"office lan","virtual_network_id":"`+tunnelTestVNetID+`"}`)

	if _, err := buildTunnelRouteBody("10.0.0.0/8", "", "", ""); err == nil || !strings.Contains(err.Error(), "--tunnel") {
		t.Fatalf("expected missing tunnel error, got %v", err)
	}
}

func TestBuildTunnelRouteBodyValidation(t *testing.T) {
	// The API caps comments at 100 characters.
	atLimit := strings.Repeat("a", 100)
	if _, err := buildTunnelRouteBody("10.0.0.0/8", tunnelTestTunnelID, atLimit, ""); err != nil {
		t.Fatalf("100-character comment rejected: %v", err)
	}
	tooLong := strings.Repeat("a", 101)
	if _, err := buildTunnelRouteBody("10.0.0.0/8", tunnelTestTunnelID, tooLong, ""); err == nil ||
		!strings.Contains(err.Error(), "--comment") || !strings.Contains(err.Error(), "100") {
		t.Fatalf("expected comment length error, got %v", err)
	}
	// Length is counted in characters, not bytes: 100 multi-byte runes fit.
	multibyte := strings.Repeat("é", 100)
	if _, err := buildTunnelRouteBody("10.0.0.0/8", tunnelTestTunnelID, multibyte, ""); err != nil {
		t.Fatalf("100 multi-byte characters rejected: %v", err)
	}

	if _, err := buildTunnelRouteBody("10.0.0.0/8", tunnelTestTunnelID, "", "vnet-1"); err == nil ||
		!strings.Contains(err.Error(), "--virtual-network-id") {
		t.Fatalf("expected virtual network UUID error, got %v", err)
	}
}

func TestBuildTunnelRouteListQuery(t *testing.T) {
	q, err := buildTunnelRouteListQuery("", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("is_deleted") != "false" || q.Get("per_page") != "100" {
		t.Errorf("query = %v", q)
	}
	q, err = buildTunnelRouteListQuery(tunnelTestTunnelID, tunnelTestVNetID, true)
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("tunnel_id") != tunnelTestTunnelID || q.Get("virtual_network_id") != tunnelTestVNetID {
		t.Errorf("query = %v", q)
	}
	if q.Has("is_deleted") {
		t.Errorf("--include-deleted should drop is_deleted, got %v", q)
	}

	if _, err := buildTunnelRouteListQuery("", "vnet-1", false); err == nil ||
		!strings.Contains(err.Error(), "--virtual-network-id") {
		t.Fatalf("expected virtual network UUID error, got %v", err)
	}
}

func TestValidateTunnelUUID(t *testing.T) {
	if err := validateTunnelUUID("--virtual-network-id", ""); err != nil {
		t.Errorf("empty value should be treated as unset: %v", err)
	}
	if err := validateTunnelUUID("--virtual-network-id", tunnelTestVNetID); err != nil {
		t.Errorf("valid UUID rejected: %v", err)
	}
	err := validateTunnelUUID("--virtual-network-id", "vnet-1")
	if err == nil || !strings.Contains(err.Error(), "--virtual-network-id") || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("err = %v, want an actionable UUID error naming the flag", err)
	}
}

// --- command behavior -------------------------------------------------------

func TestTunnelListDryRun(t *testing.T) {
	stdout, _, err := runTunnelCLI(t, "http://example.invalid",
		"tunnel", "list",
		"--status", "healthy",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := decodeTunnelDump(t, stdout)
	if dump.Method != "GET" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.Contains(dump.URL, "/accounts/"+tunnelTestAccountID+"/cfd_tunnel?") {
		t.Errorf("url = %s", dump.URL)
	}
	for _, want := range []string{"status=healthy", "is_deleted=false", "per_page=100"} {
		if !strings.Contains(dump.URL, want) {
			t.Errorf("url %s missing %q", dump.URL, want)
		}
	}
}

func TestTunnelListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel","status":"healthy",
			 "created_at":"2026-01-02T03:04:05Z","connections":[{"id":"c1"},{"id":"c2"}]},
			{"id":"11111111-2222-3333-4444-555555555555","name":"idle","status":"inactive",
			 "created_at":"2026-02-02T03:04:05Z"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got:\n%s", stdout)
	}
	if !strings.HasPrefix(lines[0], "ID") {
		t.Errorf("header = %q", lines[0])
	}
	// The API deprecated `connections`, so the table reports the tunnel's
	// status rather than a connection count.
	if strings.Contains(lines[0], "CONNS") {
		t.Errorf("header still carries the deprecated connections column: %q", lines[0])
	}
	for _, want := range []string{"NAME", "STATUS", "CREATED"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q missing %q", lines[0], want)
		}
	}
	if !strings.Contains(lines[1], "prod-tunnel") || !strings.Contains(lines[1], "healthy") {
		t.Errorf("row = %q", lines[1])
	}
	if !strings.Contains(lines[1], "2026-01-02T03:04:05Z") {
		t.Errorf("row missing created_at: %q", lines[1])
	}
	if !strings.Contains(lines[2], "idle") || !strings.Contains(lines[2], "inactive") {
		t.Errorf("row = %q", lines[2])
	}
}

func TestTunnelListJSONHonorsOutputFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if len(got) != 1 || got[0]["name"] != "prod-tunnel" {
		t.Errorf("result = %v", got)
	}
}

func TestTunnelGetResolvesName(t *testing.T) {
	var lookupQuery, getPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/accounts/" + tunnelTestAccountID + "/cfd_tunnel":
			lookupQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel"}]}`))
		case "/accounts/" + tunnelTestAccountID + "/cfd_tunnel/" + tunnelTestTunnelID:
			getPath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "get", "prod-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lookupQuery, "name=prod-tunnel") || !strings.Contains(lookupQuery, "is_deleted=false") {
		t.Errorf("lookup query = %s", lookupQuery)
	}
	if getPath == "" {
		t.Error("expected a get by resolved tunnel ID")
	}
	if !strings.Contains(stdout, "prod-tunnel") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestTunnelGetByIDSkipsLookup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel/"+tunnelTestTunnelID {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestTunnelID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runTunnelCLI(t, srv.URL, "tunnel", "get", tunnelTestTunnelID); err != nil {
		t.Fatal(err)
	}
}

func TestTunnelResolveNameErrors(t *testing.T) {
	cases := []struct {
		name    string
		result  string
		wantSub string
	}{
		{"not found", `[]`, "not found"},
		{"other name only", `[{"id":"` + tunnelTestTunnelID + `","name":"other"}]`, "not found"},
		{"ambiguous", `[{"id":"a","name":"prod-tunnel"},{"id":"b","name":"prod-tunnel"}]`, "multiple tunnels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":` + tc.result + `}`))
			}))
			defer srv.Close()

			_, _, err := runTunnelCLI(t, srv.URL, "tunnel", "get", "prod-tunnel")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

func TestTunnelCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "create", "prod-tunnel")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	tunnelAssertJSONEqual(t, gotBody, `{"name":"prod-tunnel","config_src":"cloudflare"}`)
	if !strings.Contains(stdout, tunnelTestTunnelID) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestTunnelCreateDryRunLocalConfigSrc(t *testing.T) {
	stdout, _, err := runTunnelCLI(t, "http://example.invalid",
		"tunnel", "create", "prod-tunnel",
		"--config-src", "local",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := decodeTunnelDump(t, stdout)
	if dump.Method != "POST" {
		t.Errorf("method = %s", dump.Method)
	}
	tunnelAssertJSONEqual(t, dump.Body, `{"name":"prod-tunnel","config_src":"local"}`)
}

func TestTunnelCreateRejectsBadSecretBeforeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runTunnelCLI(t, srv.URL, "tunnel", "create", "prod-tunnel", "--secret", "nope!")
	if err == nil || !strings.Contains(err.Error(), "base64") {
		t.Fatalf("expected secret error, got %v", err)
	}
}

func TestTunnelDeleteDryRun(t *testing.T) {
	stdout, _, err := runTunnelCLI(t, "http://example.invalid",
		"tunnel", "delete", tunnelTestTunnelID,
		"--force",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := decodeTunnelDump(t, stdout)
	if dump.Method != "DELETE" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/accounts/"+tunnelTestAccountID+"/cfd_tunnel/"+tunnelTestTunnelID) {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestTunnelDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			t.Fatalf("unexpected delete: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	_, _, err := runTunnelCLI(t, srv.URL, "tunnel", "delete", tunnelTestTunnelID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestTunnelDeleteHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestTunnelID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runTunnelCLI(t, srv.URL, "tunnel", "delete", tunnelTestTunnelID, "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel/"+tunnelTestTunnelID {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
}

func TestTunnelTokenHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":"eyJhIjoiYiJ9"}`))
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "token", tunnelTestTunnelID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" || gotPath != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel/"+tunnelTestTunnelID+"/token" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	if strings.TrimSpace(stdout) != `"eyJhIjoiYiJ9"` {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestTunnelConfigGetDryRun(t *testing.T) {
	stdout, _, err := runTunnelCLI(t, "http://example.invalid",
		"tunnel", "config", "get", tunnelTestTunnelID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := decodeTunnelDump(t, stdout)
	if dump.Method != "GET" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/cfd_tunnel/"+tunnelTestTunnelID+"/configurations") {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestTunnelConfigSetFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"ingress":[{"hostname":"app.example.com","service":"http://localhost:8000"},{"service":"http_status:404"}]}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"version":2}}`))
	}))
	defer srv.Close()

	if _, _, err := runTunnelCLI(t, srv.URL, "tunnel", "config", "set", tunnelTestTunnelID, "--file", path); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel/"+tunnelTestTunnelID+"/configurations" {
		t.Errorf("path = %s", gotPath)
	}
	tunnelAssertJSONEqual(t, gotBody, `{"config":`+cfg+`}`)
}

func TestTunnelConfigSetFromStdin(t *testing.T) {
	cfg := `{"tunnel_id":"x","version":7,"config":{"ingress":[{"service":"http_status:404"}]}}`
	stdout, _, err := runTunnelCLIWithStdin(t, "http://example.invalid", strings.NewReader(cfg),
		"tunnel", "config", "set", tunnelTestTunnelID,
		"--file", "-",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	dump := decodeTunnelDump(t, stdout)
	if dump.Method != "PUT" {
		t.Errorf("method = %s", dump.Method)
	}
	tunnelAssertJSONEqual(t, dump.Body, `{"config":{"ingress":[{"service":"http_status:404"}]}}`)
}

func TestTunnelConfigSetRequiresFile(t *testing.T) {
	_, _, err := runTunnelCLI(t, "http://example.invalid",
		"tunnel", "config", "set", tunnelTestTunnelID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "file") {
		t.Fatalf("expected required --file error, got %v", err)
	}
}

func TestTunnelRouteListDryRunResolvesTunnel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+tunnelTestAccountID+"/cfd_tunnel" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "list", "--tunnel", "prod-tunnel", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dump := decodeTunnelDump(t, stdout)
	if !strings.Contains(dump.URL, "/accounts/"+tunnelTestAccountID+"/teamnet/routes?") {
		t.Errorf("url = %s", dump.URL)
	}
	for _, want := range []string{"tunnel_id=" + tunnelTestTunnelID, "is_deleted=false"} {
		if !strings.Contains(dump.URL, want) {
			t.Errorf("url %s missing %q", dump.URL, want)
		}
	}
}

func TestTunnelRouteListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+tunnelTestAccountID+"/teamnet/routes" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"id":"` + tunnelTestRouteID + `","network":"10.0.0.0/8","tunnel_id":"` + tunnelTestTunnelID + `",
			 "tunnel_name":"prod-tunnel","virtual_network_id":"5f2c1a80-1c9e-4f77-8b4d-2f8a37f3b0d1","comment":"office lan"},
			{"id":"r2","network":"192.168.4.0/24","tunnel_id":"` + tunnelTestTunnelID + `"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "list")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header + 2 rows, got:\n%s", stdout)
	}
	if !strings.Contains(lines[0], "NETWORK") || !strings.Contains(lines[0], "VNET") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "10.0.0.0/8") || !strings.Contains(lines[1], "prod-tunnel") {
		t.Errorf("row = %q", lines[1])
	}
	// No tunnel_name: the tunnel ID stands in.
	if !strings.Contains(lines[2], tunnelTestTunnelID) {
		t.Errorf("row = %q", lines[2])
	}
}

func TestTunnelRouteAddHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/accounts/" + tunnelTestAccountID + "/cfd_tunnel":
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + tunnelTestTunnelID + `","name":"prod-tunnel"}]}`))
		case "/accounts/" + tunnelTestAccountID + "/teamnet/routes":
			gotMethod, gotPath = r.Method, r.URL.Path
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestRouteID + `"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	stdout, _, err := runTunnelCLI(t, srv.URL,
		"tunnel", "route", "add", "10.0.0.0/8",
		"--tunnel", "prod-tunnel",
		"--comment", "office lan",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/accounts/"+tunnelTestAccountID+"/teamnet/routes" {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
	tunnelAssertJSONEqual(t, gotBody, `{"network":"10.0.0.0/8","tunnel_id":"`+tunnelTestTunnelID+`","comment":"office lan"}`)
	if !strings.Contains(stdout, tunnelTestRouteID) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestTunnelRouteAddRejectsBadNetworkBeforeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	for _, network := range []string{"10.0.0.1", "10.0.0.5/8"} {
		if _, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "add", network, "--tunnel", tunnelTestTunnelID); err == nil {
			t.Fatalf("expected error for network %q", network)
		}
	}
}

func TestTunnelRouteAddRequiresTunnel(t *testing.T) {
	_, _, err := runTunnelCLI(t, "http://example.invalid", "tunnel", "route", "add", "10.0.0.0/8", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "tunnel") {
		t.Fatalf("expected required --tunnel error, got %v", err)
	}
}

func TestTunnelRouteRemoveByRouteID(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestRouteID + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "remove", tunnelTestRouteID, "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/accounts/"+tunnelTestAccountID+"/teamnet/routes/"+tunnelTestRouteID {
		t.Errorf("%s %s", gotMethod, gotPath)
	}
}

func TestTunnelRouteRemoveByNetwork(t *testing.T) {
	var listQuery, deletePath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/accounts/"+tunnelTestAccountID+"/teamnet/routes":
			listQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"success":true,"result":[
				{"id":"other","network":"192.168.4.0/24"},
				{"id":"` + tunnelTestRouteID + `","network":"10.0.0.0/8"}]}`))
		case r.Method == "DELETE":
			deletePath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + tunnelTestRouteID + `"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	if _, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "remove", "10.0.0.0/8", "--force"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(listQuery, "is_deleted=false") {
		t.Errorf("list query = %s", listQuery)
	}
	if deletePath != "/accounts/"+tunnelTestAccountID+"/teamnet/routes/"+tunnelTestRouteID {
		t.Errorf("delete path = %s", deletePath)
	}
}

func TestTunnelRouteRemoveByNetworkErrors(t *testing.T) {
	cases := []struct {
		name    string
		result  string
		wantSub string
	}{
		{"no match", `[{"id":"other","network":"192.168.4.0/24"}]`, "no route for network"},
		{"ambiguous", `[{"id":"a","network":"10.0.0.0/8"},{"id":"b","network":"10.0.0.0/8"}]`, "--virtual-network-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					t.Fatalf("unexpected delete: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":` + tc.result + `}`))
			}))
			defer srv.Close()

			_, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "remove", "10.0.0.0/8", "--force")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

// Malformed route and virtual network IDs are rejected before any request.
func TestTunnelRouteRejectsMalformedIDs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{
			name:    "remove bad route id",
			args:    []string{"tunnel", "route", "remove", "not-a-uuid", "--force"},
			wantSub: "expected a route ID (UUID) or a network in CIDR notation",
		},
		{
			name:    "remove bad vnet",
			args:    []string{"tunnel", "route", "remove", tunnelTestRouteID, "--virtual-network-id", "vnet-1", "--force"},
			wantSub: "--virtual-network-id",
		},
		{
			name:    "add bad vnet",
			args:    []string{"tunnel", "route", "add", "10.0.0.0/8", "--tunnel", tunnelTestTunnelID, "--virtual-network-id", "vnet-1"},
			wantSub: "--virtual-network-id",
		},
		{
			name:    "list bad vnet",
			args:    []string{"tunnel", "route", "list", "--virtual-network-id", "vnet-1"},
			wantSub: "--virtual-network-id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}))
			defer srv.Close()

			_, _, err := runTunnelCLI(t, srv.URL, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantSub)
			}
		})
	}
}

// A comment over the API limit fails before the request goes out.
func TestTunnelRouteAddRejectsLongCommentBeforeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runTunnelCLI(t, srv.URL,
		"tunnel", "route", "add", "10.0.0.0/8",
		"--tunnel", tunnelTestTunnelID,
		"--comment", strings.Repeat("a", 101),
	)
	if err == nil || !strings.Contains(err.Error(), "--comment") {
		t.Fatalf("expected comment length error, got %v", err)
	}
}

// Local flag validation must beat the tunnel name lookup: passing --tunnel by
// NAME would resolve through the API, so a bad flag has to fail first and the
// server must stay untouched. A tunnel ID would skip the lookup and hide a
// regression, so these cases deliberately use a name.
func TestTunnelRouteAddValidatesFlagsBeforeNameLookup(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantSub string
	}{
		{
			name:    "overlong comment",
			args:    []string{"--comment", strings.Repeat("a", 101)},
			wantSub: "--comment",
		},
		{
			name:    "malformed virtual network id",
			args:    []string{"--virtual-network-id", "vnet-1"},
			wantSub: "--virtual-network-id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
			}))
			defer srv.Close()

			args := append([]string{
				"tunnel", "route", "add", "10.0.0.0/8",
				"--tunnel", "prod-tunnel",
			}, tc.args...)
			_, _, err := runTunnelCLI(t, srv.URL, args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want the local flag error containing %q", err, tc.wantSub)
			}
			if requests != 0 {
				t.Errorf("server saw %d request(s); local validation must run before the tunnel lookup", requests)
			}
		})
	}
}

// The same ordering guarantee for route list, whose --tunnel is also resolved
// by name.
func TestTunnelRouteListValidatesFlagsBeforeNameLookup(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	_, _, err := runTunnelCLI(t, srv.URL,
		"tunnel", "route", "list",
		"--tunnel", "prod-tunnel",
		"--virtual-network-id", "vnet-1",
	)
	if err == nil || !strings.Contains(err.Error(), "--virtual-network-id") {
		t.Fatalf("err = %v, want a --virtual-network-id error", err)
	}
	if requests != 0 {
		t.Errorf("server saw %d request(s); local validation must run before the tunnel lookup", requests)
	}
}

func TestTunnelRouteRemoveRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			t.Fatalf("unexpected delete: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	_, _, err := runTunnelCLI(t, srv.URL, "tunnel", "route", "remove", tunnelTestRouteID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestTunnelRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")

	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--token", "test-token", "--base-url", "http://example.invalid", "tunnel", "list", "--dry-run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--account-id") {
		t.Fatalf("expected account ID error, got %v", err)
	}
}

func TestTunnelCommandsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"tunnel", "list", "extra", "--dry-run"},
		{"tunnel", "route", "list", "extra", "--dry-run"},
		{"tunnel", "get", tunnelTestTunnelID, "extra", "--dry-run"},
		{"tunnel", "route", "add", "10.0.0.0/8", "extra", "--tunnel", tunnelTestTunnelID, "--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, _, err := runTunnelCLI(t, "http://example.invalid", args...); err == nil {
				t.Fatal("expected error for stray positional args")
			}
		})
	}
}

// No command should list the same example twice, and every command with a
// Long should carry at least one.
func TestTunnelHelpExamplesAreUniquePerCommand(t *testing.T) {
	root := NewRootCmd()
	var tunnelCmd *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "tunnel" {
			tunnelCmd = c
		}
	}
	if tunnelCmd == nil {
		t.Fatal("tunnel command not registered on root")
	}

	var walk func(cmd *cobra.Command, path string)
	walk = func(cmd *cobra.Command, path string) {
		var examples []string
		for _, line := range strings.Split(cmd.Long, "\n") {
			if line := strings.TrimSpace(line); strings.HasPrefix(line, "cf ") {
				examples = append(examples, line)
			}
		}
		if cmd.RunE != nil && cmd.Long != "" && len(examples) == 0 {
			t.Errorf("%s: Long has no example", path)
		}
		seen := map[string]bool{}
		for _, e := range examples {
			if seen[e] {
				t.Errorf("%s: duplicate example %q", path, e)
			}
			seen[e] = true
		}
		for _, sub := range cmd.Commands() {
			walk(sub, path+" "+sub.Name())
		}
	}
	walk(tunnelCmd, "cf tunnel")
}

func TestTunnelHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"tunnel", "create", "--help"}, []string{"cf tunnel create", "--config-src", "--secret"}},
		{[]string{"tunnel", "list", "--help"}, []string{"cf tunnel list", "--include-deleted", "--status"}},
		{[]string{"tunnel", "token", "--help"}, []string{"cf tunnel token"}},
		{[]string{"tunnel", "config", "set", "--help"}, []string{"cf tunnel config set", "--file"}},
		{[]string{"tunnel", "route", "add", "--help"}, []string{"cf tunnel route add", "--tunnel"}},
		{[]string{"tunnel", "route", "remove", "--help"}, []string{"cf tunnel route remove", "--force"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
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
