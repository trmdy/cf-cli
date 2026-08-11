// Command gen compiles Cloudflare's OpenAPI spec into the compact operation
// registry embedded in the cf binary.
//
// Usage:
//
//	go run ./tools/gen -spec specs/openapi.json -mapping tools/gen/mapping.yaml \
//	  -out internal/registry/data/registry.json.gz -products docs/generated/products.md
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/trmdy/cf-cli/internal/registry"
)

type rawSchema struct {
	Ref  string `json:"$ref"`
	Type string `json:"type"`
	Enum []any  `json:"enum"`
}

type rawParam struct {
	Ref         string          `json:"$ref"`
	Name        string          `json:"name"`
	In          string          `json:"in"`
	Required    bool            `json:"required"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
}

type rawOp struct {
	OperationID string          `json:"operationId"`
	Summary     string          `json:"summary"`
	Deprecated  bool            `json:"deprecated"`
	Parameters  []rawParam      `json:"parameters"`
	RequestBody json.RawMessage `json:"requestBody"`
}

type spec struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Parameters map[string]rawParam        `json:"parameters"`
		Schemas    map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`
}

var httpMethods = []string{"get", "post", "put", "patch", "delete"}

var pathParamRe = regexp.MustCompile(`\{([^}]+)\}`)

func main() {
	specPath := flag.String("spec", "specs/openapi.json", "path to Cloudflare openapi.json")
	mappingPath := flag.String("mapping", "tools/gen/mapping.yaml", "path to product mapping")
	outPath := flag.String("out", "internal/registry/data/registry.json.gz", "output registry path")
	productsPath := flag.String("products", "", "optional path for a product summary report (markdown)")
	flag.Parse()

	if err := run(*specPath, *mappingPath, *outPath, *productsPath); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(specPath, mappingPath, outPath, productsPath string) error {
	specData, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("read spec (run `make spec` first?): %w", err)
	}
	var doc spec
	if err := json.Unmarshal(specData, &doc); err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}

	var m mapping
	mapData, err := os.ReadFile(mappingPath)
	if err != nil {
		return fmt.Errorf("read mapping: %w", err)
	}
	if err := yaml.Unmarshal(mapData, &m); err != nil {
		return fmt.Errorf("parse mapping: %w", err)
	}

	ops, collisions := buildOperations(&doc, m)

	reg := registry.Registry{SpecVersion: doc.Info.Version, Operations: ops}
	if err := writeRegistry(outPath, reg); err != nil {
		return err
	}

	products := map[string]int{}
	for _, op := range ops {
		products[op.Product]++
	}
	fmt.Printf("gen: %d operations across %d products (spec %s)\n", len(ops), len(products), doc.Info.Version)
	if collisions > 0 {
		fmt.Printf("gen: %d name collisions resolved with scope/method suffixes\n", collisions)
	}

	if productsPath != "" {
		if err := writeProductReport(productsPath, doc.Info.Version, ops); err != nil {
			return err
		}
	}
	return nil
}

func buildOperations(doc *spec, m mapping) ([]registry.Operation, int) {
	var ops []registry.Operation
	scopes := map[string]string{} // op index key -> scope, for collision resolution

	paths := make([]string, 0, len(doc.Paths))
	for p := range doc.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		item := doc.Paths[path]
		var shared []rawParam
		if raw, ok := item["parameters"]; ok {
			_ = json.Unmarshal(raw, &shared)
		}
		for _, method := range httpMethods {
			raw, ok := item[method]
			if !ok {
				continue
			}
			var ro rawOp
			if err := json.Unmarshal(raw, &ro); err != nil {
				continue
			}
			_, hasGet := item["get"]
			d := deriveCommand(method, path, hasGet, m)
			op := registry.Operation{
				ID:         ro.OperationID,
				Product:    d.product,
				Name:       d.name,
				Method:     strings.ToUpper(method),
				Path:       path,
				Summary:    firstLine(ro.Summary, 120),
				Params:     buildParams(doc, path, shared, ro.Parameters),
				HasBody:    len(ro.RequestBody) > 0,
				Deprecated: ro.Deprecated,
			}
			if op.ID == "" {
				op.ID = op.Method + " " + op.Path
			}
			ops = append(ops, op)
			scopes[opKey(op)] = d.scope
		}
	}

	collisions := resolveCollisions(ops, scopes)

	sort.Slice(ops, func(i, j int) bool {
		a, b := ops[i], ops[j]
		if a.Product != b.Product {
			return a.Product < b.Product
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Method < b.Method
	})
	return ops, collisions
}

func opKey(op registry.Operation) string { return op.Method + " " + op.Path }

