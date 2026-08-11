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

const sslCertsTestZoneID = "0123456789abcdef0123456789abcdef"
const sslCertsTestAccountID = "023e105f4ecef8ad9ca31a8372d0c353"

func runSSLCertsCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--zone-id", sslCertsTestZoneID,
		"--account-id", sslCertsTestAccountID,
	}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestSSLCertsPacksOrderDryRunCanonicalWire(t *testing.T) {
	zoneReads, posted := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/zones/"+sslCertsTestZoneID {
			zoneReads++
			_, _ = io.WriteString(w, `{"success":true,"result":{"name":"example.com"}}`)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/zones/"+sslCertsTestZoneID+"/ssl/certificate_packs/order" {
			posted = true
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
	}))
	defer server.Close()

	stdout, _, err := runSSLCertsCLI(t, server.URL,
		"ssl-certs", "packs", "order",
		"--host", "example.com",
		"--host", "*.example.com",
		"--certificate-authority", " LETS_ENCRYPT ",
		"--validation-method", " TXT ",
		"--validity-days", "14",
		"--cloudflare-branding=false",
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
		t.Fatalf("decode dry run: %v\n%s", err, stdout)
	}
	if dump.Method != "POST" || !strings.HasSuffix(dump.URL, "/zones/"+sslCertsTestZoneID+"/ssl/certificate_packs/order") {
		t.Fatalf("request = %s %s", dump.Method, dump.URL)
	}
	sslCertsAssertJSONEqual(t, dump.Body, `{"certificate_authority":"lets_encrypt","hosts":["example.com","*.example.com"],"type":"advanced","validation_method":"txt","validity_days":14,"cloudflare_branding":false}`)
	if zoneReads != 1 {
		t.Fatalf("zone apex reads = %d, want 1", zoneReads)
	}
	if posted {
		t.Fatal("dry run sent an order POST")
	}
}

func TestSSLCertsPacksOrderValidation(t *testing.T) {
	validHosts := make([]string, sslCertsMaxHosts)
	for i := range validHosts {
		validHosts[i] = "host.example.com"
	}
	tooManyHosts := append(append([]string(nil), validHosts...), "too-many.example.com")

	cases := []struct {
		name     string
		hosts    []string
		validity int
		wantErr  string
	}{
		{"no hosts", nil, 14, "between 1 and 50"},
		{"maximum hosts", validHosts, 14, ""},
		{"too many hosts", tooManyHosts, 14, "between 1 and 50"},
		{"validity below documented minimum", []string{"example.com"}, 13, "--validity-days"},
		{"validity at documented minimum", []string{"example.com"}, 14, ""},
		{"validity at documented maximum", []string{"example.com"}, 365, ""},
		{"validity above documented maximum", []string{"example.com"}, 366, "--validity-days"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildSSLCertsPacksOrderBody("GOOGLE", tc.hosts, "", "HTTP", tc.validity, false, false)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestSSLCertsJSONArraysRejectNullAndWrongShapes(t *testing.T) {
	cases := []string{"null", `{}`, `"example.com"`, `[null]`, `[1]`}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := sslCertsJSONArrayOfStrings("hosts", raw); err == nil {
				t.Fatalf("accepted invalid JSON shape %s", raw)
			}
		})
	}
	values, err := sslCertsJSONArrayOfStrings("hosts", `["example.com","*.example.com"]`)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(values, ","); got != "example.com,*.example.com" {
		t.Fatalf("values = %q", got)
	}
}

