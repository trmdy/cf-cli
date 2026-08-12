package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	dlpTestAccountID = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	dlpTestProfileID = "384e129d-25bd-403c-8019-bc19eb7a8a5f"
	dlpTestDatasetID = "182bd5e5-6e1a-4fe4-a799-aa6d9a6ab26e"
	dlpTestEntryID   = "9c4b1e6c-3f9d-4a2c-9d4e-9a1b2c3d4e5f"
)

func runDLPCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runDLPCLIOpts(t, serverURL, true, args...)
}

// runDLPCLIOpts runs the real command tree. When withAccount is false the
// global --account-id is omitted so the account-scope error path can be
// asserted without a configured default.
func runDLPCLIOpts(t *testing.T, serverURL string, withAccount bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	dlpIsolateConfig(t)
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := []string{"--base-url", serverURL, "--token", "test-token"}
	if withAccount {
		all = append(all, "--account-id", dlpTestAccountID)
	}
	all = append(all, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// dlpIsolateConfig blocks profile/env account defaults from leaking into tests.
func dlpIsolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")
	t.Setenv("CLOUDFLARE_ZONE_ID", "")
	t.Setenv("CF_ZONE_ID", "")
}

type dlpDump struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body"`
}

func dlpDecodeDump(t *testing.T, stdout string) dlpDump {
	t.Helper()
	var d dlpDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("decode dump: %v\n%s", err, stdout)
	}
	return d
}

func dlpAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func dlpEnvelope(result string) string {
	return `{"success":true,"errors":[],"messages":[],"result":` + result + `}`
}

// --- profiles --------------------------------------------------------------

func TestDLPProfileListDryRun(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "list", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	want := "/accounts/" + dlpTestAccountID + "/dlp/profiles"
	if d.Method != "GET" || !strings.HasSuffix(d.URL, want) {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
}

func TestDLPProfileListAllFlagIsSentOnlyWhenSet(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "list", "--all", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if d := dlpDecodeDump(t, stdout); !strings.HasSuffix(d.URL, "?all=true") {
		t.Fatalf("URL = %s", d.URL)
	}
	stdout, _, err = runDLPCLI(t, "http://example.invalid", "dlp", "profile", "list", "--all=false", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if d := dlpDecodeDump(t, stdout); !strings.HasSuffix(d.URL, "?all=false") {
		t.Fatalf("URL = %s", d.URL)
	}
}

func TestDLPProfileListRejectsUnknownTypeBeforeRequest(t *testing.T) {
	_, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "list", "--type", "shared", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "invalid --type") {
		t.Fatalf("err = %v", err)
	}
}

const dlpProfileListResult = `[
	{"id":"custom-1","type":"custom","name":"Employee IDs","ocr_enabled":true,"allowed_match_count":5,
	 "confidence_threshold":"high","entries":[{"id":"e1"}],"shared_entries":[{"id":"s1"}]},
	{"id":"pre-1","type":"predefined","name":"Credit Card Numbers","ocr_enabled":false,
	 "allowed_match_count":0,"confidence_threshold":"low","entries":[{"id":"e2"},{"id":"e3"}]},
	{"id":"int-1","type":"integration","name":"Microsoft Purview","entries":[],"shared_entries":[]}
]`

func TestDLPProfileListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+dlpTestAccountID+"/dlp/profiles" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, dlpEnvelope(dlpProfileListResult))
	}))
	defer srv.Close()

	stdout, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "TYPE", "ALLOWED MATCHES", "custom-1", "pre-1", "int-1", "Credit Card Numbers"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
	// Entry count is entries + shared entries.
	line := ""
	for _, l := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(l, "custom-1") {
			line = l
		}
	}
	if !strings.Contains(line, "2") || !strings.Contains(line, "high") || !strings.Contains(line, "true") {
		t.Fatalf("custom row = %q", line)
	}
}

func TestDLPProfileListTypeFilterAppliesToJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, dlpEnvelope(dlpProfileListResult))
	}))
	defer srv.Close()

	stdout, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "list", "--type", "custom", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	if len(got) != 1 || got[0]["id"] != "custom-1" {
		t.Fatalf("filtered result = %s", stdout)
	}

	stdout, _, err = runDLPCLI(t, srv.URL, "dlp", "profile", "list", "--type", "predefined")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "custom-1") || !strings.Contains(stdout, "pre-1") {
		t.Fatalf("table = %s", stdout)
	}
}

