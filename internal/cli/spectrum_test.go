package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const spectrumTestZoneID = "0123456789abcdef0123456789abcdef"
const spectrumTestAppID = "ea95132c15732412d22c1476fa83f27a"

// runSpectrumCLI drives the real command tree against a test server with a
// throwaway config dir and no ambient Cloudflare env vars, so zone resolution
// depends only on the arguments (explicit --zone, global --zone-id, or the
// interactive-compatible missing-zone path).
func runSpectrumCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CF_PROFILE", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_ZONE_ID", "")
	t.Setenv("CF_ZONE_ID", "")
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"--base-url", serverURL, "--token", "test-token"}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func spectrumAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func TestParseSpectrumOriginPortBounds(t *testing.T) {
	// Both sides of the documented 1..65535 bound.
	if _, err := parseSpectrumOriginPort("0"); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("port 0: %v", err)
	}
	got, err := parseSpectrumOriginPort("1")
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("port 1 = %v (%T)", got, got)
	}
	got, err = parseSpectrumOriginPort("65535")
	if err != nil {
		t.Fatal(err)
	}
	if got != 65535 {
		t.Fatalf("port 65535 = %v", got)
	}
	if _, err := parseSpectrumOriginPort("65536"); err == nil || !strings.Contains(err.Error(), "between 1 and 65535") {
		t.Fatalf("port 65536: %v", err)
	}

	// Range: wire value is a string, bounds applied to both ends.
	got, err = parseSpectrumOriginPort("1000-2000")
	if err != nil {
		t.Fatal(err)
	}
	if got != "1000-2000" {
		t.Fatalf("range = %v", got)
	}
	if _, err := parseSpectrumOriginPort("0-10"); err == nil {
		t.Fatal("expected range lower-bound error")
	}
	if _, err := parseSpectrumOriginPort("1-65536"); err == nil {
		t.Fatal("expected range upper-bound error")
	}
	if _, err := parseSpectrumOriginPort("2000-1000"); err == nil || !strings.Contains(err.Error(), "start must be <=") {
		t.Fatalf("inverted range: %v", err)
	}
}

