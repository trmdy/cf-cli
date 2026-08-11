package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{"json": JSON, "yaml": YAML, "yml": YAML, "table": Table} {
		got, err := ParseFormat(in)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("expected error for unknown format")
	}
}

func TestRenderRawJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderRaw(&buf, JSON, json.RawMessage(`{"b":1,"a":2}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\"b\": 1") {
		t.Errorf("unexpected output: %s", buf.String())
	}
}

func TestRenderRawYAML(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderRaw(&buf, YAML, json.RawMessage(`{"name":"x","count":3}`)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "name: x") || !strings.Contains(out, "count: 3") {
		t.Errorf("unexpected yaml: %s", out)
	}
}

func TestRenderRawTable(t *testing.T) {
	var buf bytes.Buffer
	raw := json.RawMessage(`[{"id":"1","name":"a","nested":{"x":1}},{"id":"2","name":"b"}]`)
	if err := RenderRaw(&buf, Table, raw); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "ID") || !strings.Contains(out, "NAME") {
		t.Errorf("headers wrong: %s", out)
	}
	if strings.Contains(out, "NESTED") {
		t.Errorf("nested column should be dropped: %s", out)
	}
	if len(strings.Split(strings.TrimSpace(out), "\n")) != 3 {
		t.Errorf("expected header + 2 rows: %s", out)
	}
}

func TestRenderRawTableFallsBackToJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderRaw(&buf, Table, json.RawMessage(`{"id":"1"}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\"id\": \"1\"") {
		t.Errorf("expected JSON fallback: %s", buf.String())
	}
}

func TestCell(t *testing.T) {
	if Cell(float64(42)) != "42" {
		t.Errorf("int-ish float: %s", Cell(float64(42)))
	}
	if Cell(true) != "true" {
		t.Errorf("bool: %s", Cell(true))
	}
	long := strings.Repeat("x", 100)
	if got := Cell(long); len(got) != 60 || !strings.HasSuffix(got, "...") {
		t.Errorf("truncation: %q (len %d)", got, len(got))
	}
	if Cell(nil) != "" {
		t.Error("nil should be empty")
	}
}

func TestApplyQuery(t *testing.T) {
	raw := json.RawMessage(`[{"id":"a","n":1},{"id":"b","n":2}]`)
	got, err := ApplyQuery(raw, ".[].id")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `["a","b"]` {
		t.Errorf("multi-output collect: %s", got)
	}
	got, err = ApplyQuery(raw, "length")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "2" {
		t.Errorf("single output: %s", got)
	}
	if _, err := ApplyQuery(raw, ".[foo"); err == nil {
		t.Error("expected parse error")
	}
}