func TestDLPProfileGetDryRun(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "get", dlpTestProfileID, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "GET" || !strings.HasSuffix(d.URL, "/dlp/profiles/"+dlpTestProfileID) {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
}

func TestDLPProfileGetRejectsEmptyID(t *testing.T) {
	_, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "get", "  ", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "profile ID must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPProfileCreateDryRunBody(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "profile", "create",
		"--name", "Employee IDs",
		"--description", "EU staff identifiers",
		"--allowed-match-count", "5",
		"--confidence-threshold", "very_high",
		"--ocr-enabled",
		"--ai-context-enabled=false",
		"--entry", `{"name":"Employee ID","description":"badge","pattern":{"regex":"EMP-[0-9]{6}","validation":"luhn"}}`,
		"--entry", `{"name":"Codenames","enabled":false,"words":["bluejay","redwood"]}`,
		"--shared-entry", dlpTestEntryID,
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "POST" || !strings.HasSuffix(d.URL, "/dlp/profiles/custom") {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
	dlpAssertJSONEqual(t, d.Body, `{
		"name":"Employee IDs",
		"description":"EU staff identifiers",
		"allowed_match_count":5,
		"confidence_threshold":"very_high",
		"ocr_enabled":true,
		"ai_context_enabled":false,
		"entries":[
			{"name":"Employee ID","description":"badge","enabled":true,"pattern":{"regex":"EMP-[0-9]{6}","validation":"luhn"}},
			{"name":"Codenames","enabled":false,"words":["bluejay","redwood"]}
		],
		"shared_entries":[{"entry_id":"`+dlpTestEntryID+`","enabled":true}]
	}`)
}

func TestDLPProfileCreateOmitsUnsetFields(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "profile", "create", "--name", "Minimal", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, dlpDecodeDump(t, stdout).Body, `{"name":"Minimal"}`)
}

func TestDLPProfileCreateValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"match count too high", []string{"--allowed-match-count", "1001"}, "between 0 and 1000"},
		{"match count negative", []string{"--allowed-match-count", "-1"}, "between 0 and 1000"},
		{"entry not JSON", []string{"--entry", "not json"}, "invalid JSON"},
		{"entry not object", []string{"--entry", `["a"]`}, "must be a JSON object"},
		{"entry unknown field", []string{"--entry", `{"name":"a","regex":"x"}`}, `unknown field "regex"`},
		{"entry no name", []string{"--entry", `{"pattern":{"regex":"x"}}`}, `"name" is required`},
		{"entry empty name", []string{"--entry", `{"name":" ","pattern":{"regex":"x"}}`}, `"name" is required`},
		{"entry both shapes", []string{"--entry", `{"name":"a","pattern":{"regex":"x"},"words":["y"]}`}, `not both`},
		{"entry neither shape", []string{"--entry", `{"name":"a"}`}, `needs either "pattern"`},
		{"entry bad enabled", []string{"--entry", `{"name":"a","enabled":"yes","words":["y"]}`}, `"enabled" must be true or false`},
		{"entry empty regex", []string{"--entry", `{"name":"a","pattern":{"regex":""}}`}, `"pattern.regex" is required`},
		{"entry bad validation", []string{"--entry", `{"name":"a","pattern":{"regex":"x","validation":"mod10"}}`}, `invalid "pattern.validation"`},
		{"entry unknown pattern field", []string{"--entry", `{"name":"a","pattern":{"regex":"x","flags":"i"}}`}, `unknown field "flags" in "pattern"`},
		{"entry empty words", []string{"--entry", `{"name":"a","words":[]}`}, "at least one word"},
		{"entry non-string word", []string{"--entry", `{"name":"a","words":[1]}`}, `"words"[0] must be a non-empty string`},
		{"entry_id on create", []string{"--entry", `{"name":"a","pattern":{"regex":"x"},"entry_id":"` + dlpTestEntryID + `"}`}, `"entry_id" is only accepted when updating`},
		{"description on word list", []string{"--entry", `{"name":"a","words":["y"],"description":"d"}`}, `word list entries take no "description"`},
		{"empty shared entry", []string{"--shared-entry", " "}, "shared entry ID must not be empty"},
		{"non-uuid shared entry", []string{"--shared-entry", "not-a-uuid"}, "expected a UUID"},
		{"duplicate shared entry", []string{"--shared-entry", dlpTestEntryID, "--shared-entry", dlpTestEntryID}, "was given twice"},
		{"empty name", []string{"--name", ""}, "--name must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"dlp", "profile", "create", "--dry-run"}, tc.args...)
			if tc.name != "empty name" {
				args = append(args, "--name", "Test")
			}
			_, _, err := runDLPCLI(t, "http://example.invalid", args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// dlpCustomProfileResult is a profile GET response carrying read-only fields,
// a field this CLI does not model (data_classes / future_field), rich entries,
// and rich shared entries.
const dlpCustomProfileResult = `{
	"id":"` + dlpTestProfileID + `",
	"type":"custom",
	"name":"Employee IDs",
	"description":"EU staff identifiers",
	"created_at":"2024-01-01T00:00:00Z",
	"updated_at":"2024-02-01T00:00:00Z",
	"allowed_match_count":0,
	"confidence_threshold":"low",
	"ocr_enabled":false,
	"ai_context_enabled":false,
	"data_classes":["3c4d5e6f-7a8b-49c0-a1b2-c3d4e5f60718"],
	"future_field":{"kept":true},
	"entries":[{"id":"` + dlpTestEntryID + `","type":"custom","name":"Employee ID","enabled":true,
		"pattern":{"regex":"EMP-[0-9]{6}"},"created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"}],
	"shared_entries":[{"id":"11111111-2222-3333-4444-555555555555","type":"predefined","name":"Credit Card","enabled":false}]
}`

func TestDLPProfileUpdateCustomReadMergeWrite(t *testing.T) {
	var put *http.Request
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/accounts/"+dlpTestAccountID+"/dlp/profiles/"+dlpTestProfileID:
			io.WriteString(w, dlpEnvelope(dlpCustomProfileResult))
		case r.Method == "PUT" && r.URL.Path == "/accounts/"+dlpTestAccountID+"/dlp/profiles/custom/"+dlpTestProfileID:
			put = r
			putBody, _ = io.ReadAll(r.Body)
			io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID,
		"--allowed-match-count", "5", "--confidence-threshold", "high")
	if err != nil {
		t.Fatal(err)
	}
	if put == nil {
		t.Fatal("no PUT was sent")
	}
	dlpAssertJSONEqual(t, putBody, `{
		"name":"Employee IDs",
		"description":"EU staff identifiers",
		"allowed_match_count":5,
		"confidence_threshold":"high",
		"ocr_enabled":false,
		"ai_context_enabled":false,
		"data_classes":["3c4d5e6f-7a8b-49c0-a1b2-c3d4e5f60718"],
		"future_field":{"kept":true},
		"shared_entries":[{"entry_id":"11111111-2222-3333-4444-555555555555","enabled":false}]
	}`)
}

func TestDLPProfileUpdateCustomReplacesEntriesOnlyWhenAsked(t *testing.T) {
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, dlpEnvelope(dlpCustomProfileResult))
			return
		}
		putBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`"}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID,
		"--entry", `{"entry_id":"`+dlpTestEntryID+`","name":"Employee ID","pattern":{"regex":"EMP-[0-9]{7}"}}`)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatal(err)
	}
	entries, ok := body["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries = %v", body["entries"])
	}
	entry := entries[0].(map[string]any)
	if entry["entry_id"] != dlpTestEntryID || entry["enabled"] != true {
		t.Fatalf("entry = %v", entry)
	}
	if pattern := entry["pattern"].(map[string]any); pattern["regex"] != "EMP-[0-9]{7}" {
		t.Fatalf("pattern = %v", pattern)
	}
}

func TestDLPProfileUpdateRejectsWordListEntries(t *testing.T) {
	_, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "update", dlpTestProfileID,
		"--entry", `{"name":"a","words":["y"]}`, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "word list entries can only be created") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPProfileUpdateDryRunReadsButDoesNotWrite(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		io.WriteString(w, dlpEnvelope(dlpCustomProfileResult))
	}))
	defer srv.Close()

	stdout, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID,
		"--description", "new text", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || !strings.HasPrefix(methods[0], "GET ") {
		t.Fatalf("requests = %v", methods)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "PUT" || !strings.HasSuffix(d.URL, "/dlp/profiles/custom/"+dlpTestProfileID) {
		t.Fatalf("dump = %s %s", d.Method, d.URL)
	}
	var body map[string]any
	if err := json.Unmarshal(d.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["description"] != "new text" {
		t.Fatalf("body = %s", d.Body)
	}
}

const dlpPredefinedProfileResult = `{
	"id":"` + dlpTestProfileID + `",
	"type":"predefined",
	"name":"Credit Card Numbers",
	"open_access":true,
	"allowed_match_count":0,
	"confidence_threshold":"low",
	"ocr_enabled":false,
	"ai_context_enabled":false,
	"context_awareness":{"enabled":true,"skip":{"files":false}},
	"entries":[
		{"id":"aaaaaaaa-0000-0000-0000-000000000001","type":"predefined","name":"Visa","enabled":true},
		{"id":"aaaaaaaa-0000-0000-0000-000000000002","type":"predefined","name":"Amex","enabled":false}
	]
}`

// The predefined write schema requires nothing, so the body carries only the
// changed fields — no echoed read values, no deprecated context_awareness or
// entries.
func TestDLPProfileUpdatePredefinedSendsOnlyChangedFields(t *testing.T) {
	var putPath string
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, dlpEnvelope(dlpPredefinedProfileResult))
			return
		}
		putPath = r.URL.Path
		putBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`"}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--ocr-enabled")
	if err != nil {
		t.Fatal(err)
	}
	if putPath != "/accounts/"+dlpTestAccountID+"/dlp/profiles/predefined/"+dlpTestProfileID {
		t.Fatalf("PUT path = %s", putPath)
	}
	dlpAssertJSONEqual(t, putBody, `{"ocr_enabled":true}`)
}