func TestParseSpectrumEdgeIPsJSONRejectsNullAndWrongShapes(t *testing.T) {
	cases := []string{
		`null`,
		`[]`,
		`"dynamic"`,
		`42`,
		`true`,
		`{`,
		``,
	}
	for _, raw := range cases {
		if _, err := parseSpectrumEdgeIPsJSON(raw); err == nil {
			t.Fatalf("expected rejection for %q", raw)
		}
	}
	obj, err := parseSpectrumEdgeIPsJSON(`{"type":"DYNAMIC","connectivity":"ALL"}`)
	if err != nil {
		t.Fatal(err)
	}
	// Canonical wire values, not the case-insensitive inputs.
	if obj["type"] != "dynamic" || obj["connectivity"] != "all" {
		t.Fatalf("canonical edge_ips = %#v", obj)
	}
	obj, err = parseSpectrumEdgeIPsJSON(`{"type":"static","ips":["192.0.2.1","192.0.2.2"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if obj["type"] != "static" {
		t.Fatalf("type = %v", obj["type"])
	}
	if _, err := parseSpectrumEdgeIPsJSON(`{"type":"static","ips":null}`); err == nil {
		t.Fatal("expected null ips rejection")
	}
	if _, err := parseSpectrumEdgeIPsJSON(`{"type":"static","ips":[]}`); err == nil {
		t.Fatal("expected empty ips rejection")
	}
	if _, err := parseSpectrumEdgeIPsJSON(`{"type":"static","ips":[1]}`); err == nil {
		t.Fatal("expected non-string ips rejection")
	}
}

func TestSpectrumOriginDNSTTLBounds(t *testing.T) {
	// Minimum is 600 inclusive.
	_, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "create",
		"--zone", spectrumTestZoneID,
		"--protocol", "tcp/22",
		"--dns-name", "ssh.example.com",
		"--dns-type", "CNAME",
		"--origin-dns", "origin.example.com",
		"--origin-dns-ttl", "599",
		"--origin-port", "22",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "at least 600") {
		t.Fatalf("ttl 599: %v", err)
	}

	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "create",
		"--zone", spectrumTestZoneID,
		"--protocol", "tcp/22",
		"--dns-name", "ssh.example.com",
		"--dns-type", "CNAME",
		"--origin-dns", "origin.example.com",
		"--origin-dns-ttl", "600",
		"--origin-port", "22",
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
	spectrumAssertJSONEqual(t, dump.Body, `{
		"protocol":"tcp/22",
		"dns":{"name":"ssh.example.com","type":"CNAME"},
		"traffic_type":"direct",
		"origin_dns":{"name":"origin.example.com","ttl":600},
		"origin_port":22
	}`)
}

func TestSpectrumCreateDryRunDirectOrigin(t *testing.T) {
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "create",
		"--zone", spectrumTestZoneID,
		"--protocol", "tcp/22",
		"--dns-name", "ssh.example.com",
		"--dns-type", "cname",
		"--traffic-type", "DIRECT",
		"--origin-direct", "tcp://192.0.2.1:22",
		"--proxy-protocol", "OFF",
		"--tls", "FULL",
		"--ip-firewall=true",
		"--argo-smart-routing=true",
		"--edge-ips-type", "dynamic",
		"--edge-ips-connectivity", "ALL",
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
	if dump.Method != "POST" {
		t.Fatalf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/zones/"+spectrumTestZoneID+"/spectrum/apps") {
		t.Fatalf("url = %s", dump.URL)
	}
	// Canonical wire enums (lowercase/UPPER as API expects).
	spectrumAssertJSONEqual(t, dump.Body, `{
		"protocol":"tcp/22",
		"dns":{"name":"ssh.example.com","type":"CNAME"},
		"traffic_type":"direct",
		"origin_direct":["tcp://192.0.2.1:22"],
		"proxy_protocol":"off",
		"tls":"full",
		"ip_firewall":true,
		"argo_smart_routing":true,
		"edge_ips":{"type":"dynamic","connectivity":"all"}
	}`)
}

func TestSpectrumCreateDryRunDNSOriginAndPortRange(t *testing.T) {
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "create",
		"--zone", spectrumTestZoneID,
		"--protocol", "tcp/1000-2000",
		"--dns-name", "range.example.com",
		"--dns-type", "ADDRESS",
		"--origin-dns", "origin.example.com",
		"--origin-dns-type", "A",
		"--origin-port", "3000-4000",
		"--edge-ips", `{"type":"static","ips":["192.0.2.1"]}`,
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
	spectrumAssertJSONEqual(t, dump.Body, `{
		"protocol":"tcp/1000-2000",
		"dns":{"name":"range.example.com","type":"ADDRESS"},
		"traffic_type":"direct",
		"origin_dns":{"name":"origin.example.com","type":"A"},
		"origin_port":"3000-4000",
		"edge_ips":{"type":"static","ips":["192.0.2.1"]}
	}`)
}

func TestSpectrumCreateValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing origin",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--dry-run"},
			want: "origin is required",
		},
		{
			name: "mixed origins",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:22", "--origin-dns", "origin.example.com", "--dry-run"},
			want: "mutually exclusive",
		},
		{
			name: "invalid traffic type",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:22", "--traffic-type", "quic", "--dry-run"},
			want: "--traffic-type",
		},
		{
			name: "invalid proxy protocol",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:22", "--proxy-protocol", "v3", "--dry-run"},
			want: "--proxy-protocol",
		},
		{
			name: "invalid tls",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:22", "--tls", "max", "--dry-run"},
			want: "--tls",
		},
		{
			name: "static edge without ips",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "ADDRESS", "--origin-direct", "tcp://192.0.2.1:22", "--edge-ips-type", "static", "--dry-run"},
			want: "--edge-ip",
		},
		{
			name: "edge json null",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:22", "--edge-ips", "null", "--dry-run"},
			want: "JSON object",
		},
		{
			name: "vnet with origin dns",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-dns", "origin.example.com", "--origin-port", "22", "--virtual-network-id", "f70ff985-a4ef-4643-bbbc-4a0ed4fc8415", "--dry-run"},
			want: "origin-direct",
		},
		{
			name: "vnet with multiple origins",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://10.0.0.5:22", "--origin-direct", "tcp://10.0.0.6:22", "--virtual-network-id", "f70ff985-a4ef-4643-bbbc-4a0ed4fc8415", "--proxy-protocol", "off", "--dry-run"},
			want: "exactly one",
		},
		{
			name: "vnet with proxy protocol on",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://10.0.0.5:22", "--virtual-network-id", "f70ff985-a4ef-4643-bbbc-4a0ed4fc8415", "--proxy-protocol", "v1", "--dry-run"},
			want: "proxy-protocol off",
		},
		{
			name: "vnet not a UUID",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://10.0.0.5:22", "--virtual-network-id", "not-a-uuid", "--proxy-protocol", "off", "--dry-run"},
			want: "UUID",
		},
		{
			name: "port below minimum",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-dns", "origin.example.com", "--origin-port", "0", "--dry-run"},
			want: "between 1 and 65535",
		},
		{
			name: "port above maximum",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-dns", "origin.example.com", "--origin-port", "65536", "--dry-run"},
			want: "between 1 and 65535",
		},
		{
			name: "unequal port range widths",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/1000-2000", "--dns-name", "range.example.com", "--dns-type", "CNAME", "--origin-dns", "origin.example.com", "--origin-port", "3000-3500", "--dry-run"},
			want: "must match protocol port range width",
		},
		{
			name: "dynamic edge with ADDRESS dns",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "ADDRESS", "--origin-direct", "tcp://192.0.2.1:22", "--edge-ips-type", "dynamic", "--dry-run"},
			want: "CNAME",
		},
		{
			name: "static edge with CNAME dns",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/22", "--dns-name", "ssh.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:22", "--edge-ips-type", "static", "--edge-ip", "192.0.2.9", "--dry-run"},
			want: "ADDRESS",
		},
		{
			name: "argo with http traffic",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "tcp/80", "--dns-name", "web.example.com", "--dns-type", "CNAME", "--origin-direct", "tcp://192.0.2.1:80", "--traffic-type", "http", "--argo-smart-routing", "--dry-run"},
			want: "traffic-type direct",
		},
		{
			name: "ip firewall on udp",
			args: []string{"spectrum", "create", "--zone", spectrumTestZoneID, "--protocol", "udp/53", "--dns-name", "dns.example.com", "--dns-type", "CNAME", "--origin-direct", "udp://192.0.2.1:53", "--ip-firewall", "--dry-run"},
			want: "TCP",
		},
		{
			name: "invalid list order before client",
			args: []string{"spectrum", "list", "--zone", spectrumTestZoneID, "--order", "name", "--dry-run"},
			want: "--order",
		},
		{
			name: "invalid list direction before client",
			args: []string{"spectrum", "list", "--zone", spectrumTestZoneID, "--direction", "up", "--dry-run"},
			want: "--direction",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runSpectrumCLI(t, "http://example.invalid", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestSpectrumCreateValidatesBeforeClient(t *testing.T) {
	// Bad local input must fail without constructing a client that would
	// attempt name-resolution traffic against the invalid base URL.
	_, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "create",
		"--zone", "example.com", // would need lookup
		"--protocol", "tcp/22",
		"--dns-name", "ssh.example.com",
		"--dns-type", "CNAME",
		// missing origin — local validation
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "origin is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestSpectrumListDryRunPathAndQuery(t *testing.T) {
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "list",
		"--zone", spectrumTestZoneID,
		"--order", "created_on",
		"--direction", "desc",
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
	if dump.Method != "GET" {
		t.Fatalf("method = %s", dump.Method)
	}
	if !strings.Contains(dump.URL, "/zones/"+spectrumTestZoneID+"/spectrum/apps") {
		t.Fatalf("url = %s", dump.URL)
	}
	if !strings.Contains(dump.URL, "order=created_on") || !strings.Contains(dump.URL, "direction=desc") {
		t.Fatalf("query missing order/direction: %s", dump.URL)
	}
	// Spectrum requires page whenever pagination params are used.
	if !strings.Contains(dump.URL, "per_page=100") || !strings.Contains(dump.URL, "page=1") {
		t.Fatalf("expected page=1 and per_page=100: %s", dump.URL)
	}
}

func TestSpectrumGetDeleteDryRunPaths(t *testing.T) {
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "get", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
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
	if dump.Method != "GET" || !strings.HasSuffix(dump.URL, "/zones/"+spectrumTestZoneID+"/spectrum/apps/"+spectrumTestAppID) {
		t.Fatalf("get = %s %s", dump.Method, dump.URL)
	}

	stdout, _, err = runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "delete", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--force",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.Method != "DELETE" || !strings.HasSuffix(dump.URL, "/zones/"+spectrumTestZoneID+"/spectrum/apps/"+spectrumTestAppID) {
		t.Fatalf("delete = %s %s", dump.Method, dump.URL)
	}
}

func TestSpectrumHTTPCommands(t *testing.T) {
	existing := `{
		"id":"` + spectrumTestAppID + `",
		"created_on":"2014-01-01T05:20:00.12345Z",
		"modified_on":"2014-01-01T05:20:00.12345Z",
		"protocol":"tcp/22",
		"dns":{"name":"ssh.example.com","type":"CNAME"},
		"traffic_type":"direct",
		"origin_direct":["tcp://192.0.2.1:22"],
		"proxy_protocol":"off",
		"tls":"off",
		"edge_ips":{"type":"dynamic","connectivity":"all"}
	}`

	cases := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantBody   string
		response   string
	}{
		{
			name:       "list",
			args:       []string{"spectrum", "list", "--zone", spectrumTestZoneID, "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + spectrumTestZoneID + "/spectrum/apps",
			response:   `{"success":true,"result":[` + existing + `],"result_info":{"page":1,"per_page":20,"total_pages":1}}`,
		},
		{
			name:       "get",
			args:       []string{"spectrum", "get", spectrumTestAppID, "--zone", spectrumTestZoneID},
			wantMethod: "GET",
			wantPath:   "/zones/" + spectrumTestZoneID + "/spectrum/apps/" + spectrumTestAppID,
			response:   `{"success":true,"result":` + existing + `}`,
		},
		{
			name: "create",
			args: []string{
				"spectrum", "create",
				"--zone", spectrumTestZoneID,
				"--protocol", "tcp/22",
				"--dns-name", "ssh.example.com",
				"--dns-type", "CNAME",
				"--origin-direct", "tcp://192.0.2.1:22",
				"--proxy-protocol", "off",
				"--tls", "full",
			},
			wantMethod: "POST",
			wantPath:   "/zones/" + spectrumTestZoneID + "/spectrum/apps",
			wantBody: `{
				"protocol":"tcp/22",
				"dns":{"name":"ssh.example.com","type":"CNAME"},
				"traffic_type":"direct",
				"origin_direct":["tcp://192.0.2.1:22"],
				"proxy_protocol":"off",
				"tls":"full"
			}`,
			response: `{"success":true,"result":` + existing + `}`,
		},
		{
			name:       "delete",
			args:       []string{"spectrum", "delete", spectrumTestAppID, "--zone", spectrumTestZoneID, "--force"},
			wantMethod: "DELETE",
			wantPath:   "/zones/" + spectrumTestZoneID + "/spectrum/apps/" + spectrumTestAppID,
			response:   `{"success":true,"result":{"id":"` + spectrumTestAppID + `"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				if r.Body != nil {
					gotBody, _ = io.ReadAll(r.Body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()

			stdout, _, err := runSpectrumCLI(t, srv.URL, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != tc.wantMethod {
				t.Fatalf("method = %s, want %s", gotMethod, tc.wantMethod)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("path = %s, want %s", gotPath, tc.wantPath)
			}
			if tc.wantBody != "" {
				spectrumAssertJSONEqual(t, gotBody, tc.wantBody)
			}
			if !json.Valid([]byte(strings.TrimSpace(stdout))) && tc.name != "list" {
				// list may be table by default; we requested json for list.
				if tc.name == "get" || tc.name == "create" || tc.name == "delete" {
					if !json.Valid([]byte(strings.TrimSpace(stdout))) {
						t.Fatalf("stdout not JSON: %s", stdout)
					}
				}
			}
			if tc.name == "list" {
				if !strings.Contains(stdout, spectrumTestAppID) {
					t.Fatalf("list json missing app id: %s", stdout)
				}
			}
			if tc.name == "get" {
				if !strings.Contains(stdout, "ssh.example.com") {
					t.Fatalf("get json missing dns name: %s", stdout)
				}
			}
		})
	}
}

func TestSpectrumListTableOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zones/"+spectrumTestZoneID+"/spectrum/apps" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %s", r.URL.Query().Get("per_page"))
		}
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("page = %s", r.URL.Query().Get("page"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"result":[{
			"id":"`+spectrumTestAppID+`",
			"protocol":"tcp/22",
			"dns":{"name":"ssh.example.com","type":"CNAME"},
			"traffic_type":"direct",
			"origin_direct":["tcp://192.0.2.1:22"]
		}],"result_info":{"page":1,"per_page":100,"total_pages":1}}`)
	}))
	defer srv.Close()

	stdout, _, err := runSpectrumCLI(t, srv.URL, "spectrum", "list", "--zone", spectrumTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "PROTOCOL", "DNS", spectrumTestAppID, "tcp/22", "ssh.example.com", "tcp://192.0.2.1:22"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q: %s", want, stdout)
		}
	}
}

func TestSpectrumListTwoPageLoop(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/zones/"+spectrumTestZoneID+"/spectrum/apps" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = io.WriteString(w, `{"success":true,"result":[
				{"id":"app-page1","protocol":"tcp/22","dns":{"name":"a.example.com","type":"CNAME"},"traffic_type":"direct","origin_direct":["tcp://192.0.2.1:22"]}
			],"result_info":{"page":1,"per_page":100,"total_pages":2,"count":1,"total_count":2}}`)
		case "2":
			_, _ = io.WriteString(w, `{"success":true,"result":[
				{"id":"app-page2","protocol":"tcp/443","dns":{"name":"b.example.com","type":"CNAME"},"traffic_type":"direct","origin_direct":["tcp://192.0.2.2:443"]}
			],"result_info":{"page":2,"per_page":100,"total_pages":2,"count":1,"total_count":2}}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
			_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
		}
	}))
	defer srv.Close()

	stdout, _, err := runSpectrumCLI(t, srv.URL,
		"spectrum", "list",
		"--zone", spectrumTestZoneID,
		"--order", "created_on",
		"--direction", "desc",
		"--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries = %v, want 2 pages", queries)
	}
	// Exact query strings: page + per_page always, order/direction preserved.
	wantQ := []string{
		"direction=desc&order=created_on&page=1&per_page=100",
		"direction=desc&order=created_on&page=2&per_page=100",
	}
	// url.Values encoding order may vary; compare as parsed maps.
	for i, q := range queries {
		got, err := url.ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		want, err := url.ParseQuery(wantQ[i])
		if err != nil {
			t.Fatal(err)
		}
		if got.Encode() != want.Encode() {
			t.Fatalf("page %d query = %q, want %q", i+1, got.Encode(), want.Encode())
		}
	}
	if !strings.Contains(stdout, "app-page1") || !strings.Contains(stdout, "app-page2") {
		t.Fatalf("merged list missing apps: %s", stdout)
	}
}

