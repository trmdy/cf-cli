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

const vectorizeTestAccountID = "0123456789abcdef0123456789abcdef"

func runVectorizeCLI(t *testing.T, serverURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runVectorizeCLIWithStdin(t, serverURL, nil, args...)
}

func runVectorizeCLIWithStdin(t *testing.T, serverURL string, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	if stdin != nil {
		root.SetIn(stdin)
	}
	all := append([]string{
		"--base-url", serverURL,
		"--token", "test-token",
		"--account-id", vectorizeTestAccountID,
	}, args...)
	root.SetArgs(all)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

func vectorizeAssertJSONEqual(t *testing.T, got []byte, want string) {
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

// vectorizeDump is the --dry-run request representation.
type vectorizeDump struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func vectorizeParseDump(t *testing.T, stdout string) vectorizeDump {
	t.Helper()
	var d vectorizeDump
	if err := json.Unmarshal([]byte(stdout), &d); err != nil {
		t.Fatalf("dry-run output not JSON: %v\n%s", err, stdout)
	}
	return d
}

// --- index create body -----------------------------------------------------

func TestBuildVectorizeCreateBodyDimensions(t *testing.T) {
	body, err := buildVectorizeCreateBody(vectorizeCreateOpts{
		name:       "product-embeddings",
		dimensions: 768,
		metric:     "cosine",
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"name":"product-embeddings","config":{"dimensions":768,"metric":"cosine"}}`)
}

func TestBuildVectorizeCreateBodyPresetAndDescription(t *testing.T) {
	body, err := buildVectorizeCreateBody(vectorizeCreateOpts{
		name:        "docs",
		preset:      "@cf/baai/bge-base-en-v1.5",
		description: "Docs search",
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"name":"docs","description":"Docs search","config":{"preset":"@cf/baai/bge-base-en-v1.5"}}`)
}

func TestBuildVectorizeCreateBodyValidation(t *testing.T) {
	cases := []struct {
		name string
		opts vectorizeCreateOpts
		want string
	}{
		{
			name: "no shape",
			opts: vectorizeCreateOpts{name: "idx"},
			want: "--dimensions and --metric, or --preset",
		},
		{
			name: "preset with dimensions",
			opts: vectorizeCreateOpts{name: "idx", preset: "@cf/baai/bge-base-en-v1.5", dimensions: 768},
			want: "cannot be combined",
		},
		{
			name: "preset with metric",
			opts: vectorizeCreateOpts{name: "idx", preset: "@cf/baai/bge-base-en-v1.5", metric: "cosine"},
			want: "cannot be combined",
		},
		{
			name: "unknown preset",
			opts: vectorizeCreateOpts{name: "idx", preset: "@cf/nope"},
			want: "unknown --preset",
		},
		{
			name: "metric without dimensions",
			opts: vectorizeCreateOpts{name: "idx", metric: "cosine"},
			want: "also needs --dimensions",
		},
		{
			name: "dimensions without metric",
			opts: vectorizeCreateOpts{name: "idx", dimensions: 768},
			want: "also needs --metric",
		},
		{
			name: "unknown metric",
			opts: vectorizeCreateOpts{name: "idx", dimensions: 768, metric: "manhattan"},
			want: "unknown --metric",
		},
		{
			name: "dimensions too large",
			opts: vectorizeCreateOpts{name: "idx", dimensions: 4096, metric: "cosine"},
			want: "between 1 and 1536",
		},
		{
			name: "dimensions negative",
			opts: vectorizeCreateOpts{name: "idx", dimensions: -1, metric: "cosine"},
			want: "between 1 and 1536",
		},
		{
			name: "invalid name",
			opts: vectorizeCreateOpts{name: "Product Embeddings", dimensions: 768, metric: "cosine"},
			want: "invalid index name",
		},
		{
			name: "name with trailing dash",
			opts: vectorizeCreateOpts{name: "idx-", dimensions: 768, metric: "cosine"},
			want: "invalid index name",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildVectorizeCreateBody(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestValidateVectorizeIndexNameAccepts(t *testing.T) {
	for _, name := range []string{"idx", "product-embeddings", "a_b", "docs2"} {
		if err := validateVectorizeIndexName(name); err != nil {
			t.Errorf("name %q rejected: %v", name, err)
		}
	}
}

// --- vector NDJSON body ----------------------------------------------------

func TestBuildVectorizeVectorsBodyNDJSON(t *testing.T) {
	in := "\n{\"id\":\"a\",\"values\":[1,2]}\n\n{\"id\":\"b\",\"values\":[3,4],\"metadata\":{\"genre\":\"drama\"}}\n"
	body, err := buildVectorizeVectorsBody([]byte(in), false)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"id\":\"a\",\"values\":[1,2]}\n{\"id\":\"b\",\"values\":[3,4],\"metadata\":{\"genre\":\"drama\"}}\n"
	if string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestBuildVectorizeVectorsBodyFromJSONArray(t *testing.T) {
	in := `[
	  {"id": "a", "values": [1, 2]},
	  {"id": "b", "values": [3, 4]}
	]`
	body, err := buildVectorizeVectorsBody([]byte(in), false)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"id\":\"a\",\"values\":[1,2]}\n{\"id\":\"b\",\"values\":[3,4]}\n"
	if string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestBuildVectorizeVectorsBodyAcceptsOptionalFields(t *testing.T) {
	in := `{"id":"a","values":[1,2],"namespace":"ns","metadata":{"k":"v"}}`
	if _, err := buildVectorizeVectorsBody([]byte(in), false); err != nil {
		t.Fatal(err)
	}
	// An explicit null namespace is the API's own "unset" representation.
	if _, err := buildVectorizeVectorsBody([]byte(`{"id":"a","values":[1],"namespace":null}`), false); err != nil {
		t.Fatal(err)
	}
}

func TestBuildVectorizeVectorsBodyValidation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "   \n\n", "no vectors given"},
		{"bad array", `[{"id":"a"`, "not valid JSON"},
		{"empty array", `[]`, "no vector objects"},
		{"not an object", `"hello"`, "vector 1 is not a JSON object"},
		{"missing id", `{"values":[1,2]}`, `vector 1 is missing a non-empty "id"`},
		{"empty id", `{"id":"","values":[1,2]}`, `vector 1 is missing a non-empty "id"`},
		{"non-string id", `{"id":42,"values":[1,2]}`, `vector 1 is missing a non-empty "id"`},
		{"missing values", `{"id":"a"}`, `vector 1 (id "a") is missing a non-empty "values"`},
		{"empty values", `{"id":"a","values":[]}`, `is missing a non-empty "values"`},
		{"null values", `{"id":"a","values":null}`, `is missing a non-empty "values"`},
		{"string values", `{"id":"a","values":["x","y"]}`, `has a "values" that is not an array of numbers`},
		{"bool values", `{"id":"a","values":[true]}`, `has a "values" that is not an array of numbers`},
		{"values not an array", `{"id":"a","values":"1,2"}`, `has a "values" that is not an array of numbers`},
		{"non-string namespace", `{"id":"a","values":[1],"namespace":7}`, `has a "namespace" that is not a string`},
		{"bad second line", "{\"id\":\"a\",\"values\":[1]}\n{oops}", "vector 2 is not a JSON object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildVectorizeVectorsBody([]byte(tc.in), false)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildVectorizeVectorsBodyByteLimits(t *testing.T) {
	atLimit := strings.Repeat("a", 64)
	overLimit := strings.Repeat("a", 65)

	if _, err := buildVectorizeVectorsBody([]byte(`{"id":"`+atLimit+`","values":[1]}`), false); err != nil {
		t.Errorf("64-byte id rejected: %v", err)
	}
	_, err := buildVectorizeVectorsBody([]byte(`{"id":"`+overLimit+`","values":[1]}`), false)
	if err == nil || !strings.Contains(err.Error(), `has an "id" of 65 bytes; the limit is 64`) {
		t.Errorf("65-byte id: err = %v", err)
	}

	if _, err := buildVectorizeVectorsBody([]byte(`{"id":"a","values":[1],"namespace":"`+atLimit+`"}`), false); err != nil {
		t.Errorf("64-byte namespace rejected: %v", err)
	}
	_, err = buildVectorizeVectorsBody([]byte(`{"id":"a","values":[1],"namespace":"`+overLimit+`"}`), false)
	if err == nil || !strings.Contains(err.Error(), `has a "namespace" of 65 bytes; the limit is 64`) {
		t.Errorf("65-byte namespace: err = %v", err)
	}
}

func TestValidateVectorizeIndexNameByteLimit(t *testing.T) {
	atLimit := "a" + strings.Repeat("b", 62) + "c" // 64 bytes, matches the pattern
	if len(atLimit) != 64 {
		t.Fatalf("fixture is %d bytes", len(atLimit))
	}
	if err := validateVectorizeIndexName(atLimit); err != nil {
		t.Errorf("64-byte index name rejected: %v", err)
	}
	err := validateVectorizeIndexName(atLimit + "d")
	if err == nil || !strings.Contains(err.Error(), "index name is 65 bytes; the limit is 64") {
		t.Errorf("65-byte index name: err = %v", err)
	}
}

// --- --unparsable-behavior discard delegates to the API --------------------

func TestBuildVectorizeVectorsBodyDiscardPassesThroughNDJSON(t *testing.T) {
	// Line 2 is unparsable and line 3 fails local validation; discard mode
	// hands both to the API rather than failing the whole batch.
	in := "{\"id\":\"a\",\"values\":[1,2]}\n{oops}\n{\"id\":\"c\"}\n"
	body, err := buildVectorizeVectorsBody([]byte(in), true)
	if err != nil {
		t.Fatalf("discard mode should not reject locally: %v", err)
	}
	want := "{\"id\":\"a\",\"values\":[1,2]}\n{oops}\n{\"id\":\"c\"}\n"
	if string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
}

func TestBuildVectorizeVectorsBodyDiscardPassesThroughJSONArray(t *testing.T) {
	in := `[
	  {"id": "a", "values": [1, 2]},
	  {"id": "b"},
	  {"values": [3]}
	]`
	body, err := buildVectorizeVectorsBody([]byte(in), true)
	if err != nil {
		t.Fatalf("discard mode should not reject locally: %v", err)
	}
	// Still one vector per line, so the API can drop exactly the bad ones.
	want := "{\"id\":\"a\",\"values\":[1,2]}\n{\"id\":\"b\"}\n{\"values\":[3]}\n"
	if string(body) != want {
		t.Fatalf("body = %q, want %q", body, want)
	}
	if lines := strings.Count(string(body), "\n"); lines != 3 {
		t.Errorf("got %d lines, want 3", lines)
	}
}

func TestBuildVectorizeVectorsBodyDiscardStillRejectsWholePayloadProblems(t *testing.T) {
	// Whole-payload problems are not per-vector, so the API cannot discard
	// its way out of them.
	if _, err := buildVectorizeVectorsBody([]byte("  \n"), true); err == nil ||
		!strings.Contains(err.Error(), "no vectors given") {
		t.Errorf("empty input: err = %v", err)
	}
	if _, err := buildVectorizeVectorsBody([]byte(`[{"id":"a"`), true); err == nil ||
		!strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("truncated array: err = %v", err)
	}
}

func TestBuildVectorizeVectorsBodyErrorModeStillStrict(t *testing.T) {
	// Explicit --unparsable-behavior error keeps the friendly local check.
	in := "{\"id\":\"a\",\"values\":[1]}\n{oops}\n"
	if _, err := buildVectorizeVectorsBody([]byte(in), false); err == nil ||
		!strings.Contains(err.Error(), "vector 2 is not a JSON object") {
		t.Fatalf("err = %v", err)
	}
}

func TestVectorizeInsertDiscardSendsUnparsableVectors(t *testing.T) {
	var gotBody []byte
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"mutationId":"m-4"}}`))
	}))
	defer srv.Close()

	_, _, err := runVectorizeCLIWithStdin(t, srv.URL,
		strings.NewReader("{\"id\":\"a\",\"values\":[1]}\n{oops}\n"),
		"vectorize", "insert", "product-embeddings",
		"--data", "@-", "--unparsable-behavior", "discard")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "unparsable-behavior=discard" {
		t.Errorf("query = %q", gotQuery)
	}
	if string(gotBody) != "{\"id\":\"a\",\"values\":[1]}\n{oops}\n" {
		t.Errorf("body = %q", gotBody)
	}
}

func TestVectorizeInsertErrorModeRejectsLocally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runVectorizeCLIWithStdin(t, srv.URL,
		strings.NewReader("{\"id\":\"a\",\"values\":[1]}\n{oops}\n"),
		"vectorize", "insert", "product-embeddings",
		"--data", "@-", "--unparsable-behavior", "error")
	if err == nil || !strings.Contains(err.Error(), "vector 2 is not a JSON object") {
		t.Fatalf("expected local rejection, got %v", err)
	}
}

// --- query body ------------------------------------------------------------

func TestBuildVectorizeQueryBodyMinimal(t *testing.T) {
	body, err := buildVectorizeQueryBody(vectorizeQueryOpts{vector: "[0.1,0.2,0.3]"})
	if err != nil {
		t.Fatal(err)
	}
	// Unset options are omitted so the API's own defaults apply.
	vectorizeAssertJSONEqual(t, body, `{"vector":[0.1,0.2,0.3]}`)
}

func TestBuildVectorizeQueryBodyAllOptions(t *testing.T) {
	body, err := buildVectorizeQueryBody(vectorizeQueryOpts{
		vector:            "[1,2]",
		filter:            `{"genre":{"$eq":"drama"}}`,
		topK:              10,
		topKSet:           true,
		returnValues:      true,
		returnValuesSet:   true,
		returnMetadata:    "all",
		returnMetadataSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"vector":[1,2],"filter":{"genre":{"$eq":"drama"}},"topK":10,"returnValues":true,"returnMetadata":"all"}`)
}

func TestBuildVectorizeQueryBodyPreservesVectorPrecision(t *testing.T) {
	body, err := buildVectorizeQueryBody(vectorizeQueryOpts{vector: "[0.12345678901234567,1e-9]"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "0.12345678901234567") {
		t.Fatalf("body lost precision: %s", body)
	}
	if !strings.Contains(string(body), "1e-9") {
		t.Fatalf("body rewrote exponent form: %s", body)
	}
}

func TestBuildVectorizeQueryBodyFromFiles(t *testing.T) {
	dir := t.TempDir()
	vecPath := filepath.Join(dir, "vector.json")
	filterPath := filepath.Join(dir, "filter.json")
	if err := os.WriteFile(vecPath, []byte("[1, 2, 3]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filterPath, []byte(`{"genre":"drama"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := buildVectorizeQueryBody(vectorizeQueryOpts{vector: "@" + vecPath, filter: "@" + filterPath})
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"vector":[1,2,3],"filter":{"genre":"drama"}}`)
}

func TestBuildVectorizeQueryBodyFromStdin(t *testing.T) {
	body, err := buildVectorizeQueryBody(vectorizeQueryOpts{
		vector: "@-",
		stdin:  strings.NewReader("[4,5,6]"),
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"vector":[4,5,6]}`)
}

func TestBuildVectorizeQueryBodyValidation(t *testing.T) {
	cases := []struct {
		name string
		opts vectorizeQueryOpts
		want string
	}{
		{"both stdin", vectorizeQueryOpts{vector: "@-", filter: "@-"}, "cannot both read stdin"},
		{"missing vector", vectorizeQueryOpts{}, "--vector is required"},
		{"vector not an array", vectorizeQueryOpts{vector: `{"a":1}`}, "--vector must be a JSON array of numbers"},
		{"vector of strings", vectorizeQueryOpts{vector: `["a"]`}, "--vector must be a JSON array of numbers"},
		{"empty vector", vectorizeQueryOpts{vector: "[]"}, "at least one number"},
		{"filter not an object", vectorizeQueryOpts{vector: "[1]", filter: "[1]"}, "--filter must be a JSON object"},
		{"top-k too small", vectorizeQueryOpts{vector: "[1]", topK: 0, topKSet: true}, "--top-k must be at least 1"},
		{"top-k negative", vectorizeQueryOpts{vector: "[1]", topK: -3, topKSet: true}, "--top-k must be at least 1"},
		{"bad return-metadata", vectorizeQueryOpts{vector: "[1]", returnMetadata: "some", returnMetadataSet: true}, "unknown --return-metadata"},
		{"missing vector file", vectorizeQueryOpts{vector: "@/nope/missing.json"}, "read --vector from"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildVectorizeQueryBody(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestBuildVectorizeQueryBodyFilterShapes(t *testing.T) {
	rejected := []struct {
		name   string
		filter string
	}{
		{"null", "null"},
		{"array", `[{"genre":"drama"}]`},
		{"string scalar", `"drama"`},
		{"number scalar", "42"},
		{"bool scalar", "true"},
		{"malformed", `{"genre":`},
	}
	for _, tc := range rejected {
		t.Run("reject "+tc.name, func(t *testing.T) {
			_, err := buildVectorizeQueryBody(vectorizeQueryOpts{vector: "[1]", filter: tc.filter})
			if err == nil || !strings.Contains(err.Error(), "--filter must be a JSON object") {
				t.Fatalf("err = %v, want a --filter object error", err)
			}
		})
	}

	accepted := []struct {
		name   string
		filter string
		want   string
	}{
		{"empty object", `{}`, `{"vector":[1],"filter":{}}`},
		{"valid object", `{"genre":{"$eq":"drama"}}`, `{"vector":[1],"filter":{"genre":{"$eq":"drama"}}}`},
	}
	for _, tc := range accepted {
		t.Run("accept "+tc.name, func(t *testing.T) {
			body, err := buildVectorizeQueryBody(vectorizeQueryOpts{vector: "[1]", filter: tc.filter})
			if err != nil {
				t.Fatal(err)
			}
			vectorizeAssertJSONEqual(t, body, tc.want)
		})
	}
}

func TestBuildVectorizeQueryBodyTopKLimits(t *testing.T) {
	cases := []struct {
		name    string
		opts    vectorizeQueryOpts
		wantErr string
	}{
		// Plain query: 100 neighbors.
		{"100 plain", vectorizeQueryOpts{vector: "[1]", topK: 100, topKSet: true}, ""},
		{"101 plain", vectorizeQueryOpts{vector: "[1]", topK: 101, topKSet: true}, "--top-k must be at most 100"},
		{"1 plain", vectorizeQueryOpts{vector: "[1]", topK: 1, topKSet: true}, ""},

		// Explicit opt-outs stay on the plain limit.
		{"100 with return-values=false", vectorizeQueryOpts{
			vector: "[1]", topK: 100, topKSet: true, returnValues: false, returnValuesSet: true}, ""},
		{"100 with return-metadata=none", vectorizeQueryOpts{
			vector: "[1]", topK: 100, topKSet: true, returnMetadata: "none", returnMetadataSet: true}, ""},
		{"101 with both opt-outs", vectorizeQueryOpts{
			vector: "[1]", topK: 101, topKSet: true, returnValues: false, returnValuesSet: true,
			returnMetadata: "none", returnMetadataSet: true}, "--top-k must be at most 100"},

		// Values or indexed/all metadata: 50 neighbors.
		{"50 with values", vectorizeQueryOpts{
			vector: "[1]", topK: 50, topKSet: true, returnValues: true, returnValuesSet: true}, ""},
		{"51 with values", vectorizeQueryOpts{
			vector: "[1]", topK: 51, topKSet: true, returnValues: true, returnValuesSet: true},
			"at most 50 when --return-values is set or --return-metadata is indexed or all"},
		{"50 with metadata indexed", vectorizeQueryOpts{
			vector: "[1]", topK: 50, topKSet: true, returnMetadata: "indexed", returnMetadataSet: true}, ""},
		{"51 with metadata indexed", vectorizeQueryOpts{
			vector: "[1]", topK: 51, topKSet: true, returnMetadata: "indexed", returnMetadataSet: true}, "at most 50"},
		{"50 with metadata all", vectorizeQueryOpts{
			vector: "[1]", topK: 50, topKSet: true, returnMetadata: "all", returnMetadataSet: true}, ""},
		{"51 with metadata all", vectorizeQueryOpts{
			vector: "[1]", topK: 51, topKSet: true, returnMetadata: "all", returnMetadataSet: true}, "at most 50"},
		{"51 with values and metadata none", vectorizeQueryOpts{
			vector: "[1]", topK: 51, topKSet: true, returnValues: true, returnValuesSet: true,
			returnMetadata: "none", returnMetadataSet: true}, "at most 50"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildVectorizeQueryBody(tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestBuildVectorizeQueryBodyTopKUncheckedWhenUnset(t *testing.T) {
	// topK is only sent when the user set it, so the API default applies and
	// there is nothing to bound.
	body, err := buildVectorizeQueryBody(vectorizeQueryOpts{
		vector: "[1]", topK: 9999, returnValues: true, returnValuesSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"vector":[1],"returnValues":true}`)
}

func TestVectorizeQueryTopKLimitViaCLI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runVectorizeCLI(t, srv.URL,
		"vectorize", "query", "product-embeddings",
		"--vector", "[1]", "--top-k", "51", "--return-values")
	if err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("expected top-k limit error, got %v", err)
	}
}

func TestVectorizeQueryRejectsNullFilterViaCLI(t *testing.T) {
	_, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "query", "product-embeddings",
		"--vector", "[1]", "--filter", "null", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "--filter must be a JSON object") {
		t.Fatalf("expected filter error, got %v", err)
	}
}

// --- metadata index body ---------------------------------------------------

func TestBuildVectorizeMetadataIndexBody(t *testing.T) {
	body, err := buildVectorizeMetadataIndexBody("genre", "string")
	if err != nil {
		t.Fatal(err)
	}
	vectorizeAssertJSONEqual(t, body, `{"propertyName":"genre","indexType":"string"}`)
}

func TestBuildVectorizeMetadataIndexBodyValidation(t *testing.T) {
	if _, err := buildVectorizeMetadataIndexBody("  ", "string"); err == nil || !strings.Contains(err.Error(), "--property") {
		t.Fatalf("expected property error, got %v", err)
	}
	if _, err := buildVectorizeMetadataIndexBody("genre", "date"); err == nil || !strings.Contains(err.Error(), "unknown --type") {
		t.Fatalf("expected type error, got %v", err)
	}
}

// --- @file / @- resolution -------------------------------------------------

func TestVectorizeReadArg(t *testing.T) {
	raw, err := vectorizeReadArg("data", "inline", nil)
	if err != nil || string(raw) != "inline" {
		t.Fatalf("inline = %q, %v", raw, err)
	}

	path := filepath.Join(t.TempDir(), "v.ndjson")
	if err := os.WriteFile(path, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err = vectorizeReadArg("data", "@"+path, nil)
	if err != nil || string(raw) != "from-file" {
		t.Fatalf("file = %q, %v", raw, err)
	}

	raw, err = vectorizeReadArg("data", "@-", strings.NewReader("from-stdin"))
	if err != nil || string(raw) != "from-stdin" {
		t.Fatalf("stdin = %q, %v", raw, err)
	}

	if _, err := vectorizeReadArg("data", "@"+filepath.Join(t.TempDir(), "missing"), nil); err == nil ||
		!strings.Contains(err.Error(), "read --data from") {
		t.Fatalf("expected file error, got %v", err)
	}
}

// --- request construction --------------------------------------------------

func TestVectorizeListDryRun(t *testing.T) {
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid", "vectorize", "list", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	if !strings.HasSuffix(d.URL, "/accounts/"+vectorizeTestAccountID+"/vectorize/v2/indexes") {
		t.Errorf("url = %s", d.URL)
	}
}

func TestVectorizeGetDryRun(t *testing.T) {
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid", "vectorize", "get", "product-embeddings", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if d.Method != "GET" {
		t.Errorf("method = %s", d.Method)
	}
	if !strings.HasSuffix(d.URL, "/vectorize/v2/indexes/product-embeddings") {
		t.Errorf("url = %s", d.URL)
	}
}

func TestVectorizeCreateDryRun(t *testing.T) {
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "create", "product-embeddings",
		"--dimensions", "768",
		"--metric", "cosine",
		"--description", "Product search",
		"--dry-run",
	)
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if d.Method != "POST" {
		t.Errorf("method = %s", d.Method)
	}
	if !strings.HasSuffix(d.URL, "/vectorize/v2/indexes") {
		t.Errorf("url = %s", d.URL)
	}
	vectorizeAssertJSONEqual(t, d.Body, `{"name":"product-embeddings","description":"Product search","config":{"dimensions":768,"metric":"cosine"}}`)
}

func TestVectorizeDeleteDryRun(t *testing.T) {
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "delete", "product-embeddings", "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if d.Method != "DELETE" {
		t.Errorf("method = %s", d.Method)
	}
	if !strings.HasSuffix(d.URL, "/vectorize/v2/indexes/product-embeddings") {
		t.Errorf("url = %s", d.URL)
	}
}

func TestVectorizeDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runVectorizeCLI(t, srv.URL, "vectorize", "delete", "product-embeddings")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestVectorizeMetadataIndexDeleteRequiresForceWithoutTTY(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()

	_, _, err := runVectorizeCLI(t, srv.URL,
		"vectorize", "metadata-index", "delete", "product-embeddings", "--property", "genre")
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("expected force/abort error, got %v", err)
	}
}

func TestVectorizeInsertDryRunSendsNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.ndjson")
	contents := "{\"id\":\"a\",\"values\":[1,2]}\n{\"id\":\"b\",\"values\":[3,4]}\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "insert", "product-embeddings", "--data", "@"+path, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if d.Method != "POST" {
		t.Errorf("method = %s", d.Method)
	}
	if !strings.HasSuffix(d.URL, "/vectorize/v2/indexes/product-embeddings/insert") {
		t.Errorf("url = %s", d.URL)
	}
	if d.Headers["Content-Type"] != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", d.Headers["Content-Type"])
	}
	// A non-JSON body is dumped as a quoted string.
	var body string
	if err := json.Unmarshal(d.Body, &body); err != nil {
		t.Fatalf("dumped body not a string: %v (%s)", err, d.Body)
	}
	if body != contents {
		t.Errorf("body = %q, want %q", body, contents)
	}
}

func TestVectorizeInsertHTTPRequest(t *testing.T) {
	var gotMethod, gotPath, gotContentType, gotQuery string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"mutationId":"m-1"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLIWithStdin(t, srv.URL,
		strings.NewReader("{\"id\":\"a\",\"values\":[1,2]}\n"),
		"vectorize", "insert", "product-embeddings", "--data", "@-")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+vectorizeTestAccountID+"/vectorize/v2/indexes/product-embeddings/insert" {
		t.Errorf("path = %s", gotPath)
	}
	if gotContentType != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", gotContentType)
	}
	if gotQuery != "" {
		t.Errorf("query = %q, want empty when --unparsable-behavior is unset", gotQuery)
	}
	if string(gotBody) != "{\"id\":\"a\",\"values\":[1,2]}\n" {
		t.Errorf("body = %q", gotBody)
	}
	if !strings.Contains(stdout, "m-1") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestVectorizeUpsertHTTPRequestWithUnparsableBehavior(t *testing.T) {
	var gotPath, gotQuery, gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"mutationId":"m-2"}}`))
	}))
	defer srv.Close()

	_, _, err := runVectorizeCLI(t, srv.URL,
		"vectorize", "upsert", "product-embeddings",
		"--data", `[{"id":"a","values":[1,2]}]`,
		"--unparsable-behavior", "discard",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gotPath, "/vectorize/v2/indexes/product-embeddings/upsert") {
		t.Errorf("path = %s", gotPath)
	}
	if gotQuery != "unparsable-behavior=discard" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotContentType != "application/x-ndjson" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
}

func TestVectorizeInsertRejectsUnknownUnparsableBehavior(t *testing.T) {
	_, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "insert", "product-embeddings",
		"--data", `{"id":"a","values":[1]}`,
		"--unparsable-behavior", "skip",
		"--dry-run",
	)
	if err == nil || !strings.Contains(err.Error(), "unknown --unparsable-behavior") {
		t.Fatalf("expected behavior error, got %v", err)
	}
}

func TestVectorizeInsertRequiresData(t *testing.T) {
	_, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "insert", "product-embeddings", "--dry-run")
	if err == nil || !strings.Contains(err.Error(), "data") {
		t.Fatalf("expected required --data error, got %v", err)
	}
}

func TestVectorizeQueryHTTPRequest(t *testing.T) {
	var gotMethod, gotPath, gotContentType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"count":1,"matches":[{"id":"a","score":0.9}]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLI(t, srv.URL,
		"vectorize", "query", "product-embeddings",
		"--vector", "[0.1,0.2]",
		"--top-k", "3",
		"--return-metadata", "indexed",
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+vectorizeTestAccountID+"/vectorize/v2/indexes/product-embeddings/query" {
		t.Errorf("path = %s", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	vectorizeAssertJSONEqual(t, gotBody, `{"vector":[0.1,0.2],"topK":3,"returnMetadata":"indexed"}`)
	if !strings.Contains(stdout, `"matches"`) {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestVectorizeListRendersTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[
			{"name":"product-embeddings","description":"Products","config":{"dimensions":768,"metric":"cosine"},"created_on":"2026-01-02T03:04:05Z"},
			{"name":"docs","config":{"dimensions":1024,"metric":"euclidean"},"created_on":"2026-02-02T03:04:05Z"}
		]}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLI(t, srv.URL, "vectorize", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "DIMENSIONS", "METRIC", "product-embeddings", "768", "cosine", "docs", "euclidean"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestVectorizeListJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"docs","config":{"dimensions":1024,"metric":"cosine"}}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLI(t, srv.URL, "vectorize", "list", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if len(result) != 1 || result[0]["name"] != "docs" {
		t.Errorf("result = %v", result)
	}
}

func TestVectorizeListHonorsQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"name":"docs"},{"name":"products"}]}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLI(t, srv.URL, "vectorize", "list", "--query", ".[].name")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "docs") || !strings.Contains(stdout, "products") {
		t.Errorf("stdout = %s", stdout)
	}
	if strings.Contains(stdout, "NAME") {
		t.Errorf("--query should not render the table: %s", stdout)
	}
}

func TestVectorizeMetadataIndexListRendersTable(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"metadataIndexes":[
			{"propertyName":"genre","indexType":"string"},
			{"propertyName":"price","indexType":"number"}
		]}}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLI(t, srv.URL, "vectorize", "metadata-index", "list", "product-embeddings")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/accounts/"+vectorizeTestAccountID+"/vectorize/v2/indexes/product-embeddings/metadata_index/list" {
		t.Errorf("path = %s", gotPath)
	}
	for _, want := range []string{"PROPERTY", "TYPE", "genre", "string", "price", "number"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("table missing %q\n%s", want, stdout)
		}
	}
}

func TestVectorizeMetadataIndexCreateHTTPRequest(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":{"mutationId":"m-3"}}`))
	}))
	defer srv.Close()

	stdout, _, err := runVectorizeCLI(t, srv.URL,
		"vectorize", "metadata-index", "create", "product-embeddings",
		"--property", "genre", "--type", "string")
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %s", gotMethod)
	}
	if gotPath != "/accounts/"+vectorizeTestAccountID+"/vectorize/v2/indexes/product-embeddings/metadata_index/create" {
		t.Errorf("path = %s", gotPath)
	}
	vectorizeAssertJSONEqual(t, gotBody, `{"propertyName":"genre","indexType":"string"}`)
	if !strings.Contains(stdout, "m-3") {
		t.Errorf("stdout = %s", stdout)
	}
}

