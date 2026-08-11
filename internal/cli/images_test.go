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
	"strconv"
	"strings"
	"testing"
)

const imagesTestAccountID = "023e105f4ecef8ad9ca31a8372d0c353"

func runImagesCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", imagesTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func imagesAssertJSONEqual(t *testing.T, got []byte, want string) {
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

func TestBuildImagesUploadFormURL(t *testing.T) {
	fields, fileField, filePath, err := buildImagesUploadForm("", "https://example.com/logo.png", "brand", `{"album":"home"}`, "user-1", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if fileField != "" || filePath != "" {
		t.Fatalf("unexpected file field %q path %q", fileField, filePath)
	}
	if fields["url"] != "https://example.com/logo.png" {
		t.Errorf("url = %q", fields["url"])
	}
	if fields["id"] != "brand" {
		t.Errorf("id = %q", fields["id"])
	}
	if fields["creator"] != "user-1" {
		t.Errorf("creator = %q", fields["creator"])
	}
	if fields["requireSignedURLs"] != "true" {
		t.Errorf("requireSignedURLs = %q", fields["requireSignedURLs"])
	}
	if fields["metadata"] != `{"album":"home"}` {
		t.Errorf("metadata = %q", fields["metadata"])
	}
}

func TestBuildImagesUploadFormFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(path, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fields, fileField, filePath, err := buildImagesUploadForm(path, "", "", "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if fileField != "file" || filePath != path {
		t.Fatalf("fileField=%q filePath=%q", fileField, filePath)
	}
	if _, ok := fields["url"]; ok {
		t.Error("url field should be absent for file upload")
	}
}

func TestBuildImagesUploadFormValidation(t *testing.T) {
	if _, _, _, err := buildImagesUploadForm("", "", "", "", "", false, false); err == nil || !strings.Contains(err.Error(), "--file or --url") {
		t.Fatalf("expected missing source error, got %v", err)
	}
	if _, _, _, err := buildImagesUploadForm("a.png", "https://example.com/a.png", "", "", "", false, false); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected mixed source error, got %v", err)
	}
	if _, _, _, err := buildImagesUploadForm("", "https://example.com/a.png", "", "not-json", "", false, false); err == nil || !strings.Contains(err.Error(), "--metadata") {
		t.Fatalf("expected metadata error, got %v", err)
	}
	if _, _, _, err := buildImagesUploadForm("/no/such/file.png", "", "", "", "", false, false); err == nil {
		t.Fatal("expected missing file error")
	}
}

func TestBuildImagesVariantOptions(t *testing.T) {
	opts, err := buildImagesVariantOptions(1366, 768, "scale-down", "none")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(opts)
	imagesAssertJSONEqual(t, raw, `{"fit":"scale-down","height":768,"metadata":"none","width":1366}`)

	if _, err := buildImagesVariantOptions(0, 10, "cover", "none"); err == nil || !strings.Contains(err.Error(), "--width") {
		t.Fatalf("expected width error, got %v", err)
	}
	if _, err := buildImagesVariantOptions(10, 0, "cover", "none"); err == nil || !strings.Contains(err.Error(), "--height") {
		t.Fatalf("expected height error, got %v", err)
	}
	if _, err := buildImagesVariantOptions(10, 10, "stretch", "none"); err == nil || !strings.Contains(err.Error(), "--fit") {
		t.Fatalf("expected fit error, got %v", err)
	}
	if _, err := buildImagesVariantOptions(10, 10, "cover", "all"); err == nil || !strings.Contains(err.Error(), "--metadata") {
		t.Fatalf("expected metadata error, got %v", err)
	}
}

func TestBuildImagesVariantCreateBody(t *testing.T) {
	body, err := buildImagesVariantCreateBody("hero", 1366, 768, "SCALE-DOWN", "NONE", true, true)
	if err != nil {
		t.Fatal(err)
	}
	imagesAssertJSONEqual(t, body, `{"id":"hero","neverRequireSignedURLs":true,"options":{"fit":"scale-down","height":768,"metadata":"none","width":1366}}`)

	body, err = buildImagesVariantCreateBody("thumb", 200, 200, "cover", "keep", false, false)
	if err != nil {
		t.Fatal(err)
	}
	imagesAssertJSONEqual(t, body, `{"id":"thumb","options":{"fit":"cover","height":200,"metadata":"keep","width":200}}`)
}

func TestBuildImagesVariantUpdateBody(t *testing.T) {
	body, err := buildImagesVariantUpdateBody(1600, 900, "cover", "copyright", false, true)
	if err != nil {
		t.Fatal(err)
	}
	imagesAssertJSONEqual(t, body, `{"neverRequireSignedURLs":false,"options":{"fit":"cover","height":900,"metadata":"copyright","width":1600}}`)
}

