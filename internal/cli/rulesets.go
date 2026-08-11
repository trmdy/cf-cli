package cli

// Rulesets porcelain manages account- and zone-scoped Cloudflare Rulesets.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

const rulesetsDefaultPerPage = 50

var rulesetsKinds = []string{"managed", "custom", "root", "zone"}

var rulesetsPhases = []string{
	"ddos_l4",
	"ddos_l7",
	"http_config_settings",
	"http_custom_errors",
	"http_log_custom_fields",
	"http_ratelimit",
	"http_request_cache_settings",
	"http_request_dynamic_redirect",
	"http_request_firewall_custom",
	"http_request_firewall_managed",
	"http_request_late_transform",
	"http_request_origin",
	"http_request_redirect",
	"http_request_sanitize",
	"http_request_sbfm",
	"http_request_transform",
	"http_response_cache_settings",
	"http_response_compression",
	"http_response_firewall_managed",
	"http_response_headers_transform",
	"magic_transit",
	"magic_transit_ids_managed",
	"magic_transit_managed",
	"magic_transit_ratelimit",
}

type rulesetsScope struct {
	scope string
	zone  string
}

type rulesetSummary struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Version string `json:"version,omitempty"`
}

func newRulesetsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rulesets",
		Short: "Manage Cloudflare Rulesets",
	}
	cmd.AddCommand(
		newRulesetsListCmd(g),
		newRulesetsGetCmd(g),
		newRulesetsCreateCmd(g),
		newRulesetsDeleteCmd(g),
		newRulesetsRuleCmd(g),
		newRulesetsEntrypointCmd(g),
	)
	return cmd
}

func newRulesetsRuleCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "rule", Short: "Manage rules within a Ruleset"}
	cmd.AddCommand(
		newRulesetsRuleAddCmd(g),
		newRulesetsRuleEditCmd(g),
		newRulesetsRuleDeleteCmd(g),
	)
	return cmd
}

func newRulesetsEntrypointCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "entrypoint", Short: "Manage phase entry point Rulesets"}
	cmd.AddCommand(
		newRulesetsEntrypointGetCmd(g),
		newRulesetsEntrypointUpdateCmd(g),
	)
	return cmd
}

func addRulesetsScopeFlags(cmd *cobra.Command, scope *rulesetsScope) {
	cmd.Flags().StringVar(&scope.scope, "scope", "account", "resource scope: account or zone")
	cmd.Flags().StringVar(&scope.zone, "zone", "", "zone name or ID (required with --scope zone; default: configured zone)")
}

// validateRulesetsScope performs all scope-only checks before a client is
// built or a zone name could cause a lookup request.
func validateRulesetsScope(scope rulesetsScope) error {
	switch scope.scope {
	case "account":
		if scope.zone != "" {
			return errors.New("--zone requires --scope zone")
		}
	case "zone":
		return nil
	default:
		return fmt.Errorf("--scope must be account or zone (got %q)", scope.scope)
	}
	return nil
}

// resolveRulesetsPath turns the explicit porcelain scope into an API path
// prefix. Zone scope uses the shared interactive resolver.
func resolveRulesetsPath(cmd *cobra.Command, g *globalOpts, scope rulesetsScope) (*api.Client, string, error) {
	if err := validateRulesetsScope(scope); err != nil {
		return nil, "", err
	}
	client, cfg, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	if scope.scope == "account" {
		if cfg.AccountID == "" {
			return nil, "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
		}
		return client, "/accounts/" + url.PathEscape(cfg.AccountID) + "/rulesets", nil
	}
	zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, scope.zone)
	if err != nil {
		return nil, "", err
	}
	return client, "/zones/" + url.PathEscape(zoneID) + "/rulesets", nil
}

// validateRulesetsID checks the API's canonical 32-character lowercase hex
// identifier format before any client construction or zone lookup.
func validateRulesetsID(label, id string) error {
	if len(id) != 32 {
		return fmt.Errorf("%s must be a 32-character lowercase hexadecimal ID (got %q)", label, id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("%s must be a 32-character lowercase hexadecimal ID (got %q)", label, id)
		}
	}
	return nil
}