func TestDLPProfileUpdatePredefinedBoundsAllowedMatchCount(t *testing.T) {
	var wrote bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			wrote = true
		}
		io.WriteString(w, dlpEnvelope(dlpPredefinedProfileResult))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--allowed-match-count", "1001")
	if err == nil || !strings.Contains(err.Error(), "between 0 and 1000") {
		t.Fatalf("err = %v", err)
	}
	if wrote {
		t.Fatal("a write was sent for an out-of-range predefined update")
	}
}

// The custom update schema declares no bound on allowed_match_count and types
// confidence_threshold as a plain nullable string, so neither is rejected.
func TestDLPProfileUpdateCustomAppliesNoInventedBounds(t *testing.T) {
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, dlpEnvelope(dlpCustomProfileResult))
			return
		}
		putBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`"}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID,
		"--allowed-match-count", "5000", "--confidence-threshold", "extremely_high")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(putBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["allowed_match_count"] != float64(5000) || body["confidence_threshold"] != "extremely_high" {
		t.Fatalf("body = %s", putBody)
	}
}

// A value the read returned unchanged is never re-validated on the way out.
func TestDLPProfileUpdateCustomKeepsUnknownResponseValues(t *testing.T) {
	var putBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`","type":"custom","name":"X",
				"allowed_match_count":9000,"confidence_threshold":"a_future_value"}`))
			return
		}
		putBody, _ = io.ReadAll(r.Body)
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`"}`))
	}))
	defer srv.Close()

	if _, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--ocr-enabled"); err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, putBody, `{"name":"X","allowed_match_count":9000,
		"confidence_threshold":"a_future_value","ocr_enabled":true}`)
}

func TestDLPProfileUpdatePredefinedRejectsCustomOnlyFlags(t *testing.T) {
	var wrote bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			wrote = true
		}
		io.WriteString(w, dlpEnvelope(dlpPredefinedProfileResult))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--name", "Mine")
	if err == nil || !strings.Contains(err.Error(), "predefined profiles accept only") {
		t.Fatalf("err = %v", err)
	}
	if wrote {
		t.Fatal("a write was sent for a rejected update")
	}
}

func TestDLPProfileUpdateIntegrationProfileIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected %s", r.Method)
		}
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`","type":"integration","name":"Purview"}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--ocr-enabled")
	if err == nil || !strings.Contains(err.Error(), "integration profile") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPProfileUpdateRejectsUnpreservableSharedEntries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("unexpected %s", r.Method)
		}
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`","type":"custom","name":"X",
			"shared_entries":[{"name":"no id or enabled"}]}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--ocr-enabled")
	if err == nil || !strings.Contains(err.Error(), "shared_entries[0] has no id") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPProfileUpdateNeedsAtLeastOneFlag(t *testing.T) {
	_, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "profile", "update", dlpTestProfileID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPProfileDeleteTargetsCustomPath(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "profile", "delete", dlpTestProfileID, "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "DELETE" || !strings.HasSuffix(d.URL, "/dlp/profiles/custom/"+dlpTestProfileID) {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
}

// --- datasets --------------------------------------------------------------

func TestDLPDatasetListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+dlpTestAccountID+"/dlp/datasets" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, dlpEnvelope(`[
			{"id":"`+dlpTestDatasetID+`","name":"Customer emails","status":"complete","secret":true,
			 "num_cells":1200,"columns":[{"entry_id":"c1"}],"uploads":[{"version":1},{"version":2}],
			 "updated_at":"2024-05-01T10:00:00Z"}
		]`))
	}))
	defer srv.Close()

	stdout, _, err := runDLPCLI(t, srv.URL, "dlp", "dataset", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"STATUS", "SECRET", "CELLS", "VERSIONS", "Customer emails", "1200", "complete"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("table missing %q:\n%s", want, stdout)
		}
	}
}

func TestDLPDatasetCreateDryRunBody(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "create", "--name", "Customer emails", "--description", "Q3",
		"--secret", "--encoding-version", "2", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "POST" || !strings.HasSuffix(d.URL, "/dlp/datasets") {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
	dlpAssertJSONEqual(t, d.Body, `{"name":"Customer emails","description":"Q3","secret":true,"encoding_version":2}`)
}

func TestDLPDatasetCreateCaseSensitivityContract(t *testing.T) {
	// Case-insensitive matching is only valid for explicitly non-secret
	// word lists.
	_, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "create", "--name", "words", "--case-sensitive=false", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--case-sensitive=false requires --secret=false") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "create", "--name", "words", "--secret", "--case-sensitive=false", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--case-sensitive=false requires --secret=false") {
		t.Fatalf("err = %v", err)
	}
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "create", "--name", "words", "--secret=false", "--case-sensitive=false", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, dlpDecodeDump(t, stdout).Body, `{"name":"words","secret":false,"case_sensitive":false}`)
	// Case-sensitive stays valid for secret datasets.
	stdout, _, err = runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "create", "--name", "edm", "--secret", "--case-sensitive", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, dlpDecodeDump(t, stdout).Body, `{"name":"edm","secret":true,"case_sensitive":true}`)
}

func TestDLPDatasetCreateBoundsEncodingVersion(t *testing.T) {
	for _, v := range []string{"-1", "2147483648"} {
		_, _, err := runDLPCLI(t, "http://example.invalid",
			"dlp", "dataset", "create", "--name", "x", "--encoding-version", v, "--dry-run")
		if err == nil || !strings.Contains(err.Error(), "--encoding-version must be between 0 and 2147483647") {
			t.Fatalf("encoding-version %s: err = %v", v, err)
		}
	}
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "create", "--name", "x", "--encoding-version", "2147483647", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, dlpDecodeDump(t, stdout).Body, `{"name":"x","encoding_version":2147483647}`)
}

// The custom update schema drops the 0-1000 bound but keeps format: int32, so
// a value the wire type cannot hold is refused before the read.
func TestDLPProfileUpdateBoundsAllowedMatchCountToInt32(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, dlpEnvelope(dlpCustomProfileResult))
	}))
	defer srv.Close()

	for _, v := range []string{"2147483648", "-2147483649"} {
		_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--allowed-match-count", v)
		if err == nil || !strings.Contains(err.Error(), "--allowed-match-count must fit in a 32-bit integer") {
			t.Fatalf("allowed-match-count %s: err = %v", v, err)
		}
	}
	if calls != 0 {
		t.Fatalf("out-of-int32 updates caused %d request(s)", calls)
	}
}

func TestDLPDatasetUpdateSendsOnlyChangedFields(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "update", dlpTestDatasetID, "--description", "Q4 list", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "PUT" || !strings.HasSuffix(d.URL, "/dlp/datasets/"+dlpTestDatasetID) {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
	dlpAssertJSONEqual(t, d.Body, `{"description":"Q4 list"}`)
}

func TestDLPDatasetUpdateValidation(t *testing.T) {
	_, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "dataset", "update", dlpTestDatasetID, "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "nothing to update") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "update", dlpTestDatasetID, "--name", "", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--name must not be empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPDatasetUploadTwoSteps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.txt")
	contents := "bluejay\nredwood\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var uploaded []byte
	var prepareMethod, prepareQuery string
	var uploadMethod, uploadPath, uploadQuery, uploadContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "/accounts/" + dlpTestAccountID + "/dlp/datasets/" + dlpTestDatasetID
		switch r.URL.Path {
		case base + "/upload":
			prepareMethod, prepareQuery = r.Method, r.URL.RawQuery
			io.WriteString(w, dlpEnvelope(`{"version":7,"max_cells":10000,"encoding_version":1}`))
		case base + "/upload/7":
			uploadMethod, uploadPath = r.Method, r.URL.Path
			uploadQuery = r.URL.RawQuery
			uploadContentType = r.Header.Get("Content-Type")
			uploaded, _ = io.ReadAll(r.Body)
			io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestDatasetID+`","status":"processing"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	stdout, stderr, err := runDLPCLI(t, srv.URL, "dlp", "dataset", "upload", dlpTestDatasetID, "--file", path)
	if err != nil {
		t.Fatal(err)
	}
	base := "/accounts/" + dlpTestAccountID + "/dlp/datasets/" + dlpTestDatasetID
	if prepareMethod != "POST" || prepareQuery != "" {
		t.Fatalf("prepare request = %s %s?%s", prepareMethod, base+"/upload", prepareQuery)
	}
	if uploadPath == "" {
		t.Fatal("upload request was never sent")
	}
	if uploadMethod != "POST" || uploadPath != base+"/upload/7" || uploadQuery != "" {
		t.Fatalf("upload request = %s %s?%s, want POST %s with no query",
			uploadMethod, uploadPath, uploadQuery, base+"/upload/7")
	}
	if string(uploaded) != contents {
		t.Fatalf("uploaded = %q", uploaded)
	}
	if uploadContentType != "application/octet-stream" {
		t.Fatalf("content type = %q", uploadContentType)
	}
	if !strings.Contains(stderr, "as version 7") {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "processing") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestDLPDatasetUploadDryRunShowsPrepareOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.txt")
	if err := os.WriteFile(path, []byte("bluejay\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, dlpEnvelope(`{"version":7}`))
	}))
	defer srv.Close()

	stdout, stderr, err := runDLPCLI(t, srv.URL, "dlp", "dataset", "upload", dlpTestDatasetID, "--file", path, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("dry run sent %d request(s)", calls)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "POST" || !strings.HasSuffix(d.URL, "/dlp/datasets/"+dlpTestDatasetID+"/upload") {
		t.Fatalf("dump = %s %s", d.Method, d.URL)
	}
	if !strings.Contains(stderr, "8 byte(s)") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestDLPDatasetUploadValidatesFileBeforeNetwork(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "dataset", "upload", dlpTestDatasetID,
		"--file", filepath.Join(t.TempDir(), "missing.txt"))
	if err == nil || !strings.Contains(err.Error(), "read --file") {
		t.Fatalf("err = %v", err)
	}
	_, _, err = runDLPCLI(t, srv.URL, "dlp", "dataset", "upload", dlpTestDatasetID, "--file", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("sent %d request(s) despite an invalid --file", calls)
	}
}

