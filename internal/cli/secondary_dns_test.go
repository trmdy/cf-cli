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

const secondaryDNSTestAccountID = "0123456789abcdef0123456789abcdef"
const secondaryDNSTestZoneID = "fedcba9876543210fedcba9876543210"

func runSecondaryDNSCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
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

func secondaryDNSAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func secondaryDNSWriteEnvelope(w http.ResponseWriter, result string, resultInfo string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"success":true,"result":`+result+`,"result_info":`+resultInfo+`}`)
}

func TestSecondaryDNSCreateDryRunBodies(t *testing.T) {
	stdout, _, err := runSecondaryDNSCLI(t, "http://example.invalid",
		"secondary-dns", "peers", "create", "primary-ns",
		"--account-id", secondaryDNSTestAccountID, "--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var peerDump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &peerDump); err != nil {
		t.Fatal(err)
	}
	if peerDump.Method != http.MethodPost || !strings.HasSuffix(peerDump.URL, "/accounts/"+secondaryDNSTestAccountID+"/secondary_dns/peers") {
		t.Fatalf("peer dump = %#v", peerDump)
	}
	secondaryDNSAssertJSONEqual(t, peerDump.Body, `{"name":"primary-ns"}`)

	stdout, _, err = runSecondaryDNSCLI(t, "http://example.invalid",
		"secondary-dns", "tsigs", "create", "tsig.customer.cf.",
		"--account-id", secondaryDNSTestAccountID, "--secret", "secret-value", "--algo", "hmac-sha512.", "--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var tsigDump struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &tsigDump); err != nil {
		t.Fatal(err)
	}
	secondaryDNSAssertJSONEqual(t, tsigDump.Body, `{"name":"tsig.customer.cf.","secret":"secret-value","algo":"hmac-sha512."}`)

	stdout, _, err = runSecondaryDNSCLI(t, "http://example.invalid",
		"secondary-dns", "incoming", "create", "--zone", secondaryDNSTestZoneID,
		"--name", "example.com.", "--peer", "peer-a", "--peer", "peer-b", "--auto-refresh-seconds", "300", "--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var incomingDump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &incomingDump); err != nil {
		t.Fatal(err)
	}
	if incomingDump.Method != http.MethodPost || !strings.HasSuffix(incomingDump.URL, "/zones/"+secondaryDNSTestZoneID+"/secondary_dns/incoming") {
		t.Fatalf("incoming dump = %#v", incomingDump)
	}
	secondaryDNSAssertJSONEqual(t, incomingDump.Body, `{"name":"example.com.","peers":["peer-a","peer-b"],"auto_refresh_seconds":300}`)
}

func TestSecondaryDNSLocalValidationBeforeNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("invalid local input must not make an HTTP request")
	}))
	defer server.Close()

	_, _, err := runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "incoming", "create", "--zone", secondaryDNSTestZoneID,
		"--name", "example.com.", "--auto-refresh-seconds", "299",
	)
	if err == nil || !strings.Contains(err.Error(), "at least 300") {
		t.Fatalf("auto refresh validation error = %v", err)
	}

	_, _, err = runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "tsigs", "create", "tsig.customer.cf.",
		"--account-id", secondaryDNSTestAccountID, "--secret", "secret-value",
	)
	if err == nil || !strings.Contains(err.Error(), "required flag(s) \"algo\"") {
		t.Fatalf("missing algorithm error = %v", err)
	}

	_, _, err = runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "peers", "update", "peer-id", "--account-id", secondaryDNSTestAccountID,
		"--ip", "not-an-ip",
	)
	if err == nil || !strings.Contains(err.Error(), "valid IPv4 or IPv6") {
		t.Fatalf("peer IP validation error = %v", err)
	}
}

func TestSecondaryDNSPeerIPValidation(t *testing.T) {
	for _, value := range []string{"192.0.2.53", "2001:db8::53"} {
		if err := secondaryDNSValidatePeerIP(value); err != nil {
			t.Fatalf("valid IP %q: %v", value, err)
		}
	}
	if err := secondaryDNSValidatePeerIP("primary.example.com"); err == nil {
		t.Fatal("expected hostname rejection")
	}

	if err := secondaryDNSValidatePeerObject(map[string]any{"name": "peer", "ip": "invalid"}); err == nil {
		t.Fatal("expected invalid IP from merged peer")
	}
}

func TestSecondaryDNSPeerUpdateReadMergeWrite(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != "/accounts/"+secondaryDNSTestAccountID+"/secondary_dns/peers/peer-id" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			secondaryDNSWriteEnvelope(w, `{"id":"peer-id","name":"old-peer","ip":"192.0.2.1","ixfr_enable":false,"api_future":"preserve"}`, "null")
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			secondaryDNSAssertJSONEqual(t, body, `{"name":"old-peer","ip":"192.0.2.53","ixfr_enable":true,"api_future":"preserve"}`)
			secondaryDNSWriteEnvelope(w, `{}`, "null")
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	_, _, err := runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "peers", "update", "peer-id", "--account-id", secondaryDNSTestAccountID,
		"--ip", "192.0.2.53", "--ixfr-enable=true",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET,PUT" {
		t.Fatalf("methods = %v, want GET,PUT", methods)
	}
}

func TestSecondaryDNSPeerUpdateRejectsInvalidIPFromRead(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Method == http.MethodPut {
			t.Fatal("invalid merged peer must not be written")
		}
		secondaryDNSWriteEnvelope(w, `{"id":"peer-id","name":"old-peer","ip":"not-an-ip"}`, "null")
	}))
	defer server.Close()

	_, _, err := runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "peers", "update", "peer-id", "--account-id", secondaryDNSTestAccountID,
		"--name", "new-peer",
	)
	if err == nil || !strings.Contains(err.Error(), "valid IPv4 or IPv6") {
		t.Fatalf("merged peer validation error = %v", err)
	}
	if strings.Join(methods, ",") != http.MethodGet {
		t.Fatalf("methods = %v, want GET only", methods)
	}
}

func TestSecondaryDNSIncomingUpdateStripsReadOnlyAndPreservesUnknown(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != "/zones/"+secondaryDNSTestZoneID+"/secondary_dns/incoming" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			secondaryDNSWriteEnvelope(w, `{"id":"config-id","name":"example.com.","peers":["peer-a"],"auto_refresh_seconds":86400,"checked_time":"now","created_time":"then","modified_time":"later","soa_serial":123,"api_future":"preserve"}`, "null")
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			secondaryDNSAssertJSONEqual(t, body, `{"name":"example.com.","peers":["peer-a"],"auto_refresh_seconds":300,"api_future":"preserve"}`)
			secondaryDNSWriteEnvelope(w, `{}`, "null")
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	_, _, err := runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "incoming", "update", "--zone", secondaryDNSTestZoneID, "--auto-refresh-seconds", "300",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "GET,PUT" {
		t.Fatalf("methods = %v, want GET,PUT", methods)
	}
}

func TestSecondaryDNSIncomingUpdateReplacesPeerList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			secondaryDNSWriteEnvelope(w, `{"id":"config-id","name":"example.com.","peers":["peer-a"],"auto_refresh_seconds":86400}`, "null")
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			secondaryDNSAssertJSONEqual(t, body, `{"name":"example.com.","peers":["peer-b"],"auto_refresh_seconds":86400}`)
			secondaryDNSWriteEnvelope(w, `{}`, "null")
		default:
			t.Fatalf("method = %s", r.Method)
		}
	}))
	defer server.Close()

	_, _, err := runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "incoming", "update", "--zone", secondaryDNSTestZoneID, "--peer", "peer-b",
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSecondaryDNSTSIGListPaginatesAndOutgoingNotify(t *testing.T) {
	var sawSecondPage, sawNotify bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/accounts/" + secondaryDNSTestAccountID + "/secondary_dns/tsigs":
			if r.Method != http.MethodGet {
				t.Fatalf("list method = %s", r.Method)
			}
			if r.URL.Query().Get("page") == "2" {
				sawSecondPage = true
				secondaryDNSWriteEnvelope(w, `[{"id":"two","name":"second.","algo":"hmac-sha256."}]`, `{"page":2,"total_pages":2}`)
				return
			}
			secondaryDNSWriteEnvelope(w, `[{"id":"one","name":"first.","algo":"hmac-sha512."}]`, `{"page":1,"total_pages":2}`)
		case "/zones/" + secondaryDNSTestZoneID + "/secondary_dns/outgoing/force_notify":
			if r.Method != http.MethodPost {
				t.Fatalf("notify method = %s", r.Method)
			}
			sawNotify = true
			secondaryDNSWriteEnvelope(w, `{}`, "null")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	stdout, _, err := runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "tsigs", "list", "--account-id", secondaryDNSTestAccountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sawSecondPage || !strings.Contains(stdout, "first.") || !strings.Contains(stdout, "second.") {
		t.Fatalf("pagination failed: second=%t output=%q", sawSecondPage, stdout)
	}

	_, _, err = runSecondaryDNSCLI(t, server.URL,
		"secondary-dns", "outgoing", "notify", "--zone", secondaryDNSTestZoneID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sawNotify {
		t.Fatal("outgoing notify was not sent")
	}
}

func TestSecondaryDNSConfigValidationAllowsEmptyPeerList(t *testing.T) {
	body, err := secondaryDNSBuildConfigCreateBody("outgoing", secondaryDNSConfigFlags{name: "example.com.", peers: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	secondaryDNSAssertJSONEqual(t, body, `{"name":"example.com.","peers":[]}`)
}

// TestSecondaryDNSRequestMatrix keeps one compact, real-command-tree request
// contract for every Secondary DNS leaf. Update entries prove their required
// GET-before-PUT flow; delete entries use --force so no terminal prompt is
// involved in the HTTP assertion.
func TestSecondaryDNSRequestMatrix(t *testing.T) {
	type request struct {
		method, path, body, result string
	}
	type testCase struct {
		name string
		args []string
		want []request
	}
	accountRoot := "/accounts/" + secondaryDNSTestAccountID + "/secondary_dns"
	zoneRoot := "/zones/" + secondaryDNSTestZoneID + "/secondary_dns"
	peerCurrent := `{"id":"peer-id","name":"peer-name","ip":"192.0.2.53"}`
	tsigCurrent := `{"id":"tsig-id","name":"tsig.example.","secret":"old-secret","algo":"hmac-sha256."}`
	incomingCurrent := `{"id":"config-id","name":"example.com.","peers":["peer-a"],"auto_refresh_seconds":86400}`
	outgoingCurrent := `{"id":"config-id","name":"example.com.","peers":["peer-a"]}`

	cases := []testCase{
		{"peers list", []string{"secondary-dns", "peers", "list", "--account-id", secondaryDNSTestAccountID}, []request{{http.MethodGet, accountRoot + "/peers", "", "[]"}}},
		{"peers get", []string{"secondary-dns", "peers", "get", "peer-id", "--account-id", secondaryDNSTestAccountID}, []request{{http.MethodGet, accountRoot + "/peers/peer-id", "", "{}"}}},
		{"peers create", []string{"secondary-dns", "peers", "create", "peer-name", "--account-id", secondaryDNSTestAccountID}, []request{{http.MethodPost, accountRoot + "/peers", `{"name":"peer-name"}`, "{}"}}},
		{"peers update", []string{"secondary-dns", "peers", "update", "peer-id", "--account-id", secondaryDNSTestAccountID, "--name", "new-peer"}, []request{{http.MethodGet, accountRoot + "/peers/peer-id", "", peerCurrent}, {http.MethodPut, accountRoot + "/peers/peer-id", `{"name":"new-peer","ip":"192.0.2.53"}`, "{}"}}},
		{"peers delete force", []string{"secondary-dns", "peers", "delete", "peer-id", "--account-id", secondaryDNSTestAccountID, "--force"}, []request{{http.MethodDelete, accountRoot + "/peers/peer-id", "", "{}"}}},

		{"tsigs list", []string{"secondary-dns", "tsigs", "list", "--account-id", secondaryDNSTestAccountID}, []request{{http.MethodGet, accountRoot + "/tsigs", "", "[]"}}},
		{"tsigs get", []string{"secondary-dns", "tsigs", "get", "tsig-id", "--account-id", secondaryDNSTestAccountID}, []request{{http.MethodGet, accountRoot + "/tsigs/tsig-id", "", "{}"}}},
		{"tsigs create", []string{"secondary-dns", "tsigs", "create", "tsig.example.", "--account-id", secondaryDNSTestAccountID, "--secret", "new-secret", "--algo", "hmac-sha512."}, []request{{http.MethodPost, accountRoot + "/tsigs", `{"name":"tsig.example.","secret":"new-secret","algo":"hmac-sha512."}`, "{}"}}},
		{"tsigs update", []string{"secondary-dns", "tsigs", "update", "tsig-id", "--account-id", secondaryDNSTestAccountID, "--algo", "hmac-sha512."}, []request{{http.MethodGet, accountRoot + "/tsigs/tsig-id", "", tsigCurrent}, {http.MethodPut, accountRoot + "/tsigs/tsig-id", `{"name":"tsig.example.","secret":"old-secret","algo":"hmac-sha512."}`, "{}"}}},
		{"tsigs delete force", []string{"secondary-dns", "tsigs", "delete", "tsig-id", "--account-id", secondaryDNSTestAccountID, "--force"}, []request{{http.MethodDelete, accountRoot + "/tsigs/tsig-id", "", "{}"}}},

		{"incoming get", []string{"secondary-dns", "incoming", "get", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodGet, zoneRoot + "/incoming", "", "{}"}}},
		{"incoming create", []string{"secondary-dns", "incoming", "create", "--zone", secondaryDNSTestZoneID, "--name", "example.com."}, []request{{http.MethodPost, zoneRoot + "/incoming", `{"name":"example.com.","peers":[],"auto_refresh_seconds":86400}`, "{}"}}},
		{"incoming update", []string{"secondary-dns", "incoming", "update", "--zone", secondaryDNSTestZoneID, "--name", "new.example."}, []request{{http.MethodGet, zoneRoot + "/incoming", "", incomingCurrent}, {http.MethodPut, zoneRoot + "/incoming", `{"name":"new.example.","peers":["peer-a"],"auto_refresh_seconds":86400}`, "{}"}}},
		{"incoming delete force", []string{"secondary-dns", "incoming", "delete", "--zone", secondaryDNSTestZoneID, "--force"}, []request{{http.MethodDelete, zoneRoot + "/incoming", "", "{}"}}},
		{"incoming force axfr", []string{"secondary-dns", "incoming", "force-axfr", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodPost, zoneRoot + "/force_axfr", "", "{}"}}},

		{"outgoing get", []string{"secondary-dns", "outgoing", "get", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodGet, zoneRoot + "/outgoing", "", "{}"}}},
		{"outgoing create", []string{"secondary-dns", "outgoing", "create", "--zone", secondaryDNSTestZoneID, "--name", "example.com."}, []request{{http.MethodPost, zoneRoot + "/outgoing", `{"name":"example.com.","peers":[]}`, "{}"}}},
		{"outgoing update", []string{"secondary-dns", "outgoing", "update", "--zone", secondaryDNSTestZoneID, "--name", "new.example."}, []request{{http.MethodGet, zoneRoot + "/outgoing", "", outgoingCurrent}, {http.MethodPut, zoneRoot + "/outgoing", `{"name":"new.example.","peers":["peer-a"]}`, "{}"}}},
		{"outgoing delete force", []string{"secondary-dns", "outgoing", "delete", "--zone", secondaryDNSTestZoneID, "--force"}, []request{{http.MethodDelete, zoneRoot + "/outgoing", "", "{}"}}},
		{"outgoing status", []string{"secondary-dns", "outgoing", "status", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodGet, zoneRoot + "/outgoing/status", "", "{}"}}},
		{"outgoing enable", []string{"secondary-dns", "outgoing", "enable", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodPost, zoneRoot + "/outgoing/enable", "", "{}"}}},
		{"outgoing disable", []string{"secondary-dns", "outgoing", "disable", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodPost, zoneRoot + "/outgoing/disable", "", "{}"}}},
		{"outgoing notify", []string{"secondary-dns", "outgoing", "notify", "--zone", secondaryDNSTestZoneID}, []request{{http.MethodPost, zoneRoot + "/outgoing/force_notify", "", "{}"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if next >= len(tc.want) {
					t.Fatalf("unexpected extra request %s %s", r.Method, r.URL.Path)
				}
				want := tc.want[next]
				next++
				if r.Method != want.method || r.URL.Path != want.path {
					t.Fatalf("request = %s %s, want %s %s", r.Method, r.URL.Path, want.method, want.path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				if want.body == "" {
					if len(body) != 0 {
						t.Fatalf("unexpected body %s", body)
					}
				} else {
					secondaryDNSAssertJSONEqual(t, body, want.body)
				}
				secondaryDNSWriteEnvelope(w, want.result, "null")
			}))
			defer server.Close()

			_, _, err := runSecondaryDNSCLI(t, server.URL, tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			if next != len(tc.want) {
				t.Fatalf("request count = %d, want %d", next, len(tc.want))
			}
		})
	}
}

func TestSecondaryDNSDestructiveDryRunMatrix(t *testing.T) {
	cases := [][]string{
		{"secondary-dns", "peers", "delete", "peer-id", "--account-id", secondaryDNSTestAccountID, "--dry-run"},
		{"secondary-dns", "tsigs", "delete", "tsig-id", "--account-id", secondaryDNSTestAccountID, "--dry-run"},
		{"secondary-dns", "incoming", "delete", "--zone", secondaryDNSTestZoneID, "--dry-run"},
		{"secondary-dns", "outgoing", "delete", "--zone", secondaryDNSTestZoneID, "--dry-run"},
	}
	for _, args := range cases {
		stdout, _, err := runSecondaryDNSCLI(t, "http://example.invalid", args...)
		if err != nil {
			t.Fatal(err)
		}
		var dump struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
			t.Fatal(err)
		}
		if dump.Method != http.MethodDelete {
			t.Fatalf("dry-run method = %q, want DELETE", dump.Method)
		}
	}
}