func validateRulesetsEnum(flag, value string, allowed []string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

func newRulesetsListCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var perPage int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Rulesets",
		Long: `List Rulesets in an account or zone. Results are fetched across every API page.

Examples:

  cf rulesets list --account-id $ACCOUNT_ID
  cf rulesets list --scope zone --zone example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if perPage < 1 || perPage > rulesetsDefaultPerPage {
				return fmt.Errorf("--per-page must be between 1 and %d", rulesetsDefaultPerPage)
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("per_page", fmt.Sprint(perPage))
			return runRulesetsListRequest(cmd, g, client, api.Request{Method: "GET", Path: prefix, Query: q})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	cmd.Flags().IntVar(&perPage, "per-page", rulesetsDefaultPerPage, "Rulesets returned per API page (1 through 50)")
	return cmd
}

func newRulesetsGetCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	cmd := &cobra.Command{
		Use:   "get <ruleset-id>",
		Short: "Show a Ruleset",
		Long: `Show the latest version of a Ruleset.

Examples:

  cf rulesets get 2f2feab2026849078ba485f918791bdc --account-id $ACCOUNT_ID
  cf rulesets get 2f2feab2026849078ba485f918791bdc --scope zone --zone example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsID("ruleset ID", args[0]); err != nil {
				return err
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "GET", Path: prefix + "/" + args[0]})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	return cmd
}

type rulesetsCreateOptions struct {
	name, description, kind, phase, rules string
}

func newRulesetsCreateCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var options rulesetsCreateOptions
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Ruleset",
		Long: `Create a Ruleset. --rules accepts a JSON array inline, from @file, or from @- (stdin).

Examples:

  cf rulesets create --account-id $ACCOUNT_ID --name "custom firewall" --kind root --phase http_request_firewall_custom --rules '[{"action":"block","expression":"ip.src eq 192.0.2.1"}]'
  cf rulesets create --scope zone --zone example.com --name redirects --kind zone --phase http_request_dynamic_redirect`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildRulesetsCreateBody(cmd, options)
			if err != nil {
				return err
			}
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "POST", Path: prefix, Body: body})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	flags := cmd.Flags()
	flags.StringVar(&options.name, "name", "", "human-readable Ruleset name")
	flags.StringVar(&options.description, "description", "", "informative Ruleset description")
	flags.StringVar(&options.kind, "kind", "", "Ruleset kind: managed, custom, root, or zone")
	flags.StringVar(&options.phase, "phase", "", "Ruleset phase")
	flags.StringVar(&options.rules, "rules", "", "Rules as a JSON array (inline, @file, or @- for stdin)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("kind")
	_ = cmd.MarkFlagRequired("phase")
	return cmd
}

func buildRulesetsCreateBody(cmd *cobra.Command, options rulesetsCreateOptions) ([]byte, error) {
	if strings.TrimSpace(options.name) == "" {
		return nil, errors.New("--name must not be empty")
	}
	if err := validateRulesetsEnum("kind", options.kind, rulesetsKinds); err != nil {
		return nil, err
	}
	if err := validateRulesetsEnum("phase", options.phase, rulesetsPhases); err != nil {
		return nil, err
	}
	body := map[string]any{"name": options.name, "kind": options.kind, "phase": options.phase}
	if cmd.Flags().Changed("description") {
		body["description"] = options.description
	}
	if cmd.Flags().Changed("rules") {
		rules, err := parseRulesetsJSONArray(cmd, "rules", options.rules)
		if err != nil {
			return nil, err
		}
		if err := validateRulesetsRules(rules); err != nil {
			return nil, err
		}
		body["rules"] = rules
	}
	return json.Marshal(body)
}

func newRulesetsDeleteCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <ruleset-id>",
		Short: "Delete a Ruleset",
		Long: `Delete every version of a Ruleset.

Example:

  cf rulesets delete 2f2feab2026849078ba485f918791bdc --account-id $ACCOUNT_ID --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsID("ruleset ID", args[0]); err != nil {
				return err
			}
			if !force && !g.DryRun && !rulesetsInteractiveStdin() {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete every version of Ruleset %s?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "DELETE", Path: prefix + "/" + args[0]})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func rulesetsInteractiveStdin() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func newRulesetsRuleAddCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var rule string
	cmd := &cobra.Command{
		Use:   "add <ruleset-id>",
		Short: "Add a rule to a Ruleset",
		Long: `Add a rule to a Ruleset. This creates a new Ruleset version. --rule accepts a JSON object inline, from @file, or from @- (stdin).