func TestVectorizeMetadataIndexDeleteDryRun(t *testing.T) {
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "metadata-index", "delete", "product-embeddings",
		"--property", "genre", "--force", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if d.Method != "POST" {
		t.Errorf("method = %s", d.Method)
	}
	if !strings.HasSuffix(d.URL, "/metadata_index/delete") {
		t.Errorf("url = %s", d.URL)
	}
	vectorizeAssertJSONEqual(t, d.Body, `{"propertyName":"genre"}`)
}

func TestVectorizeEscapesIndexNameInPath(t *testing.T) {
	stdout, _, err := runVectorizeCLI(t, "http://example.invalid",
		"vectorize", "get", "weird name/../x", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	d := vectorizeParseDump(t, stdout)
	if strings.Contains(d.URL, "/../") {
		t.Errorf("index name not escaped: %s", d.URL)
	}
}

func TestVectorizeRequiresAccountID(t *testing.T) {
	t.Setenv("CF_CONFIG_DIR", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CF_ACCOUNT_ID", "")

	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"--base-url", "http://example.invalid", "--token", "t", "vectorize", "list", "--dry-run"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "no account specified") {
		t.Fatalf("expected account error, got %v", err)
	}
}

func TestVectorizeHelpIncludesExamples(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"vectorize", "create", "--help"}, []string{"cf vectorize create", "--dimensions", "--metric", "--preset"}},
		{[]string{"vectorize", "insert", "--help"}, []string{"cf vectorize insert", "@file", "--data", "--unparsable-behavior"}},
		{[]string{"vectorize", "upsert", "--help"}, []string{"cf vectorize upsert", "--data"}},
		{[]string{"vectorize", "query", "--help"}, []string{"cf vectorize query", "--vector", "--top-k", "--filter"}},
		{[]string{"vectorize", "metadata-index", "create", "--help"}, []string{"cf vectorize metadata-index create", "--property", "--type"}},
		{[]string{"vectorize", "delete", "--help"}, []string{"cf vectorize delete", "--force"}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, "-"), func(t *testing.T) {
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

func TestVectorizeCommandsRejectStrayArgs(t *testing.T) {
	cases := [][]string{
		{"vectorize", "list", "extra", "--dry-run"},
		{"vectorize", "get", "idx", "extra", "--dry-run"},
		{"vectorize", "create", "idx", "extra", "--dimensions", "8", "--metric", "cosine", "--dry-run"},
		{"vectorize", "query", "idx", "extra", "--vector", "[1]", "--dry-run"},
		{"vectorize", "metadata-index", "list", "idx", "extra", "--dry-run"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			if _, _, err := runVectorizeCLI(t, "http://example.invalid", args...); err == nil {
				t.Fatal("expected error for stray positional args")
			}
		})
	}
}
