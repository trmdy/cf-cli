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

const (
	dnsConfigTestZoneID    = "023e105f4ecef8ad9ca31a8372d0c353"
	dnsConfigTestAccountID = "abcdef0123456789abcdef0123456789"
	// 32 characters: the maximum the pinned spec allows for
	// dns-firewall_identifier.
	dnsConfigTestClusterID = "9f7b6a5c4d3e2f19081726354453627a"
	// 33 characters: one over the limit.
	dnsConfigTestLongClusterID = "9f7b6a5c4d3e2f19081726354453627ab"
)

// dnsConfigIsolateConfig blocks profile/env defaults from leaking into scope
// resolution tests.
func dnsConfigIsolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_ZONE_ID", "")
	t.Setenv("CF_ZONE_ID", "")
}

// runDNSConfigCLI drives the real command tree against a test server, with
// both scopes supplied through the global flags.
func runDNSConfigCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	dnsConfigIsolateConfig(t)
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetIn(strings.NewReader(""))
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", dnsConfigTestAccountID,
		"--zone-id", dnsConfigTestZoneID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func dnsConfigAssertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want JSON: %v\n%s", err, want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Fatalf("JSON mismatch\ngot:  %s\nwant: %s", gb, wb)
	}
}

func dnsConfigParseDump(t *testing.T, stdout string) (method, rawURL string, body json.RawMessage) {
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

// dnsConfigRecorder captures every request the CLI actually sends.
type dnsConfigRecorder struct {
	methods []string
	paths   []string
	queries []string
	bodies  []string
}

func (r *dnsConfigRecorder) record(req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.methods = append(r.methods, req.Method)
	r.paths = append(r.paths, req.URL.Path)
	r.queries = append(r.queries, req.URL.RawQuery)
	r.bodies = append(r.bodies, string(body))
}

// --- zone DNS settings: flag mapping ---------------------------------------

// bindDNSConfigSetFlags registers the `set` flags onto a bare command and
// parses args, so Changed() reflects a real invocation.
func bindDNSConfigSetFlags(t *testing.T, f *dnsConfigSettingsFlags, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "set"}
	dnsConfigSettingFlags(cmd, f)
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

// bindDNSConfigFirewallFlags registers every firewall setting (including the
// create-only address count, so its update-time rejection stays testable) and
// parses args.
func bindDNSConfigFirewallFlags(t *testing.T, f *dnsConfigFirewallFlags, args ...string) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{Use: "firewall"}
	dnsConfigFirewallSettingFlags(cmd, f)
	cmd.Flags().StringVar(&f.name, "name", "", "cluster name")
	cmd.Flags().IntVar(&f.ipCount, "dns-firewall-ip-count", 0, "address count")
	if err := cmd.Flags().Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return cmd
}