func TestSpectrumUpdateFetchMergePut(t *testing.T) {
	var methods []string
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		wantPath := "/zones/" + spectrumTestZoneID + "/spectrum/apps/" + spectrumTestAppID
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			// Include an unmodeled field so the raw-map merge must preserve it.
			_, _ = io.WriteString(w, `{"success":true,"result":{
				"id":"`+spectrumTestAppID+`",
				"created_on":"2014-01-01T05:20:00.12345Z",
				"modified_on":"2014-01-01T05:20:00.12345Z",
				"protocol":"tcp/22",
				"dns":{"name":"ssh.example.com","type":"CNAME"},
				"traffic_type":"direct",
				"origin_direct":["tcp://192.0.2.1:22"],
				"proxy_protocol":"off",
				"tls":"off",
				"edge_ips":{"type":"dynamic","connectivity":"all"},
				"future_field":{"nested":true,"n":1}
			}}`)
		case "PUT":
			putBody, _ = io.ReadAll(r.Body)
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"`+spectrumTestAppID+`","tls":"full"}}`)
		default:
			t.Errorf("unexpected method %s", r.Method)
			http.Error(w, "bad method", 500)
		}
	}))
	defer srv.Close()

	stdout, _, err := runSpectrumCLI(t, srv.URL,
		"spectrum", "update", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--tls", "FULL",
		"--ip-firewall=true",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "GET" || methods[1] != "PUT" {
		t.Fatalf("methods = %v", methods)
	}
	var body map[string]any
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["tls"] != "full" {
		t.Fatalf("tls = %v", body["tls"])
	}
	if body["ip_firewall"] != true {
		t.Fatalf("ip_firewall = %v", body["ip_firewall"])
	}
	// Read-only fields stripped; preserved fields still present.
	if _, ok := body["id"]; ok {
		t.Fatalf("id should be stripped from PUT body: %v", body)
	}
	if body["protocol"] != "tcp/22" {
		t.Fatalf("protocol preserved = %v", body["protocol"])
	}
	// Unknown API fields must survive the raw-object merge.
	ff, ok := body["future_field"].(map[string]any)
	if !ok || ff["nested"] != true {
		t.Fatalf("future_field not preserved: %v", body["future_field"])
	}
	if !strings.Contains(stdout, spectrumTestAppID) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestSpectrumUpdateOriginDirectClearsDNSOrigin(t *testing.T) {
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = io.WriteString(w, `{"success":true,"result":{
				"id":"`+spectrumTestAppID+`",
				"protocol":"tcp/22",
				"dns":{"name":"ssh.example.com","type":"CNAME"},
				"traffic_type":"direct",
				"origin_dns":{"name":"origin.example.com","ttl":600},
				"origin_port":22
			}}`)
			return
		}
		putBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{"success":true,"result":{}}`)
	}))
	defer srv.Close()

	_, _, err := runSpectrumCLI(t, srv.URL,
		"spectrum", "update", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--origin-direct", "tcp://192.0.2.9:22",
	)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["origin_dns"]; ok {
		t.Fatalf("origin_dns should be cleared: %v", body)
	}
	if _, ok := body["origin_port"]; ok {
		t.Fatalf("origin_port should be cleared: %v", body)
	}
	spectrumAssertJSONEqual(t, mustJSON(t, body["origin_direct"]), `["tcp://192.0.2.9:22"]`)
}

