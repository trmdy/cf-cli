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
	"unicode/utf8"

	"github.com/spf13/cobra"
)

const (
	turnstileTestAccountID = "abcdef0123456789abcdef0123456789"
	turnstileTestSiteKey   = "0x4AAAAAAADnPIDROrmt1Wwj"
)

func runTurnstileCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", turnstileTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func turnstileWidgetsPath() string {
	return "/accounts/" + turnstileTestAccountID + "/challenges/widgets"
}

// --- body building ---------------------------------------------------------

func TestBuildTurnstileCreateBodyDefaults(t *testing.T) {
	body, err := buildTurnstileCreateBody(turnstileCreateOpts{
		Name:    "checkout form",
		Domains: []string{"example.com"},
		Mode:    "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	turnstileAssertJSONEqual(t, body, `{"name":"checkout form","domains":["example.com"],"mode":"managed"}`)
}

func TestBuildTurnstileCreateBodyAllFields(t *testing.T) {
	yes, no := true, false
	body, err := buildTurnstileCreateBody(turnstileCreateOpts{
		Name:           "signup",
		Domains:        []string{"example.com", "www.example.com"},
		Mode:           "invisible",
		ClearanceLevel: "interactive",
		Region:         "china",
		BotFightMode:   &yes,
		EphemeralID:    &no,
		Offlabel:       &yes,
	})
	if err != nil {
		t.Fatal(err)
	}
	turnstileAssertJSONEqual(t, body, `{
		"name":"signup",
		"domains":["example.com","www.example.com"],
		"mode":"invisible",
		"clearance_level":"interactive",
		"region":"china",
		"bot_fight_mode":true,
		"ephemeral_id":false,
		"offlabel":true
	}`)
}

// An unset --region is omitted so the API applies its own default.
func TestBuildTurnstileCreateBodyOmitsUnsetRegion(t *testing.T) {
	body, err := buildTurnstileCreateBody(turnstileCreateOpts{
		Name:    "checkout",
		Domains: []string{"example.com"},
		Mode:    "managed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "region") {
		t.Fatalf("body should omit region when unset: %s", body)
	}
}

func TestBuildTurnstileCreateBodyValidation(t *testing.T) {
	valid := func(mutate func(*turnstileCreateOpts)) func() ([]byte, error) {
		return func() ([]byte, error) {
			o := turnstileCreateOpts{Name: "w", Domains: []string{"example.com"}, Mode: "managed"}
			mutate(&o)
			return buildTurnstileCreateBody(o)
		}
	}
	cases := []struct {
		name    string
		run     func() ([]byte, error)
		wantErr string
	}{
		{"empty name", valid(func(o *turnstileCreateOpts) { o.Name = "  " }), "name is empty"},
		{"no domains", valid(func(o *turnstileCreateOpts) { o.Domains = nil }), "--domain"},
		{"empty domain", valid(func(o *turnstileCreateOpts) { o.Domains = []string{""} }), "--domain"},
		{"bad mode", valid(func(o *turnstileCreateOpts) { o.Mode = "interactive" }), "--mode must be one of"},
		{"bad clearance level", valid(func(o *turnstileCreateOpts) { o.ClearanceLevel = "sometimes" }), "--clearance-level must be one of"},
		{"bad region", valid(func(o *turnstileCreateOpts) { o.Region = "mars" }), "--region must be one of: world, china"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.run(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// --- schema bounds ---------------------------------------------------------

func turnstileName(n int) string { return strings.Repeat("a", n) }

func turnstileDomainList(n int) []string {
	d := make([]string, n)
	for i := range d {
		d[i] = "d" + strconv.Itoa(i) + ".example.com"
	}
	return d
}

func TestTurnstileNameBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"one character", turnstileName(1), false},
		{"at the limit", turnstileName(turnstileMaxNameLen), false},
		{"one over the limit", turnstileName(turnstileMaxNameLen + 1), true},
		{"empty", "", true},
		// Length is counted in characters, not bytes: 254 multi-byte runes are
		// 762 bytes but still a legal name.
		{"multi-byte at the limit", strings.Repeat("é", turnstileMaxNameLen), false},
		{"multi-byte one over", strings.Repeat("é", turnstileMaxNameLen+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTurnstileName(tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %d-rune name", utf8.RuneCountInString(tc.value))
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Same bound, enforced through the create path.
			_, cerr := buildTurnstileCreateBody(turnstileCreateOpts{Name: tc.value, Domains: []string{"example.com"}, Mode: "managed"})
			if (cerr != nil) != tc.wantErr {
				t.Fatalf("create body error = %v, wantErr %v", cerr, tc.wantErr)
			}
		})
	}
}

func TestTurnstileDomainCountBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		domains []string
		wantErr bool
	}{
		{"one domain", turnstileDomainList(1), false},
		{"at the limit", turnstileDomainList(turnstileMaxDomains), false},
		{"one over the limit", turnstileDomainList(turnstileMaxDomains + 1), true},
		{"none", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTurnstileDomains(tc.domains)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate error = %v, wantErr %v", err, tc.wantErr)
			}
			_, cerr := buildTurnstileCreateBody(turnstileCreateOpts{Name: "w", Domains: tc.domains, Mode: "managed"})
			if (cerr != nil) != tc.wantErr {
				t.Fatalf("create body error = %v, wantErr %v", cerr, tc.wantErr)
			}
		})
	}
	if err := validateTurnstileDomains(turnstileDomainList(turnstileMaxDomains + 1)); err == nil ||
		!strings.Contains(err.Error(), "at most 10") {
		t.Fatalf("error should name the limit, got %v", err)
	}
}

// The bounds apply to the merged replacement body too, including when the
// offending value came from the server rather than from a flag.
func TestTurnstileUpdateEnforcesBoundsOnMergedBody(t *testing.T) {
	overLongName := turnstileName(turnstileMaxNameLen + 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected %s after a bounds failure", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"success": true,
			"result":  map[string]any{"sitekey": turnstileTestSiteKey, "name": overLongName, "domains": []string{"example.com"}, "mode": "managed"},
		})
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	_, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "update", turnstileTestSiteKey, "--mode", "invisible")
	if err == nil || !strings.Contains(err.Error(), "at most 254") {
		t.Fatalf("expected merged-body bounds error, got %v", err)
	}
	if !strings.Contains(err.Error(), turnstileTestSiteKey) {
		t.Fatalf("error should name the widget: %v", err)
	}
}

