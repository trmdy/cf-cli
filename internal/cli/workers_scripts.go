package cli

// Workers scripts porcelain: script lifecycle (list/get/upload/download/
// delete), script secrets, and the per-script workers.dev subdomain.
// Uploads and content downloads stream through api.Client.DoStream because
// Worker bundles (wasm modules, source maps) are routinely large.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// workersScriptsScript is the subset of a Worker's list entry we tabulate.
// The API's "id" field is the script name.
type workersScriptsScript struct {
	ID         string   `json:"id,omitempty"`
	CreatedOn  string   `json:"created_on,omitempty"`
	ModifiedOn string   `json:"modified_on,omitempty"`
	UsageModel string   `json:"usage_model,omitempty"`
	Handlers   []string `json:"handlers,omitempty"`
}

type workersScriptsSecret struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

func newWorkersScriptsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "script",
		Short: "Manage Worker scripts, secrets, and workers.dev subdomains",
	}
	cmd.AddCommand(
		newWorkersScriptsListCmd(g),
		newWorkersScriptsGetCmd(g),
		newWorkersScriptsUploadCmd(g),
		newWorkersScriptsDownloadCmd(g),
		newWorkersScriptsDeleteCmd(g),
		newWorkersScriptsSecretCmd(g),
		newWorkersScriptsSubdomainCmd(g),
	)
	return cmd
}

// ---------------------------------------------------------------------------
// paths and shared validation

func workersScriptsBasePath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/workers/scripts"
}

func workersScriptsScriptPath(accountID, script string) string {
	return workersScriptsBasePath(accountID) + "/" + url.PathEscape(script)
}

func workersScriptsSettingsPath(accountID, script string) string {
	return workersScriptsScriptPath(accountID, script) + "/settings"
}

func workersScriptsContentPath(accountID, script string) string {
	return workersScriptsScriptPath(accountID, script) + "/content/v2"
}

func workersScriptsSecretsPath(accountID, script string) string {
	return workersScriptsScriptPath(accountID, script) + "/secrets"
}

func workersScriptsSecretPath(accountID, script, secret string) string {
	return workersScriptsSecretsPath(accountID, script) + "/" + url.PathEscape(secret)
}

func workersScriptsSubdomainPath(accountID, script string) string {
	return workersScriptsScriptPath(accountID, script) + "/subdomain"
}

func workersScriptsAccountID(accountID string) (string, error) {
	if accountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

// workersScriptsValidateName checks a Worker script name locally so a typo
// never becomes a request against a mangled path.
func workersScriptsValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("script name cannot be empty")
	}
	if strings.ContainsAny(name, "/?#") {
		return "", fmt.Errorf("invalid script name %q: names cannot contain '/', '?', or '#'", name)
	}
	return name, nil
}

func workersScriptsValidateSecretName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("secret name cannot be empty")
	}
	if strings.ContainsAny(name, "/?#") {
		return "", fmt.Errorf("invalid secret name %q: names cannot contain '/', '?', or '#'", name)
	}
	return name, nil
}

// ---------------------------------------------------------------------------
// list