func TestSpectrumUpdateNothingToUpdate(t *testing.T) {
	_, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "update", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("error = %v", err)
	}
}

func TestSpectrumUpdateValidatesBeforeRead(t *testing.T) {
	// Invalid enum must fail before the GET against example.invalid.
	_, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "update", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--tls", "nope",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "--tls") {
		t.Fatalf("error = %v", err)
	}
}

func TestSpectrumUpdateCrossFieldAfterMerge(t *testing.T) {
	// GET returns ADDRESS dns + static edge; switching dns-type to CNAME
	// without rebuilding edge_ips must fail after merge, before PUT.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected method %s (should fail before PUT)", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"result":{
			"id":"`+spectrumTestAppID+`",
			"protocol":"tcp/22",
			"dns":{"name":"ssh.example.com","type":"ADDRESS"},
			"traffic_type":"direct",
			"origin_direct":["tcp://192.0.2.1:22"],
			"edge_ips":{"type":"static","ips":["192.0.2.9"]}
		}}`)
	}))
	defer srv.Close()

	_, _, err := runSpectrumCLI(t, srv.URL,
		"spectrum", "update", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--dns-type", "CNAME",
	)
	if err == nil || !strings.Contains(err.Error(), "static") || !strings.Contains(err.Error(), "ADDRESS") {
		t.Fatalf("error = %v, want static/ADDRESS mismatch after merge", err)
	}
}

func TestSpectrumUpdateOriginPortRequiresOriginDNS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"result":{
			"id":"`+spectrumTestAppID+`",
			"protocol":"tcp/22",
			"dns":{"name":"ssh.example.com","type":"CNAME"},
			"traffic_type":"direct",
			"origin_direct":["tcp://192.0.2.1:22"]
		}}`)
	}))
	defer srv.Close()

	_, _, err := runSpectrumCLI(t, srv.URL,
		"spectrum", "update", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
		"--origin-port", "22",
	)
	if err == nil || !strings.Contains(err.Error(), "origin_dns") {
		t.Fatalf("error = %v, want origin_port requires origin_dns", err)
	}
}