func TestDNSConfigSettingsPatchScalars(t *testing.T) {
	var f dnsConfigSettingsFlags
	cmd := bindDNSConfigSetFlags(t, &f, "--flatten-all-cnames=false", "--zone-mode", "DNS_ONLY", "--ns-ttl", "300")
	top, soa, ns, err := dnsConfigSettingsPatch(cmd, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(soa) != 0 || len(ns) != 0 {
		t.Fatalf("nested patches should be empty, got soa=%v ns=%v", soa, ns)
	}
	raw, _ := json.Marshal(top)
	dnsConfigAssertJSONEqual(t, raw, `{"flatten_all_cnames":false,"zone_mode":"dns_only","ns_ttl":300}`)
}

func TestDNSConfigSettingsPatchRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no flags", nil, "nothing to update"},
		{"ns-ttl low", []string{"--ns-ttl", "29"}, "--ns-ttl must be between 30 and 86400"},
		{"ns-ttl high", []string{"--ns-ttl", "86401"}, "--ns-ttl must be between 30 and 86400"},
		{"zone mode", []string{"--zone-mode", "flexible"}, `invalid --zone-mode "flexible"`},
		{"nameserver type", []string{"--nameserver-type", "cloudflare.fastest"}, `invalid --nameserver-type "cloudflare.fastest"`},
		{"ns-set low", []string{"--ns-set", "0"}, "--ns-set must be between 1 and 5"},
		{"ns-set high", []string{"--ns-set", "6"}, "--ns-set must be between 1 and 5"},
		{"soa refresh low", []string{"--soa-refresh", "599"}, "--soa-refresh must be between 600 and 86400"},
		{"soa retry high", []string{"--soa-retry", "86401"}, "--soa-retry must be between 600 and 86400"},
		{"soa expire low", []string{"--soa-expire", "86399"}, "--soa-expire must be between 86400 and 2419200"},
		{"soa expire high", []string{"--soa-expire", "2419201"}, "--soa-expire must be between 86400 and 2419200"},
		{"soa min-ttl low", []string{"--soa-min-ttl", "59"}, "--soa-min-ttl must be between 60 and 86400"},
		{"soa ttl low", []string{"--soa-ttl", "299"}, "--soa-ttl must be between 300 and 86400"},
		{"soa ttl high", []string{"--soa-ttl", "86401"}, "--soa-ttl must be between 300 and 86400"},
		{"empty mname", []string{"--soa-mname", "  "}, "--soa-mname must not be empty"},
		{"empty rname", []string{"--soa-rname", ""}, "--soa-rname must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f dnsConfigSettingsFlags
			cmd := bindDNSConfigSetFlags(t, &f, tc.args...)
			if _, _, _, err := dnsConfigSettingsPatch(cmd, f); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestDNSConfigSettingsPatchAcceptsBounds(t *testing.T) {
	var f dnsConfigSettingsFlags
	cmd := bindDNSConfigSetFlags(t, &f,
		"--ns-ttl", "30", "--ns-set", "5",
		"--soa-refresh", "600", "--soa-retry", "86400",
		"--soa-expire", "2419200", "--soa-min-ttl", "60", "--soa-ttl", "86400")
	top, soa, ns, err := dnsConfigSettingsPatch(cmd, f)
	if err != nil {
		t.Fatal(err)
	}
	if top["ns_ttl"] != 30 {
		t.Fatalf("ns_ttl = %v", top["ns_ttl"])
	}
	if ns["ns_set"] != 5 {
		t.Fatalf("ns_set = %v", ns["ns_set"])
	}
	raw, _ := json.Marshal(soa)
	dnsConfigAssertJSONEqual(t, raw, `{"refresh":600,"retry":86400,"expire":2419200,"min_ttl":60,"ttl":86400}`)
}

func TestDNSConfigValidateSOAReportsMissing(t *testing.T) {
	err := dnsConfigValidateSOA(map[string]any{"mname": nil, "rname": "admin.example.com", "ttl": 3600})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"refresh", "retry", "expire", "min_ttl"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to name %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "mname") {
		t.Fatalf("err = %v, a present null mname is valid", err)
	}
}

func TestDNSConfigValidateNameserversRequiresType(t *testing.T) {
	if err := dnsConfigValidateNameservers(map[string]any{"ns_set": 2}); err == nil {
		t.Fatal("expected an error for a nameservers object without a type")
	}
	if err := dnsConfigValidateNameservers(map[string]any{"type": "cloudflare.standard"}); err != nil {
		t.Fatal(err)
	}
}

// --- zone DNS settings: HTTP behavior --------------------------------------

func TestDNSConfigGetSendsZoneScopedRead(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"zone_mode":"standard"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "get")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "GET" {
		t.Fatalf("requests = %v %v", rec.methods, rec.paths)
	}
	if want := "/zones/" + dnsConfigTestZoneID + "/dns_settings"; rec.paths[0] != want {
		t.Fatalf("path = %s, want %s", rec.paths[0], want)
	}
	dnsConfigAssertJSONEqual(t, []byte(stdout), `{"zone_mode":"standard"}`)
}

