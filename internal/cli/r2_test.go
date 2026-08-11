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

const r2TestAccountID = "0123456789abcdef0123456789abcdef"

func runR2CLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", r2TestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func TestR2BucketCommandsSendExpectedRequests(t *testing.T) {
	var requests []struct {
		method string
		path   string
		query  string
		body   []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, struct {
			method string
			path   string
			query  string
			body   []byte
		}{r.Method, r.URL.Path, r.URL.RawQuery, body})
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case "GET":
			if strings.HasSuffix(r.URL.Path, "/r2/buckets") {
				_, _ = w.Write([]byte(`{"success":true,"result":{"buckets":[{"name":"photos","creation_date":"2026-01-02T03:04:05Z","location":"WNAM"}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"result":{"name":"photos","location":"WNAM"}}`))
		case "POST":
			_, _ = w.Write([]byte(`{"success":true,"result":{"name":"photos"}}`))
		case "DELETE":
			_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	stdout, _, err := runR2CLI(t, srv.URL, "r2", "list", "--name-contains", "photo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "NAME") || !strings.Contains(stdout, "photos") {
		t.Fatalf("list output = %q", stdout)
	}
	if _, _, err := runR2CLI(t, srv.URL, "r2", "create", "photos", "--location", "wnam"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runR2CLI(t, srv.URL, "--output", "json", "r2", "info", "photos"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runR2CLI(t, srv.URL, "r2", "delete", "photos", "--force"); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 4 {
		t.Fatalf("requests = %d, want 4", len(requests))
	}
	path := "/accounts/" + r2TestAccountID + "/r2/buckets"
	if got := requests[0]; got.method != "GET" || got.path != path || got.query != "name_contains=photo&per_page=100" {
		t.Errorf("list request = %#v", got)
	}
	if got := requests[1]; got.method != "POST" || got.path != path {
		t.Errorf("create request = %#v", got)
	} else {
		r2AssertJSONEqual(t, got.body, `{"name":"photos","locationHint":"wnam"}`)
	}
	if got := requests[2]; got.method != "GET" || got.path != path+"/photos" {
		t.Errorf("info request = %#v", got)
	}
	if got := requests[3]; got.method != "DELETE" || got.path != path+"/photos" {
		t.Errorf("delete request = %#v", got)
	}
}

func TestR2ListFollowsCursorAcrossPages(t *testing.T) {
	var cursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.HasSuffix(r.URL.Path, "/r2/buckets") {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("per_page"); got != "100" {
			t.Fatalf("per_page = %q", got)
		}
		cursor := r.URL.Query().Get("cursor")
		cursors = append(cursors, cursor)
		w.Header().Set("Content-Type", "application/json")
		switch cursor {
		case "":
			_, _ = w.Write([]byte(`{"success":true,"result":{"buckets":[{"name":"first"}],"cursor":"next-page"}}`))
		case "next-page":
			_, _ = w.Write([]byte(`{"success":true,"result":{"buckets":[{"name":"second"}]}}`))
		default:
			t.Fatalf("unexpected cursor %q", cursor)
		}
	}))
	defer srv.Close()

	stdout, _, err := runR2CLI(t, srv.URL, "--output", "json", "r2", "list")
	if err != nil {
		t.Fatal(err)
	}
	var result r2BucketList
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("list output is not JSON: %v\n%s", err, stdout)
	}
	if len(result.Buckets) != 2 || result.Buckets[0].Name != "first" || result.Buckets[1].Name != "second" {
		t.Fatalf("buckets = %#v", result.Buckets)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "next-page" {
		t.Fatalf("cursors = %#v", cursors)
	}
}

func TestR2BucketCommandsDryRun(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		method     string
		pathSuffix string
		query      string
		body       string
	}{
		{"list", []string{"r2", "list", "--name-contains", "photo", "--dry-run"}, "GET", "/r2/buckets", "name_contains=photo&per_page=100", ""},
		{"create", []string{"r2", "create", "photos", "--dry-run"}, "POST", "/r2/buckets", "", `{"name":"photos"}`},
		{"info", []string{"r2", "info", "photos", "--dry-run"}, "GET", "/r2/buckets/photos", "", ""},
		{"delete", []string{"r2", "delete", "photos", "--dry-run"}, "DELETE", "/r2/buckets/photos", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := runR2CLI(t, "http://example.invalid", tc.args...)
			if err != nil {
				t.Fatal(err)
			}
			var dump struct {
				Method string          `json:"method"`
				URL    string          `json:"url"`
				Body   json.RawMessage `json:"body"`
			}
			if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
				t.Fatalf("dry-run output is not JSON: %v\n%s", err, stdout)
			}
			if dump.Method != tc.method || !strings.Contains(dump.URL, tc.pathSuffix) {
				t.Errorf("dump = %+v", dump)
			}
			if tc.query != "" && !strings.Contains(dump.URL, tc.query) {
				t.Errorf("url = %q, want query %q", dump.URL, tc.query)
			}
			if tc.body != "" {
				r2AssertJSONEqual(t, dump.Body, tc.body)
			}
		})
	}
}

func TestR2ValidationAndArguments(t *testing.T) {
	_, _, err := runR2CLI(t, "http://example.invalid", "--account-id", "", "r2", "list", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("expected account error, got %v", err)
	}
	_, _, err = runR2CLI(t, "http://example.invalid", "r2", "create", "photos", "--location", "", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--location cannot be empty") {
		t.Fatalf("expected location error, got %v", err)
	}
	_, _, err = runR2CLI(t, "http://example.invalid", "r2", "delete", "photos")
	if err == nil || !strings.Contains(err.Error(), "pass --force") {
		t.Fatalf("expected destructive confirmation error, got %v", err)
	}
	for _, args := range [][]string{
		{"r2", "create", "", "--dry-run"},
		{"r2", "info", "   ", "--dry-run"},
		{"r2", "delete", "", "--force", "--dry-run"},
	} {
		_, _, err := runR2CLI(t, "http://example.invalid", args...)
		if err == nil || !strings.Contains(err.Error(), "bucket name cannot be empty") {
			t.Fatalf("expected bucket-name error for %v, got %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"r2", "list", "extra", "--dry-run"},
		{"r2", "create", "one", "two", "--dry-run"},
		{"r2", "info", "one", "two", "--dry-run"},
		{"r2", "delete", "one", "two", "--dry-run"},
	} {
		_, _, err := runR2CLI(t, "http://example.invalid", args...)
		if err == nil {
			t.Fatalf("expected argument error for %v", args)
		}
	}
}

func TestR2HelpIncludesExamples(t *testing.T) {
	for _, args := range [][]string{
		{"r2", "list", "--help"},
		{"r2", "create", "--help"},
		{"r2", "info", "--help"},
		{"r2", "delete", "--help"},
	} {
		root := NewRootCmd()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Examples:") || !strings.Contains(out.String(), "cf r2") {
			t.Errorf("help for %v missing example:\n%s", args, out.String())
		}
	}
}

func r2AssertJSONEqual(t *testing.T, got []byte, want string) {
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