func TestDLPDatasetUploadRequiresVersionFromAPI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "words.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, dlpEnvelope(`{"max_cells":10}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "dataset", "upload", dlpTestDatasetID, "--file", path)
	if err == nil || !strings.Contains(err.Error(), "did not return a version") {
		t.Fatalf("err = %v", err)
	}
}

func TestDLPDatasetDeleteDryRun(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid",
		"dlp", "dataset", "delete", dlpTestDatasetID, "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "DELETE" || !strings.HasSuffix(d.URL, "/dlp/datasets/"+dlpTestDatasetID) {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
}

// --- payload log -----------------------------------------------------------

func TestDLPPayloadLogGetDryRun(t *testing.T) {
	stdout, _, err := runDLPCLI(t, "http://example.invalid", "dlp", "payload-log", "get", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "GET" || !strings.HasSuffix(d.URL, "/dlp/payload_log") {
		t.Fatalf("request = %s %s", d.Method, d.URL)
	}
}

func dlpPayloadLogServer(t *testing.T, current string, putBody *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/"+dlpTestAccountID+"/dlp/payload_log" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Method == "GET" {
			io.WriteString(w, dlpEnvelope(current))
			return
		}
		if putBody != nil {
			*putBody, _ = io.ReadAll(r.Body)
		}
		io.WriteString(w, dlpEnvelope(`{"updated_at":"2024-06-01T00:00:00Z"}`))
	}))
}