func newWorkersScriptsListCmd(g *globalOpts) *cobra.Command {
	var tags []string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Workers in the account",
		Long: `List the Workers uploaded to the account. The API's "id" field is the
script name.

Tag filters use the API's <tag>:yes / <tag>:no grammar, where "yes" keeps
scripts carrying the tag and "no" excludes them.

Examples:

  cf workers script list
  cf workers script list --tag team:yes
  cf workers script list --tag team:yes --tag deprecated:no
  cf workers script list --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := workersScriptsListQuery(tags)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersScriptsBasePath(accountID), Query: q}
			if g.DryRun {
				return runWorkersScriptsRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var scripts []workersScriptsScript
			if err := json.Unmarshal(env.Result, &scripts); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(scripts))
			for _, s := range scripts {
				rows = append(rows, []string{
					s.ID,
					s.CreatedOn,
					s.ModifiedOn,
					s.UsageModel,
					output.Cell(strings.Join(s.Handlers, ",")),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "CREATED_ON", "MODIFIED_ON", "USAGE_MODEL", "HANDLERS"}, rows)
		},
	}
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "filter by tag as <tag>:yes or <tag>:no (repeatable)")
	return cmd
}

// workersScriptsListQuery turns repeated --tag values into the API's single
// comma-separated "tags" parameter, rejecting anything outside its grammar.
func workersScriptsListQuery(tags []string) (url.Values, error) {
	q := url.Values{}
	if len(tags) == 0 {
		return q, nil
	}
	pairs := make([]string, 0, len(tags))
	for _, raw := range tags {
		t := strings.TrimSpace(raw)
		i := strings.LastIndex(t, ":")
		if i <= 0 {
			return nil, fmt.Errorf("invalid --tag %q: expected <tag>:yes or <tag>:no", raw)
		}
		name, allowed := t[:i], t[i+1:]
		if name == "" {
			return nil, fmt.Errorf("invalid --tag %q: expected <tag>:yes or <tag>:no", raw)
		}
		if allowed != "yes" && allowed != "no" {
			return nil, fmt.Errorf("invalid --tag %q: the value after ':' must be yes or no", raw)
		}
		if strings.Contains(name, ",") {
			return nil, fmt.Errorf("invalid --tag %q: tag names cannot contain ','", raw)
		}
		pairs = append(pairs, name+":"+allowed)
	}
	q.Set("tags", strings.Join(pairs, ","))
	return q, nil
}

// ---------------------------------------------------------------------------
// get

func newWorkersScriptsGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <script>",
		Short: "Show a Worker's settings and bindings",
		Long: `Show the settings for one Worker: bindings, compatibility date and flags,
placement, tags, and observability.

The Workers API has no single-script metadata endpoint — GET on the script
itself returns the code — so this reads the script's settings. Use
"cf workers script download" for the code.

Examples:

  cf workers script get my-worker
  cf workers script get my-worker --query .bindings`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersScriptsSettingsPath(accountID, script)}
			return runWorkersScriptsRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// upload

// workersScriptsModule is one part of a modules-format Worker upload: the
// module name the runtime sees, its wire content type, and the local file.
type workersScriptsModule struct {
	Name        string
	ContentType string
	Path        string
}

// workersScriptsUpload is the fully-validated upload body: the metadata JSON
// object plus the module parts, in declaration order.
type workersScriptsUpload struct {
	Metadata []byte
	Modules  []workersScriptsModule
}

// workersScriptsModuleTypes maps the CLI's module type names to the exact
// content types the Workers upload API uses to classify each part.
var workersScriptsModuleTypes = map[string]string{
	"esm":                "application/javascript+module",
	"commonjs":           "application/javascript",
	"text":               "text/plain",
	"data":               "application/octet-stream",
	"wasm":               "application/wasm",
	"python":             "text/x-python",
	"python-requirement": "text/x-python-requirement",
	"sourcemap":          "application/source-map",
}

// workersScriptsExtTypes infers a module type from the module name's
// extension. ".js" is treated as an ES module because that is the modules
// format this command uploads; pass --module-type for CommonJS.
var workersScriptsExtTypes = map[string]string{
	".js":   "esm",
	".mjs":  "esm",
	".cjs":  "commonjs",
	".wasm": "wasm",
	".txt":  "text",
	".html": "text",
	".bin":  "data",
	".py":   "python",
	".map":  "sourcemap",
}

func workersScriptsModuleTypeNames() []string {
	names := make([]string, 0, len(workersScriptsModuleTypes))
	for name := range workersScriptsModuleTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func newWorkersScriptsUploadCmd(g *globalOpts) *cobra.Command {
	var modules, moduleTypes, compatFlags, keepBindings []string
	var mainModule, metadata, bindings, compatDate string
	var logpush bool
	cmd := &cobra.Command{
		Use:   "upload <script>",
		Short: "Upload a Worker from local module files",
		Long: `Upload (create or replace) a Worker in the modules format. The request is a
multipart/form-data body: a "metadata" part holding the upload metadata JSON,
followed by one part per module, named after the module and typed with the
content type the Workers runtime uses to classify it.

Module types are inferred from the file extension (.js/.mjs esm, .cjs
commonjs, .wasm wasm, .txt/.html text, .bin data, .py python, .map
sourcemap) and can be set explicitly with --module-type <module>=<type>.
Types: ` + strings.Join(workersScriptsModuleTypeNames(), ", ") + `.

--module takes [name=]path; without an explicit name the module is named
after the file. The entry point is --main-module, and it must match one of
the uploaded module names; with exactly one module it defaults to that
module.

An upload replaces the Worker: bindings and settings that are not part of
this request are dropped. Pass --keep-bindings secret_text to preserve
existing secrets, and --metadata for metadata fields this command does not
expose as flags.

Examples:

  cf workers script upload my-worker --module ./worker.js
  cf workers script upload my-worker --module ./dist/index.mjs --main-module index.mjs --compatibility-date 2025-01-15
  cf workers script upload my-worker --module worker.js=./dist/worker.js --module lib.wasm=./dist/lib.wasm --keep-bindings secret_text
  cf workers script upload my-worker --module ./worker.js --bindings '[{"type":"kv_namespace","name":"KV","namespace_id":"0f2ac74b498b48028cb68387c421e279"}]'
  cf workers script upload my-worker --module ./worker.js --metadata @./metadata.json --dry-run`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			spec, err := buildWorkersScriptsUpload(workersScriptsUploadFlags{
				Modules:        modules,
				ModuleTypes:    moduleTypes,
				MainModule:     mainModule,
				Metadata:       metadata,
				Bindings:       bindings,
				CompatDate:     compatDate,
				CompatFlags:    compatFlags,
				CompatFlagsSet: cmd.Flags().Changed("compatibility-flag"),
				KeepBindings:   keepBindings,
				Logpush:        logpush,
				LogpushSet:     cmd.Flags().Changed("logpush"),
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: workersScriptsScriptPath(accountID, script)}
			return runWorkersScriptsUpload(cmd, g, client, req, spec)
		},
	}
	cmd.Flags().StringArrayVar(&modules, "module", nil, "module file to upload as [name=]path (repeatable)")
	cmd.Flags().StringArrayVar(&moduleTypes, "module-type", nil, "override a module's type as <module>=<type> (repeatable)")
	cmd.Flags().StringVar(&mainModule, "main-module", "", "name of the entry point module (default: the only module)")
	cmd.Flags().StringVar(&metadata, "metadata", "", "base upload metadata as inline JSON object or @file")
	cmd.Flags().StringVar(&bindings, "bindings", "", "bindings as an inline JSON array or @file")
	cmd.Flags().StringVar(&compatDate, "compatibility-date", "", "runtime compatibility date (YYYY-MM-DD)")
	cmd.Flags().StringArrayVar(&compatFlags, "compatibility-flag", nil, "runtime compatibility flag (repeatable)")
	cmd.Flags().StringArrayVar(&keepBindings, "keep-bindings", nil, "binding type to preserve from the current Worker, e.g. secret_text (repeatable)")
	cmd.Flags().BoolVar(&logpush, "logpush", false, "send Worker logs to configured logpush jobs")
	_ = cmd.MarkFlagRequired("module")
	return cmd
}

type workersScriptsUploadFlags struct {
	Modules        []string
	ModuleTypes    []string
	MainModule     string
	Metadata       string
	Bindings       string
	CompatDate     string
	CompatFlags    []string
	CompatFlagsSet bool
	KeepBindings   []string
	Logpush        bool
	LogpushSet     bool
}

// buildWorkersScriptsUpload validates every local input — module files,
// module types, metadata shape, main-module rules — before any client is
// built, and returns the exact multipart payload to send.
func buildWorkersScriptsUpload(f workersScriptsUploadFlags) (*workersScriptsUpload, error) {
	modules, err := workersScriptsParseModules(f.Modules)
	if err != nil {
		return nil, err
	}
	if err := workersScriptsApplyModuleTypes(modules, f.ModuleTypes); err != nil {
		return nil, err
	}
	meta, err := workersScriptsParseMetadata(f.Metadata)
	if err != nil {
		return nil, err
	}
	mainModule, err := workersScriptsMainModule(modules, f.MainModule, meta)
	if err != nil {
		return nil, err
	}
	meta["main_module"] = mainModule
	if f.Bindings != "" {
		list, err := workersScriptsParseBindings(f.Bindings)
		if err != nil {
			return nil, err
		}
		meta["bindings"] = list
	}
	if f.CompatDate != "" {
		date, err := workersScriptsCompatDate(f.CompatDate)
		if err != nil {
			return nil, err
		}
		meta["compatibility_date"] = date
	}
	if f.CompatFlagsSet {
		flags, err := workersScriptsStringList(f.CompatFlags, "--compatibility-flag")
		if err != nil {
			return nil, err
		}
		meta["compatibility_flags"] = flags
	}
	if len(f.KeepBindings) > 0 {
		keep, err := workersScriptsStringList(f.KeepBindings, "--keep-bindings")
		if err != nil {
			return nil, err
		}
		meta["keep_bindings"] = keep
	}
	if f.LogpushSet {
		meta["logpush"] = f.Logpush
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	return &workersScriptsUpload{Metadata: raw, Modules: modules}, nil
}

// workersScriptsParseModules turns [name=]path values into named modules and
// checks that every file exists and is readable as a regular file.
func workersScriptsParseModules(specs []string) ([]workersScriptsModule, error) {
	if len(specs) == 0 {
		return nil, errors.New("no modules to upload: pass at least one --module [name=]path")
	}
	modules := make([]workersScriptsModule, 0, len(specs))
	seen := map[string]bool{}
	for _, raw := range specs {
		spec := strings.TrimSpace(raw)
		if spec == "" {
			return nil, errors.New("--module value cannot be empty")
		}
		name, path := "", spec
		if i := strings.Index(spec, "="); i >= 0 {
			name, path = strings.TrimSpace(spec[:i]), strings.TrimSpace(spec[i+1:])
			if name == "" {
				return nil, fmt.Errorf("invalid --module %q: module name before '=' is empty", raw)
			}
			if path == "" {
				return nil, fmt.Errorf("invalid --module %q: file path after '=' is empty", raw)
			}
		} else {
			name = filepath.Base(path)
		}
		if err := workersScriptsValidateModuleName(name); err != nil {
			return nil, err
		}
		if name == "metadata" {
			return nil, errors.New(`a module cannot be named "metadata": that part carries the upload metadata`)
		}
		if seen[name] {
			return nil, fmt.Errorf("duplicate module name %q", name)
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("read module %q: %w", name, err)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("module %q: %q is a directory", name, path)
		}
		seen[name] = true
		modules = append(modules, workersScriptsModule{Name: name, Path: path})
	}
	return modules, nil
}

// workersScriptsApplyModuleTypes resolves each module's content type from an
// explicit --module-type override, else from the module name's extension.
func workersScriptsApplyModuleTypes(modules []workersScriptsModule, overrides []string) error {
	byName := map[string]int{}
	for i, m := range modules {
		byName[m.Name] = i
	}
	set := map[string]bool{}
	for _, raw := range overrides {
		spec := strings.TrimSpace(raw)
		i := strings.Index(spec, "=")
		if i <= 0 || i == len(spec)-1 {
			return fmt.Errorf("invalid --module-type %q: expected <module>=<type>", raw)
		}
		name := strings.TrimSpace(spec[:i])
		typeName := strings.ToLower(strings.TrimSpace(spec[i+1:]))
		idx, ok := byName[name]
		if !ok {
			return fmt.Errorf("--module-type %q: no module named %q was uploaded", raw, name)
		}
		contentType, ok := workersScriptsModuleTypes[typeName]
		if !ok {
			return fmt.Errorf("--module-type %q: unknown type %q (expected one of: %s)", raw, typeName, strings.Join(workersScriptsModuleTypeNames(), ", "))
		}
		if set[name] {
			return fmt.Errorf("--module-type set more than once for module %q", name)
		}
		set[name] = true
		modules[idx].ContentType = contentType
	}
	for i, m := range modules {
		if m.ContentType != "" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(m.Name))
		typeName, ok := workersScriptsExtTypes[ext]
		if !ok {
			return fmt.Errorf("cannot infer the module type of %q: pass --module-type %s=<type> (one of: %s)", m.Name, m.Name, strings.Join(workersScriptsModuleTypeNames(), ", "))
		}
		modules[i].ContentType = workersScriptsModuleTypes[typeName]
	}
	return nil
}

// workersScriptsParseMetadata decodes --metadata (inline JSON or @file) into
// a base metadata object. null, arrays, and scalars are rejected, as is the
// service-worker "body_part" form this command does not upload.
func workersScriptsParseMetadata(spec string) (map[string]any, error) {
	if strings.TrimSpace(spec) == "" {
		return map[string]any{}, nil
	}
	raw, err := workersScriptsReadJSONArg(spec, "--metadata")
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("--metadata must be a JSON object")
	}
	obj, ok := decoded.(map[string]any)
	if !ok || obj == nil {
		return nil, errors.New("--metadata must be a JSON object")
	}
	if _, ok := obj["body_part"]; ok {
		return nil, errors.New(`--metadata sets "body_part": this command uploads the modules format only, so use --module and --main-module`)
	}
	return obj, nil
}

// workersScriptsMainModule applies the main-module rules: an explicit
// --main-module wins, then metadata's main_module, then the sole module.
// Whichever is chosen must name one of the uploaded modules.
func workersScriptsMainModule(modules []workersScriptsModule, flag string, meta map[string]any) (string, error) {
	names := make([]string, 0, len(modules))
	for _, m := range modules {
		names = append(names, m.Name)
	}
	has := func(name string) bool {
		for _, n := range names {
			if n == name {
				return true
			}
		}
		return false
	}
	if main := strings.TrimSpace(flag); main != "" {
		if !has(main) {
			return "", fmt.Errorf("--main-module %q does not match an uploaded module (have: %s)", main, strings.Join(names, ", "))
		}
		return main, nil
	}
	if v, ok := meta["main_module"]; ok {
		main, ok := v.(string)
		if !ok || strings.TrimSpace(main) == "" {
			return "", errors.New(`--metadata "main_module" must be a non-empty string`)
		}
		main = strings.TrimSpace(main)
		if !has(main) {
			return "", fmt.Errorf("--metadata main_module %q does not match an uploaded module (have: %s)", main, strings.Join(names, ", "))
		}
		return main, nil
	}
	if len(modules) == 1 {
		return modules[0].Name, nil
	}
	return "", fmt.Errorf("%d modules uploaded: pass --main-module to name the entry point (have: %s)", len(modules), strings.Join(names, ", "))
}

// workersScriptsParseBindings requires a JSON array of binding objects, each
// with a name and a type. null and wrong shapes are rejected.
func workersScriptsParseBindings(spec string) ([]any, error) {
	raw, err := workersScriptsReadJSONArg(spec, "--bindings")
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, errors.New("--bindings must be a JSON array of binding objects")
	}
	list, ok := decoded.([]any)
	if !ok || list == nil {
		return nil, errors.New("--bindings must be a JSON array of binding objects")
	}
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || obj == nil {
			return nil, fmt.Errorf("--bindings item %d must be a JSON object", i)
		}
		name, _ := obj["name"].(string)
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("--bindings item %d: missing non-empty string \"name\"", i)
		}
		bindType, _ := obj["type"].(string)
		if strings.TrimSpace(bindType) == "" {
			return nil, fmt.Errorf("--bindings item %d (%s): missing non-empty string \"type\"", i, name)
		}
	}
	return list, nil
}

// workersScriptsCompatDate validates the runtime compatibility date, which
// the API accepts only as a real YYYY-MM-DD calendar date.
func workersScriptsCompatDate(date string) (string, error) {
	date = strings.TrimSpace(date)
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", fmt.Errorf("--compatibility-date %q must be a date in YYYY-MM-DD form", date)
	}
	if t.Format("2006-01-02") != date {
		return "", fmt.Errorf("--compatibility-date %q must be a date in YYYY-MM-DD form", date)
	}
	return date, nil
}

func workersScriptsStringList(values []string, flag string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, fmt.Errorf("%s value cannot be empty", flag)
		}
		out = append(out, v)
	}
	return out, nil
}

// workersScriptsReadJSONArg accepts inline JSON or @file for JSON-valued
// flags.
func workersScriptsReadJSONArg(spec, flag string) ([]byte, error) {
	spec = strings.TrimSpace(spec)
	if !strings.HasPrefix(spec, "@") {
		return []byte(spec), nil
	}
	path := strings.TrimPrefix(spec, "@")
	if path == "" {
		return nil, fmt.Errorf("%s @file path is empty", flag)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s file %q: %w", flag, path, err)
	}
	return data, nil
}

// writeMultipart writes the upload body: the metadata part first, then one
// part per module in declaration order. It closes the writer, so the caller
// gets a complete body.
func (u *workersScriptsUpload) writeMultipart(mw *multipart.Writer) error {
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="metadata"`)
	h.Set("Content-Type", "application/json")
	part, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := part.Write(u.Metadata); err != nil {
		return err
	}
	for _, m := range u.Modules {
		disposition, err := workersScriptsDisposition(m.Name)
		if err != nil {
			return err
		}
		mh := textproto.MIMEHeader{}
		mh.Set("Content-Disposition", disposition)
		mh.Set("Content-Type", m.ContentType)
		part, err := mw.CreatePart(mh)
		if err != nil {
			return err
		}
		f, err := os.Open(m.Path)
		if err != nil {
			return fmt.Errorf("open module %q: %w", m.Name, err)
		}
		_, copyErr := io.Copy(part, f)
		closeErr := f.Close()
		if copyErr != nil {
			return fmt.Errorf("read module %q: %w", m.Name, copyErr)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return mw.Close()
}

// workersScriptsDisposition builds a module part's Content-Disposition. The
// module name is sent raw-UTF-8 in a quoted string — the form wrangler and
// the API's own examples use, and the only form whose "name" the API matches
// against main_module verbatim — so quotes and backslashes are escaped and
// the result is parsed back to prove it decodes to the exact module name.
// workersScriptsValidateModuleName has already rejected the control
// characters that could forge a header; this re-check keeps the writer safe
// on its own.
func workersScriptsDisposition(name string) (string, error) {
	if err := workersScriptsValidateModuleName(name); err != nil {
		return "", err
	}
	escaped := strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(name)
	disposition := fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escaped, escaped)
	_, params, err := mime.ParseMediaType(disposition)
	if err != nil {
		return "", fmt.Errorf("module %q cannot be sent as a multipart part name: %w", name, err)
	}
	if params["name"] != name || params["filename"] != name {
		return "", fmt.Errorf("module %q cannot be sent as a multipart part name", name)
	}
	return disposition, nil
}

