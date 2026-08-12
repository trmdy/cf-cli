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
	chTestZoneID   = "023e105f4ecef8ad9ca31a8372d0c353"
	chTestHostname = "app.customer.com"
	chTestID       = "0d89c70d-ad9f-4843-b99f-6cc0252067e9"
)

func runCustomHostnamesCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func chAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got not JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want not JSON: %v\n%s", err, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", gb, wb)
	}
}

func chParseDump(t *testing.T, stdout string) (method, url string, body json.RawMessage) {
	t.Helper()
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dump: %v\n%s", err, stdout)
	}
	return dump.Method, dump.URL, dump.Body
}

// --- body builders ---------------------------------------------------------

func TestBuildCustomHostnameCreateBodyDefaults(t *testing.T) {
	body, err := buildCustomHostnameCreateBody(customHostnameWriteOpts{
		Hostname:   chTestHostname,
		IncludeSSL: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{
		"hostname":"app.customer.com",
		"ssl":{"method":"http","type":"dv"}
	}`)
}

func TestBuildCustomHostnameCreateBodyFull(t *testing.T) {
	origin := "origin.example.com"
	sni := "sni.example.com"
	wild := true
	brand := true
	body, err := buildCustomHostnameCreateBody(customHostnameWriteOpts{
		Hostname:           chTestHostname,
		CustomOriginServer: &origin,
		CustomOriginSNI:    &sni,
		CustomMetadata:     map[string]string{"customer_id": "c1"},
		IncludeSSL:         true,
		SSLMethod:          "txt",
		SSLType:            "dv",
		SSLBundle:          "optimal",
		SSLCA:              "lets_encrypt",
		SSLMinTLS:          "1.2",
		SSLHTTP2:           "on",
		SSLTLS13:           "on",
		SSLEarly:           "off",
		SSLWild:            &wild,
		SSLBrand:           &brand,
	})
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{
		"hostname":"app.customer.com",
		"custom_origin_server":"origin.example.com",
		"custom_origin_sni":"sni.example.com",
		"custom_metadata":{"customer_id":"c1"},
		"ssl":{
			"method":"txt",
			"type":"dv",
			"bundle_method":"optimal",
			"certificate_authority":"lets_encrypt",
			"wildcard":true,
			"cloudflare_branding":true,
			"settings":{
				"min_tls_version":"1.2",
				"http2":"on",
				"tls_1_3":"on",
				"early_hints":"off"
			}
		}
	}`)
}

func TestBuildCustomHostnameCreateBodyNoSSL(t *testing.T) {
	body, err := buildCustomHostnameCreateBody(customHostnameWriteOpts{
		Hostname:   chTestHostname,
		IncludeSSL: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{"hostname":"app.customer.com"}`)
}

func TestBuildCustomHostnameCreateBodyValidation(t *testing.T) {
	empty := "  "
	host255 := strings.Repeat("a", 255)
	host256 := strings.Repeat("a", 256)
	// JSON Schema maxLength counts code points: 255 multi-byte runes are valid
	// even when the UTF-8 byte length exceeds 255.
	host255MB := strings.Repeat("ä", 255)
	host256MB := strings.Repeat("ä", 256)
	cases := []struct {
		name string
		o    customHostnameWriteOpts
		want string
	}{
		{"empty hostname", customHostnameWriteOpts{Hostname: "  ", IncludeSSL: true}, "hostname must not be empty"},
		{"hostname max 255 ok", customHostnameWriteOpts{Hostname: host255, IncludeSSL: true}, ""},
		{"hostname 256 rejected", customHostnameWriteOpts{Hostname: host256, IncludeSSL: true}, "at most 255"},
		{"hostname 255 multibyte ok", customHostnameWriteOpts{Hostname: host255MB, IncludeSSL: true}, ""},
		{"hostname 256 multibyte rejected", customHostnameWriteOpts{Hostname: host256MB, IncludeSSL: true}, "at most 255"},
		{"empty origin", customHostnameWriteOpts{Hostname: "a.com", CustomOriginServer: &empty, IncludeSSL: true}, "--custom-origin-server must not be empty"},
		{"bad method", customHostnameWriteOpts{Hostname: "a.com", IncludeSSL: true, SSLMethod: "dns"}, "--ssl-method must be one of"},
		{"bad type", customHostnameWriteOpts{Hostname: "a.com", IncludeSSL: true, SSLType: "ov"}, "--ssl-type must be one of"},
		{"bad ca", customHostnameWriteOpts{Hostname: "a.com", IncludeSSL: true, SSLCA: "sectigo"}, "--certificate-authority must be one of"},
		{"bad min tls", customHostnameWriteOpts{Hostname: "a.com", IncludeSSL: true, SSLMinTLS: "1.4"}, "--min-tls-version must be one of"},
		{"bad http2", customHostnameWriteOpts{Hostname: "a.com", IncludeSSL: true, SSLHTTP2: "true"}, "--http2 must be one of"},
		{"digicert allowed on create", customHostnameWriteOpts{Hostname: "a.com", IncludeSSL: true, SSLCA: "digicert"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildCustomHostnameCreateBody(tc.o)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuildCustomHostnameUpdateBodyPartial(t *testing.T) {
	origin := "origin2.example.com"
	body, err := buildCustomHostnameUpdateBody(customHostnameWriteOpts{
		CustomOriginServer: &origin,
	})
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{"custom_origin_server":"origin2.example.com"}`)
}

func TestBuildCustomHostnameUpdateBodySSLOnly(t *testing.T) {
	body, err := buildCustomHostnameUpdateBody(customHostnameWriteOpts{
		IncludeSSL: true,
		SSLMethod:  "http",
		SSLType:    "dv",
	})
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{"ssl":{"method":"http","type":"dv"}}`)
}

func TestBuildCustomHostnameUpdateBodyClearOrigin(t *testing.T) {
	empty := ""
	body, err := buildCustomHostnameUpdateBody(customHostnameWriteOpts{
		CustomOriginServer: &empty,
	})
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{"custom_origin_server":""}`)
}

func TestBuildCustomHostnameUpdateBodyValidation(t *testing.T) {
	cases := []struct {
		name string
		o    customHostnameWriteOpts
		want string
	}{
		{"nothing", customHostnameWriteOpts{}, "nothing to update"},
		{"ssl empty", customHostnameWriteOpts{IncludeSSL: true}, "ssl update requires at least one"},
		{"bad bundle", customHostnameWriteOpts{IncludeSSL: true, SSLBundle: "max"}, "--bundle-method must be one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildCustomHostnameUpdateBody(tc.o); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestBuildCustomHostnameFallbackBody(t *testing.T) {
	body, err := buildCustomHostnameFallbackBody("fallback.example.com")
	if err != nil {
		t.Fatal(err)
	}
	chAssertJSONEqual(t, body, `{"origin":"fallback.example.com"}`)
	if _, err := buildCustomHostnameFallbackBody("  "); err == nil || !strings.Contains(err.Error(), "origin must not be empty") {
		t.Fatalf("error = %v", err)
	}
	origin255 := strings.Repeat("o", 255)
	if _, err := buildCustomHostnameFallbackBody(origin255); err != nil {
		t.Fatalf("255-char origin should be accepted: %v", err)
	}
	if _, err := buildCustomHostnameFallbackBody(strings.Repeat("o", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v, want max 255", err)
	}
	// Multibyte code-point bounds (JSON Schema maxLength).
	if _, err := buildCustomHostnameFallbackBody(strings.Repeat("ö", 255)); err != nil {
		t.Fatalf("255 multibyte origin should pass: %v", err)
	}
	if _, err := buildCustomHostnameFallbackBody(strings.Repeat("ö", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v, want max 255 multibyte", err)
	}
}

func TestValidateCustomHostnameNameBounds(t *testing.T) {
	if err := validateCustomHostnameName(strings.Repeat("h", 255)); err != nil {
		t.Fatalf("255 should pass: %v", err)
	}
	if err := validateCustomHostnameName(strings.Repeat("h", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v", err)
	}
	if err := validateCustomHostnameName(strings.Repeat("世", 255)); err != nil {
		t.Fatalf("255 multibyte should pass: %v", err)
	}
	if err := validateCustomHostnameName(strings.Repeat("世", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v", err)
	}
	// Byte length > 255 but code points == 255 must still pass.
	mb255 := strings.Repeat("ä", 255)
	if len(mb255) <= 255 {
		t.Fatalf("test setup: expected byte length > 255, got %d", len(mb255))
	}
	if err := validateCustomHostnameName(mb255); err != nil {
		t.Fatalf("code-point maxLength should pass: %v", err)
	}
	if err := validateCustomHostnameOrigin(strings.Repeat("o", 255)); err != nil {
		t.Fatalf("255 origin should pass: %v", err)
	}
	if err := validateCustomHostnameOrigin(strings.Repeat("o", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v", err)
	}
	if err := validateCustomHostnameOrigin(strings.Repeat("ö", 255)); err != nil {
		t.Fatalf("255 multibyte origin should pass: %v", err)
	}
	if err := validateCustomHostnameOrigin(strings.Repeat("ö", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateCustomHostnameListFilters(t *testing.T) {
	// List id is exactly 36 code points (UUID form in the pinned example).
	if err := validateCustomHostnameListID(chTestID); err != nil {
		t.Fatal(err)
	}
	if len(chTestID) != 36 {
		t.Fatalf("test id length = %d, want 36", len(chTestID))
	}
	if err := validateCustomHostnameListID(strings.Repeat("a", 35)); err == nil || !strings.Contains(err.Error(), "exactly 36") {
		t.Fatalf("error = %v", err)
	}
	if err := validateCustomHostnameListID(strings.Repeat("a", 37)); err == nil || !strings.Contains(err.Error(), "exactly 36") {
		t.Fatalf("error = %v", err)
	}
	if err := validateCustomHostnameListHostname(strings.Repeat("h", 255)); err != nil {
		t.Fatal(err)
	}
	if err := validateCustomHostnameListHostname(strings.Repeat("h", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v", err)
	}
	if err := validateCustomHostnameListHostname(strings.Repeat("ä", 255)); err != nil {
		t.Fatalf("multibyte 255 list hostname should pass: %v", err)
	}
	if err := validateCustomHostnameListHostname(strings.Repeat("ä", 256)); err == nil || !strings.Contains(err.Error(), "at most 255") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseCustomHostnameMetadata(t *testing.T) {
	m, err := parseCustomHostnameMetadata(`{"a":"1","b":"two"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m["a"] != "1" || m["b"] != "two" {
		t.Fatalf("got %#v", m)
	}
	for _, raw := range []string{"null", "[]", `"x"`, `{"a":1}`, ""} {
		if _, err := parseCustomHostnameMetadata(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}

// --- validation before network ---------------------------------------------

func TestCustomHostnameValidationBeforeClient(t *testing.T) {
	// Invalid input must fail without needing a reachable base URL.
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "create empty hostname",
			args: []string{"custom-hostnames", "create", "  ", "--zone", chTestZoneID},
			want: "hostname must not be empty",
		},
		{
			name: "create bad ssl method",
			args: []string{"custom-hostnames", "create", chTestHostname, "--zone", chTestZoneID, "--ssl-method", "dns"},
			want: "--ssl-method must be one of",
		},
		{
			name: "create no-ssl with ssl flag",
			args: []string{"custom-hostnames", "create", chTestHostname, "--zone", chTestZoneID, "--no-ssl", "--ssl-method", "http"},
			want: "--no-ssl cannot be combined",
		},
		{
			name: "update nothing",
			args: []string{"custom-hostnames", "update", chTestID, "--zone", chTestZoneID},
			want: "nothing to update",
		},
		{
			name: "list id and hostname exclusive",
			args: []string{"custom-hostnames", "list", "--zone", chTestZoneID, "--id", chTestID, "--hostname", chTestHostname},
			want: "--id cannot be combined with --hostname",
		},
		{
			name: "list id wrong length",
			args: []string{"custom-hostnames", "list", "--zone", chTestZoneID, "--id", "too-short"},
			want: "exactly 36",
		},
		{
			name: "list hostname 256 rejected",
			args: []string{"custom-hostnames", "list", "--zone", chTestZoneID, "--hostname", strings.Repeat("x", 256)},
			want: "at most 255",
		},
		{
			name: "list hostname 256 multibyte rejected",
			args: []string{"custom-hostnames", "list", "--zone", chTestZoneID, "--hostname", strings.Repeat("ä", 256)},
			want: "at most 255",
		},
		{
			name: "list bad status",
			args: []string{"custom-hostnames", "list", "--zone", chTestZoneID, "--status", "nope"},
			want: "--status must be one of",
		},
		{
			name: "list digicert filter rejected",
			args: []string{"custom-hostnames", "list", "--zone", chTestZoneID, "--certificate-authority", "digicert"},
			want: "--certificate-authority must be one of",
		},
		{
			name: "fallback empty origin",
			args: []string{"custom-hostnames", "fallback-origin", "set", "  ", "--zone", chTestZoneID},
			want: "origin must not be empty",
		},
		{
			name: "create hostname 256 rejected before network",
			args: []string{"custom-hostnames", "create", strings.Repeat("a", 256), "--zone", chTestZoneID},
			want: "at most 255",
		},
		{
			name: "create hostname 256 multibyte rejected before network",
			args: []string{"custom-hostnames", "create", strings.Repeat("ä", 256), "--zone", chTestZoneID},
			want: "at most 255",
		},
		{
			name: "fallback origin 256 rejected before network",
			args: []string{"custom-hostnames", "fallback-origin", "set", strings.Repeat("b", 256), "--zone", chTestZoneID},
			want: "at most 255",
		},
		{
			name: "fallback origin 256 multibyte rejected before network",
			args: []string{"custom-hostnames", "fallback-origin", "set", strings.Repeat("ö", 256), "--zone", chTestZoneID},
			want: "at most 255",
		},
		{
			name: "list rejects extra args",
			args: []string{"custom-hostnames", "list", "extra", "--zone", chTestZoneID},
			want: "unknown command",
		},
		{
			name: "fallback get rejects extra args",
			args: []string{"custom-hostnames", "fallback-origin", "get", "extra", "--zone", chTestZoneID},
			want: "unknown command",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCustomHostnamesCLI(t, "http://127.0.0.1:1", tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

// --- dry-run request construction ------------------------------------------

func TestCustomHostnameCreateDryRun(t *testing.T) {
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "create", chTestHostname,
		"--zone", chTestZoneID,
		"--custom-origin-server", "origin.example.com",
		"--ssl-method", "txt",
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := chParseDump(t, stdout)
	if method != "POST" {
		t.Fatalf("method = %s", method)
	}
	if !strings.Contains(u, "/zones/"+chTestZoneID+"/custom_hostnames") {
		t.Fatalf("url = %s", u)
	}
	if strings.Contains(u, chTestID) {
		t.Fatalf("create must not include id path: %s", u)
	}
	chAssertJSONEqual(t, body, `{
		"hostname":"app.customer.com",
		"custom_origin_server":"origin.example.com",
		"ssl":{"method":"txt","type":"dv"}
	}`)
}

func TestCustomHostnameUpdateDryRunNoRead(t *testing.T) {
	// Partial PATCH must not perform a GET during dry-run.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		t.Fatalf("unexpected request during dry-run: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "update", chTestID,
		"--zone", chTestZoneID,
		"--custom-origin-server", "origin2.example.com",
		"--min-tls-version", "1.2",
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("hits = %d, want 0", hits)
	}
	method, u, body := chParseDump(t, stdout)
	if method != "PATCH" {
		t.Fatalf("method = %s", method)
	}
	if !strings.HasSuffix(u, "/zones/"+chTestZoneID+"/custom_hostnames/"+chTestID) &&
		!strings.Contains(u, "/custom_hostnames/"+chTestID) {
		t.Fatalf("url = %s", u)
	}
	chAssertJSONEqual(t, body, `{
		"custom_origin_server":"origin2.example.com",
		"ssl":{"settings":{"min_tls_version":"1.2"}}
	}`)
}

func TestCustomHostnameDeleteDryRun(t *testing.T) {
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "delete", chTestID, "--zone", chTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := chParseDump(t, stdout)
	if method != "DELETE" {
		t.Fatalf("method = %s", method)
	}
	if !strings.Contains(u, "/custom_hostnames/"+chTestID) {
		t.Fatalf("url = %s", u)
	}
	if len(body) > 0 && string(body) != "null" {
		t.Fatalf("delete body = %s", body)
	}
}

func TestCustomHostnameFallbackSetDryRun(t *testing.T) {
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "fallback-origin", "set", "fallback.example.com",
		"--zone", chTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	method, u, body := chParseDump(t, stdout)
	if method != "PUT" {
		t.Fatalf("method = %s", method)
	}
	if !strings.Contains(u, "/custom_hostnames/fallback_origin") {
		t.Fatalf("url = %s", u)
	}
	chAssertJSONEqual(t, body, `{"origin":"fallback.example.com"}`)
}

func TestCustomHostnameListDryRunQuery(t *testing.T) {
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "list",
		"--zone", chTestZoneID,
		"--hostname", chTestHostname,
		"--status", "pending",
		"--ssl-status", "pending_validation",
		"--order", "ssl_status",
		"--direction", "desc",
		"--certificate-authority", "google",
		"--wildcard",
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	method, u, _ := chParseDump(t, stdout)
	if method != "GET" {
		t.Fatalf("method = %s", method)
	}
	for _, want := range []string{
		"hostname=app.customer.com",
		"hostname_status=pending",
		"ssl_status=pending_validation",
		"order=ssl_status",
		"direction=desc",
		"certificate_authority=google",
		"wildcard=true",
		"per_page=50",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("url missing %q: %s", want, u)
		}
	}
}

func TestCustomHostnameListDryRunIDFilter(t *testing.T) {
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "list",
		"--zone", chTestZoneID,
		"--id", chTestID,
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	_, u, _ := chParseDump(t, stdout)
	if !strings.Contains(u, "id="+chTestID) {
		t.Fatalf("url missing id: %s", u)
	}
	if strings.Contains(u, "hostname=") {
		t.Fatalf("url should not include hostname: %s", u)
	}
}

func TestCustomHostnameListIDHostnameMutualExclusion(t *testing.T) {
	// Pinned list parameter descriptions forbid combining id with hostname.
	_, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "list",
		"--zone", chTestZoneID,
		"--id", chTestID,
		"--hostname", chTestHostname,
		"--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--id cannot be combined with --hostname") {
		t.Fatalf("error = %v", err)
	}
}

func TestCustomHostnameCreateHostname255Accepted(t *testing.T) {
	host255 := strings.Repeat("a", 255)
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "create", host255,
		"--zone", chTestZoneID, "--no-ssl", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := chParseDump(t, stdout)
	chAssertJSONEqual(t, body, `{"hostname":"`+host255+`"}`)
}

func TestCustomHostnameCreateHostname255MultibyteAccepted(t *testing.T) {
	host255 := strings.Repeat("ä", 255)
	if len(host255) <= 255 {
		t.Fatalf("expected multi-byte length > 255, got %d", len(host255))
	}
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "create", host255,
		"--zone", chTestZoneID, "--no-ssl", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := chParseDump(t, stdout)
	chAssertJSONEqual(t, body, `{"hostname":"`+host255+`"}`)
}

func TestCustomHostnameFallbackSetOrigin255Accepted(t *testing.T) {
	origin255 := strings.Repeat("o", 255)
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "fallback-origin", "set", origin255,
		"--zone", chTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := chParseDump(t, stdout)
	chAssertJSONEqual(t, body, `{"origin":"`+origin255+`"}`)
}

func TestCustomHostnameFallbackSetOrigin255MultibyteAccepted(t *testing.T) {
	origin255 := strings.Repeat("ö", 255)
	if len(origin255) <= 255 {
		t.Fatalf("expected multi-byte length > 255, got %d", len(origin255))
	}
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "fallback-origin", "set", origin255,
		"--zone", chTestZoneID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	_, _, body := chParseDump(t, stdout)
	chAssertJSONEqual(t, body, `{"origin":"`+origin255+`"}`)
}

func TestCustomHostnameListHostname255MultibyteDryRun(t *testing.T) {
	host255 := strings.Repeat("ä", 255)
	stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid",
		"custom-hostnames", "list",
		"--zone", chTestZoneID,
		"--hostname", host255,
		"--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	_, u, _ := chParseDump(t, stdout)
	if !strings.Contains(u, "hostname=") {
		t.Fatalf("url missing hostname: %s", u)
	}
}

func TestCustomHostnameNoArgsLeafCommands(t *testing.T) {
	// cobra.NoArgs surfaces as "unknown command" / accepts no args for these leaves.
	cases := []struct {
		name string
		args []string
	}{
		{"list", []string{"custom-hostnames", "list", "extra", "--zone", chTestZoneID, "--dry-run"}},
		{"fallback-get", []string{"custom-hostnames", "fallback-origin", "get", "extra", "--zone", chTestZoneID, "--dry-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCustomHostnamesCLI(t, "http://example.invalid", tc.args...)
			if err == nil {
				t.Fatal("expected error for extra positional args")
			}
			msg := err.Error()
			// Cobra may say "unknown command" or "accepts no args" depending on version.
			if !strings.Contains(msg, "unknown command") && !strings.Contains(msg, "accepts no args") && !strings.Contains(msg, "accepts 0 arg") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// --- live httptest -----------------------------------------------------------

type chCapture struct {
	method string
	path   string
	query  string
	body   []byte
}

func chServer(t *testing.T, status int, result any, capture *chCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture.method = r.Method
			capture.path = r.URL.Path
			capture.query = r.URL.RawQuery
			if r.Body != nil {
				b, _ := io.ReadAll(r.Body)
				capture.body = b
			}
		}
		// Zone name resolution path should not be hit when zone ID is given.
		if r.URL.Path == "/zones" {
			t.Fatalf("unexpected zone lookup")
		}
		payload, err := json.Marshal(map[string]any{
			"success":  true,
			"errors":   []any{},
			"messages": []any{},
			"result":   result,
			"result_info": map[string]any{
				"page": 1, "per_page": 50, "count": 1, "total_count": 1, "total_pages": 1,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(payload)
	}))
}

func TestCustomHostnameListLiveTable(t *testing.T) {
	var cap chCapture
	result := []map[string]any{{
		"id":                   chTestID,
		"hostname":             chTestHostname,
		"status":               "active",
		"custom_origin_server": "origin.example.com",
		"ssl":                  map[string]any{"status": "active", "method": "http"},
	}}
	srv := chServer(t, 200, result, &cap)
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "list", "--zone", chTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "GET" || !strings.HasSuffix(cap.path, "/custom_hostnames") {
		t.Fatalf("got %s %s", cap.method, cap.path)
	}
	if !strings.Contains(stdout, chTestID) || !strings.Contains(stdout, chTestHostname) {
		t.Fatalf("table missing rows: %s", stdout)
	}
	if !strings.Contains(stdout, "STATUS") || !strings.Contains(stdout, "SSL") {
		t.Fatalf("table headers: %s", stdout)
	}
}

func TestCustomHostnameCreateLive(t *testing.T) {
	var cap chCapture
	srv := chServer(t, 200, map[string]any{
		"id": chTestID, "hostname": chTestHostname, "status": "pending",
	}, &cap)
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "create", chTestHostname,
		"--zone", chTestZoneID,
		"--certificate-authority", "google",
		"--http2", "on")
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "POST" {
		t.Fatalf("method = %s", cap.method)
	}
	chAssertJSONEqual(t, cap.body, `{
		"hostname":"app.customer.com",
		"ssl":{"method":"http","type":"dv","certificate_authority":"google","settings":{"http2":"on"}}
	}`)
	if !strings.Contains(stdout, chTestID) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestCustomHostnameUpdateLive(t *testing.T) {
	var cap chCapture
	srv := chServer(t, 200, map[string]any{"id": chTestID}, &cap)
	defer srv.Close()

	_, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "update", chTestID,
		"--zone", chTestZoneID,
		"--custom-origin-sni", ":request_host_header:",
		"--ssl-method", "email",
		"--ssl-type", "dv")
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "PATCH" {
		t.Fatalf("method = %s", cap.method)
	}
	if !strings.HasSuffix(cap.path, "/custom_hostnames/"+chTestID) {
		t.Fatalf("path = %s", cap.path)
	}
	chAssertJSONEqual(t, cap.body, `{
		"custom_origin_sni":":request_host_header:",
		"ssl":{"method":"email","type":"dv"}
	}`)
}

func TestCustomHostnameDeleteLiveForce(t *testing.T) {
	var cap chCapture
	srv := chServer(t, 200, map[string]any{"id": chTestID}, &cap)
	defer srv.Close()

	_, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "delete", chTestID, "--zone", chTestZoneID, "--force")
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "DELETE" {
		t.Fatalf("method = %s", cap.method)
	}
}

func TestCustomHostnameDeleteAbortsWithoutForce(t *testing.T) {
	// Non-TTY stdin: confirm returns false; delete must abort without HTTP.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "delete", chTestID, "--zone", chTestZoneID)
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("error = %v", err)
	}
	if hits != 0 {
		t.Fatalf("hits = %d", hits)
	}
}

func TestCustomHostnameSSLStatusLiveTable(t *testing.T) {
	var cap chCapture
	result := map[string]any{
		"id":       chTestID,
		"hostname": chTestHostname,
		"status":   "pending",
		"ssl": map[string]any{
			"status":                "pending_validation",
			"method":                "http",
			"type":                  "dv",
			"certificate_authority": "google",
			"wildcard":              false,
			"expires_on":            "2021-02-06T18:11:23.531995Z",
			"validation_errors":     []map[string]any{{"message": "CAA SERVFAIL"}},
		},
	}
	srv := chServer(t, 200, result, &cap)
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "ssl", chTestID, "--zone", chTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "GET" || !strings.HasSuffix(cap.path, "/custom_hostnames/"+chTestID) {
		t.Fatalf("got %s %s", cap.method, cap.path)
	}
	for _, want := range []string{chTestHostname, "pending_validation", "http", "google", "CAA SERVFAIL"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q: %s", want, stdout)
		}
	}
}

func TestCustomHostnameSSLStatusJSON(t *testing.T) {
	result := map[string]any{
		"id":       chTestID,
		"hostname": chTestHostname,
		"ssl": map[string]any{
			"status": "active",
			"method": "txt",
			"type":   "dv",
		},
	}
	srv := chServer(t, 200, result, nil)
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "ssl", chTestID, "--zone", chTestZoneID, "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	// JSON mode should render the ssl object, not the full hostname wrapper.
	if strings.Contains(stdout, `"hostname"`) {
		t.Fatalf("expected ssl-only JSON, got %s", stdout)
	}
	if !strings.Contains(stdout, `"status"`) || !strings.Contains(stdout, `"txt"`) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestCustomHostnameFallbackGetLive(t *testing.T) {
	var cap chCapture
	result := map[string]any{
		"origin":     "fallback.example.com",
		"status":     "active",
		"updated_at": "2020-03-16T18:11:23.531995Z",
		"errors":     []string{},
	}
	srv := chServer(t, 200, result, &cap)
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "fallback-origin", "get", "--zone", chTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "GET" || !strings.HasSuffix(cap.path, "/fallback_origin") {
		t.Fatalf("got %s %s", cap.method, cap.path)
	}
	if !strings.Contains(stdout, "fallback.example.com") || !strings.Contains(stdout, "active") {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestCustomHostnameFallbackSetLive(t *testing.T) {
	var cap chCapture
	srv := chServer(t, 200, map[string]any{
		"origin": "fallback.example.com",
		"status": "pending_deployment",
	}, &cap)
	defer srv.Close()

	_, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "fallback-origin", "set", "fallback.example.com",
		"--zone", chTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "PUT" {
		t.Fatalf("method = %s", cap.method)
	}
	chAssertJSONEqual(t, cap.body, `{"origin":"fallback.example.com"}`)
}

func TestCustomHostnameGetLive(t *testing.T) {
	var cap chCapture
	srv := chServer(t, 200, map[string]any{
		"id": chTestID, "hostname": chTestHostname, "status": "active",
	}, &cap)
	defer srv.Close()

	stdout, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "get", chTestID, "--zone", chTestZoneID)
	if err != nil {
		t.Fatal(err)
	}
	if cap.method != "GET" {
		t.Fatalf("method = %s", cap.method)
	}
	if !strings.Contains(stdout, chTestHostname) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestCustomHostnameZoneNameResolution(t *testing.T) {
	var chPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			if r.URL.Query().Get("name") != "example.com" {
				t.Fatalf("zone query = %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + chTestZoneID + `","name":"example.com"}]}`))
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/custom_hostnames"):
			chPath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":[],"result_info":{"page":1,"per_page":50,"count":0,"total_count":0,"total_pages":0}}`))
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := runCustomHostnamesCLI(t, srv.URL,
		"custom-hostnames", "list", "--zone", "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if chPath != "/zones/"+chTestZoneID+"/custom_hostnames" {
		t.Fatalf("path = %s", chPath)
	}
}

func TestCustomHostnameHelpExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"custom-hostnames", "create", "--help"}, []string{"cf custom-hostnames create", "--ssl-method"}},
		{[]string{"custom-hostnames", "ssl", "--help"}, []string{"cf custom-hostnames ssl", "SSL"}},
		{[]string{"custom-hostnames", "fallback-origin", "set", "--help"}, []string{"cf custom-hostnames fallback-origin set", "origin"}},
	}
	for _, tc := range cases {
		stdout, _, err := runCustomHostnamesCLI(t, "http://example.invalid", tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(stdout, w) {
				t.Fatalf("%v missing %q in:\n%s", tc.args, w, stdout)
			}
		}
	}
}