func TestDLPPayloadLogSetPreservesTheFieldYouDidNotTouch(t *testing.T) {
	var body []byte
	srv := dlpPayloadLogServer(t, `{"public_key":"ZXhpc3RpbmctcGF5bG9hZC1sb2ctcHVibGljLWtleSE=","masking_level":"full","updated_at":"2024-05-01T00:00:00Z"}`, &body)
	defer srv.Close()

	if _, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--masking-level", "partial"); err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, body, `{"public_key":"ZXhpc3RpbmctcGF5bG9hZC1sb2ctcHVibGljLWtleSE=","masking_level":"partial"}`)

	body = nil
	if _, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--public-key", "bmV3LXBheWxvYWQtbG9nLXB1YmxpYy1rZXktMzJieXQ="); err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, body, `{"public_key":"bmV3LXBheWxvYWQtbG9nLXB1YmxpYy1rZXktMzJieXQ=","masking_level":"full"}`)
}

func TestDLPPayloadLogSetDisableClearsTheKey(t *testing.T) {
	var body []byte
	srv := dlpPayloadLogServer(t, `{"public_key":"ZXhpc3RpbmctcGF5bG9hZC1sb2ctcHVibGljLWtleSE=","masking_level":"clear"}`, &body)
	defer srv.Close()

	if _, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--disable"); err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, body, `{"public_key":null,"masking_level":"clear"}`)
}