func TestDNSConfigSetScalarOnlyDoesNotRead(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"multi_provider":true}}`))
	}))
	defer srv.Close()

	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--multi-provider"); err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "PATCH" {
		t.Fatalf("requests = %v, want a single PATCH", rec.methods)
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[0]), `{"multi_provider":true}`)
}

func TestDNSConfigSetMergesSOAAndPreservesUnknownFields(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{
				"flatten_all_cnames":false,
				"nameservers":{"type":"cloudflare.standard","ns_set":1},
				"soa":{"mname":"ns1.example.com","rname":"admin.example.com","refresh":10000,
				       "retry":2400,"expire":604800,"min_ttl":1800,"ttl":3600,"future_field":"keep"}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{}}`))
	}))
	defer srv.Close()

	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--soa-ttl", "7200", "--flatten-all-cnames"); err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 2 || rec.methods[0] != "GET" || rec.methods[1] != "PATCH" {
		t.Fatalf("requests = %v, want GET then PATCH", rec.methods)
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[1]), `{
		"flatten_all_cnames":true,
		"soa":{"mname":"ns1.example.com","rname":"admin.example.com","refresh":10000,
		       "retry":2400,"expire":604800,"min_ttl":1800,"ttl":7200,"future_field":"keep"}}`)
}

func TestDNSConfigSetMergesNameservers(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{
				"nameservers":{"type":"cloudflare.advanced","ns_set":1},
				"soa":{"mname":"ns1.example.com","rname":"admin.example.com","refresh":10000,
				       "retry":2400,"expire":604800,"min_ttl":1800,"ttl":3600}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{}}`))
	}))
	defer srv.Close()

	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--ns-set", "3"); err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 2 {
		t.Fatalf("requests = %v, want GET then PATCH", rec.methods)
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[1]),
		`{"nameservers":{"type":"cloudflare.advanced","ns_set":3}}`)
}

func TestDNSConfigSetRejectsIncompleteCurrentSOA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"soa":{"ttl":3600}}}`))
	}))
	defer srv.Close()

	_, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--soa-ttl", "7200")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want a complaint about the incomplete SOA", err)
	}
}

func TestDNSConfigSetDryRunReadsOnlyForNestedFields(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{
			"soa":{"mname":"ns1.example.com","rname":"admin.example.com","refresh":10000,
			       "retry":2400,"expire":604800,"min_ttl":1800,"ttl":3600}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--soa-min-ttl", "300", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "GET" {
		t.Fatalf("requests = %v, dry-run must read exactly once and never write", rec.methods)
	}
	method, rawURL, body := dnsConfigParseDump(t, stdout)
	if method != "PATCH" || !strings.HasSuffix(rawURL, "/zones/"+dnsConfigTestZoneID+"/dns_settings") {
		t.Fatalf("dump = %s %s", method, rawURL)
	}
	dnsConfigAssertJSONEqual(t, body,
		`{"soa":{"mname":"ns1.example.com","rname":"admin.example.com","refresh":10000,
		         "retry":2400,"expire":604800,"min_ttl":300,"ttl":3600}}`)
}

func TestDNSConfigSetScalarDryRunSendsNothing(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	stdout, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--zone-mode", "cdn_only", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 0 {
		t.Fatalf("requests = %v, want none", rec.methods)
	}
	method, _, body := dnsConfigParseDump(t, stdout)
	if method != "PATCH" {
		t.Fatalf("method = %s", method)
	}
	dnsConfigAssertJSONEqual(t, body, `{"zone_mode":"cdn_only"}`)
}

func TestDNSConfigSetValidatesBeforeAnyNetworkWork(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "set", "--soa-ttl", "1")
	if err == nil || !strings.Contains(err.Error(), "--soa-ttl must be between 300 and 86400") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.methods) != 0 {
		t.Fatalf("requests = %v, want none before validation passes", rec.methods)
	}
}

// --- DNSSEC ----------------------------------------------------------------

