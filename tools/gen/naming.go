package main

import "strings"

// The command taxonomy is derived from URL paths, not spec tags: Cloudflare's
// tags are inconsistent (529 of them, with duplicates), while paths are
// uniform. The first path segment after the /accounts/{id} or /zones/{id}
// scope prefix names the product group; the rest of the path plus the HTTP
// method names the operation.

type mapping struct {
	// Overrides maps "scope:segment" (scope is account|zone|root) or a bare
	// segment to a product name, replacing the default slugification.
	Overrides map[string]string `yaml:"overrides"`
	// OpOverrides maps "METHOD /path" to an explicit product/name for
	// endpoints where derivation produces a bad command (e.g. purge_cache).
	OpOverrides map[string]opOverride `yaml:"op_overrides"`
}

type opOverride struct {
	Product string `yaml:"product"`
	Name    string `yaml:"name"`
}

type derived struct {
	product string
	name    string
	scope   string // account | zone | root — used for collision resolution
}

func isParamSeg(s string) bool { return strings.HasPrefix(s, "{") }

func slugSeg(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}

// deriveCommand maps an HTTP method + path template to (product, name).
// hasGetSibling reports whether the same path also supports GET: a POST to a
// trailing non-param segment is only treated as an action verb (import,
// batch, verify, ...) when there is no GET sibling — collections that
// support both GET and POST are ordinary create endpoints.
func deriveCommand(method, path string, hasGetSibling bool, m mapping) derived {
	methodUpper := strings.ToUpper(method)
	segs := strings.Split(strings.Trim(path, "/"), "/")

	scope := "root"
	rest := segs
	if len(segs) >= 2 && isParamSeg(segs[1]) && (segs[0] == "accounts" || segs[0] == "zones") {
		if segs[0] == "accounts" {
			scope = "account"
		} else {
			scope = "zone"
		}
		rest = segs[2:]
	}
	productSeg := segs[0]
	if len(rest) > 0 {
		productSeg = rest[0]
		rest = rest[1:]
	} else {
		rest = nil
	}

	product := slugSeg(productSeg)
	if v, ok := m.Overrides[scope+":"+productSeg]; ok {
		product = v
	} else if v, ok := m.Overrides[productSeg]; ok {
		product = v
	}

	if o, ok := m.OpOverrides[methodUpper+" "+path]; ok {
		d := derived{product: product, name: o.Name, scope: scope}
		if o.Product != "" {
			d.product = o.Product
		}
		return d
	}

	endsWithParam := isParamSeg(segs[len(segs)-1])
	var verb string
	switch methodUpper {
	case "GET":
		if endsWithParam {
			verb = "get"
		} else {
			verb = "list"
		}
	case "POST":
		verb = "create"
	case "PUT":
		verb = "replace"
	case "PATCH":
		verb = "update"
	case "DELETE":
		verb = "delete"
	default:
		verb = strings.ToLower(methodUpper)
	}

	// POST to a trailing action segment reads as the action itself:
	// POST .../dns_records/import -> "import", not "import-create".
	if methodUpper == "POST" && !hasGetSibling && len(rest) > 0 && !isParamSeg(rest[len(rest)-1]) {
		verb = slugSeg(rest[len(rest)-1])
		rest = rest[:len(rest)-1]
	}

	var nouns []string
	for _, s := range rest {
		if !isParamSeg(s) {
			nouns = append(nouns, slugSeg(s))
		}
	}
	name := verb
	if len(nouns) > 0 {
		name = strings.Join(nouns, "-") + "-" + verb
	}
	return derived{product: product, name: name, scope: scope}
}
