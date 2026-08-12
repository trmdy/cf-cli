package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const addressingTestAccountID = "0123456789abcdef0123456789abcdef"
const addressingTestPrefixID = "fedcba9876543210fedcba9876543210"
const addressingTestMapID = "11111111111111111111111111111111"
const addressingTestZoneID = "22222222222222222222222222222222"

func runAddressingCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", addressingTestAccountID,
	}, args...))
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

type addressingRequestDump struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body"`
}

func addressingDryRun(t *testing.T, args ...string) addressingRequestDump {
	t.Helper()
	stdout, _, err := runAddressingCLI(t, "http://example.invalid", append(args, "--dry-run")...)
	if err != nil {
		t.Fatal(err)
	}
	var dump addressingRequestDump
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("decode dump: %v\n%s", err, stdout)
	}
	return dump
}

func TestAddressingLeavesExactDryRunRequests(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "letter.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := "/accounts/" + addressingTestAccountID + "/addressing"
	cases := []struct {
		name      string
		args      []string
		method    string
		path      string
		body      string
		multipart bool
	}{
		{"prefix list", []string{"addressing", "prefix", "list"}, "GET", base + "/prefixes", "", false},
		{"prefix get", []string{"addressing", "prefix", "get", addressingTestPrefixID}, "GET", base + "/prefixes/" + addressingTestPrefixID, "", false},
		{"advertisement get", []string{"addressing", "advertisement", "get", addressingTestPrefixID}, "GET", base + "/prefixes/" + addressingTestPrefixID + "/bgp/status", "", false},
		{"advertisement set", []string{"addressing", "advertisement", "set", addressingTestPrefixID, "--advertised=false"}, "PATCH", base + "/prefixes/" + addressingTestPrefixID + "/bgp/status", `{"advertised":false}`, false},
		{"map list", []string{"addressing", "map", "list"}, "GET", base + "/address_maps", "", false},
		{"map get", []string{"addressing", "map", "get", addressingTestMapID}, "GET", base + "/address_maps/" + addressingTestMapID, "", false},
		{"map create", []string{"addressing", "map", "create", "--enabled=true"}, "POST", base + "/address_maps", `{"enabled":true}`, false},
		{"map update", []string{"addressing", "map", "update", addressingTestMapID, "--description", "new"}, "PATCH", base + "/address_maps/" + addressingTestMapID, `{"description":"new"}`, false},
		{"map delete", []string{"addressing", "map", "delete", addressingTestMapID, "--force"}, "DELETE", base + "/address_maps/" + addressingTestMapID, "", false},
		{"map ip add", []string{"addressing", "map", "ip", "add", addressingTestMapID, "2001:db8::1"}, "PUT", base + "/address_maps/" + addressingTestMapID + "/ips/2001:db8::1", "", false},
		{"map ip remove", []string{"addressing", "map", "ip", "remove", addressingTestMapID, "2001:db8::1", "--force"}, "DELETE", base + "/address_maps/" + addressingTestMapID + "/ips/2001:db8::1", "", false},
		{"map account add", []string{"addressing", "map", "account", "add", addressingTestMapID, addressingTestZoneID}, "PUT", base + "/address_maps/" + addressingTestMapID + "/accounts/" + addressingTestZoneID, "", false},
		{"map account remove", []string{"addressing", "map", "account", "remove", addressingTestMapID, addressingTestZoneID, "--force"}, "DELETE", base + "/address_maps/" + addressingTestMapID + "/accounts/" + addressingTestZoneID, "", false},
		{"map zone add", []string{"addressing", "map", "zone", "add", addressingTestMapID, "--zone", addressingTestZoneID}, "PUT", base + "/address_maps/" + addressingTestMapID + "/zones/" + addressingTestZoneID, "", false},
		{"map zone remove", []string{"addressing", "map", "zone", "remove", addressingTestMapID, "--zone", addressingTestZoneID, "--force"}, "DELETE", base + "/address_maps/" + addressingTestMapID + "/zones/" + addressingTestZoneID, "", false},
		{"loa upload", []string{"addressing", "loa", "upload", "--file", pdf}, "POST", base + "/loa_documents", "", true},
		{"loa download", []string{"addressing", "loa", "download", addressingTestPrefixID}, "GET", base + "/loa_documents/" + addressingTestPrefixID + "/download", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dump := addressingDryRun(t, tc.args...)
			if dump.Method != tc.method || !strings.HasSuffix(dump.URL, tc.path) {
				t.Fatalf("request = %s %s, want %s %s", dump.Method, dump.URL, tc.method, tc.path)
			}
			if tc.multipart {
				var got string
				if err := json.Unmarshal(dump.Body, &got); err != nil || !strings.Contains(got, "name=\"loa_document\"") || !strings.Contains(got, "%PDF-test") {
					t.Fatalf("multipart body = %s (%v)", dump.Body, err)
				}
				return
			}
			if tc.body == "" {
				if len(dump.Body) != 0 {
					t.Fatalf("body = %s, want no body", dump.Body)
				}
				return
			}
			var got, want any
			if err := json.Unmarshal(dump.Body, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.body), &want); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("body = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestAddressingMapCreateEnabledWireStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"omitted", []string{"addressing", "map", "create"}, `{}`},
		{"true", []string{"addressing", "map", "create", "--enabled=true"}, `{"enabled":true}`},
		{"false", []string{"addressing", "map", "create", "--enabled=false"}, `{"enabled":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dump := addressingDryRun(t, tc.args...)
			var got, want any
			if err := json.Unmarshal(dump.Body, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("body = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestAddressingIdentifierEscapingAndCodePointLimit(t *testing.T) {
	escaped := "a/b?c#d"
	dump := addressingDryRun(t, "addressing", "prefix", "get", escaped)
	if !strings.HasSuffix(dump.URL, "/prefixes/a%2Fb%3Fc%23d") {
		t.Fatalf("escaped URL = %s", dump.URL)
	}
	if _, err := addressingIdentifier("prefix ID", strings.Repeat("é", 32)); err != nil {
		t.Fatalf("32 code points rejected: %v", err)
	}
	if _, err := addressingIdentifier("prefix ID", strings.Repeat("é", 33)); err == nil || !strings.Contains(err.Error(), "at most 32") {
		t.Fatalf("33 code points error = %v", err)
	}
}

func TestAddressingPrefixListAutoPaginates(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"two","cidr":"2001:db8::/48","asn":2,"approved":"V"}],"result_info":{"page":2,"total_pages":2}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"one","cidr":"192.0.2.0/24","asn":1,"approved":"P"}],"result_info":{"page":1,"total_pages":2}}`)
	}))
	defer server.Close()

	stdout, _, err := runAddressingCLI(t, server.URL, "--output", "json", "addressing", "prefix", "list")
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 || !strings.Contains(stdout, `"id": "one"`) || !strings.Contains(stdout, `"id": "two"`) {
		t.Fatalf("pages=%d output=%s", pages, stdout)
	}
}