func TestParseVariantList(t *testing.T) {
	raw := []byte(`{"variants":{"hero":{"id":"hero","options":{"fit":"cover","width":100,"height":50,"metadata":"none"},"neverRequireSignedURLs":true},"public":{"id":"public","options":{"fit":"scale-down","width":200,"height":200,"metadata":"keep"}}}}`)
	variants, err := parseVariantList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 2 {
		t.Fatalf("len = %d", len(variants))
	}
	if variants["hero"].Options == nil || variants["hero"].Options.Width != 100 {
		t.Fatalf("hero = %+v", variants["hero"])
	}
}

func TestImagesListDryRun(t *testing.T) {
	stdout, _, err := runImagesCLI(t, "http://example.invalid",
		"images", "list",
		"--creator", "user-1",
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
	if !strings.Contains(dump.URL, "/accounts/"+imagesTestAccountID+"/images/v1") {
		t.Errorf("url = %s", dump.URL)
	}
	if !strings.Contains(dump.URL, "per_page=100") || !strings.Contains(dump.URL, "page=1") {
		t.Errorf("url missing pagination: %s", dump.URL)
	}
	if !strings.Contains(dump.URL, "creator=user-1") {
		t.Errorf("url missing creator: %s", dump.URL)
	}
}

func TestImagesListHTTPRequest(t *testing.T) {
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"success":true,"result":{"images":[{"id":"img-1","filename":"a.png","uploaded":"2024-01-01T00:00:00Z","requireSignedURLs":false,"variants":["https://x/public"]},{"id":"img-2","filename":"b.jpg","uploaded":"2024-01-02T00:00:00Z","requireSignedURLs":true,"variants":[]}]},"result_info":{"page":1,"per_page":100,"total_pages":1,"count":2,"total_count":2}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"result":{"images":[]}}`))
		}
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "list")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotPaths) != 1 {
		t.Fatalf("paths = %v", gotPaths)
	}
	if !strings.Contains(stdout, "img-1") || !strings.Contains(stdout, "a.png") {
		t.Errorf("stdout = %s", stdout)
	}
	if !strings.Contains(stdout, "FILENAME") {
		t.Errorf("expected table headers, got %s", stdout)
	}
}

func TestImagesListJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"images":[{"id":"img-1","filename":"a.png"}]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	imagesAssertJSONEqual(t, []byte(stdout), `[{"id":"img-1","filename":"a.png"}]`)
}

func TestImagesListPaginates(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages = append(pages, r.URL.Query().Get("page"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			// full page of 100 forces another request
			var b strings.Builder
			b.WriteString(`{"success":true,"result":{"images":[`)
			for i := 0; i < 100; i++ {
				if i > 0 {
					b.WriteByte(',')
				}
				b.WriteString(`{"id":"p1-`)
				b.WriteString(strconv.Itoa(i))
				b.WriteString(`","filename":"a.png"}`)
			}
			b.WriteString(`]}}`)
			_, _ = w.Write([]byte(b.String()))
		case "2":
			_, _ = w.Write([]byte(`{"success":true,"result":{"images":[{"id":"p2-0","filename":"b.png"}]}}`))
		default:
			t.Fatalf("unexpected page %s", r.URL.Query().Get("page"))
		}
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Fatalf("pages = %v", pages)
	}
	if !strings.Contains(stdout, `"p1-0"`) || !strings.Contains(stdout, `"p2-0"`) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesGetHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"img-1","filename":"logo.png"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "get", "img-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "GET" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/img-1" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, "logo.png") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesGetDryRun(t *testing.T) {
	stdout, _, err := runImagesCLI(t, "http://example.invalid",
		"images", "get", "img-1", "--dry-run",
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+imagesTestAccountID+"/images/v1/img-1") {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestImagesUploadDryRunURL(t *testing.T) {
	stdout, _, err := runImagesCLI(t, "http://example.invalid",
		"images", "upload",
		"--url", "https://example.com/logo.png",
		"--id", "brand-logo",
		"--require-signed-urls",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	var dump struct {
		Method  string         `json:"method"`
		URL     string         `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    map[string]any `json:"body"`
	}
	if err := json.Unmarshal([]byte(stdout), &dump); err != nil {
		t.Fatalf("parse dump: %v\n%s", err, stdout)
	}
	if dump.Method != "POST" {
		t.Errorf("method = %s", dump.Method)
	}
	if !strings.HasSuffix(dump.URL, "/accounts/"+imagesTestAccountID+"/images/v1") {
		t.Errorf("url = %s", dump.URL)
	}
	if !strings.Contains(dump.Headers["Content-Type"], "multipart/form-data") {
		t.Errorf("content-type = %q", dump.Headers["Content-Type"])
	}
	if dump.Body["url"] != "https://example.com/logo.png" {
		t.Errorf("body url = %v", dump.Body["url"])
	}
	if dump.Body["id"] != "brand-logo" {
		t.Errorf("body id = %v", dump.Body["id"])
	}
	if dump.Body["requireSignedURLs"] != "true" {
		t.Errorf("body requireSignedURLs = %v", dump.Body["requireSignedURLs"])
	}
}

func TestImagesUploadHTTPMultipartURL(t *testing.T) {
	var gotMethod, gotPath, gotCT string
	var form urlValues
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		mediaType, params, err := mime.ParseMediaType(gotCT)
		if err != nil || mediaType != "multipart/form-data" {
			t.Errorf("content-type = %q err=%v", gotCT, err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		form = urlValues{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(part)
			form[part.FormName()] = string(b)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"uploaded-1","filename":"logo.png"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL,
		"images", "upload",
		"--url", "https://example.com/logo.png",
		"--metadata", `{"k":"v"}`,
		"--creator", "c1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1" {
		t.Errorf("path = %s", gotPath)
	}
	if form["url"] != "https://example.com/logo.png" {
		t.Errorf("form url = %q", form["url"])
	}
	if form["metadata"] != `{"k":"v"}` {
		t.Errorf("form metadata = %q", form["metadata"])
	}
	if form["creator"] != "c1" {
		t.Errorf("form creator = %q", form["creator"])
	}
	if !strings.Contains(stdout, "uploaded-1") {
		t.Errorf("stdout = %s", stdout)
	}
}

// urlValues is a tiny map alias for multipart form assertions.
type urlValues map[string]string

func TestImagesUploadHTTPMultipartFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(path, []byte("fake-png-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotFileName, gotFileBody string
	var form urlValues
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content-type = %q err=%v", r.Header.Get("Content-Type"), err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		form = urlValues{}
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "file" {
				gotFileName = part.FileName()
				gotFileBody = string(b)
			} else {
				form[part.FormName()] = string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"file-1"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL,
		"images", "upload",
		"--file", path,
		"--id", "custom-id",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotFileName != "logo.png" {
		t.Errorf("filename = %q", gotFileName)
	}
	if gotFileBody != "fake-png-data" {
		t.Errorf("file body = %q", gotFileBody)
	}
	if form["id"] != "custom-id" {
		t.Errorf("form id = %q", form["id"])
	}
	if !strings.Contains(stdout, "file-1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesUploadRequiresSource(t *testing.T) {
	_, _, err := runImagesCLI(t, "http://example.invalid", "images", "upload", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--file or --url") {
		t.Fatalf("expected source error, got %v", err)
	}
}

func TestImagesDeleteDryRun(t *testing.T) {
	stdout, _, err := runImagesCLI(t, "http://example.invalid",
		"images", "delete", "img-1", "--force", "--dry-run",
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+imagesTestAccountID+"/images/v1/img-1") {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestImagesDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runImagesCLI(t, srv.URL, "images", "delete", "img-1")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestImagesDeleteHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runImagesCLI(t, srv.URL, "images", "delete", "img-1", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/img-1" {
		t.Errorf("path = %s", gotPath)
	}
}

func TestImagesVariantListHTTPRequest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"variants":{"hero":{"id":"hero","options":{"fit":"scale-down","width":1366,"height":768,"metadata":"none"},"neverRequireSignedURLs":true},"public":{"id":"public","options":{"fit":"contain","width":100,"height":100,"metadata":"keep"}}}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "variant", "list")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/variants" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, "hero") || !strings.Contains(stdout, "1366") {
		t.Errorf("stdout = %s", stdout)
	}
	// sorted ids: hero before public
	if iHero, iPublic := strings.Index(stdout, "hero"), strings.Index(stdout, "public"); iHero < 0 || iPublic < 0 || iHero > iPublic {
		t.Errorf("expected hero before public in table:\n%s", stdout)
	}
}

func TestImagesVariantCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"variant":{"id":"hero"}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL,
		"images", "variant", "create", "hero",
		"--width", "1366",
		"--height", "768",
		"--fit", "scale-down",
		"--metadata", "none",
		"--never-require-signed-urls",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/variants" {
		t.Errorf("path = %s", gotPath)
	}
	imagesAssertJSONEqual(t, gotBody, `{"id":"hero","neverRequireSignedURLs":true,"options":{"fit":"scale-down","height":768,"metadata":"none","width":1366}}`)
	if !strings.Contains(stdout, "hero") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesVariantCreateDryRun(t *testing.T) {
	stdout, _, err := runImagesCLI(t, "http://example.invalid",
		"images", "variant", "create", "thumb",
		"--width", "200",
		"--height", "200",
		"--fit", "cover",
		"--metadata", "keep",
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+imagesTestAccountID+"/images/v1/variants") {
		t.Errorf("url = %s", dump.URL)
	}
	imagesAssertJSONEqual(t, dump.Body, `{"id":"thumb","options":{"fit":"cover","height":200,"metadata":"keep","width":200}}`)
}

func TestImagesVariantCreateInvalidFit(t *testing.T) {
	_, _, err := runImagesCLI(t, "http://example.invalid",
		"images", "variant", "create", "x",
		"--width", "10",
		"--height", "10",
		"--fit", "stretch",
		"--metadata", "none",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "--fit") {
		t.Fatalf("expected fit error, got %v", err)
	}
}

func TestImagesVariantUpdateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"variant":{"id":"hero"}}}`))
	}))
	defer srv.Close()

	_, _, err := runImagesCLI(t, srv.URL,
		"images", "variant", "update", "hero",
		"--width", "1600",
		"--height", "900",
		"--fit", "cover",
		"--metadata", "none",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PATCH" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/variants/hero" {
		t.Errorf("path = %s", gotPath)
	}
	imagesAssertJSONEqual(t, gotBody, `{"options":{"fit":"cover","height":900,"metadata":"none","width":1600}}`)
}

func TestImagesVariantGetHTTPRequest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"variant":{"id":"hero"}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "variant", "get", "hero")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/variants/hero" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, "hero") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesVariantDeleteHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{}}`))
	}))
	defer srv.Close()

	_, _, err := runImagesCLI(t, srv.URL, "images", "variant", "delete", "hero", "--force")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/variants/hero" {
		t.Errorf("path = %s", gotPath)
	}
}

func TestImagesVariantDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request")
	}))
	defer srv.Close()

	_, _, err := runImagesCLI(t, srv.URL, "images", "variant", "delete", "hero")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestImagesUsageHTTPRequest(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"count":{"current":1000,"allowed":100000}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "usage")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+imagesTestAccountID+"/images/v1/stats" {
		t.Errorf("path = %s", gotPath)
	}
	if !strings.Contains(stdout, "1000") || !strings.Contains(stdout, "100000") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesUsageTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"count":{"current":12,"allowed":100}}}`))
	}))
	defer srv.Close()

	stdout, _, err := runImagesCLI(t, srv.URL, "images", "usage", "--output", "table")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "CURRENT") || !strings.Contains(stdout, "ALLOWED") {
		t.Errorf("stdout = %s", stdout)
	}
	if !strings.Contains(stdout, "12") || !strings.Contains(stdout, "100") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestImagesUsageDryRun(t *testing.T) {
	stdout, _, err := runImagesCLI(t, "http://example.invalid", "images", "usage", "--dry-run")
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
	if !strings.HasSuffix(dump.URL, "/accounts/"+imagesTestAccountID+"/images/v1/stats") {
		t.Errorf("url = %s", dump.URL)
	}
}

func TestImagesRequiresAccountID(t *testing.T) {
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{
		"--base-url", "http://example.invalid",
		"--token", "test-token",
		"images", "list", "--dry-run",
	})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "account ID") {
		t.Fatalf("expected account ID error, got %v", err)
	}
}

func TestImagesHelpIncludesExamples(t *testing.T) {
	root := NewRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"images", "upload", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"--file", "--url", "cf images upload"} {
		if !strings.Contains(help, want) {
			t.Errorf("upload help missing %q\n%s", want, help)
		}
	}

	out.Reset()
	root = NewRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"images", "variant", "create", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help = out.String()
	for _, want := range []string{"--width", "--height", "--fit", "--metadata", "cf images variant create"} {
		if !strings.Contains(help, want) {
			t.Errorf("variant create help missing %q\n%s", want, help)
		}
	}
}

func TestImagesCommandsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"images", "list", "extra", "--dry-run"},
		{"images", "usage", "extra", "--dry-run"},
		{"images", "variant", "list", "extra", "--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, _, err := runImagesCLI(t, "http://example.invalid", args...)
			if err == nil {
				t.Fatal("expected error for stray args")
			}
		})
	}
}