func TestDLPPayloadLogSetAlwaysSendsPublicKey(t *testing.T) {
	var body []byte
	srv := dlpPayloadLogServer(t, `{"updated_at":"2024-05-01T00:00:00Z"}`, &body)
	defer srv.Close()

	if _, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--masking-level", "default"); err != nil {
		t.Fatal(err)
	}
	dlpAssertJSONEqual(t, body, `{"public_key":null,"masking_level":"default"}`)
}

func TestDLPPayloadLogSetDryRunReadsButDoesNotWrite(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		io.WriteString(w, dlpEnvelope(`{"public_key":"ZXhpc3RpbmctcGF5bG9hZC1sb2ctcHVibGljLWtleSE=","masking_level":"full"}`))
	}))
	defer srv.Close()

	stdout, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--masking-level", "partial", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 1 || methods[0] != "GET" {
		t.Fatalf("requests = %v", methods)
	}
	d := dlpDecodeDump(t, stdout)
	if d.Method != "PUT" {
		t.Fatalf("dump method = %s", d.Method)
	}
	dlpAssertJSONEqual(t, d.Body, `{"public_key":"ZXhpc3RpbmctcGF5bG9hZC1sb2ctcHVibGljLWtleSE=","masking_level":"partial"}`)
}

func TestDLPPayloadLogSetValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no flags", nil, "nothing to set"},
		{"key and disable", []string{"--public-key", "ZXhhbXBsZQ==", "--disable"}, "cannot be combined"},
		{"empty key", []string{"--public-key", " "}, "--public-key must not be empty"},
		{"bad masking level", []string{"--masking-level", "none"}, `invalid --masking-level "none"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"dlp", "payload-log", "set", "--dry-run"}, tc.args...)
			_, _, err := runDLPCLI(t, "http://example.invalid", args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// --- account scope ---------------------------------------------------------

func TestDLPRequiresAccountID(t *testing.T) {
	for _, args := range [][]string{
		{"dlp", "profile", "list"},
		{"dlp", "dataset", "list"},
		{"dlp", "payload-log", "get"},
	} {
		_, _, err := runDLPCLIOpts(t, "http://example.invalid", false, append(args, "--dry-run")...)
		if err == nil || !strings.Contains(err.Error(), "no account specified") {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

func TestDLPAccountIDIsPathEscaped(t *testing.T) {
	dlpIsolateConfig(t)
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t",
		"--account-id", "acct/../evil", "dlp", "profile", "list", "--dry-run"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acct%2F..%2Fevil") {
		t.Fatalf("account ID was not escaped:\n%s", out.String())
	}
}

// --- identifier and key validation ----------------------------------------

// Every documented UUID path or body identifier is checked before the client
// is built, so a malformed ID costs no request.
func TestDLPRejectsMalformedIdentifiersWithoutRequests(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()

	// A real file, so the upload case can only fail on the identifier.
	uploadFile := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(uploadFile, []byte("bluejay\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"profile get", []string{"dlp", "profile", "get", "384e129d"}, "invalid profile ID"},
		{"profile update", []string{"dlp", "profile", "update", "nope", "--ocr-enabled"}, "invalid profile ID"},
		{"profile delete", []string{"dlp", "profile", "delete", "nope", "--force"}, "invalid profile ID"},
		{"dataset get", []string{"dlp", "dataset", "get", "nope"}, "invalid dataset ID"},
		{"dataset update", []string{"dlp", "dataset", "update", "nope", "--name", "x"}, "invalid dataset ID"},
		{"dataset delete", []string{"dlp", "dataset", "delete", "nope", "--force"}, "invalid dataset ID"},
		{"dataset upload", []string{"dlp", "dataset", "upload", "nope", "--file", uploadFile}, "invalid dataset ID"},
		{"shared entry", []string{"dlp", "profile", "create", "--name", "x", "--shared-entry", "nope"}, "invalid shared entry ID"},
		{"entry_id", []string{"dlp", "profile", "update", dlpTestProfileID,
			"--entry", `{"entry_id":"nope","name":"a","pattern":{"regex":"x"}}`}, "invalid entry_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runDLPCLI(t, srv.URL, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("malformed identifiers caused %d request(s)", calls)
	}
}

func TestDLPPayloadLogRejectsNonBase64KeyWithoutRequests(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, dlpEnvelope(`{"public_key":null}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--public-key", "not base64!!")
	if err == nil || !strings.Contains(err.Error(), "must be base64-encoded") {
		t.Fatalf("err = %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid key caused %d request(s)", calls)
	}
	// A well-formed key still gets through to the read-merge-write.
	if _, _, err := runDLPCLI(t, srv.URL, "dlp", "payload-log", "set", "--public-key", "ZXhhbXBsZS1wYXlsb2FkLWxvZy1wdWJsaWMta2V5ISE="); err != nil {
		t.Fatal(err)
	}
}