// workersScriptsValidateModuleName rejects module names that cannot travel
// safely in a MIME header: control characters (CR and LF above all, which
// could inject headers or split the body) and invalid UTF-8. Directory
// separators are allowed — nested module names are how the runtime resolves
// relative imports.
func workersScriptsValidateModuleName(name string) error {
	if name == "" {
		return errors.New("module name cannot be empty")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("invalid module name %q: names must be valid UTF-8", name)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid module name %q: names cannot contain control characters", name)
		}
	}
	return nil
}

// runWorkersScriptsUpload streams the multipart body with DoStream so large
// bundles are not buffered. --dry-run renders the complete body instead, so
// the exact wire format is inspectable without uploading.
func runWorkersScriptsUpload(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request, spec *workersScriptsUpload) error {
	if g.DryRun {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := spec.writeMultipart(mw); err != nil {
			return err
		}
		req.Body = buf.Bytes()
		req.ContentType = mw.FormDataContentType()
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	req.ContentType = mw.FormDataContentType()
	go func() {
		pw.CloseWithError(spec.writeMultipart(mw))
	}()
	resp, err := client.DoStream(cmd.Context(), req, pr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return renderWorkersScriptsEnvelope(cmd, g, resp.Body)
}

// renderWorkersScriptsEnvelope parses a streamed success response as the
// standard envelope and renders its result.
func renderWorkersScriptsEnvelope(cmd *cobra.Command, g *globalOpts, body io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(body, 10<<20))
	if err != nil {
		return err
	}
	var env api.Envelope
	if err := json.Unmarshal(bytes.TrimSpace(data), &env); err != nil {
		return g.renderValue(cmd, string(data), output.JSON)
	}
	if !env.Success {
		return fmt.Errorf("upload failed: %s", workersScriptsErrorText(env.Errors))
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}

func workersScriptsErrorText(msgs []api.Message) string {
	if len(msgs) == 0 {
		return "the API reported success=false without an error message"
	}
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, fmt.Sprintf("%d: %s", m.Code, m.Message))
	}
	return strings.Join(parts, "; ")
}

