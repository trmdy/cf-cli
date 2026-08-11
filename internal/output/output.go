// Package output renders command results as json, yaml, or a table.
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

type Format string

const (
	JSON  Format = "json"
	YAML  Format = "yaml"
	Table Format = "table"
)

// ParseFormat validates a --output value. Empty means "let the command pick".
func ParseFormat(s string) (Format, error) {
	switch s {
	case "json":
		return JSON, nil
	case "yaml", "yml":
		return YAML, nil
	case "table":
		return Table, nil
	}
	return "", fmt.Errorf("unknown output format %q (expected json, yaml, or table)", s)
}

// Render writes v in the given format (Table is not supported here; use
// RenderTable with explicit columns).
func Render(w io.Writer, f Format, v any) error {
	switch f {
	case YAML:
		enc := yaml.NewEncoder(w)
		enc.SetIndent(2)
		if err := enc.Encode(v); err != nil {
			return err
		}
		return enc.Close()
	default:
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
}

// RenderRaw writes an already-encoded JSON value in the given format. For
// Table it attempts a generic table over an array of objects and falls back
// to JSON otherwise.
func RenderRaw(w io.Writer, f Format, raw json.RawMessage) error {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	switch f {
	case YAML:
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return Render(w, YAML, v)
	case Table:
		if headers, rows, ok := genericTable(raw); ok {
			return RenderTable(w, headers, rows)
		}
		fallthrough
	default:
		var buf bytes.Buffer
		if err := json.Indent(&buf, raw, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
		_, err := w.Write(buf.Bytes())
		return err
	}
}

// RenderTable writes an aligned table.
func RenderTable(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	return tw.Flush()
}

// genericTable builds a best-effort table from an array of flat objects,
// using the scalar keys of the first element as columns.
func genericTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
		return nil, nil, false
	}
	var cols []string
	for k, v := range items[0] {
		switch v.(type) {
		case map[string]any, []any:
		default:
			cols = append(cols, k)
		}
	}
	if len(cols) == 0 {
		return nil, nil, false
	}
	sort.Strings(cols)
	// id and name first when present, for scanability
	prio := func(c string) int {
		switch c {
		case "id":
			return 0
		case "name":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(cols, func(i, j int) bool { return prio(cols[i]) < prio(cols[j]) })
	if len(cols) > 8 {
		cols = cols[:8]
	}
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = strings.ToUpper(c)
	}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		row := make([]string, len(cols))
		for i, c := range cols {
			row[i] = Cell(it[c])
		}
		rows = append(rows, row)
	}
	return headers, rows, true
}

// Cell formats a single scalar value for table display.
func Cell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		if len(x) > 60 {
			return x[:57] + "..."
		}
		return x
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	case bool:
		return fmt.Sprintf("%t", x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}