// --- argument arity --------------------------------------------------------

func TestDLPArgumentFreeCommandsRejectExtraArgs(t *testing.T) {
	for _, args := range [][]string{
		{"dlp", "profile", "list", "stray"},
		{"dlp", "profile", "create", "stray", "--name", "x"},
		{"dlp", "dataset", "list", "stray"},
		{"dlp", "dataset", "create", "stray", "--name", "x"},
		{"dlp", "payload-log", "get", "stray"},
		{"dlp", "payload-log", "set", "stray", "--disable"},
	} {
		_, _, err := runDLPCLI(t, "http://example.invalid", append(args, "--dry-run")...)
		if err == nil || !strings.Contains(err.Error(), "unknown command") && !strings.Contains(err.Error(), "accepts 0 arg") {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
}

// --- exact requests for every leaf -----------------------------------------

// One compact assertion per leaf that needs no read: exact method, path, and
// body.
func TestDLPExactRequests(t *testing.T) {
	base := "https://api.cloudflare.com/client/v4/accounts/" + dlpTestAccountID + "/dlp"
	uploadFile := filepath.Join(t.TempDir(), "words.txt")
	if err := os.WriteFile(uploadFile, []byte("bluejay\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		leaf   string
		args   []string
		method string
		url    string
		body   string
	}{
		{"profile list", []string{"dlp", "profile", "list"}, "GET", base + "/profiles", ""},
		{"profile get", []string{"dlp", "profile", "get", dlpTestProfileID}, "GET", base + "/profiles/" + dlpTestProfileID, ""},
		{"profile create", []string{"dlp", "profile", "create", "--name", "Employee IDs"}, "POST",
			base + "/profiles/custom", `{"name":"Employee IDs"}`},
		{"profile delete", []string{"dlp", "profile", "delete", dlpTestProfileID, "--force"}, "DELETE",
			base + "/profiles/custom/" + dlpTestProfileID, ""},
		{"dataset list", []string{"dlp", "dataset", "list"}, "GET", base + "/datasets", ""},
		{"dataset get", []string{"dlp", "dataset", "get", dlpTestDatasetID}, "GET", base + "/datasets/" + dlpTestDatasetID, ""},
		{"dataset create", []string{"dlp", "dataset", "create", "--name", "Codenames"}, "POST",
			base + "/datasets", `{"name":"Codenames"}`},
		{"dataset update", []string{"dlp", "dataset", "update", dlpTestDatasetID, "--name", "Codenames v2"}, "PUT",
			base + "/datasets/" + dlpTestDatasetID, `{"name":"Codenames v2"}`},
		{"dataset upload", []string{"dlp", "dataset", "upload", dlpTestDatasetID, "--file", uploadFile}, "POST",
			base + "/datasets/" + dlpTestDatasetID + "/upload", ""},
		{"dataset delete", []string{"dlp", "dataset", "delete", dlpTestDatasetID, "--force"}, "DELETE",
			base + "/datasets/" + dlpTestDatasetID, ""},
		{"payload-log get", []string{"dlp", "payload-log", "get"}, "GET", base + "/payload_log", ""},
	}
	for _, tc := range cases {
		t.Run(tc.leaf, func(t *testing.T) {
			stdout, _, err := runDLPCLI(t, "", append(tc.args, "--dry-run")...)
			if err != nil {
				t.Fatal(err)
			}
			d := dlpDecodeDump(t, stdout)
			if d.Method != tc.method || d.URL != tc.url {
				t.Fatalf("request = %s %s, want %s %s", d.Method, d.URL, tc.method, tc.url)
			}
			if tc.body == "" {
				if len(d.Body) != 0 {
					t.Fatalf("unexpected body %s", d.Body)
				}
				return
			}
			dlpAssertJSONEqual(t, d.Body, tc.body)
		})
	}
}

// The two leaves that read before writing, asserted end to end.
func TestDLPExactRequestsAfterRead(t *testing.T) {
	base := "/accounts/" + dlpTestAccountID + "/dlp"
	cases := []struct {
		leaf    string
		args    []string
		current string
		path    string
		body    string
	}{
		{"profile update custom", []string{"dlp", "profile", "update", dlpTestProfileID, "--name", "Renamed"},
			`{"id":"` + dlpTestProfileID + `","type":"custom","name":"Old","ocr_enabled":false}`,
			base + "/profiles/custom/" + dlpTestProfileID,
			`{"name":"Renamed","ocr_enabled":false}`},
		{"profile update predefined", []string{"dlp", "profile", "update", dlpTestProfileID, "--allowed-match-count", "5"},
			`{"id":"` + dlpTestProfileID + `","type":"predefined","name":"Credit Cards","ocr_enabled":false}`,
			base + "/profiles/predefined/" + dlpTestProfileID,
			`{"allowed_match_count":5}`},
		{"payload-log set", []string{"dlp", "payload-log", "set", "--masking-level", "clear"},
			`{"public_key":"ZXhhbXBsZQ==","masking_level":"full"}`,
			base + "/payload_log",
			`{"public_key":"ZXhhbXBsZQ==","masking_level":"clear"}`},
	}
	for _, tc := range cases {
		t.Run(tc.leaf, func(t *testing.T) {
			var method, path string
			var body []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					io.WriteString(w, dlpEnvelope(tc.current))
					return
				}
				method, path = r.Method, r.URL.Path
				body, _ = io.ReadAll(r.Body)
				io.WriteString(w, dlpEnvelope(`{}`))
			}))
			defer srv.Close()

			if _, _, err := runDLPCLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if method != "PUT" || path != tc.path {
				t.Fatalf("request = %s %s, want PUT %s", method, path, tc.path)
			}
			dlpAssertJSONEqual(t, body, tc.body)
		})
	}
}