func TestBuildDNSConfigDNSSECEnableBodyOnlySendsGivenModes(t *testing.T) {
	cmd := newDNSConfigDNSSECEnableCmd(&globalOpts{})
	if err := cmd.Flags().Parse([]string{"--use-nsec3", "--presigned=false"}); err != nil {
		t.Fatal(err)
	}
	body, err := buildDNSConfigDNSSECEnableBody(cmd, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	dnsConfigAssertJSONEqual(t, body, `{"status":"active","dnssec_use_nsec3":true,"dnssec_presigned":false}`)
}

func TestDNSConfigDNSSECEnableSendsPatch(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"status":"pending"}}`))
	}))
	defer srv.Close()

	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "dnssec", "enable", "--multi-signer"); err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "PATCH" {
		t.Fatalf("requests = %v", rec.methods)
	}
	if want := "/zones/" + dnsConfigTestZoneID + "/dnssec"; rec.paths[0] != want {
		t.Fatalf("path = %s, want %s", rec.paths[0], want)
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[0]), `{"status":"active","dnssec_multi_signer":true}`)
}

func TestDNSConfigDNSSECGetReads(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"status":"active","ds":"example.com. 3600 IN DS 1 13 2 abc"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "dnssec", "get")
	if err != nil {
		t.Fatal(err)
	}
	if rec.methods[0] != "GET" || rec.paths[0] != "/zones/"+dnsConfigTestZoneID+"/dnssec" {
		t.Fatalf("request = %s %s", rec.methods[0], rec.paths[0])
	}
	if !strings.Contains(stdout, `"status": "active"`) && !strings.Contains(stdout, `"status":"active"`) {
		t.Fatalf("stdout = %s", stdout)
	}
}

func TestDNSConfigDNSSECDisableSendsDisabledStatus(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"status":"pending-disabled"}}`))
	}))
	defer srv.Close()

	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "dnssec", "disable", "--force"); err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "PATCH" {
		t.Fatalf("requests = %v", rec.methods)
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[0]), `{"status":"disabled"}`)
}

func TestDNSConfigDNSSECDisableWithoutForceAborts(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "dnssec", "disable")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want an abort without a TTY", err)
	}
	if len(rec.methods) != 0 {
		t.Fatalf("requests = %v, want none", rec.methods)
	}
}