func TestSpectrumEqualPortRangeWidthsAccepted(t *testing.T) {
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "create",
		"--zone", spectrumTestZoneID,
		"--protocol", "tcp/1000-2000",
		"--dns-name", "range.example.com",
		"--dns-type", "CNAME",
		"--origin-dns", "origin.example.com",
		"--origin-port", "3000-4000",
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
	spectrumAssertJSONEqual(t, dump.Body, `{
		"protocol":"tcp/1000-2000",
		"dns":{"name":"range.example.com","type":"CNAME"},
		"traffic_type":"direct",
		"origin_dns":{"name":"origin.example.com"},
		"origin_port":"3000-4000"
	}`)
}

func TestSpectrumProtocolPortWidthBounds(t *testing.T) {
	if _, err := spectrumProtocolPortWidth("tcp/0"); err == nil {
		t.Fatal("expected port 0 rejection")
	}
	w, err := spectrumProtocolPortWidth("tcp/1")
	if err != nil || w != 1 {
		t.Fatalf("tcp/1 = %d, %v", w, err)
	}
	w, err = spectrumProtocolPortWidth("tcp/65535")
	if err != nil || w != 1 {
		t.Fatalf("tcp/65535 = %d, %v", w, err)
	}
	if _, err := spectrumProtocolPortWidth("tcp/65536"); err == nil {
		t.Fatal("expected port 65536 rejection")
	}
	w, err = spectrumProtocolPortWidth("tcp/1-1")
	if err != nil || w != 1 {
		t.Fatalf("tcp/1-1 = %d, %v", w, err)
	}
	w, err = spectrumProtocolPortWidth("tcp/1-2")
	if err != nil || w != 2 {
		t.Fatalf("tcp/1-2 = %d, %v", w, err)
	}
	if _, err := spectrumProtocolPortWidth("tcp/2-1"); err == nil {
		t.Fatal("expected inverted range rejection")
	}
}