func TestTurnstileUpdateRejectsOutOfBoundsFlagsBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	cases := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"name too long", []string{"--name", turnstileName(turnstileMaxNameLen + 1)}, "at most 254"},
		{"too many domains", append([]string{}, turnstileDomainFlags(turnstileMaxDomains+1)...), "at most 10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"turnstile", "update", turnstileTestSiteKey}, tc.args...)
			_, _, err := runTurnstileCLI(t, srv.URL, args...)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func turnstileDomainFlags(n int) []string {
	var args []string
	for _, d := range turnstileDomainList(n) {
		args = append(args, "--domain", d)
	}
	return args
}

// --- filter grammar --------------------------------------------------------

func TestParseTurnstileFilter(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		want     string
		wantSend bool
		wantErr  bool
	}{
		{"name field", "name:checkout", "name:checkout", true, false},
		{"sitekey field", "sitekey:0x4AAA", "sitekey:0x4AAA", true, false},
		{"field is case-insensitive", "Name:Checkout", "name:Checkout", true, false},
		{"value keeps colons", "name:a:b", "name:a:b", true, false},
		{"explicitly empty means no filter", "", "", false, false},
		{"bare term", "checkout", "", false, true},
		{"unknown field", "domain:example.com", "", false, true},
		{"empty value", "name:", "", false, true},
		{"empty field", ":checkout", "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, send, err := parseTurnstileFilter(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if !strings.Contains(err.Error(), "field:value") {
					t.Errorf("error should show the grammar: %v", err)
				}
				return
			}
			if got != tc.want || send != tc.wantSend {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, send, tc.want, tc.wantSend)
			}
		})
	}
}

func TestTurnstileListRejectsBareFilterBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "list", "--filter", "checkout")
	if err == nil || !strings.Contains(err.Error(), "field:value") {
		t.Fatalf("expected filter grammar error, got %v", err)
	}
}