func TestDNSConfigDNSSECDisableDryRunSkipsConfirmation(t *testing.T) {
	stdout, _, err := runDNSConfigCLI(t, "http://example.invalid",
		"dns", "config", "dnssec", "disable", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	method, rawURL, body := dnsConfigParseDump(t, stdout)
	if method != "PATCH" || !strings.HasSuffix(rawURL, "/zones/"+dnsConfigTestZoneID+"/dnssec") {
		t.Fatalf("dump = %s %s", method, rawURL)
	}
	dnsConfigAssertJSONEqual(t, body, `{"status":"disabled"}`)
}

// --- DNS Firewall: flag mapping --------------------------------------------

func TestDNSConfigValidateUpstreamIPs(t *testing.T) {
	got, err := dnsConfigValidateUpstreamIPs([]string{"192.0.2.1", " 2001:db8::1 "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "192.0.2.1" || got[1] != "2001:db8::1" {
		t.Fatalf("ips = %v", got)
	}
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, "--upstream-ip is required"},
		{"not an address", []string{"ns1.example.com"}, "expected an IPv4 or IPv6 address"},
		{"cidr", []string{"192.0.2.0/24"}, "expected an IPv4 or IPv6 address"},
		{"duplicate", []string{"192.0.2.1", "192.0.2.1"}, "duplicate --upstream-ip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := dnsConfigValidateUpstreamIPs(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestDNSConfigFirewallFieldsCreate(t *testing.T) {
	var f dnsConfigFirewallFlags
	cmd := bindDNSConfigFirewallFlags(t, &f,
		"--upstream-ip", "192.0.2.1",
		"--minimum-cache-ttl", "30", "--maximum-cache-ttl", "36000",
		"--negative-cache-ttl", "60", "--retries", "0",
		"--deprecate-any-requests", "--ecs-fallback=false",
		"--attack-mitigation", "--attack-mitigation-only-when-unhealthy=false",
		"--dns-firewall-ip-count", "10", "--name", "edge-resolver")
	body, err := dnsConfigFirewallFields(cmd, f, true)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	dnsConfigAssertJSONEqual(t, raw, `{
		"name":"edge-resolver","upstream_ips":["192.0.2.1"],
		"minimum_cache_ttl":30,"maximum_cache_ttl":36000,"negative_cache_ttl":60,"retries":0,
		"deprecate_any_requests":true,"ecs_fallback":false,
		"attack_mitigation":{"enabled":true,"only_when_upstream_unhealthy":false},
		"dns_firewall_ip_count":10}`)
}

func TestDNSConfigFirewallFieldsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		create bool
		args   []string
		fname  string
		want   string
	}{
		{"blank name", true, []string{"--upstream-ip", "192.0.2.1"}, "  ", "cluster name must not be empty"},
		{"long name", true, []string{"--upstream-ip", "192.0.2.1"}, strings.Repeat("a", 161), "at most 160 characters"},
		{"no upstream", true, nil, "edge", "--upstream-ip is required"},
		{"min cache low", true, []string{"--upstream-ip", "192.0.2.1", "--minimum-cache-ttl", "29"}, "edge", "--minimum-cache-ttl must be between 30 and 36000"},
		{"max cache high", true, []string{"--upstream-ip", "192.0.2.1", "--maximum-cache-ttl", "36001"}, "edge", "--maximum-cache-ttl must be between 30 and 36000"},
		{"negative cache low", true, []string{"--upstream-ip", "192.0.2.1", "--negative-cache-ttl", "29"}, "edge", "--negative-cache-ttl must be between 30 and 36000"},
		{"retries high", true, []string{"--upstream-ip", "192.0.2.1", "--retries", "3"}, "edge", "--retries must be between 0 and 2"},
		{"ratelimit low", true, []string{"--upstream-ip", "192.0.2.1", "--ratelimit", "99"}, "edge", "--ratelimit must be between 100 and 1000000000"},
		{"ratelimit high", true, []string{"--upstream-ip", "192.0.2.1", "--ratelimit", "1000000001"}, "edge", "--ratelimit must be between 100 and 1000000000"},
		{"ratelimit conflict", true, []string{"--upstream-ip", "192.0.2.1", "--ratelimit", "500", "--no-ratelimit"}, "edge", "mutually exclusive"},
		{"no-ratelimit false", true, []string{"--upstream-ip", "192.0.2.1", "--no-ratelimit=false"}, "edge", "takes no false form"},
		{"lone mitigation detail", false, []string{"--attack-mitigation-only-when-unhealthy"}, "", "needs --attack-mitigation as well"},
		{"ip count on update", false, []string{"--dns-firewall-ip-count", "3"}, "", "only be set when the cluster is created"},
		{"ip count high", true, []string{"--upstream-ip", "192.0.2.1", "--dns-firewall-ip-count", "11"}, "edge", "--dns-firewall-ip-count must be between 1 and 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f dnsConfigFirewallFlags
			cmd := bindDNSConfigFirewallFlags(t, &f, tc.args...)
			if tc.create {
				f.name = tc.fname
			}
			if _, err := dnsConfigFirewallFields(cmd, f, tc.create); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestDNSConfigFirewallClusterIDLength(t *testing.T) {
	if err := dnsConfigFirewallClusterID(dnsConfigTestClusterID); err != nil {
		t.Fatalf("32 characters must be accepted: %v", err)
	}
	if err := dnsConfigFirewallClusterID(dnsConfigTestLongClusterID); err == nil ||
		!strings.Contains(err.Error(), "at most 32 characters, got 33") {
		t.Fatalf("err = %v, want a 33-character rejection", err)
	}
	if err := dnsConfigFirewallClusterID("   "); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("err = %v, want an empty-ID rejection", err)
	}
}

// TestDNSConfigFirewallRejectsLongClusterIDBeforeClientWork pins that an
// over-long identifier fails locally: no request reaches the server for get,
// update, or delete.
func TestDNSConfigFirewallRejectsLongClusterIDBeforeClientWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"get", []string{"dns", "config", "firewall", "get", dnsConfigTestLongClusterID}},
		{"update", []string{"dns", "config", "firewall", "update", dnsConfigTestLongClusterID, "--retries", "1"}},
		{"delete", []string{"dns", "config", "firewall", "delete", dnsConfigTestLongClusterID, "--force"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &dnsConfigRecorder{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				rec.record(r)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer srv.Close()

			_, _, err := runDNSConfigCLI(t, srv.URL, tc.args...)
			if err == nil || !strings.Contains(err.Error(), "at most 32 characters") {
				t.Fatalf("err = %v, want a length rejection", err)
			}
			if len(rec.methods) != 0 {
				t.Fatalf("requests = %v, want none", rec.methods)
			}
		})
	}
}

// TestDNSConfigFirewallNameLengthIsCodePoints pins that the 160-character
// limit counts Unicode code points, not bytes: 160 three-byte runes (480
// bytes) are accepted and 161 are not.
func TestDNSConfigFirewallNameLengthIsCodePoints(t *testing.T) {
	const rune3 = "☃" // three bytes in UTF-8
	ok := strings.Repeat(rune3, 160)
	tooLong := strings.Repeat(rune3, 161)
	if len(ok) != 480 || len(tooLong) != 483 {
		t.Fatalf("fixture byte lengths = %d/%d, want 480/483", len(ok), len(tooLong))
	}

	var f dnsConfigFirewallFlags
	cmd := bindDNSConfigFirewallFlags(t, &f, "--upstream-ip", "192.0.2.1")
	f.name = ok
	body, err := dnsConfigFirewallFields(cmd, f, true)
	if err != nil {
		t.Fatalf("160 code points must be accepted: %v", err)
	}
	if body["name"] != ok {
		t.Fatalf("name = %v", body["name"])
	}

	f.name = tooLong
	if _, err := dnsConfigFirewallFields(cmd, f, true); err == nil ||
		!strings.Contains(err.Error(), "at most 160 characters, got 161") {
		t.Fatalf("err = %v, want a 161-code-point rejection", err)
	}
}

func TestDNSConfigFirewallFieldsUpdateIsPartial(t *testing.T) {
	var f dnsConfigFirewallFlags
	cmd := bindDNSConfigFirewallFlags(t, &f, "--no-ratelimit")
	body, err := dnsConfigFirewallFields(cmd, f, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(body)
	dnsConfigAssertJSONEqual(t, raw, `{"ratelimit":null}`)
}

// --- DNS Firewall: HTTP behavior -------------------------------------------

func TestDNSConfigFirewallListPaginatesAndRendersTable(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
				{"id":"b2","name":"second","upstream_ips":["192.0.2.9"],"dns_firewall_ips":["203.0.113.9"],"ratelimit":600,"modified_on":"2026-08-02T00:00:00Z"}],
				"result_info":{"page":2,"per_page":1,"count":1,"total_count":2,"total_pages":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":[
			{"id":"a1","name":"first","upstream_ips":["192.0.2.1","2001:db8::1"],"dns_firewall_ips":["203.0.113.1"],"ratelimit":null,"modified_on":"2026-08-01T00:00:00Z"}],
			"result_info":{"page":1,"per_page":1,"count":1,"total_count":2,"total_pages":2}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "list")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 2 {
		t.Fatalf("requests = %v, want both pages fetched", rec.methods)
	}
	if rec.paths[0] != "/accounts/"+dnsConfigTestAccountID+"/dns_firewall" {
		t.Fatalf("path = %s", rec.paths[0])
	}
	if !strings.Contains(rec.queries[0], "per_page=100") {
		t.Fatalf("query = %s, want per_page=100", rec.queries[0])
	}
	for _, want := range []string{"FIREWALL IPS", "first", "second", "off", "600", "2001:db8::1"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestDNSConfigFirewallGetSendsAccountScopedRead(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"` + dnsConfigTestClusterID + `","name":"edge-resolver"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "get", dnsConfigTestClusterID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "GET" {
		t.Fatalf("requests = %v, want a single GET", rec.methods)
	}
	if want := "/accounts/" + dnsConfigTestAccountID + "/dns_firewall/" + dnsConfigTestClusterID; rec.paths[0] != want {
		t.Fatalf("path = %s, want %s", rec.paths[0], want)
	}
	if rec.queries[0] != "" {
		t.Fatalf("query = %q, want none", rec.queries[0])
	}
	if rec.bodies[0] != "" {
		t.Fatalf("body = %q, want none", rec.bodies[0])
	}
	dnsConfigAssertJSONEqual(t, []byte(stdout),
		`{"id":"`+dnsConfigTestClusterID+`","name":"edge-resolver"}`)
}

func TestDNSConfigFirewallCreateSendsAccountScopedPost(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"a1"}}`))
	}))
	defer srv.Close()

	_, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "create", "edge-resolver",
		"--upstream-ip", "192.0.2.1", "--upstream-ip", "2001:db8::1", "--attack-mitigation")
	if err != nil {
		t.Fatal(err)
	}
	if rec.methods[0] != "POST" || rec.paths[0] != "/accounts/"+dnsConfigTestAccountID+"/dns_firewall" {
		t.Fatalf("request = %s %s", rec.methods[0], rec.paths[0])
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[0]), `{
		"name":"edge-resolver","upstream_ips":["192.0.2.1","2001:db8::1"],
		"attack_mitigation":{"enabled":true}}`)
}

func TestDNSConfigFirewallUpdateNeedsAtLeastOneFlag(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "update", dnsConfigTestClusterID)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.methods) != 0 {
		t.Fatalf("requests = %v, want none", rec.methods)
	}
}

func TestDNSConfigFirewallUpdateSendsPatch(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"a1"}}`))
	}))
	defer srv.Close()

	_, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "update", dnsConfigTestClusterID,
		"--name", "renamed", "--retries", "1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "PATCH" {
		t.Fatalf("requests = %v, want one PATCH and no read", rec.methods)
	}
	if want := "/accounts/" + dnsConfigTestAccountID + "/dns_firewall/" + dnsConfigTestClusterID; rec.paths[0] != want {
		t.Fatalf("path = %s, want %s", rec.paths[0], want)
	}
	dnsConfigAssertJSONEqual(t, []byte(rec.bodies[0]), `{"name":"renamed","retries":1}`)
}