func TestSpectrumUUIDValidation(t *testing.T) {
	if err := validateSpectrumUUID("--virtual-network-id", "f70ff985-a4ef-4643-bbbc-4a0ed4fc8415"); err != nil {
		t.Fatal(err)
	}
	if err := validateSpectrumUUID("--virtual-network-id", "f70ff985a4ef4643bbbc4a0ed4fc8415"); err != nil {
		t.Fatal(err)
	}
	if err := validateSpectrumUUID("--virtual-network-id", "not-a-uuid"); err == nil || !strings.Contains(err.Error(), "UUID") {
		t.Fatalf("err = %v", err)
	}
	if err := validateSpectrumUUID("--virtual-network-id", ""); err == nil {
		t.Fatal("expected empty rejection")
	}
}

// --- zone resolution (resolveZoneInteractive) --------------------------------

func TestSpectrumZoneResolutionExplicitFlag(t *testing.T) {
	// Explicit --zone (ID) wins; no ambient config.
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "list",
		"--zone", spectrumTestZoneID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump.URL, "/zones/"+spectrumTestZoneID+"/spectrum/apps") {
		t.Fatalf("url = %s", dump.URL)
	}
}

func TestSpectrumZoneResolutionConfiguredZoneID(t *testing.T) {
	// Global --zone-id supplies the configured zone when --zone is omitted.
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"--zone-id", spectrumTestZoneID,
		"spectrum", "get", spectrumTestAppID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(dump.URL, "/zones/"+spectrumTestZoneID+"/spectrum/apps/"+spectrumTestAppID) {
		t.Fatalf("url = %s", dump.URL)
	}
}