// An explicitly empty --filter behaves exactly like omitting it: no filter
// parameter is sent, and it is not an error.
func TestTurnstileListExplicitlyEmptyFilterSendsNoFilter(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[]}`))
	}))
	defer srv.Close()

	if _, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "list", "--filter", ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotQuery, "filter") {
		t.Fatalf("query should carry no filter: %s", gotQuery)
	}
}

// --- update merge ----------------------------------------------------------

func turnstileCurrentWidget() turnstileWidget {
	yes, no := true, false
	return turnstileWidget{
		SiteKey:        turnstileTestSiteKey,
		Name:           "old name",
		Domains:        []string{"example.com"},
		Mode:           "managed",
		ClearanceLevel: "jschallenge",
		BotFightMode:   &yes,
		EphemeralID:    &no,
		Offlabel:       &no,
		Region:         "world",
		CreatedOn:      "2024-01-01T00:00:00Z",
	}
}

func TestMergeTurnstileWidgetKeepsUntouchedFields(t *testing.T) {
	name := "new name"
	got := mergeTurnstileWidget(turnstileCurrentWidget(), turnstileOverrides{Name: &name})
	if got.Name != "new name" {
		t.Errorf("name = %q", got.Name)
	}
	if strings.Join(got.Domains, ",") != "example.com" {
		t.Errorf("domains = %v", got.Domains)
	}
	if got.Mode != "managed" || got.ClearanceLevel != "jschallenge" {
		t.Errorf("mode/clearance = %q/%q", got.Mode, got.ClearanceLevel)
	}
	if got.BotFightMode == nil || !*got.BotFightMode {
		t.Errorf("bot_fight_mode = %v", got.BotFightMode)
	}
	if got.EphemeralID == nil || *got.EphemeralID {
		t.Errorf("ephemeral_id = %v", got.EphemeralID)
	}
	// Server-owned fields must not be echoed back in the replacement body.
	if got.SiteKey != "" || got.CreatedOn != "" || got.Region != "" {
		t.Errorf("merged widget leaked read-only fields: %+v", got)
	}
}

func TestMergeTurnstileWidgetOverridesEveryField(t *testing.T) {
	name, mode, clearance := "new", "invisible", "no_clearance"
	no, yes := false, true
	got := mergeTurnstileWidget(turnstileCurrentWidget(), turnstileOverrides{
		Name:           &name,
		Domains:        []string{"a.example.com", "b.example.com"},
		Mode:           &mode,
		ClearanceLevel: &clearance,
		BotFightMode:   &no,
		EphemeralID:    &yes,
		Offlabel:       &yes,
	})
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	turnstileAssertJSONEqual(t, body, `{
		"name":"new",
		"domains":["a.example.com","b.example.com"],
		"mode":"invisible",
		"clearance_level":"no_clearance",
		"bot_fight_mode":false,
		"ephemeral_id":true,
		"offlabel":true
	}`)
}

func TestTurnstileOverridesValidate(t *testing.T) {
	empty := ""
	bad := "sideways"
	cases := []struct {
		name    string
		o       turnstileOverrides
		wantErr string
	}{
		{"no changes", turnstileOverrides{}, "nothing to update"},
		{"empty name", turnstileOverrides{Name: &empty}, "name is empty"},
		{"empty domain", turnstileOverrides{Domains: []string{"example.com", " "}}, "--domain"},
		{"bad mode", turnstileOverrides{Mode: &bad}, "--mode must be one of"},
		{"bad clearance", turnstileOverrides{ClearanceLevel: &bad}, "--clearance-level must be one of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.o.validate(); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
	no := false
	if err := (turnstileOverrides{BotFightMode: &no}).validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- request construction --------------------------------------------------

func TestTurnstileListHTTPRequest(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"sitekey":"` + turnstileTestSiteKey + `","name":"checkout","mode":"managed","domains":["example.com","www.example.com"],"created_on":"2024-01-01T00:00:00Z"}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "list", "--filter", "name:check", "--order", "created_on", "--direction", "desc")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != turnstileWidgetsPath() {
		t.Errorf("path = %s", gotPath)
	}
	for _, want := range []string{"filter=name%3Acheck", "order=created_on", "direction=desc", "per_page=100"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	for _, want := range []string{"SITEKEY", "NAME", "MODE", "DOMAINS", turnstileTestSiteKey, "example.com,www.example.com"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestTurnstileListRejectsBadEnums(t *testing.T) {
	for _, tc := range []struct{ flag, value, wantErr string }{
		{"--order", "sitekeys", "--order must be one of"},
		{"--direction", "sideways", "--direction must be one of"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			_, _, err := runTurnstileCLI(t, "http://example.invalid", "turnstile", "list", tc.flag, tc.value, "--dry-run")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestTurnstileListJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"sitekey":"` + turnstileTestSiteKey + `","name":"checkout"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var widgets []turnstileWidget
	if err := json.Unmarshal([]byte(stdout), &widgets); err != nil {
		t.Fatalf("json output not an array: %v\n%s", err, stdout)
	}
	if len(widgets) != 1 || widgets[0].SiteKey != turnstileTestSiteKey {
		t.Fatalf("widgets = %+v", widgets)
	}
}

func TestTurnstileGetHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `","secret":"0x4xxx"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "get", turnstileTestSiteKey)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != turnstileWidgetsPath()+"/"+turnstileTestSiteKey {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, "0x4xxx") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestTurnstileCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runTurnstileCLI(t, srv.URL,
		"turnstile", "create", "checkout form",
		"--domain", "example.com",
		"--domain", "www.example.com",
		"--mode", "invisible",
		"--bot-fight-mode",
		"--offlabel=false",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != turnstileWidgetsPath() {
		t.Errorf("path = %s", gotPath)
	}
	turnstileAssertJSONEqual(t, gotBody, `{
		"name":"checkout form",
		"domains":["example.com","www.example.com"],
		"mode":"invisible",
		"bot_fight_mode":true,
		"offlabel":false
	}`)
	if !strings.Contains(stdout, turnstileTestSiteKey) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestTurnstileCreateDryRun(t *testing.T) {
	stdout, _, err := runTurnstileCLI(t, "http://example.invalid",
		"turnstile", "create", "checkout",
		"--domain", "example.com",
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
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	if dump.Method != "POST" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, turnstileWidgetsPath()) {
		t.Errorf("url = %s", dump.URL)
	}
	turnstileAssertJSONEqual(t, dump.Body, `{"name":"checkout","domains":["example.com"],"mode":"managed"}`)
}