func TestDNSConfigFirewallDeleteRequiresConfirmation(t *testing.T) {
	rec := &dnsConfigRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"messages":[],"result":{"id":"a1"}}`))
	}))
	defer srv.Close()

	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "delete", dnsConfigTestClusterID); err == nil {
		t.Fatal("expected an abort without --force")
	}
	if len(rec.methods) != 0 {
		t.Fatalf("requests = %v, want none", rec.methods)
	}
	if _, _, err := runDNSConfigCLI(t, srv.URL, "dns", "config", "firewall", "delete", dnsConfigTestClusterID, "--force"); err != nil {
		t.Fatal(err)
	}
	if len(rec.methods) != 1 || rec.methods[0] != "DELETE" {
		t.Fatalf("requests = %v", rec.methods)
	}
}

// --- scope errors ----------------------------------------------------------

func TestDNSConfigFirewallNeedsAccount(t *testing.T) {
	dnsConfigIsolateConfig(t)
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t",
		"dns", "config", "firewall", "list"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("err = %v", err)
	}
}

func TestDNSConfigDNSSECNeedsZone(t *testing.T) {
	dnsConfigIsolateConfig(t)
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t", "--dry-run",
		"dns", "config", "dnssec", "get"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no zone specified") {
		t.Fatalf("err = %v", err)
	}
}