// --- destructive command behavior ------------------------------------------

func TestDLPForcedDeletesReachTheAPI(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		path string
	}{
		{"profile", []string{"dlp", "profile", "delete", dlpTestProfileID, "--force"},
			"/accounts/" + dlpTestAccountID + "/dlp/profiles/custom/" + dlpTestProfileID},
		{"dataset", []string{"dlp", "dataset", "delete", dlpTestDatasetID, "--force"},
			"/accounts/" + dlpTestAccountID + "/dlp/datasets/" + dlpTestDatasetID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var method, path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				io.WriteString(w, dlpEnvelope(`null`))
			}))
			defer srv.Close()

			if _, _, err := runDLPCLI(t, srv.URL, tc.args...); err != nil {
				t.Fatal(err)
			}
			if method != "DELETE" || path != tc.path {
				t.Fatalf("request = %s %s, want DELETE %s", method, path, tc.path)
			}
		})
	}
}

// dlpNoTTYStdin points os.Stdin at a regular file so the shared confirm helper
// takes its non-terminal path and declines instead of blocking on input.
func dlpNoTTYStdin(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = saved
		f.Close()
	})
}

func TestDLPDeletesWithoutForceOrTTYSendNothing(t *testing.T) {
	dlpNoTTYStdin(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, dlpEnvelope(`null`))
	}))
	defer srv.Close()

	for _, args := range [][]string{
		{"dlp", "profile", "delete", dlpTestProfileID},
		{"dlp", "dataset", "delete", dlpTestDatasetID},
	} {
		_, _, err := runDLPCLI(t, srv.URL, args...)
		if err == nil || !strings.Contains(err.Error(), "aborted (pass --force to skip confirmation)") {
			t.Fatalf("%v: err = %v", args, err)
		}
	}
	if calls != 0 {
		t.Fatalf("declined deletes still sent %d request(s)", calls)
	}
}

// A destructive dry run neither prompts nor writes: it prints the request it
// would have sent.
func TestDLPDeleteDryRunWithoutForceSendsNothing(t *testing.T) {
	dlpNoTTYStdin(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer srv.Close()

	for _, tc := range []struct {
		args []string
		path string
	}{
		{[]string{"dlp", "profile", "delete", dlpTestProfileID, "--dry-run"}, "/dlp/profiles/custom/" + dlpTestProfileID},
		{[]string{"dlp", "dataset", "delete", dlpTestDatasetID, "--dry-run"}, "/dlp/datasets/" + dlpTestDatasetID},
	} {
		stdout, _, err := runDLPCLI(t, srv.URL, tc.args...)
		if err != nil {
			t.Fatal(err)
		}
		d := dlpDecodeDump(t, stdout)
		if d.Method != "DELETE" || !strings.HasSuffix(d.URL, tc.path) {
			t.Fatalf("dump = %s %s", d.Method, d.URL)
		}
	}
	if calls != 0 {
		t.Fatalf("dry-run deletes sent %d request(s)", calls)
	}
}

// A shared entry ID the API returns is re-sent verbatim in the complete custom
// write body, so a malformed one is caught before the PUT rather than shipped.
func TestDLPProfileUpdateRejectsMalformedCurrentSharedEntryID(t *testing.T) {
	var wrote bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			wrote = true
			io.WriteString(w, dlpEnvelope(`{}`))
			return
		}
		io.WriteString(w, dlpEnvelope(`{"id":"`+dlpTestProfileID+`","type":"custom","name":"X",
			"shared_entries":[{"id":"`+dlpTestEntryID+`","enabled":true},{"id":"not-a-uuid","enabled":false}]}`))
	}))
	defer srv.Close()

	_, _, err := runDLPCLI(t, srv.URL, "dlp", "profile", "update", dlpTestProfileID, "--ocr-enabled")
	if err == nil || !strings.Contains(err.Error(), `invalid shared_entries[1] id "not-a-uuid"`) {
		t.Fatalf("err = %v", err)
	}
	if wrote {
		t.Fatal("a PUT was sent despite a malformed shared entry ID")
	}
}