func TestTurnstileCreateRequiresDomain(t *testing.T) {
	_, _, err := runTurnstileCLI(t, "http://example.invalid", "turnstile", "create", "checkout", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("expected domain requirement error, got %v", err)
	}
}

func TestTurnstileUpdateReadsThenReplaces(t *testing.T) {
	var puts int
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := turnstileWidgetsPath() + "/" + turnstileTestSiteKey
		switch {
		case r.Method == "GET" && r.URL.Path == path:
			_, _ = w.Write([]byte(`{"success":true,"result":{
				"sitekey":"` + turnstileTestSiteKey + `",
				"name":"old name",
				"domains":["example.com"],
				"mode":"managed",
				"clearance_level":"jschallenge",
				"bot_fight_mode":true,
				"region":"world",
				"created_on":"2024-01-01T00:00:00Z"
			}}`))
		case r.Method == "PUT" && r.URL.Path == path:
			puts++
			gotBody, _ = io.ReadAll(r.Body)
			_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `","name":"new name"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	stdout, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "update", turnstileTestSiteKey, "--name", "new name")
	if err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Fatalf("PUT count = %d", puts)
	}
	// Unchanged fields are preserved; read-only fields are not sent back.
	turnstileAssertJSONEqual(t, gotBody, `{
		"name":"new name",
		"domains":["example.com"],
		"mode":"managed",
		"clearance_level":"jschallenge",
		"bot_fight_mode":true
	}`)
	if !strings.Contains(stdout, "new name") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestTurnstileUpdateDryRunDoesNotWrite(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("dry-run sent a %s request", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `","name":"old","domains":["example.com"],"mode":"managed"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "update", turnstileTestSiteKey, "--mode", "invisible", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	if dump.Method != "PUT" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, turnstileWidgetsPath()+"/"+turnstileTestSiteKey) {
		t.Errorf("url = %s", dump.URL)
	}
	turnstileAssertJSONEqual(t, dump.Body, `{"name":"old","domains":["example.com"],"mode":"invisible"}`)
}

func TestTurnstileUpdateWithoutChangesFailsBeforeAnyRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "update", turnstileTestSiteKey)
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("expected nothing-to-update error, got %v", err)
	}
}

func TestTurnstileDeleteHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "delete", turnstileTestSiteKey, "--force"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != turnstileWidgetsPath()+"/"+turnstileTestSiteKey {
		t.Errorf("path = %s", gotPath)
	}
}

func TestTurnstileDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "delete", turnstileTestSiteKey)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestTurnstileRotateSecretHTTPRequest(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"grace period", nil, `{"invalidate_immediately":false}`},
		{"immediate", []string{"--invalidate-immediately"}, `{"invalidate_immediately":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `","secret":"0x4newsecret"}}`))
			}))
			defer srv.Close()

			args := append([]string{"turnstile", "rotate-secret", turnstileTestSiteKey, "--force"}, tc.args...)
			stdout, _, err := runTurnstileCLI(t, srv.URL, args...)
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != "POST" {
				t.Errorf("method = %s", gotMethod)
			}
			if gotPath != turnstileWidgetsPath()+"/"+turnstileTestSiteKey+"/rotate_secret" {
				t.Errorf("path = %s", gotPath)
			}
			turnstileAssertJSONEqual(t, gotBody, tc.want)
			if !strings.Contains(stdout, "0x4newsecret") {
				t.Errorf("stdout = %s", stdout)
			}
		})
	}
}

func TestTurnstileRotateSecretRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "rotate-secret", turnstileTestSiteKey)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestTurnstileSiteKeyIsPathEscaped(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	if _, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "get", "weird/key"); err != nil {
		t.Fatal(err)
	}
	if gotPath != turnstileWidgetsPath()+"/weird%2Fkey" {
		t.Errorf("escaped path = %s", gotPath)
	}
}

// --- region is create-only -------------------------------------------------

func TestTurnstileCreateSendsRegion(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runTurnstileCLI(t, srv.URL,
		"turnstile", "create", "checkout",
		"--domain", "example.cn",
		"--region", "china",
	); err != nil {
		t.Fatal(err)
	}
	turnstileAssertJSONEqual(t, gotBody, `{"name":"checkout","domains":["example.cn"],"mode":"managed","region":"china"}`)
}

func TestTurnstileCreateRejectsUnknownRegion(t *testing.T) {
	_, _, err := runTurnstileCLI(t, "http://example.invalid",
		"turnstile", "create", "checkout",
		"--domain", "example.com",
		"--region", "mars",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "--region must be one of: world, china") {
		t.Fatalf("expected region enum error, got %v", err)
	}
}

// Region is immutable, so update must not offer it and must never send it —
// not from a flag and not by echoing back what the server reported.
func TestTurnstileUpdateHasNoRegionFlag(t *testing.T) {
	var update *cobra.Command
	for _, c := range newTurnstileCmd(&globalOpts{}).Commands() {
		if c.Name() == "update" {
			update = c
		}
	}
	if update == nil {
		t.Fatal("update command not found")
	}
	if f := update.Flags().Lookup("region"); f != nil {
		t.Fatal("update must not expose --region: the API fixes region at creation")
	}
	_, _, err := runTurnstileCLI(t, "http://example.invalid",
		"turnstile", "update", turnstileTestSiteKey, "--region", "china", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("expected unknown flag error, got %v", err)
	}
}

func TestTurnstileUpdateNeverSendsRegion(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `","name":"old","domains":["example.cn"],"mode":"managed","region":"china"}}`))
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"success":true,"result":{"sitekey":"` + turnstileTestSiteKey + `"}}`))
	}))
	defer srv.Close()

	if _, _, err := runTurnstileCLI(t, srv.URL, "turnstile", "update", turnstileTestSiteKey, "--name", "new"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gotBody), "region") {
		t.Fatalf("update body must not carry region: %s", gotBody)
	}
	turnstileAssertJSONEqual(t, gotBody, `{"name":"new","domains":["example.cn"],"mode":"managed"}`)
}

// --- account scoping and help ---------------------------------------------

func TestTurnstileRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t", "--dry-run", "turnstile", "list"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "missing account ID") {
		t.Fatalf("expected missing account ID error, got %v", err)
	}
	if !strings.Contains(err.Error(), "--account-id") {
		t.Fatalf("error should say how to provide the account ID: %v", err)
	}
}

func TestTurnstileCommandsRejectStrayArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"list", []string{"turnstile", "list", "extra", "--dry-run"}},
		{"get", []string{"turnstile", "get", turnstileTestSiteKey, "extra", "--dry-run"}},
		{"delete", []string{"turnstile", "delete", turnstileTestSiteKey, "extra", "--force", "--dry-run"}},
		{"rotate-secret", []string{"turnstile", "rotate-secret", turnstileTestSiteKey, "extra", "--force", "--dry-run"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := runTurnstileCLI(t, "http://example.invalid", tc.args...); err == nil {
				t.Fatal("expected error for stray positional args")
			}
		})
	}
}

func TestTurnstileHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"turnstile", "create", "--help"}, []string{"cf turnstile create", "--domain", "--mode", "--region", "world or china", "--region china"}},
		{[]string{"turnstile", "list", "--help"}, []string{"cf turnstile list --filter name:checkout", "field:value", "name or sitekey"}},
		{[]string{"turnstile", "update", "--help"}, []string{"cf turnstile update", "--clearance-level", "reads the widget"}},
		{[]string{"turnstile", "rotate-secret", "--help"}, []string{"cf turnstile rotate-secret", "--invalidate-immediately", "--force"}},
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

func turnstileAssertJSONEqual(t *testing.T, got []byte, want string) {
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
