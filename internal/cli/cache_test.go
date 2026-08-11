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

const testZoneID = "0123456789abcdef0123456789abcdef"

func runCacheCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
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

func TestBuildCachePurgeBodyEverything(t *testing.T) {
	body, summary, err := buildCachePurgeBody(true, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "everything" {
		t.Fatalf("summary = %q", summary)
	}
	assertJSONEqual(t, body, `{"purge_everything":true}`)
}

func TestBuildCachePurgeBodyURL(t *testing.T) {
	body, summary, err := buildCachePurgeBody(false, []string{"https://example.com/a.js", "https://example.com/b.css"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "2 URL(s)" {
		t.Fatalf("summary = %q", summary)
	}
	assertJSONEqual(t, body, `{"files":["https://example.com/a.js","https://example.com/b.css"]}`)
}

func TestBuildCachePurgeBodyTagHostPrefix(t *testing.T) {
	body, _, err := buildCachePurgeBody(false, nil, []string{"product", "images"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, body, `{"tags":["product","images"]}`)

	body, _, err = buildCachePurgeBody(false, nil, nil, []string{"www.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, body, `{"hosts":["www.example.com"]}`)

	body, _, err = buildCachePurgeBody(false, nil, nil, nil, []string{"www.example.com/static"})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, body, `{"prefixes":["www.example.com/static"]}`)
}

func TestBuildCachePurgeBodyValidation(t *testing.T) {
	if _, _, err := buildCachePurgeBody(false, nil, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "specify a purge mode") {
		t.Fatalf("expected missing mode error, got %v", err)
	}
	if _, _, err := buildCachePurgeBody(true, []string{"https://example.com/"}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	if _, _, err := buildCachePurgeBody(false, nil, []string{"ok"}, []string{"www.example.com"}, nil); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
	if _, _, err := buildCachePurgeBody(false, []string{""}, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "--url") {
		t.Fatalf("expected empty url error, got %v", err)
	}
}

func TestBuildSmartTieredBody(t *testing.T) {
	body, err := buildSmartTieredBody("on")
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, body, `{"value":"on"}`)

	body, err = buildSmartTieredBody("OFF")
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, body, `{"value":"off"}`)

	if _, err := buildSmartTieredBody("maybe"); err == nil || !strings.Contains(err.Error(), "on or off") {
		t.Fatalf("expected value error, got %v", err)
	}
}

func TestCachePurgeDryRunEverything(t *testing.T) {
	stdout, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "purge",
		"--zone", testZoneID,
		"--everything",
		"--force",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump map[string]any
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	if dump["method"] != "POST" {
		t.Errorf("method = %v", dump["method"])
	}
	url, _ := dump["url"].(string)
	if !strings.HasSuffix(url, "/zones/"+testZoneID+"/purge_cache") {
		t.Errorf("url = %s", url)
	}
	body, _ := json.Marshal(dump["body"])
	assertJSONEqual(t, body, `{"purge_everything":true}`)
}

func TestCachePurgeDryRunByURL(t *testing.T) {
	stdout, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "purge",
		"--zone", testZoneID,
		"--url", "https://example.com/a.js",
		"--url", "https://example.com/b.css",
		"--force",
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
		t.Errorf("method = %s", dump.Method)
	}
	assertJSONEqual(t, dump.Body, `{"files":["https://example.com/a.js","https://example.com/b.css"]}`)
}

func TestCachePurgeDryRunByTagHostPrefix(t *testing.T) {
	cases := []struct {
		flag  string
		value string
		want  string
	}{
		{"--tag", "product", `{"tags":["product"]}`},
		{"--host", "www.example.com", `{"hosts":["www.example.com"]}`},
		{"--prefix", "www.example.com/static", `{"prefixes":["www.example.com/static"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			stdout, _, err := runCacheCLI(t, "http://example.invalid",
				"cache", "purge",
				"--zone", testZoneID,
				tc.flag, tc.value,
				"--force",
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
			assertJSONEqual(t, dump.Body, tc.want)
		})
	}
}

func TestCachePurgeRequiresMode(t *testing.T) {
	_, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "purge",
		"--zone", testZoneID,
		"--force",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "purge mode") {
		t.Fatalf("expected purge mode error, got %v", err)
	}
}

func TestCachePurgeRejectsMixedModes(t *testing.T) {
	_, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "purge",
		"--zone", testZoneID,
		"--everything",
		"--tag", "x",
		"--force",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed mode error, got %v", err)
	}
}

func TestCachePurgeRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runCacheCLI(t, srv.URL,
		"cache", "purge",
		"--zone", testZoneID,
		"--everything",
	)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestCachePurgeHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"purge-1"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runCacheCLI(t, srv.URL,
		"cache", "purge",
		"--zone", testZoneID,
		"--tag", "product",
		"--tag", "images",
		"--force",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/zones/"+testZoneID+"/purge_cache" {
		t.Errorf("path = %s", gotPath)
	}
	assertJSONEqual(t, gotBody, `{"tags":["product","images"]}`)
	if !strings.Contains(stdout, "purge-1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestCacheSmartTieredGetHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"value":"on","id":"tiered_cache_smart_topology_enable","editable":true}}`))
	}))
	defer srv.Close()

	stdout, _, err := runCacheCLI(t, srv.URL,
		"cache", "smart-tiered", "get",
		"--zone", testZoneID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/zones/"+testZoneID+"/cache/tiered_cache_smart_topology_enable" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, `"value"`) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestCacheSmartTieredGetDryRun(t *testing.T) {
	stdout, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "smart-tiered", "get",
		"--zone", testZoneID,
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
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/zones/"+testZoneID+"/cache/tiered_cache_smart_topology_enable") {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestCacheSmartTieredSetHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"value":"on"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runCacheCLI(t, srv.URL,
		"cache", "smart-tiered", "set",
		"--zone", testZoneID,
		"--value", "on",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PATCH" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/zones/"+testZoneID+"/cache/tiered_cache_smart_topology_enable" {
		t.Errorf("path = %s", gotPath)
	}
	assertJSONEqual(t, gotBody, `{"value":"on"}`)
	if !strings.Contains(stdout, "on") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestCacheSmartTieredSetDryRunOff(t *testing.T) {
	stdout, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "smart-tiered", "set",
		"--zone", testZoneID,
		"--value", "off",
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
	if dump.Method != "PATCH" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/zones/"+testZoneID+"/cache/tiered_cache_smart_topology_enable") {
		t.Errorf("url = %s", dump.URL)
	}
	assertJSONEqual(t, dump.Body, `{"value":"off"}`)
}

func TestCacheSmartTieredSetInvalidValue(t *testing.T) {
	_, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "smart-tiered", "set",
		"--zone", testZoneID,
		"--value", "maybe",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "on or off") {
		t.Fatalf("expected value error, got %v", err)
	}
}

