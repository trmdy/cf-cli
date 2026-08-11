package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
	"github.com/trmdy/cf-cli/internal/registry"
)

// flag names that would collide with global or built-in flags; API params
// with these names are exposed as --param-<name>.
var reservedFlags = map[string]bool{
	"output": true, "profile": true, "token": true, "account-id": true,
	"zone-id": true, "base-url": true, "dry-run": true, "data": true,
	"field": true, "paginate": true, "force": true, "help": true,
}

func newAPICmd(g *globalOpts) *cobra.Command {
	apiCmd := &cobra.Command{
		Use:   "api",
		Short: "Low-level access to every Cloudflare API endpoint",
		Long:  "Generated commands covering the full Cloudflare API, one group per product.\nRun `cf api <product>` to list a product's operations, or use\n`cf api raw <method> <path>` as an escape hatch for anything else.",
	}
	apiCmd.AddCommand(newRawCmd(g))

	reg, err := registry.Load()
	if err != nil {
		apiCmd.Long += "\n\nWARNING: embedded operation registry failed to load: " + err.Error()
		return apiCmd
	}
	for _, product := range reg.Products() {
		ops := reg.ByProduct(product)
		pc := &cobra.Command{
			Use:   product,
			Short: fmt.Sprintf("%d operations", len(ops)),
		}
		for _, op := range ops {
			pc.AddCommand(newOpCmd(g, op))
		}
		apiCmd.AddCommand(pc)
	}
	return apiCmd
}

// flagNameFor converts an API parameter name to a kebab-case CLI flag name
// (any non-alphanumeric run becomes one dash: "issue_class~neq" ->
// "issue-class-neq"), avoiding reserved names and duplicates. The original
// parameter name is still what gets sent on the wire.
func flagNameFor(param string, used map[string]bool) string {
	var b strings.Builder
	dash := false
	for _, c := range strings.ToLower(param) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(c)
		} else {
			dash = true
		}
	}
	n := b.String()
	if n == "" {
		n = "param"
	}
	if reservedFlags[n] {
		n = "param-" + n
	}
	for used[n] {
		n += "-2"
	}
	used[n] = true
	return n
}

func paramDesc(p registry.Param) string {
	d := p.Description
	if i := strings.IndexByte(d, '\n'); i >= 0 {
		d = d[:i]
	}
	if len(d) > 120 {
		d = d[:117] + "..."
	}
	if len(p.Enum) > 0 {
		enum := p.Enum
		if len(enum) > 8 {
			enum = enum[:8]
		}
		d = strings.TrimRight(d, " .") + " (one of: " + strings.Join(enum, ", ") + ")"
	}
	if d == "" {
		d = p.In + " parameter"
	}
	return d
}

func newOpCmd(g *globalOpts, op registry.Operation) *cobra.Command {
	strVals := map[string]*string{}
	arrVals := map[string]*[]string{}
	flagFor := map[string]string{}
	var data string
	var fields []string
	var paginate bool

	short := op.Summary
	if short == "" {
		short = op.Method + " " + op.Path
	}
	if op.Deprecated {
		short = "(deprecated) " + short
	}
	cmd := &cobra.Command{
		Use:   op.Name,
		Short: short,
		Long:  fmt.Sprintf("%s\n\n%s %s", short, op.Method, op.Path),
	}
	flags := cmd.Flags()
	used := map[string]bool{}
	for _, p := range op.Params {
		if isScopeParam(p.Name) {
			continue
		}
		fn := flagNameFor(p.Name, used)
		flagFor[p.Name] = fn
		if p.Type == "array" {
			var v []string
			arrVals[p.Name] = &v
			flags.StringArrayVar(&v, fn, nil, paramDesc(p))
		} else {
			var v string
			strVals[p.Name] = &v
			flags.StringVar(&v, fn, "", paramDesc(p))
		}
		if p.Required {
			_ = cmd.MarkFlagRequired(fn)
		}
	}
	if op.HasBody {
		flags.StringVar(&data, "data", "", "JSON request body (inline string, @file, or @- for stdin)")
		flags.StringArrayVarP(&fields, "field", "f", nil, "set a body field as key=value (value parsed as JSON when possible; dots nest; repeatable)")
	}
	if op.Method == "GET" {
		flags.BoolVar(&paginate, "paginate", false, "fetch all pages of results")
	}

	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		client, cfg, err := g.client(true)
		if err != nil {
			return err
		}
		vals := paramValues{
			flagFor: flagFor,
			str:     strVals,
			arr:     arrVals,
			changed: cmd.Flags().Changed,
		}
		req, err := buildRequest(op, cfg.AccountID, cfg.ZoneID, vals)
		if err != nil {
			return err
		}
		if op.HasBody || data != "" || len(fields) > 0 {
			body, err := buildBody(data, fields, cmd.InOrStdin())
			if err != nil {
				return err
			}
			req.Body = body
		}
		return execute(cmd, g, client, req, paginate)
	}
	return cmd
}

func newRawCmd(g *globalOpts) *cobra.Command {
	var data string
	var fields []string
	var paginate bool
	cmd := &cobra.Command{
		Use:   "raw <method> <path>",
		Short: "Send a raw request to any API path",
		Long:  "Escape hatch for endpoints without a generated command.\n\nExample:\n  cf api raw GET /zones\n  cf api raw POST /zones/$ZONE/purge_cache -f purge_everything=true",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := strings.ToUpper(args[0])
			switch method {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
			default:
				return fmt.Errorf("invalid HTTP method %q", args[0])
			}
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			body, err := buildBody(data, fields, cmd.InOrStdin())
			if err != nil {
				return err
			}
			req := api.Request{Method: method, Path: args[1], Body: body}
			return execute(cmd, g, client, req, paginate)
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "JSON request body (inline string, @file, or @- for stdin)")
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "set a body field as key=value (value parsed as JSON when possible; dots nest; repeatable)")
	cmd.Flags().BoolVar(&paginate, "paginate", false, "fetch all pages of results (GET only)")
	return cmd
}

// execute performs the request (or dumps it under --dry-run) and renders the
// result envelope.
func execute(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request, paginate bool) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	ctx := cmd.Context()
	var env *api.Envelope
	var err error
	if paginate && req.Method == "GET" {
		env, err = client.DoAutoPaginate(ctx, req)
	} else {
		env, err = client.Do(ctx, req)
	}
	if err != nil {
		return err
	}
	for _, m := range env.Messages {
		fmt.Fprintf(cmd.ErrOrStderr(), "note: %s\n", m.Message)
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}