func TestSSLCertsListBounds(t *testing.T) {
	tests := []struct {
		name       string
		page       int
		perPage    int
		pageSet    bool
		perPageSet bool
		wantErr    string
	}{
		{"page below minimum", 0, 0, true, false, "at least 1"},
		{"page at minimum", 1, 0, true, false, ""},
		{"per page below minimum", 0, 4, false, true, "between 5 and 50"},
		{"per page at minimum", 0, 5, false, true, ""},
		{"per page at maximum", 0, 50, false, true, ""},
		{"per page above maximum", 0, 51, false, true, "between 5 and 50"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			query, err := buildSSLCertsPacksListQuery(" PRODUCTION ", tc.page, tc.perPage, true, tc.pageSet, tc.perPageSet)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				if query.Get("deploy") != "production" || query.Get("status") != "all" {
					t.Fatalf("query = %s", query.Encode())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestSSLCertsOriginCACreateContractAndWire(t *testing.T) {
	body, err := buildSSLCertsOriginCACreateBody("CSR", strings.NewReader(""), nil, `["example.com","*.example.com"]`, " ORIGIN-ECC ", 5475, true)
	if err != nil {
		t.Fatal(err)
	}
	sslCertsAssertJSONEqual(t, body, `{"csr":"CSR","hostnames":["example.com","*.example.com"],"request_type":"origin-ecc","requested_validity":5475}`)

	for _, validity := range []int{6, 5476} {
		if _, err := buildSSLCertsOriginCACreateBody("CSR", strings.NewReader(""), []string{"example.com"}, "", "origin-rsa", validity, true); err == nil || !strings.Contains(err.Error(), "--requested-validity") {
			t.Fatalf("validity %d error = %v", validity, err)
		}
	}
	if _, err := buildSSLCertsOriginCACreateBody("CSR", strings.NewReader(""), []string{"example.com"}, "", "origin-rsa", 7, true); err != nil {
		t.Fatalf("validity at documented minimum: %v", err)
	}
	if _, err := buildSSLCertsOriginCACreateBody("CSR", strings.NewReader(""), nil, "null", "origin-rsa", 0, false); err == nil || !strings.Contains(err.Error(), "JSON array") {
		t.Fatalf("null hostnames error = %v", err)
	}
}

func TestSSLCertsOriginCAHostnameValidation(t *testing.T) {
	valid := []string{"example.com", "www.example.com", "*.example.com", "bücher.example"}
	if err := sslCertsValidateOriginCAHostnames(valid); err != nil {
		t.Fatalf("valid hostnames rejected: %v", err)
	}
	for _, hostname := range []string{"example", "*.com", "*.*.example.com", "foo.*.example.com", "www_example.com"} {
		t.Run(hostname, func(t *testing.T) {
			if err := sslCertsValidateOriginCAHostnames([]string{hostname}); err == nil {
				t.Fatalf("accepted invalid Origin CA hostname %q", hostname)
			}
		})
	}
}

func TestSSLCertsMTLSContractAndWire(t *testing.T) {
	body, err := buildSSLCertsMTLSUploadBody("CERT", "KEY", "internal-root", false, true, strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	sslCertsAssertJSONEqual(t, body, `{"ca":false,"certificates":"CERT","private_key":"KEY","name":"internal-root"}`)
	if _, err := buildSSLCertsMTLSUploadBody("CERT", "", "", false, false, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "--ca") {
		t.Fatalf("missing ca error = %v", err)
	}
	query, err := buildSSLCertsMTLSListQuery([]string{" CUSTOM,gateway_managed ", "ACCESS_MANAGED"})
	if err != nil {
		t.Fatal(err)
	}
	if got := query.Get("type"); got != "custom,gateway_managed,access_managed" {
		t.Fatalf("type = %q", got)
	}
}

func TestSSLCertsCommandsUseExactEndpointScopes(t *testing.T) {
	requests := make(chan *http.Request, 16)
	bodies := make(chan []byte, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- r
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/zones/"+sslCertsTestZoneID {
			_, _ = io.WriteString(w, `{"success":true,"result":{"name":"example.com"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
	}))
	defer server.Close()

	cases := []struct {
		args       []string
		wantMethod string
		wantPath   string
		wantBody   string
	}{
		{
			args:       []string{"ssl-certs", "packs", "list", "--deploy", "STAGING", "--all-statuses", "--page", "1", "--per-page", "5", "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/zones/" + sslCertsTestZoneID + "/ssl/certificate_packs",
		},
		{
			args:       []string{"ssl-certs", "packs", "order", "--host", "example.com", "--certificate-authority", "ssl_com", "--validation-method", "email", "--validity-days", "365"},
			wantMethod: "POST",
			wantPath:   "/zones/" + sslCertsTestZoneID + "/ssl/certificate_packs/order",
			wantBody:   `{"certificate_authority":"ssl_com","hosts":["example.com"],"type":"advanced","validation_method":"email","validity_days":365}`,
		},
		{
			args:       []string{"ssl-certs", "origin-ca", "list", "--page", "1", "--per-page", "50", "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/certificates",
		},
		{
			args:       []string{"ssl-certs", "origin-ca", "get", "origin-cert"},
			wantMethod: "GET",
			wantPath:   "/certificates/origin-cert",
		},
		{
			args:       []string{"ssl-certs", "origin-ca", "create", "--csr", "CSR", "--hostname", "example.com", "--request-type", "origin-rsa"},
			wantMethod: "POST",
			wantPath:   "/certificates",
			wantBody:   `{"csr":"CSR","hostnames":["example.com"],"request_type":"origin-rsa"}`,
		},
		{
			args:       []string{"ssl-certs", "origin-ca", "revoke", "origin-cert", "--force"},
			wantMethod: "DELETE",
			wantPath:   "/certificates/origin-cert",
		},
		{
			args:       []string{"ssl-certs", "mtls", "list", "--type", "CUSTOM", "--output", "json"},
			wantMethod: "GET",
			wantPath:   "/accounts/" + sslCertsTestAccountID + "/mtls_certificates",
		},
		{
			args:       []string{"ssl-certs", "mtls", "upload", "--certificates", "CERT", "--ca=false"},
			wantMethod: "POST",
			wantPath:   "/accounts/" + sslCertsTestAccountID + "/mtls_certificates",
			wantBody:   `{"ca":false,"certificates":"CERT"}`,
		},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args[1:3], " "), func(t *testing.T) {
			if _, _, err := runSSLCertsCLI(t, server.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			var req *http.Request
			var body []byte
			for {
				req = <-requests
				body = <-bodies
				if req.Method == tc.wantMethod && req.URL.Path == tc.wantPath {
					break
				}
				if tc.wantPath == "/zones/"+sslCertsTestZoneID+"/ssl/certificate_packs/order" && req.Method == "GET" && req.URL.Path == "/zones/"+sslCertsTestZoneID {
					continue
				}
				t.Fatalf("unexpected request = %s %s", req.Method, req.URL.Path)
			}
			if req.Method != tc.wantMethod || req.URL.Path != tc.wantPath {
				t.Fatalf("request = %s %s, want %s %s", req.Method, req.URL.Path, tc.wantMethod, tc.wantPath)
			}
			if tc.wantBody != "" {
				sslCertsAssertJSONEqual(t, body, tc.wantBody)
			}
			if req.URL.Path == "/certificates" && req.Method == "GET" && req.URL.Query().Get("zone_id") != sslCertsTestZoneID {
				t.Fatalf("origin list zone_id = %q", req.URL.Query().Get("zone_id"))
			}
			if strings.Contains(req.URL.Path, "/ssl/certificate_packs") && req.Method == "GET" {
				query := req.URL.Query()
				if query.Get("deploy") != "staging" || query.Get("status") != "all" || query.Get("page") != "1" || query.Get("per_page") != "5" {
					t.Fatalf("packs list query = %s", query.Encode())
				}
			}
			if req.URL.Path == "/certificates" && req.Method == "GET" {
				query := req.URL.Query()
				if query.Get("page") != "1" || query.Get("per_page") != "50" {
					t.Fatalf("origin list query = %s", query.Encode())
				}
			}
			if req.URL.Path == "/accounts/"+sslCertsTestAccountID+"/mtls_certificates" && req.Method == "GET" && req.URL.Query().Get("type") != "custom" {
				t.Fatalf("mTLS list type = %q", req.URL.Query().Get("type"))
			}
		})
	}
}

func TestSSLCertsZoneNameResolutionUsesInteractiveResolver(t *testing.T) {
	var requests []*http.Request
	lookupQueryValid := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/zones" {
			if r.URL.Query().Get("name") != "example.com" {
				lookupQueryValid = false
			}
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"`+sslCertsTestZoneID+`","name":"example.com"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
	}))
	defer server.Close()

	for _, args := range [][]string{
		{"ssl-certs", "packs", "list", "--zone", "example.com", "--output", "json"},
		{"ssl-certs", "origin-ca", "list", "--zone", "example.com", "--output", "json"},
	} {
		if _, _, err := runSSLCertsCLI(t, server.URL, args...); err != nil {
			t.Fatal(err)
		}
	}
	if len(requests) != 4 {
		t.Fatalf("request count = %d, want 4", len(requests))
	}
	if !lookupQueryValid {
		t.Fatal("zone resolver did not look up the requested zone name")
	}
	for _, req := range []*http.Request{requests[0], requests[2]} {
		if req.Method != "GET" || req.URL.Path != "/zones" || req.URL.Query().Get("name") != "example.com" {
			t.Fatalf("resolver request = %s %s?%s", req.Method, req.URL.Path, req.URL.RawQuery)
		}
	}
}

func TestSSLCertsPacksOrderRequiresResolvedZoneApex(t *testing.T) {
	posted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/zones/"+sslCertsTestZoneID {
			_, _ = io.WriteString(w, `{"success":true,"result":{"name":"example.com"}}`)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/zones/"+sslCertsTestZoneID+"/ssl/certificate_packs/order" {
			posted = true
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
	}))
	defer server.Close()

	_, _, err := runSSLCertsCLI(t, server.URL,
		"ssl-certs", "packs", "order",
		"--host", "www.example.com",
		"--certificate-authority", "google",
		"--validation-method", "txt",
		"--validity-days", "14",
	)
	if err == nil || !strings.Contains(err.Error(), "include the zone apex") {
		t.Fatalf("error = %v", err)
	}
	if posted {
		t.Fatal("order POST was sent without the resolved zone apex")
	}
	if err := sslCertsRequireZoneApex([]string{"www.example.com", "EXAMPLE.COM."}, "example.com"); err != nil {
		t.Fatalf("present apex rejected: %v", err)
	}
}

func TestSSLCertsPacksOrderDryRunRejectsMissingResolvedZoneApex(t *testing.T) {
	zoneReads, posted := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" && r.URL.Path == "/zones/"+sslCertsTestZoneID {
			zoneReads++
			_, _ = io.WriteString(w, `{"success":true,"result":{"name":"example.com"}}`)
			return
		}
		if r.Method == "POST" && r.URL.Path == "/zones/"+sslCertsTestZoneID+"/ssl/certificate_packs/order" {
			posted = true
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
	}))
	defer server.Close()

	stdout, _, err := runSSLCertsCLI(t, server.URL,
		"ssl-certs", "packs", "order",
		"--host", "www.example.com",
		"--certificate-authority", "google",
		"--validation-method", "txt",
		"--validity-days", "14",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "include the zone apex") {
		t.Fatalf("error = %v", err)
	}
	if stdout != "" {
		t.Fatalf("dry run stdout = %q, want empty on validation error", stdout)
	}
	if zoneReads != 1 {
		t.Fatalf("zone apex reads = %d, want 1", zoneReads)
	}
	if posted {
		t.Fatal("dry run sent an order POST without the zone apex")
	}
}

func TestSSLCertsInvalidInputDoesNotResolveZone(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, _, err := runSSLCertsCLI(t, server.URL,
		"ssl-certs", "packs", "order",
		"--zone", "example.com",
		"--host", "example.com",
		"--certificate-authority", "invalid",
		"--validation-method", "txt",
		"--validity-days", "14",
	)
	if err == nil || !strings.Contains(err.Error(), "--certificate-authority") {
		t.Fatalf("error = %v", err)
	}
	if hits != 0 {
		t.Fatalf("invalid input made %d HTTP request(s)", hits)
	}
}

func sslCertsAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode actual JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &wantValue); err != nil {
		t.Fatalf("decode expected JSON: %v\n%s", err, want)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("JSON mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}
