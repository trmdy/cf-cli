// Package registry holds the generated index of every Cloudflare API
// operation. The index is produced by tools/gen from Cloudflare's published
// OpenAPI spec and embedded into the binary, so the full `cf api` command
// tree works offline with no spec file present.
package registry

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"sync"
)

//go:embed data/registry.json.gz
var dataFS embed.FS

// Param describes a single path or query parameter of an operation.
type Param struct {
	Name        string   `json:"name"`
	In          string   `json:"in"` // "path" or "query"
	Type        string   `json:"type,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Operation is one endpoint from the Cloudflare API, addressed as
// `cf api <product> <name>`.
type Operation struct {
	ID         string  `json:"id"`
	Product    string  `json:"product"`
	Name       string  `json:"name"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Summary    string  `json:"summary,omitempty"`
	Params     []Param `json:"params,omitempty"`
	HasBody    bool    `json:"has_body,omitempty"`
	Deprecated bool    `json:"deprecated,omitempty"`
}

type Registry struct {
	SpecVersion string      `json:"spec_version"`
	Operations  []Operation `json:"operations"`

	byProduct map[string][]Operation
	products  []string
}

var (
	loadOnce sync.Once
	loaded   *Registry
	loadErr  error
)

// Load returns the embedded registry, decoded once per process.
func Load() (*Registry, error) {
	loadOnce.Do(func() {
		f, err := dataFS.Open("data/registry.json.gz")
		if err != nil {
			loadErr = fmt.Errorf("open embedded registry: %w", err)
			return
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			loadErr = fmt.Errorf("decompress registry: %w", err)
			return
		}
		defer zr.Close()
		data, err := io.ReadAll(zr)
		if err != nil {
			loadErr = fmt.Errorf("read registry: %w", err)
			return
		}
		var reg Registry
		if err := json.Unmarshal(data, &reg); err != nil {
			loadErr = fmt.Errorf("parse registry: %w", err)
			return
		}
		reg.buildIndex()
		loaded = &reg
	})
	return loaded, loadErr
}

func (r *Registry) buildIndex() {
	r.byProduct = make(map[string][]Operation)
	for _, op := range r.Operations {
		r.byProduct[op.Product] = append(r.byProduct[op.Product], op)
	}
	r.products = make([]string, 0, len(r.byProduct))
	for p := range r.byProduct {
		r.products = append(r.products, p)
	}
	sort.Strings(r.products)
	for _, ops := range r.byProduct {
		sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	}
}

// Products returns all product group names, sorted.
func (r *Registry) Products() []string { return r.products }

// ByProduct returns the operations of one product group, sorted by name.
func (r *Registry) ByProduct(name string) []Operation { return r.byProduct[name] }
