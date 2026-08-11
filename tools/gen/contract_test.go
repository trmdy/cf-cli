package main

// Contract tests: verify the embedded registry against the OpenAPI spec on
// disk. This is the offline correctness gate — every generated command must
// correspond to a real endpoint with the right parameters. Skipped when the
// spec is absent (fresh clone); run `make spec gen test` for the full check.
// A failure here usually means the spec moved: re-run `make gen`.

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/trmdy/cf-cli/internal/registry"
)

func loadSpecForTest(t *testing.T) *spec {
	t.Helper()
	path := os.Getenv("CF_OPENAPI_SPEC")
	if path == "" {
		path = "../../specs/openapi.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("spec not available (%v); run `make spec` to enable contract tests", err)
	}
	var doc spec
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	return &doc
}

func TestRegistryMatchesSpec(t *testing.T) {
	doc := loadSpecForTest(t)
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("load embedded registry: %v", err)
	}

	// Every registry operation must exist in the spec with matching params.
	for _, op := range reg.Operations {
		item, ok := doc.Paths[op.Path]
		if !ok {
			t.Errorf("%s %s: path missing from spec (stale registry? run `make gen`)", op.Method, op.Path)
			continue
		}
		raw, ok := item[strings.ToLower(op.Method)]
		if !ok {
			t.Errorf("%s %s: method missing from spec", op.Method, op.Path)
			continue
		}

		var shared []rawParam
		if sp, ok := item["parameters"]; ok {
			_ = json.Unmarshal(sp, &shared)
		}
		var ro rawOp
		if err := json.Unmarshal(raw, &ro); err != nil {
			t.Errorf("%s %s: cannot parse spec operation: %v", op.Method, op.Path, err)
			continue
		}
		specParams := buildParams(doc, op.Path, shared, ro.Parameters)
		specByKey := map[string]registry.Param{}
		for _, p := range specParams {
			specByKey[p.In+":"+p.Name] = p
		}
		opByKey := map[string]registry.Param{}
		for _, p := range op.Params {
			opByKey[p.In+":"+p.Name] = p
		}
		for k, sp := range specByKey {
			rp, ok := opByKey[k]
			if !ok {
				t.Errorf("%s %s: spec param %s missing from registry", op.Method, op.Path, k)
				continue
			}
			if sp.Required && !rp.Required {
				t.Errorf("%s %s: param %s should be required", op.Method, op.Path, k)
			}
		}
		for k := range opByKey {
			if _, ok := specByKey[k]; !ok {
				t.Errorf("%s %s: registry param %s not in spec", op.Method, op.Path, k)
			}
		}

		// Every {placeholder} in the path must be covered by a path param.
		for _, m := range pathParamRe.FindAllStringSubmatch(op.Path, -1) {
			if _, ok := opByKey["path:"+m[1]]; !ok {
				t.Errorf("%s %s: path placeholder {%s} has no registry param", op.Method, op.Path, m[1])
			}
		}
	}

	// Every spec operation must be present in the registry (full coverage).
	total := 0
	inRegistry := map[string]bool{}
	for _, op := range reg.Operations {
		inRegistry[op.Method+" "+op.Path] = true
	}
	for path, item := range doc.Paths {
		for _, method := range httpMethods {
			if _, ok := item[method]; !ok {
				continue
			}
			total++
			if !inRegistry[strings.ToUpper(method)+" "+path] {
				t.Errorf("spec operation %s %s missing from registry", strings.ToUpper(method), path)
			}
		}
	}
	if len(reg.Operations) != total {
		t.Errorf("registry has %d operations, spec has %d", len(reg.Operations), total)
	}
}

func TestRegistryCommandNamesUnique(t *testing.T) {
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("load embedded registry: %v", err)
	}
	seen := map[string]string{}
	for _, op := range reg.Operations {
		key := op.Product + " " + op.Name
		if op.Name == "" {
			t.Errorf("%s %s: empty command name", op.Method, op.Path)
		}
		if prev, ok := seen[key]; ok {
			t.Errorf("duplicate command %q: %s and %s %s", key, prev, op.Method, op.Path)
		}
		seen[key] = op.Method + " " + op.Path
	}
}