Example:

  cf rulesets rule add 2f2feab2026849078ba485f918791bdc --account-id $ACCOUNT_ID --rule '{"action":"block","expression":"ip.src eq 192.0.2.1"}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildRulesetsRuleBody(cmd, rule)
			if err != nil {
				return err
			}
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsID("ruleset ID", args[0]); err != nil {
				return err
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "POST", Path: prefix + "/" + args[0] + "/rules", Body: body})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	cmd.Flags().StringVar(&rule, "rule", "", "rule as a JSON object (inline, @file, or @- for stdin)")
	_ = cmd.MarkFlagRequired("rule")
	return cmd
}

func newRulesetsRuleEditCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var rule string
	cmd := &cobra.Command{
		Use:   "edit <ruleset-id> <rule-id>",
		Short: "Edit a Ruleset rule",
		Long: `Edit a rule in a Ruleset. This creates a new Ruleset version. --rule accepts a JSON object inline, from @file, or from @- (stdin).

Example:

  cf rulesets rule edit 2f2feab2026849078ba485f918791bdc 3a03d665bac047339bb530ecb439a90d --account-id $ACCOUNT_ID --rule '{"enabled":false}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildRulesetsRuleBody(cmd, rule)
			if err != nil {
				return err
			}
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsID("ruleset ID", args[0]); err != nil {
				return err
			}
			if err := validateRulesetsID("rule ID", args[1]); err != nil {
				return err
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "PATCH", Path: prefix + "/" + args[0] + "/rules/" + args[1], Body: body})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	cmd.Flags().StringVar(&rule, "rule", "", "rule changes as a JSON object (inline, @file, or @- for stdin)")
	_ = cmd.MarkFlagRequired("rule")
	return cmd
}

func newRulesetsRuleDeleteCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <ruleset-id> <rule-id>",
		Short: "Delete a Ruleset rule",
		Long: `Delete a rule from a Ruleset. This creates a new Ruleset version.

Example:

  cf rulesets rule delete 2f2feab2026849078ba485f918791bdc 3a03d665bac047339bb530ecb439a90d --account-id $ACCOUNT_ID --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsID("ruleset ID", args[0]); err != nil {
				return err
			}
			if err := validateRulesetsID("rule ID", args[1]); err != nil {
				return err
			}
			if !force && !g.DryRun && !rulesetsInteractiveStdin() {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete rule %s from Ruleset %s?", args[1], args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "DELETE", Path: prefix + "/" + args[0] + "/rules/" + args[1]})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newRulesetsEntrypointGetCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	cmd := &cobra.Command{
		Use:   "get <phase>",
		Short: "Show a phase entry point Ruleset",
		Long: `Show the latest entry point Ruleset for a phase.

Examples:

  cf rulesets entrypoint get http_request_firewall_custom --account-id $ACCOUNT_ID
  cf rulesets entrypoint get http_request_transform --scope zone --zone example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsEnum("phase", args[0], rulesetsPhases); err != nil {
				return err
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "GET", Path: prefix + "/phases/" + args[0] + "/entrypoint"})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	return cmd
}

type rulesetsEntrypointOptions struct {
	name, description, rules string
}

func newRulesetsEntrypointUpdateCmd(g *globalOpts) *cobra.Command {
	var scope rulesetsScope
	var options rulesetsEntrypointOptions
	cmd := &cobra.Command{
		Use:   "update <phase>",
		Short: "Update a phase entry point Ruleset",
		Long: `Update a phase entry point Ruleset. This creates a new Ruleset version. --rules accepts a JSON array inline, from @file, or from @- (stdin).

Example:

  cf rulesets entrypoint update http_request_firewall_custom --account-id $ACCOUNT_ID --rules '[{"action":"execute","expression":"true","action_parameters":{"id":"2f2feab2026849078ba485f918791bdc"}}]'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildRulesetsEntrypointBody(cmd, options)
			if err != nil {
				return err
			}
			if err := validateRulesetsScope(scope); err != nil {
				return err
			}
			if err := validateRulesetsEnum("phase", args[0], rulesetsPhases); err != nil {
				return err
			}
			client, prefix, err := resolveRulesetsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runRulesetsRequest(cmd, g, client, api.Request{Method: "PUT", Path: prefix + "/phases/" + args[0] + "/entrypoint", Body: body})
		},
	}
	addRulesetsScopeFlags(cmd, &scope)
	flags := cmd.Flags()
	flags.StringVar(&options.name, "name", "", "human-readable Ruleset name")
	flags.StringVar(&options.description, "description", "", "informative Ruleset description")
	flags.StringVar(&options.rules, "rules", "", "Rules as a JSON array (inline, @file, or @- for stdin)")
	return cmd
}