func TestSpectrumZoneResolutionNameLookup(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			if r.URL.Query().Get("name") != "example.com" {
				t.Errorf("zone lookup name = %q", r.URL.Query().Get("name"))
			}
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"`+spectrumTestZoneID+`","name":"example.com"}]}`)
		case r.Method == "GET" && r.URL.Path == "/zones/"+spectrumTestZoneID+"/spectrum/apps/"+spectrumTestAppID:
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"`+spectrumTestAppID+`","protocol":"tcp/22"}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.Error(w, "nope", 500)
		}
	}))
	defer srv.Close()

	stdout, _, err := runSpectrumCLI(t, srv.URL,
		"spectrum", "get", spectrumTestAppID,
		"--zone", "example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPaths) < 2 || gotPaths[0] != "GET /zones" {
		t.Fatalf("paths = %v, want zone lookup then app get", gotPaths)
	}
	if !strings.Contains(stdout, spectrumTestAppID) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestSpectrumZoneResolutionMissingIsInteractiveCompatible(t *testing.T) {
	// Non-TTY dry-run with no --zone and no configured zone must fail with the
	// resolveZoneInteractive guidance (not the old resolveZoneID-only message).
	_, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "list",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("err = %v, want no zone specified", err)
	}
	msg := err.Error()
	for _, want := range []string{"--zone", "CLOUDFLARE_ZONE_ID", "profile set", "interactively"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing zone error should mention %q: %v", want, err)
		}
	}
}

func TestSpectrumPathEscapesAppID(t *testing.T) {
	stdout, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "get", "app/id",
		"--zone", spectrumTestZoneID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump.URL, "/spectrum/apps/app%2Fid") {
		t.Fatalf("expected path-escaped app id: %s", dump.URL)
	}
}

func TestSpectrumCreateHelpExample(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"spectrum", "create", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"cf spectrum create", "--protocol", "--origin-direct", "--origin-dns", "--dns-name"} {
		if !strings.Contains(help, want) {
			t.Errorf("help missing %q: %s", want, help)
		}
	}
}

func TestSpectrumDeleteRequiresForceWithoutTTY(t *testing.T) {
	// Non-TTY stdin declines confirmation; without --force the command aborts
	// before network I/O.
	_, _, err := runSpectrumCLI(t, "http://example.invalid",
		"spectrum", "delete", spectrumTestAppID,
		"--zone", spectrumTestZoneID,
	)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("error = %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