// resolveCollisions disambiguates operations that derived the same
// product+name: first by scope (account/zone variants of the same resource),
// then by HTTP method, then by a positional index. Returns how many
// operations were renamed.
func resolveCollisions(ops []registry.Operation, scopes map[string]string) int {
	renamed := 0
	for pass := 0; pass < 3; pass++ {
		groups := map[string][]int{}
		for i, op := range ops {
			k := op.Product + " " + op.Name
			groups[k] = append(groups[k], i)
		}
		anyCollision := false
		for _, idxs := range groups {
			if len(idxs) < 2 {
				continue
			}
			anyCollision = true
			switch pass {
			case 0: // scope suffix, only when scopes actually differ
				seen := map[string]bool{}
				differ := false
				for _, i := range idxs {
					s := scopes[opKey(ops[i])]
					if seen[s] {
						continue
					}
					if len(seen) > 0 {
						differ = true
					}
					seen[s] = true
				}
				if differ {
					for _, i := range idxs {
						ops[i].Name += "-" + scopes[opKey(ops[i])]
						renamed++
					}
				}
			case 1: // method suffix
				methods := map[string]int{}
				for _, i := range idxs {
					methods[ops[i].Method]++
				}
				if len(methods) > 1 {
					for _, i := range idxs {
						ops[i].Name += "-" + strings.ToLower(ops[i].Method)
						renamed++
					}
				}
			case 2: // last resort: positional suffix on all but the first
				sort.Slice(idxs, func(a, b int) bool { return ops[idxs[a]].Path < ops[idxs[b]].Path })
				for n, i := range idxs[1:] {
					ops[i].Name += fmt.Sprintf("-%d", n+2)
					renamed++
				}
			}
		}
		if !anyCollision {
			break
		}
	}
	return renamed
}

func buildParams(doc *spec, path string, shared, own []rawParam) []registry.Param {
	merged := map[string]registry.Param{} // key: in+name
	order := []string{}
	add := func(rp rawParam) {
		p, ok := resolveParam(doc, rp)
		if !ok {
			return
		}
		k := p.In + ":" + p.Name
		if _, exists := merged[k]; !exists {
			order = append(order, k)
		}
		merged[k] = p
	}
	for _, rp := range shared {
		add(rp)
	}
	for _, rp := range own {
		add(rp) // op-level params override shared ones
	}

	// Safety net: some paths template parameters that the spec forgets to
	// declare. Synthesize them so URL building can never leave a brace.
	for _, mtch := range pathParamRe.FindAllStringSubmatch(path, -1) {
		name := mtch[1]
		k := "path:" + name
		if _, ok := merged[k]; !ok {
			merged[k] = registry.Param{Name: name, In: "path", Type: "string", Required: true}
			order = append(order, k)
		}
	}

	out := make([]registry.Param, 0, len(order))
	for _, k := range order {
		out = append(out, merged[k])
	}
	// path params first, then query, stable by name within each
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].In != out[j].In {
			return out[i].In == "path"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func resolveParam(doc *spec, rp rawParam) (registry.Param, bool) {
	if rp.Ref != "" {
		key := rp.Ref[strings.LastIndexByte(rp.Ref, '/')+1:]
		resolved, ok := doc.Components.Parameters[key]
		if !ok {
			return registry.Param{}, false
		}
		rp = resolved
	}
	if rp.Name == "" || (rp.In != "path" && rp.In != "query") {
		return registry.Param{}, false
	}
	p := registry.Param{
		Name:        rp.Name,
		In:          rp.In,
		Required:    rp.Required || rp.In == "path",
		Description: firstLine(rp.Description, 160),
		Type:        "string",
	}
	if len(rp.Schema) > 0 {
		p.Type, p.Enum = schemaType(doc, rp.Schema, 0)
	}
	return p, true
}

func schemaType(doc *spec, raw json.RawMessage, depth int) (string, []string) {
	if depth > 3 {
		return "string", nil
	}
	var s rawSchema
	if err := json.Unmarshal(raw, &s); err != nil {
		return "string", nil
	}
	if s.Ref != "" {
		key := s.Ref[strings.LastIndexByte(s.Ref, '/')+1:]
		if target, ok := doc.Components.Schemas[key]; ok {
			return schemaType(doc, target, depth+1)
		}
		return "string", nil
	}
	var enum []string
	for _, e := range s.Enum {
		enum = append(enum, fmt.Sprint(e))
	}
	switch s.Type {
	case "integer", "number", "boolean", "array":
		return s.Type, enum
	default:
		return "string", enum
	}
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max-3] + "..."
	}
	return s
}

func writeRegistry(path string, reg registry.Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := zw.Write(data); err != nil {
		return err
	}
	return zw.Close()
}

func writeProductReport(path, specVersion string, ops []registry.Operation) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	products := map[string]int{}
	for _, op := range ops {
		products[op.Product]++
	}
	names := make([]string, 0, len(products))
	for p := range products {
		names = append(names, p)
	}
	sort.Strings(names)

	var b strings.Builder
	fmt.Fprintf(&b, "# Generated product groups\n\n")
	fmt.Fprintf(&b, "Spec version %s — %d operations across %d products.\n\n", specVersion, len(ops), len(names))
	fmt.Fprintf(&b, "This file is generated by tools/gen; it is the sharding unit for parallel\nporcelain development (one agent per product).\n\n")
	fmt.Fprintf(&b, "| Product | Operations |\n|---|---|\n")
	for _, p := range names {
		fmt.Fprintf(&b, "| `%s` | %d |\n", p, products[p])
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