func buildRulesetsEntrypointBody(cmd *cobra.Command, options rulesetsEntrypointOptions) ([]byte, error) {
	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		if strings.TrimSpace(options.name) == "" {
			return nil, errors.New("--name must not be empty")
		}
		body["name"] = options.name
	}
	if cmd.Flags().Changed("description") {
		body["description"] = options.description
	}
	if cmd.Flags().Changed("rules") {
		rules, err := parseRulesetsJSONArray(cmd, "rules", options.rules)
		if err != nil {
			return nil, err
		}
		if err := validateRulesetsRules(rules); err != nil {
			return nil, err
		}
		body["rules"] = rules
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to update: pass --name, --description, or --rules")
	}
	return json.Marshal(body)
}

func buildRulesetsRuleBody(cmd *cobra.Command, raw string) ([]byte, error) {
	rule, err := parseRulesetsJSONObject(cmd, "rule", raw)
	if err != nil {
		return nil, err
	}
	if err := validateRulesetsRule(rule); err != nil {
		return nil, err
	}
	return json.Marshal(rule)
}

// parseRulesetsJSONObject and parseRulesetsJSONArray deliberately decode via
// any before asserting the type: unmarshalling JSON null into a map or slice
// otherwise quietly succeeds with a nil value.
func parseRulesetsJSONObject(cmd *cobra.Command, flag, value string) (map[string]any, error) {
	raw, err := buildBody(value, nil, cmd.InOrStdin())
	if err != nil {
		return nil, fmt.Errorf("--%s must be a JSON object: %w", flag, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("--%s must be a JSON object", flag)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON object: %w", flag, err)
	}
	object, ok := decoded.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("--%s must be a JSON object", flag)
	}
	return object, nil
}

func parseRulesetsJSONArray(cmd *cobra.Command, flag, value string) ([]any, error) {
	raw, err := buildBody(value, nil, cmd.InOrStdin())
	if err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array: %w", flag, err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("--%s must be a JSON array", flag)
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array: %w", flag, err)
	}
	array, ok := decoded.([]any)
	if !ok || array == nil {
		return nil, fmt.Errorf("--%s must be a JSON array", flag)
	}
	return array, nil
}

func validateRulesetsRules(rules []any) error {
	for i, value := range rules {
		rule, ok := value.(map[string]any)
		if !ok || rule == nil {
			return fmt.Errorf("--rules item %d must be a JSON object", i+1)
		}
		if err := validateRulesetsRule(rule); err != nil {
			return fmt.Errorf("--rules item %d: %w", i+1, err)
		}
	}
	return nil
}

func validateRulesetsRule(rule map[string]any) error {
	if len(rule) == 0 {
		return errors.New("rule must not be an empty JSON object")
	}
	if position, ok := rule["position"]; ok {
		positionObject, ok := position.(map[string]any)
		if !ok || positionObject == nil {
			return errors.New("rule position must be a JSON object")
		}
		if index, ok := positionObject["index"]; ok {
			value, ok := index.(float64)
			if !ok || value != float64(int(value)) || value < 1 {
				return errors.New("rule position.index must be an integer of at least 1")
			}
		}
	}
	return nil
}

func runRulesetsRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func runRulesetsListRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	env, err := client.DoAutoPaginate(cmd.Context(), req)
	if err != nil {
		return err
	}
	format := g.format(output.Table)
	if g.Query != "" || format != output.Table {
		return g.renderResult(cmd, env.Result, output.JSON)
	}
	var rulesets []rulesetSummary
	if err := json.Unmarshal(env.Result, &rulesets); err != nil {
		return g.renderResult(cmd, env.Result, output.JSON)
	}
	rows := make([][]string, 0, len(rulesets))
	for _, ruleset := range rulesets {
		rows = append(rows, []string{
			ruleset.ID,
			output.Cell(ruleset.Name),
			ruleset.Kind,
			ruleset.Phase,
			ruleset.Version,
		})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "KIND", "PHASE", "VERSION"}, rows)
}