// ---------------------------------------------------------------------------
// download

func newWorkersScriptsDownloadCmd(g *globalOpts) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "download <script>",
		Short: "Download a Worker's code",
		Long: `Download the code of a Worker. The response is written as raw bytes: a
modules Worker comes back as a multipart/form-data bundle, a service-worker
script as JavaScript. The transfer is streamed, so large bundles are not
buffered in memory.

Because the payload is not JSON, --output and --query do not apply. Use
--file to write to disk (an existing file is overwritten).

Examples:

  cf workers script download my-worker
  cf workers script download my-worker --file ./worker.bundle`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			if g.Query != "" {
				return errors.New("--query is not supported for script download: the response is raw script content, not JSON")
			}
			if g.Output != "" && !g.DryRun {
				return errors.New("--output is not supported for script download: the response is raw script content, not JSON")
			}
			if cmd.Flags().Changed("file") && strings.TrimSpace(file) == "" {
				return errors.New("--file path cannot be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersScriptsContentPath(accountID, script)}
			if g.DryRun {
				return runWorkersScriptsRequest(cmd, g, client, req)
			}
			resp, err := client.DoStream(cmd.Context(), req, nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			dest := strings.TrimSpace(file)
			if dest == "" {
				_, err := io.Copy(cmd.OutOrStdout(), resp.Body)
				return err
			}
			f, err := os.Create(dest)
			if err != nil {
				return fmt.Errorf("write --file: %w", err)
			}
			if _, err := io.Copy(f, resp.Body); err != nil {
				f.Close()
				return fmt.Errorf("write --file %q: %w", dest, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("write --file %q: %w", dest, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "write the content to this file instead of stdout")
	return cmd
}

// ---------------------------------------------------------------------------
// delete

func newWorkersScriptsDeleteCmd(g *globalOpts) *cobra.Command {
	var force, deleteBindings bool
	cmd := &cobra.Command{
		Use:   "delete <script>",
		Short: "Delete a Worker",
		Long: `Delete a Worker.

By default the API refuses to delete a Worker that other resources bind to.
--delete-bindings sends the API's force flag, which deletes those bindings
and Durable Objects along with the Worker. --force only skips this command's
confirmation prompt.

Examples:

  cf workers script delete my-worker --force
  cf workers script delete my-worker --delete-bindings --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Worker %s?", script)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			q := url.Values{}
			if deleteBindings {
				q.Set("force", "true")
			}
			req := api.Request{Method: "DELETE", Path: workersScriptsScriptPath(accountID, script), Query: q}
			return runWorkersScriptsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&deleteBindings, "delete-bindings", false, "delete the Worker even when other resources bind to it, removing those bindings")
	return cmd
}

// ---------------------------------------------------------------------------
// secrets

func newWorkersScriptsSecretCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage a Worker's secrets",
	}
	cmd.AddCommand(
		newWorkersScriptsSecretListCmd(g),
		newWorkersScriptsSecretPutCmd(g),
		newWorkersScriptsSecretDeleteCmd(g),
	)
	return cmd
}

func newWorkersScriptsSecretListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <script>",
		Short: "List a Worker's secret bindings",
		Long: `List the secret bindings of a Worker. The API returns names and binding
types only; secret values are never readable.

Examples:

  cf workers script secret list my-worker
  cf workers script secret list my-worker --output json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersScriptsSecretsPath(accountID, script)}
			if g.DryRun {
				return runWorkersScriptsRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var secrets []workersScriptsSecret
			if err := json.Unmarshal(env.Result, &secrets); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(secrets))
			for _, s := range secrets {
				rows = append(rows, []string{s.Name, s.Type})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "TYPE"}, rows)
		},
	}
	return cmd
}

func newWorkersScriptsSecretPutCmd(g *globalOpts) *cobra.Command {
	var value string
	cmd := &cobra.Command{
		Use:   "put <script> <secret-name>",
		Short: "Create or replace a Worker secret",
		Long: `Create or replace a secret_text binding on a Worker. Writing a secret that
already exists overwrites its value.

Use --value @file to read the secret from a file, or --value @- to read it
from stdin and keep it out of the shell history. Values read from a file or
stdin have their trailing newline removed; an inline --value is sent
verbatim.

Examples:

  cf workers script secret put my-worker API_TOKEN --value s3cret
  cf workers script secret put my-worker API_TOKEN --value @./token.txt
  echo -n "$TOKEN" | cf workers script secret put my-worker API_TOKEN --value @-`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			secret, err := workersScriptsValidateSecretName(args[1])
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("value") {
				return errors.New("missing --value: pass a string, @file, or @- for stdin")
			}
			text, err := workersScriptsReadValue(value, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if text == "" {
				return errors.New("secret value cannot be empty")
			}
			body, err := json.Marshal(map[string]string{
				"name": secret,
				"text": text,
				"type": "secret_text",
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: workersScriptsSecretsPath(accountID, script), Body: body}
			return runWorkersScriptsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&value, "value", "", "secret value (inline string, @file, or @- for stdin)")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

// workersScriptsReadValue loads a secret value: inline string, @file, or @-
// for stdin. Trailing newlines from files and stdin are trimmed so a
// heredoc or `echo` does not smuggle a newline into the secret.
func workersScriptsReadValue(spec string, stdin io.Reader) (string, error) {
	switch {
	case spec == "@-":
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read --value from stdin: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	case strings.HasPrefix(spec, "@"):
		path := strings.TrimPrefix(spec, "@")
		if path == "" {
			return "", errors.New("--value @file path is empty")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --value file %q: %w", path, err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	default:
		return spec, nil
	}
}

func newWorkersScriptsSecretDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <script> <secret-name>",
		Short: "Delete a Worker secret",
		Long: `Delete a secret binding from a Worker.

Examples:

  cf workers script secret delete my-worker API_TOKEN --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			secret, err := workersScriptsValidateSecretName(args[1])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete secret %s from Worker %s?", secret, script)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: workersScriptsSecretPath(accountID, script, secret)}
			return runWorkersScriptsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// ---------------------------------------------------------------------------
// subdomain

func newWorkersScriptsSubdomainCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "subdomain",
		Short: "Manage a Worker's workers.dev subdomain route",
	}
	cmd.AddCommand(
		newWorkersScriptsSubdomainGetCmd(g),
		newWorkersScriptsSubdomainEnableCmd(g),
	)
	return cmd
}

func newWorkersScriptsSubdomainGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <script>",
		Short: "Show whether a Worker is served on workers.dev",
		Long: `Show a Worker's workers.dev subdomain settings: whether the Worker is served
on <script>.<account-subdomain>.workers.dev, and whether preview URLs are
enabled.

Examples:

  cf workers script subdomain get my-worker`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersScriptsSubdomainPath(accountID, script)}
			return runWorkersScriptsRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newWorkersScriptsSubdomainEnableCmd(g *globalOpts) *cobra.Command {
	var previews bool
	cmd := &cobra.Command{
		Use:   "enable <script>",
		Short: "Serve a Worker on its workers.dev subdomain",
		Long: `Enable the workers.dev route for a Worker, so it is served on
<script>.<account-subdomain>.workers.dev. The account must already have a
workers.dev subdomain registered.

Preview URLs are enabled with the route; pass --previews=false to serve only
the production URL.

Examples:

  cf workers script subdomain enable my-worker
  cf workers script subdomain enable my-worker --previews=false`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersScriptsValidateName(args[0])
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]bool{
				"enabled":          true,
				"previews_enabled": previews,
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := workersScriptsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: workersScriptsSubdomainPath(accountID, script), Body: body}
			return runWorkersScriptsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&previews, "previews", true, "also enable preview URLs for the Worker")
	return cmd
}

// ---------------------------------------------------------------------------

func runWorkersScriptsRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	env, err := client.Do(cmd.Context(), req)
	if err != nil {
		return err
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}