func TestCacheSmartTieredSetRequiresValue(t *testing.T) {
	_, _, err := runCacheCLI(t, "http://example.invalid",
		"cache", "smart-tiered", "set",
		"--zone", testZoneID,
		"--dry-run",
	)
	if err == nil {
		t.Fatal("expected required --value error")
	}
	if !strings.Contains(err.Error(), "value") && !strings.Contains(err.Error(), "required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCachePurgeResolvesZoneName(t *testing.T) {
	var purgePath string
	var sawZonesLookup bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			sawZonesLookup = true
			if r.URL.Query().Get("name") != "example.com" {
				t.Errorf("lookup name = %q", r.URL.Query().Get("name"))
			}
			_, _ = w.Write([]byte(`{"success":true,"result":[{"id":"` + testZoneID + `","name":"example.com"}]}`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/purge_cache"):
			purgePath = r.URL.Path
			_, _ = w.Write([]byte(`{"success":true,"result":{"id":"ok"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	_, _, err := runCacheCLI(t, srv.URL,
		"cache", "purge",
		"--zone", "example.com",
		"--everything",
		"--force",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sawZonesLookup {
		t.Error("expected zone name lookup")
	}
	if purgePath != "/zones/"+testZoneID+"/purge_cache" {
		t.Errorf("purge path = %s", purgePath)
	}
}

func TestCacheHelpIncludesExamples(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"cache", "purge", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"--everything", "--url", "--tag", "--host", "--prefix", "--force", "cf cache purge"} {
		if !strings.Contains(help, want) {
			t.Errorf("purge help missing %q\n%s", want, help)
		}
	}

	out.Reset()
	root = NewRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"cache", "smart-tiered", "set", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help = out.String()
	for _, want := range []string{"--value", "cf cache smart-tiered set", "on"} {
		if !strings.Contains(help, want) {
			t.Errorf("set help missing %q\n%s", want, help)
		}
	}
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
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
