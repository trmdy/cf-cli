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
	"time"
)

const streamTestAccountID = "a1b2c3d4e5f6789012345678abcdef01"
const streamTestVideoID = "ea95132c15732412d22c1476fa83f27a"

func runStreamCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", streamTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func streamAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func TestBuildStreamDirectUploadBodyMinimal(t *testing.T) {
	body, err := buildStreamDirectUploadBody(streamDirectUploadOpts{MaxDurationSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	streamAssertJSONEqual(t, body, `{"maxDurationSeconds":3600}`)
}

func TestBuildStreamDirectUploadBodyFull(t *testing.T) {
	body, err := buildStreamDirectUploadBody(streamDirectUploadOpts{
		MaxDurationSeconds:    600,
		Expiry:                "2026-12-01T00:00:00Z",
		Creator:               "user-42",
		Name:                  "promo.mp4",
		AllowedOrigins:        []string{"example.com", "*.app.example.com"},
		RequireSignedURLs:     true,
		RequireSignedURLsSet:  true,
		ScheduledDeletion:     "2027-01-01T00:00:00Z",
		ThumbnailTimestampPct: 0.5,
		ThumbnailPctSet:       true,
		WatermarkUID:          "wm-uid-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	streamAssertJSONEqual(t, body, `{
		"maxDurationSeconds": 600,
		"expiry": "2026-12-01T00:00:00Z",
		"creator": "user-42",
		"meta": {"name": "promo.mp4"},
		"allowedOrigins": ["example.com", "*.app.example.com"],
		"requireSignedURLs": true,
		"scheduledDeletion": "2027-01-01T00:00:00Z",
		"thumbnailTimestampPct": 0.5,
		"watermark": {"uid": "wm-uid-1"}
	}`)
}

func TestBuildStreamDirectUploadBodyValidation(t *testing.T) {
	if _, err := buildStreamDirectUploadBody(streamDirectUploadOpts{}); err == nil || !strings.Contains(err.Error(), "max-duration-seconds") {
		t.Fatalf("expected max-duration error, got %v", err)
	}
	if _, err := buildStreamDirectUploadBody(streamDirectUploadOpts{MaxDurationSeconds: -2}); err == nil {
		t.Fatal("expected error for invalid max duration")
	}
	if _, err := buildStreamDirectUploadBody(streamDirectUploadOpts{
		MaxDurationSeconds: 60,
		Expiry:             "not-a-date",
	}); err == nil || !strings.Contains(err.Error(), "expiry") {
		t.Fatalf("expected expiry error, got %v", err)
	}
	if _, err := buildStreamDirectUploadBody(streamDirectUploadOpts{
		MaxDurationSeconds:    60,
		ThumbnailPctSet:       true,
		ThumbnailTimestampPct: 1.5,
	}); err == nil || !strings.Contains(err.Error(), "thumbnail-timestamp-pct") {
		t.Fatalf("expected thumbnail error, got %v", err)
	}
	if _, err := buildStreamDirectUploadBody(streamDirectUploadOpts{
		MaxDurationSeconds: 60,
		AllowedOrigins:     []string{"ok", ""},
	}); err == nil || !strings.Contains(err.Error(), "allowed-origin") {
		t.Fatalf("expected allowed-origin error, got %v", err)
	}
	// -1 (unknown) is allowed by the API.
	body, err := buildStreamDirectUploadBody(streamDirectUploadOpts{MaxDurationSeconds: -1})
	if err != nil {
		t.Fatal(err)
	}
	streamAssertJSONEqual(t, body, `{"maxDurationSeconds":-1}`)
}

func TestBuildStreamTokenBodyEmpty(t *testing.T) {
	body, err := buildStreamTokenBody(streamTokenOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		t.Fatalf("expected nil body for defaults, got %s", body)
	}
}

func TestBuildStreamTokenBodyExpiresIn(t *testing.T) {
	now := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)
	body, err := buildStreamTokenBody(streamTokenOpts{
		ExpiresIn: "30m",
		Now:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantExp := now.Add(30 * time.Minute).Unix()
	streamAssertJSONEqual(t, body, `{"exp":`+strconv.FormatInt(wantExp, 10)+`}`)
}

func TestBuildStreamTokenBodyExpAndDownloadable(t *testing.T) {
	body, err := buildStreamTokenBody(streamTokenOpts{
		Exp:             1735689600,
		ExpSet:          true,
		Downloadable:    true,
		DownloadableSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	streamAssertJSONEqual(t, body, `{"exp":1735689600,"downloadable":true}`)
}

func TestBuildStreamTokenBodyValidation(t *testing.T) {
	if _, err := buildStreamTokenBody(streamTokenOpts{ExpSet: true, Exp: 1, ExpiresIn: "1h"}); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
	if _, err := buildStreamTokenBody(streamTokenOpts{ExpiresIn: "not-a-duration"}); err == nil {
		t.Fatal("expected duration parse error")
	}
	if _, err := buildStreamTokenBody(streamTokenOpts{ExpiresIn: "0s"}); err == nil || !strings.Contains(err.Error(), "positive") {
		t.Fatalf("expected positive duration error, got %v", err)
	}
	if _, err := buildStreamTokenBody(streamTokenOpts{ExpiresIn: "48h"}); err == nil || !strings.Contains(err.Error(), "24h") {
		t.Fatalf("expected 24h limit error, got %v", err)
	}
	if _, err := buildStreamTokenBody(streamTokenOpts{ExpSet: true, Exp: 0}); err == nil {
		t.Fatal("expected positive exp error")
	}
}

func TestStreamListDryRun(t *testing.T) {
	stdout, _, err := runStreamCLI(t, "http://example.invalid",
		"stream", "list",
		"--status", "ready",
		"--search", "promo",
		"--type", "vod",
		"--name", "clip.mp4",
		"--creator", "c1",
		"--limit", "50",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump map[string]any
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	if dump["method"] != "GET" {
		t.Errorf("method = %v", dump["method"])
	}
	u, _ := dump["url"].(string)
	if !strings.Contains(u, "/accounts/"+streamTestAccountID+"/stream") {
		t.Errorf("url = %s", u)
	}
	for _, want := range []string{"status=ready", "search=promo", "type=vod", "video_name=clip.mp4", "creator=c1", "limit=50"} {
		if !strings.Contains(u, want) {
			t.Errorf("url missing %q: %s", want, u)
		}
	}
}

func TestStreamListTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/accounts/"+streamTestAccountID+"/stream" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"result": [
				{
					"uid": "` + streamTestVideoID + `",
					"meta": {"name": "promo.mp4"},
					"duration": 12.5,
					"readyToStream": true,
					"created": "2026-01-02T03:04:05Z",
					"status": {"state": "ready"}
				}
			]
		}`))
	}))
	defer srv.Close()

	stdout, _, err := runStreamCLI(t, srv.URL, "stream", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"UID", "NAME", "STATUS", streamTestVideoID, "promo.mp4", "ready", "12.5s", "true"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestStreamListJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"uid":"v1","meta":{"name":"a.mp4"}}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runStreamCLI(t, srv.URL, "stream", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var videos []map[string]any
	if err := json.Unmarshal([]byte(stdout), &videos); err != nil {
		t.Fatalf("expected JSON array: %v\n%s", err, stdout)
	}
	if len(videos) != 1 || videos[0]["uid"] != "v1" {
		t.Fatalf("videos = %v", videos)
	}
}

func TestStreamGetHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"uid":"` + streamTestVideoID + `","readyToStream":true}}`))
	}))
	defer srv.Close()

	stdout, _, err := runStreamCLI(t, srv.URL, "stream", "get", streamTestVideoID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+streamTestAccountID+"/stream/"+streamTestVideoID {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, streamTestVideoID) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestStreamGetDryRun(t *testing.T) {
	stdout, _, err := runStreamCLI(t, "http://example.invalid",
		"stream", "get", streamTestVideoID, "--dry-run",
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+streamTestAccountID+"/stream/"+streamTestVideoID) {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestStreamDeleteHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"uid":"` + streamTestVideoID + `"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runStreamCLI(t, srv.URL, "stream", "delete", streamTestVideoID, "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+streamTestAccountID+"/stream/"+streamTestVideoID {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, streamTestVideoID) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestStreamDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runStreamCLI(t, srv.URL, "stream", "delete", streamTestVideoID)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestStreamDeleteDryRunSkipsConfirm(t *testing.T) {
	stdout, _, err := runStreamCLI(t, "http://example.invalid",
		"stream", "delete", streamTestVideoID, "--dry-run",
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
	if dump.Method != "DELETE" {
		t.Errorf("method = %s", dump.Method)
	}
}

func TestStreamUploadHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"uid":"new-uid","uploadURL":"https://upload.example/u"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runStreamCLI(t, srv.URL,
		"stream", "upload",
		"--max-duration-seconds", "3600",
		"--name", "clip.mp4",
		"--require-signed-urls",
		"--allowed-origin", "example.com",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+streamTestAccountID+"/stream/direct_upload" {
		t.Errorf("path = %s", gotPath)
	}
	streamAssertJSONEqual(t, gotBody, `{
		"maxDurationSeconds": 3600,
		"meta": {"name": "clip.mp4"},
		"requireSignedURLs": true,
		"allowedOrigins": ["example.com"]
	}`)
	if !strings.Contains(stdout, "uploadURL") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestStreamUploadDryRun(t *testing.T) {
	stdout, _, err := runStreamCLI(t, "http://example.invalid",
		"stream", "upload",
		"--max-duration-seconds", "600",
		"--expiry", "2026-12-01T00:00:00Z",
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+streamTestAccountID+"/stream/direct_upload") {
		t.Errorf("url = %s", dump.URL)
	}
	streamAssertJSONEqual(t, dump.Body, `{"maxDurationSeconds":600,"expiry":"2026-12-01T00:00:00Z"}`)
}

func TestStreamUploadRequiresMaxDuration(t *testing.T) {
	_, _, err := runStreamCLI(t, "http://example.invalid", "stream", "upload", "--dry-run")
	if err == nil {
		t.Fatal("expected required flag error")
	}
	if !strings.Contains(err.Error(), "max-duration-seconds") && !strings.Contains(err.Error(), "required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamTokenHTTPRequestDefaultBody(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	var hadContentType bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		hadContentType = r.Header.Get("Content-Type") != ""
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"token":"signed.jwt.here"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runStreamCLI(t, srv.URL, "stream", "token", streamTestVideoID)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+streamTestAccountID+"/stream/"+streamTestVideoID+"/token" {
		t.Errorf("path = %s", gotPath)
	}
	if len(gotBody) != 0 {
		t.Errorf("expected empty body for defaults, got %s", gotBody)
	}
	if hadContentType {
		// Client only sets Content-Type when body is non-empty.
		t.Error("did not expect Content-Type with empty body")
	}
	if !strings.Contains(stdout, "signed.jwt.here") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestStreamTokenDryRunWithExpiresIn(t *testing.T) {
	stdout, _, err := runStreamCLI(t, "http://example.invalid",
		"stream", "token", streamTestVideoID,
		"--expires-in", "1h",
		"--downloadable",
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+streamTestAccountID+"/stream/"+streamTestVideoID+"/token") {
		t.Errorf("url = %s", dump.URL)
	}
	var body map[string]any
	if err := json.Unmarshal(dump.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["downloadable"] != true {
		t.Errorf("downloadable = %v", body["downloadable"])
	}
	exp, ok := body["exp"].(float64)
	if !ok || exp <= 0 {
		t.Errorf("exp = %v", body["exp"])
	}
}

func TestStreamTokenRejectsExpAndExpiresIn(t *testing.T) {
	_, _, err := runStreamCLI(t, "http://example.invalid",
		"stream", "token", streamTestVideoID,
		"--exp", "1735689600",
		"--expires-in", "1h",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}

func TestStreamRequiresAccountID(t *testing.T) {
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "test-token",
		"stream", "list",
		"--dry-run",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "account ID") {
		t.Fatalf("expected missing account ID error, got %v", err)
	}
}

func TestStreamPathHelpers(t *testing.T) {
	if got := streamVideosPath("acct"); got != "/accounts/acct/stream" {
		t.Errorf("videos path = %s", got)
	}
	if got := streamVideoPath("acct", "vid/with spaces"); got != "/accounts/acct/stream/vid%2Fwith%20spaces" {
		t.Errorf("video path = %s", got)
	}
	if got := streamDirectUploadPath("acct"); got != "/accounts/acct/stream/direct_upload" {
		t.Errorf("direct upload path = %s", got)
	}
	if got := streamTokenPath("acct", "vid"); got != "/accounts/acct/stream/vid/token" {
		t.Errorf("token path = %s", got)
	}
}

func TestStreamFormatDuration(t *testing.T) {
	cases := map[float64]string{
		-1:   "",
		0:    "0s",
		12:   "12s",
		12.5: "12.5s",
	}
	for in, want := range cases {
		if got := streamFormatDuration(in); got != want {
			t.Errorf("streamFormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestStreamCommandsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"stream", "list", "extra", "--dry-run"},
		{"stream", "upload", "extra", "--max-duration-seconds", "60", "--dry-run"},
	}
	for _, args := range cases {
		_, _, err := runStreamCLI(t, "http://example.invalid", args...)
		if err == nil {
			t.Fatalf("expected error for stray args: %v", args)
		}
	}
}

func TestStreamHelpIncludesExamples(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"stream", "upload", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"--max-duration-seconds", "cf stream upload", "direct upload"} {
		if !strings.Contains(help, want) {
			t.Errorf("upload help missing %q\n%s", want, help)
		}
	}

	out.Reset()
	root = NewRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"stream", "token", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help = out.String()
	for _, want := range []string{"cf stream token", "--expires-in", "--downloadable"} {
		if !strings.Contains(help, want) {
			t.Errorf("token help missing %q\n%s", want, help)
		}
	}
}
