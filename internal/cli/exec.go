package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/TormodHaugland/cf-cli/internal/api"
	"github.com/TormodHaugland/cf-cli/internal/registry"
)

// scope params are filled from --account-id/--zone-id (flag > env > profile)
// instead of per-operation flags.
func isAccountParam(name string) bool {
	return name == "account_id" || name == "account_identifier"
}

func isZoneParam(name string) bool {
	return name == "zone_id" || name == "zone_identifier"
}

func isScopeParam(name string) bool { return isAccountParam(name) || isZoneParam(name) }

// paramValues carries resolved flag values for one operation invocation.
type paramValues struct {
	flagFor map[string]string    // param name -> flag name
	str     map[string]*string   // scalar params
	arr     map[string]*[]string // array params
	changed func(flag string) bool
}

// buildRequest substitutes path parameters and collects query parameters
// into a concrete api.Request. It fails loudly on any unresolved template.
func buildRequest(op registry.Operation, accountID, zoneID string, vals paramValues) (api.Request, error) {
	path := op.Path
	q := url.Values{}
	for _, p := range op.Params {
		switch p.In {
		case "path":
			var v string
			if isAccountParam(p.Name) {
				v = accountID
				if v == "" {
					return api.Request{}, errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
				}
			} else if isZoneParam(p.Name) {
				v = zoneID
				if v == "" {
					return api.Request{}, errors.New("missing zone ID: pass --zone-id, set CLOUDFLARE_ZONE_ID, or configure a profile")
				}
			} else {
				if ptr := vals.str[p.Name]; ptr != nil {
					v = *ptr
				}
				if v == "" {
					return api.Request{}, fmt.Errorf("missing required flag --%s", vals.flagFor[p.Name])
				}
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(v))
		case "query":
			if p.Type == "array" {
				if ptr := vals.arr[p.Name]; ptr != nil {
					for _, v := range *ptr {
						q.Add(p.Name, v)
					}
				}
			} else if vals.changed != nil && vals.changed(vals.flagFor[p.Name]) {
				q.Add(p.Name, *vals.str[p.Name])
			}
		}
	}
	if i := strings.IndexByte(path, '{'); i >= 0 {
		return api.Request{}, fmt.Errorf("internal error: unresolved path parameter in %s (registry out of date?)", path)
	}
	return api.Request{Method: op.Method, Path: path, Query: q}, nil
}

// buildBody assembles a JSON request body from --data or repeated --field
// key=value pairs (values parsed as JSON when possible, dots nest).
func buildBody(data string, fields []string, stdin io.Reader) ([]byte, error) {
	if data != "" && len(fields) > 0 {
		return nil, errors.New("--data and --field are mutually exclusive")
	}
	if data != "" {
		var raw []byte
		var err error
		switch {
		case data == "@-":
			raw, err = io.ReadAll(stdin)
		case strings.HasPrefix(data, "@"):
			raw, err = os.ReadFile(strings.TrimPrefix(data, "@"))
		default:
			raw = []byte(data)
		}
		if err != nil {
			return nil, err
		}
		if !json.Valid(raw) {
			return nil, errors.New("--data is not valid JSON")
		}
		return raw, nil
	}
	if len(fields) == 0 {
		return nil, nil
	}
	obj := map[string]any{}
	for _, f := range fields {
		k, v, ok := strings.Cut(f, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("invalid --field %q (expected key=value)", f)
		}
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			parsed = v // plain string
		}
		setDotted(obj, k, parsed)
	}
	return json.Marshal(obj)
}

// setDotted sets obj["a"]["b"] = val for key "a.b", creating maps as needed.
func setDotted(obj map[string]any, key string, val any) {
	parts := strings.Split(key, ".")
	m := obj
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = val
}