func TestAddressingLOADocumentMultipartDryRunAndDownload(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "letter.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runAddressingCLI(t, "http://example.invalid", "addressing", "loa", "upload", "--file", pdf, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatal(err)
	}
	mediaType, params, err := mime.ParseMediaType(dump.Headers["Content-Type"])
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] != "cf-cli-addressing-loa-dry-run" {
		t.Fatalf("content type = %q (%v)", dump.Headers["Content-Type"], err)
	}
	part, err := multipart.NewReader(strings.NewReader(dump.Body), params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(part)
	if part.FormName() != "loa_document" || part.FileName() != "letter.pdf" || string(data) != "%PDF-test" {
		t.Fatalf("part = name=%q file=%q body=%q", part.FormName(), part.FileName(), data)
	}

	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
			t.Fatalf("live upload content type = %q (%v)", r.Header.Get("Content-Type"), err)
		}
		part, err := multipart.NewReader(r.Body, params["boundary"]).NextPart()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(part)
		if part.FormName() != "loa_document" || part.FileName() != "letter.pdf" || string(body) != "%PDF-test" {
			t.Fatalf("live part = name=%q file=%q body=%q", part.FormName(), part.FileName(), body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"loa-1"}}`)
	}))
	defer uploadServer.Close()
	stdout, _, err = runAddressingCLI(t, uploadServer.URL, "addressing", "loa", "upload", "--file", pdf)
	if err != nil || !strings.Contains(stdout, `"id": "loa-1"`) {
		t.Fatalf("live upload output=%q err=%v", stdout, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-download"))
	}))
	defer server.Close()
	downloaded := filepath.Join(dir, "downloaded.pdf")
	_, stderr, err := runAddressingCLI(t, server.URL, "addressing", "loa", "download", addressingTestPrefixID, "--file", downloaded)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(downloaded)
	if err != nil || string(data) != "%PDF-download" || !strings.Contains(stderr, "wrote") {
		t.Fatalf("download = %q stderr=%q err=%v", data, stderr, err)
	}
	_, _, err = runAddressingCLI(t, server.URL, "--output", "json", "addressing", "loa", "download", addressingTestPrefixID)
	if err == nil || !strings.Contains(err.Error(), "--output is not supported") {
		t.Fatalf("output error = %v", err)
	}
}

func TestAddressingLocalValidationPrecedesClient(t *testing.T) {
	_, _, err := runAddressingCLI(t, "http://127.0.0.1:1", "addressing", "map", "ip", "add", addressingTestMapID, "not-an-ip")
	if err == nil || !strings.Contains(err.Error(), "IPv4 or IPv6") {
		t.Fatalf("error = %v", err)
	}
	notPDF := filepath.Join(t.TempDir(), "letter.pdf")
	if err := os.WriteFile(notPDF, []byte("not a PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildAddressingLOADocumentUpload(notPDF); err == nil || !strings.Contains(err.Error(), "%PDF-") {
		t.Fatalf("LOA file validation error = %v", err)
	}
	_, _, err = runAddressingCLI(t, "http://127.0.0.1:1", "addressing", "loa", "upload", "--file", notPDF)
	if err == nil || !strings.Contains(err.Error(), "%PDF-") {
		t.Fatalf("LOA command validation error = %v", err)
	}
}
