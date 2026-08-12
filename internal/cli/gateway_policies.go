package cli

// Gateway policies porcelain: the Zero Trust Gateway rules that filter DNS,
// HTTP and network traffic, plus the Zero Trust lists those rules match on.
//
// Everything here is account-scoped (`/accounts/{id}/gateway/...`) — there is
// no zone scope for Gateway, so no zone resolution happens in this file.
//
// The sub-shard boundary allows this product exactly one registration line in
// internal/cli/gateway.go, so both resources hang off a single group command:
//
//	cf gateway policy rule ...   Gateway rules (DNS/HTTP/L4 filtering policies)
//	cf gateway policy list ...   Zero Trust lists and their items
//
// Deliberately out of scope, still reachable through `cf api zero-trust-gateway`:
// bulk list upload (`/gateway/lists/upload`), tenant-level rule inspection,
// `reset_expiration`, and the long tail of `rule_settings` (browser isolation
// controls, block pages, egress and L4 override settings, DNS resolvers).
// `cf gateway policy rule update` preserves those settings untouched, so they
// survive edits made here.
//
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// gatewayPolicyActions are the rule actions the API accepts, in their
// canonical wire spelling (spec: POST /accounts/{id}/gateway/rules, action).
var gatewayPolicyActions = []string{
	"on", "off", "allow", "block", "scan", "noscan", "safesearch",
	"ytrestricted", "isolate", "noisolate", "override", "l4_override",
	"egress", "resolve", "quarantine", "redirect",
}

// gatewayPolicyFilters are the traffic layers a rule can evaluate. The API
// caps `filters` at exactly one entry (minItems/maxItems 1), so `--filter`
// takes a single value.
var gatewayPolicyFilters = []string{"http", "dns", "l4", "egress", "dns_resolver"}

// gatewayPolicyListTypes are the Zero Trust list types, canonically uppercase.
var gatewayPolicyListTypes = []string{
	"SERIAL", "URL", "DOMAIN", "EMAIL", "IP", "CATEGORY", "LOCATION",
	"DEVICE", "AAGUID",
}

// gatewayPolicyRuleServerFields are the rule fields the API owns. They come
// back from GET but are rejected or ignored on the write schema, so a
// read-merge-write update has to drop them.
var gatewayPolicyRuleServerFields = []string{
	"id", "created_at", "updated_at", "deleted_at", "version",
	"read_only", "sharable", "source_account", "warning_status",
}

// gatewayPolicyListServerFields are the Zero Trust list fields the API owns.
// `type` is create-only (absent from the PUT schema) and `items` has its own
// endpoint, so both are dropped alongside the read-only fields.
var gatewayPolicyListServerFields = []string{
	"id", "created_at", "updated_at", "count", "type", "items",
}

// gatewayPolicyMaxPages bounds the endpoint-local page loop.
const gatewayPolicyMaxPages = 1000

func newGatewayPoliciesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage Gateway rules and the Zero Trust lists they match on",
	}
	cmd.AddCommand(
		newGatewayPolicyRuleCmd(g),
		newGatewayPolicyListCmd(g),
	)
	return cmd
}

// --- shared helpers --------------------------------------------------------

// gatewayPolicyAccountID validates the resolved account scope. Every command
// in this file is account-scoped.
func gatewayPolicyAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

func gatewayPolicyRulesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/gateway/rules"
}

func gatewayPolicyRulePath(accountID, ruleID string) string {
	return gatewayPolicyRulesPath(accountID) + "/" + url.PathEscape(ruleID)
}

func gatewayPolicyListsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/gateway/lists"
}

func gatewayPolicyListPath(accountID, listID string) string {
	return gatewayPolicyListsPath(accountID) + "/" + url.PathEscape(listID)
}

func gatewayPolicyListItemsPath(accountID, listID string) string {
	return gatewayPolicyListPath(accountID, listID) + "/items"
}

// gatewayPolicyCanonical matches a flag value case-insensitively against the
// values the API documents and returns the canonical spelling, so the wire
// body never carries a user's casing.
func gatewayPolicyCanonical(flag, value string, allowed []string) (string, error) {
	for _, a := range allowed {
		if strings.EqualFold(value, a) {
			return a, nil
		}
	}
	return "", fmt.Errorf("invalid --%s %q: expected one of %s", flag, value, strings.Join(allowed, ", "))
}

// gatewayPolicyRequireArg rejects blank positional arguments before any
// network work happens.
func gatewayPolicyRequireArg(what, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	return nil
}

// gatewayPolicyIDMaxLength is the bound the spec puts on every Gateway rule
// and Zero Trust list identifier: both path parameters resolve to the
// zero-trust-gateway UUID schema, which sets maxLength 36 and no pattern.
const gatewayPolicyIDMaxLength = 36

// gatewayPolicyRequireID validates a rule or list identifier before any client
// construction or network work. The bound counts Unicode code points, matching
// how the spec measures string length, so a multi-byte identifier is not
// rejected for its UTF-8 size.
func gatewayPolicyRequireID(what, value string) error {
	if err := gatewayPolicyRequireArg(what, value); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(value); n > gatewayPolicyIDMaxLength {
		return fmt.Errorf("%s must be at most %d characters, got %d", what, gatewayPolicyIDMaxLength, n)
	}
	return nil
}

func runGatewayPolicyRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// runGatewayPolicyCollection fetches every page of a Gateway collection and
// renders it as a table, falling back to JSON when the decoder cannot read
// the result.
func runGatewayPolicyCollection(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request,
	table func(json.RawMessage) ([]string, [][]string, bool)) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	raw, err := gatewayPolicyCollect(cmd.Context(), client, req)
	if err != nil {
		return err
	}
	format := g.format(output.Table)
	if g.Query != "" || format != output.Table {
		return g.renderResult(cmd, raw, output.JSON)
	}
	headers, rows, ok := table(raw)
	if !ok {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
	}
	return output.RenderTable(cmd.OutOrStdout(), headers, rows)
}

// gatewayPolicyCollect walks a Gateway collection endpoint page by page.
//
// These endpoints report `result_info` with page/per_page/total_count but no
// total_pages and no cursor, so Client.DoAutoPaginate stops after the first
// page and would silently truncate a long list. This loop follows the count
// metadata instead, and stops as soon as the endpoint stops honoring the page
// parameter rather than re-reading one page forever.
func gatewayPolicyCollect(ctx context.Context, client *api.Client, req api.Request) (json.RawMessage, error) {
	merged := []json.RawMessage{}
	page := 1
	for i := 0; i < gatewayPolicyMaxPages; i++ {
		attempt := req
		if page > 1 {
			q := url.Values{}
			for k, vs := range req.Query {
				for _, v := range vs {
					q.Add(k, v)
				}
			}
			q.Set("page", strconv.Itoa(page))
			attempt.Query = q
		}
		env, err := client.Do(ctx, attempt)
		if err != nil {
			return nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(env.Result, &items); err != nil {
			if i == 0 {
				return env.Result, nil // not a collection response
			}
			return nil, fmt.Errorf("gateway pagination: page %d result was not an array", page)
		}
		ri := env.ResultInfo
		if page > 1 && ri != nil && ri.Page != 0 && ri.Page != page {
			// The endpoint ignored ?page and re-served one we already have.
			break
		}
		merged = append(merged, items...)
		if ri == nil || len(items) == 0 || ri.TotalCount <= 0 || len(merged) >= ri.TotalCount {
			break
		}
		page++
	}
	return json.Marshal(merged)
}

// gatewayPolicyReadObject fetches the object a read-merge-write update has to
// re-send in full.
func gatewayPolicyReadObject(cmd *cobra.Command, client *api.Client, path, what string) (map[string]any, error) {
	env, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s before update: %w", what, err)
	}
	var current map[string]any
	if err := json.Unmarshal(env.Result, &current); err != nil || current == nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", what)
	}
	return current, nil
}

// gatewayPolicyReplacementBody drops the fields the API assigns and applies
// the caller's changes on top of the stored object, leaving every other
// writable field — including ones this CLI does not model — untouched.
func gatewayPolicyReplacementBody(current map[string]any, drop []string, changes map[string]any) ([]byte, error) {
	for _, field := range drop {
		delete(current, field)
	}
	for k, v := range changes {
		current[k] = v
	}
	return json.Marshal(current)
}

// --- gateway rules ---------------------------------------------------------

type gatewayPolicyRuleRow struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Action     string   `json:"action"`
	Filters    []string `json:"filters"`
	Precedence *int     `json:"precedence"`
	Enabled    *bool    `json:"enabled"`
}

func newGatewayPolicyRuleCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rule",
		Short: "Manage Gateway rules (DNS, HTTP, and network filtering policies)",
	}
	cmd.AddCommand(
		newGatewayPolicyRuleListCmd(g),
		newGatewayPolicyRuleGetCmd(g),
		newGatewayPolicyRuleCreateCmd(g),
		newGatewayPolicyRuleUpdateCmd(g),
		newGatewayPolicyRuleEnableCmd(g),
		newGatewayPolicyRuleDisableCmd(g),
		newGatewayPolicyRuleDeleteCmd(g),
	)
	return cmd
}

func newGatewayPolicyRuleListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Gateway rules in precedence order",
		Long: `List Gateway rules.

Rules are returned in the order the API evaluates them; lower precedence
values are evaluated first.

Examples:

  cf gateway policy rule list
  cf gateway policy rule list --query '[.[] | select(.filters[0] == "dns")]'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayPolicyRulesPath(accountID)}
			return runGatewayPolicyCollection(cmd, g, client, req, gatewayPolicyRuleTable)
		},
	}
	return cmd
}

func gatewayPolicyRuleTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var rules []gatewayPolicyRuleRow
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(rules))
	for _, r := range rules {
		precedence := ""
		if r.Precedence != nil {
			precedence = strconv.Itoa(*r.Precedence)
		}
		enabled := ""
		if r.Enabled != nil {
			enabled = strconv.FormatBool(*r.Enabled)
		}
		rows = append(rows, []string{
			r.ID,
			output.Cell(r.Name),
			r.Action,
			strings.Join(r.Filters, ","),
			precedence,
			enabled,
		})
	}
	return []string{"ID", "NAME", "ACTION", "FILTERS", "PRECEDENCE", "ENABLED"}, rows, true
}

func newGatewayPolicyRuleGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <rule-id>",
		Short: "Show one Gateway rule",
		Long: `Show one Gateway rule, including its full rule_settings.

Examples:

  cf gateway policy rule get 3a1b2c4d-0000-4000-8000-000000000000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("rule ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayPolicyRulePath(accountID, args[0])}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// gatewayPolicyRuleOpts holds the rule fields this porcelain exposes.
type gatewayPolicyRuleOpts struct {
	name          string
	action        string
	filter        string
	traffic       string
	identity      string
	devicePosture string
	description   string
	precedence    int
	enabled       bool
}

func addGatewayPolicyRuleFlags(cmd *cobra.Command, o *gatewayPolicyRuleOpts) {
	cmd.Flags().StringVar(&o.action, "action", "", "action to take when the rule matches ("+strings.Join(gatewayPolicyActions, ", ")+")")
	cmd.Flags().StringVar(&o.filter, "filter", "", "traffic layer the rule evaluates ("+strings.Join(gatewayPolicyFilters, ", ")+")")
	cmd.Flags().StringVar(&o.traffic, "traffic", "", "wirefilter expression matching traffic, e.g. any(dns.domains[*] in $malware)")
	cmd.Flags().StringVar(&o.identity, "identity", "", "wirefilter expression matching identity, e.g. any(identity.groups.name[*] in {\"finance\"})")
	cmd.Flags().StringVar(&o.devicePosture, "device-posture", "", "wirefilter expression matching device posture checks")
	cmd.Flags().StringVar(&o.description, "description", "", "rule description")
	cmd.Flags().IntVar(&o.precedence, "precedence", 0, "evaluation order; lower values are evaluated first")
}

// gatewayPolicyRuleChanges validates every rule flag the caller set and turns
// them into wire fields. It runs before any client or network work.
func gatewayPolicyRuleChanges(cmd *cobra.Command, o gatewayPolicyRuleOpts) (map[string]any, error) {
	changes := map[string]any{}
	if cmd.Flags().Changed("action") {
		action, err := gatewayPolicyCanonical("action", o.action, gatewayPolicyActions)
		if err != nil {
			return nil, err
		}
		changes["action"] = action
	}
	if cmd.Flags().Changed("filter") {
		filter, err := gatewayPolicyCanonical("filter", o.filter, gatewayPolicyFilters)
		if err != nil {
			return nil, err
		}
		changes["filters"] = []string{filter}
	}
	if cmd.Flags().Changed("name") {
		if strings.TrimSpace(o.name) == "" {
			return nil, errors.New("--name must not be empty")
		}
		changes["name"] = o.name
	}
	// traffic, identity and device_posture default to "" in the API, so an
	// explicit empty value is a legitimate way to clear them.
	for _, f := range []struct {
		flag, field, value string
	}{
		{"traffic", "traffic", o.traffic},
		{"identity", "identity", o.identity},
		{"device-posture", "device_posture", o.devicePosture},
		{"description", "description", o.description},
	} {
		if cmd.Flags().Changed(f.flag) {
			changes[f.field] = f.value
		}
	}
	if cmd.Flags().Changed("precedence") {
		changes["precedence"] = o.precedence
	}
	if cmd.Flags().Changed("enabled") {
		changes["enabled"] = o.enabled
	}
	return changes, nil
}

func newGatewayPolicyRuleCreateCmd(g *globalOpts) *cobra.Command {
	var o gatewayPolicyRuleOpts
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Gateway rule",
		Long: `Create a Gateway rule.

--action is required. --filter picks the layer the rule evaluates; the API
accepts exactly one. New rules are disabled unless --enabled is passed.

Examples:

  cf gateway policy rule create "Block malware" --action block --filter dns \
    --traffic 'any(dns.content_category[*] in {80 83})' --enabled
  cf gateway policy rule create "Isolate webmail" --action isolate --filter http \
    --traffic 'any(http.request.uri.content_category[*] in {135})' --precedence 100`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireArg("rule name", args[0]); err != nil {
				return err
			}
			changes, err := gatewayPolicyRuleChanges(cmd, o)
			if err != nil {
				return err
			}
			changes["name"] = args[0]
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			body, err := json.Marshal(changes)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: gatewayPolicyRulesPath(accountID), Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	addGatewayPolicyRuleFlags(cmd, &o)
	cmd.Flags().BoolVar(&o.enabled, "enabled", false, "enable the rule immediately")
	_ = cmd.MarkFlagRequired("action")
	return cmd
}

func newGatewayPolicyRuleUpdateCmd(g *globalOpts) *cobra.Command {
	var o gatewayPolicyRuleOpts
	cmd := &cobra.Command{
		Use:   "update <rule-id>",
		Short: "Update fields of a Gateway rule",
		Long: `Update a Gateway rule.

The rule API replaces the whole rule on update, so this command reads the
current rule first, applies the flags you passed, and sends the merged rule
back. Settings this CLI does not model — browser isolation controls, block
pages, egress and L4 override settings, schedules — are preserved as stored.
Because the merge needs the stored rule, --dry-run performs the read before
printing the request it would send.

Use "cf gateway policy rule enable|disable" to toggle a rule on or off; that
path patches a single field and needs no read.

Examples:

  cf gateway policy rule update 3a1b2c4d-0000-4000-8000-000000000000 --precedence 50
  cf gateway policy rule update 3a1b2c4d-0000-4000-8000-000000000000 \
    --traffic 'any(dns.domains[*] in $blocked_domains)'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("rule ID", args[0]); err != nil {
				return err
			}
			changes, err := gatewayPolicyRuleChanges(cmd, o)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --action, --filter, --traffic, --identity, --device-posture, --description, --precedence")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			path := gatewayPolicyRulePath(accountID, args[0])
			current, err := gatewayPolicyReadObject(cmd, client, path, "Gateway rule "+args[0])
			if err != nil {
				return err
			}
			gatewayPolicyStripRuleExpiration(current)
			body, err := gatewayPolicyReplacementBody(current, gatewayPolicyRuleServerFields, changes)
			if err != nil {
				return err
			}
			if err := gatewayPolicyValidateRuleBody(body); err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: path, Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&o.name, "name", "", "rule name")
	addGatewayPolicyRuleFlags(cmd, &o)
	return cmd
}

// gatewayPolicyStripRuleExpiration removes the read-only `expired` marker the
// API reports inside a DNS rule's expiration object.
func gatewayPolicyStripRuleExpiration(current map[string]any) {
	expiration, ok := current["expiration"].(map[string]any)
	if !ok {
		return
	}
	delete(expiration, "expired")
}

// gatewayPolicyValidateRuleBody checks the fields the rule write schema marks
// required, so a merge over an unexpected response fails here with a usable
// message instead of as an opaque API error.
func gatewayPolicyValidateRuleBody(body []byte) error {
	var rule struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(body, &rule); err != nil {
		return fmt.Errorf("build rule update: %w", err)
	}
	if strings.TrimSpace(rule.Name) == "" {
		return errors.New("the stored rule has no name; pass --name to set one")
	}
	if strings.TrimSpace(rule.Action) == "" {
		return errors.New("the stored rule has no action; pass --action to set one")
	}
	return nil
}

func newGatewayPolicyRuleEnableCmd(g *globalOpts) *cobra.Command {
	return newGatewayPolicyRuleToggleCmd(g, true)
}

func newGatewayPolicyRuleDisableCmd(g *globalOpts) *cobra.Command {
	return newGatewayPolicyRuleToggleCmd(g, false)
}

// newGatewayPolicyRuleToggleCmd builds the enable/disable pair. Both patch
// only `enabled`, which the rule PATCH schema supports, so neither needs the
// read-merge-write dance that `update` does.
func newGatewayPolicyRuleToggleCmd(g *globalOpts, enable bool) *cobra.Command {
	verb, short := "disable", "Turn a Gateway rule off without deleting it"
	if enable {
		verb, short = "enable", "Turn a Gateway rule on"
	}
	cmd := &cobra.Command{
		Use:   verb + " <rule-id>",
		Short: short,
		Long: fmt.Sprintf(`%s.

This patches only the enabled flag, leaving every other rule field untouched.

Examples:

  cf gateway policy rule %s 3a1b2c4d-0000-4000-8000-000000000000`, short, verb),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("rule ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]any{"enabled": enable})
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: gatewayPolicyRulePath(accountID, args[0]), Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newGatewayPolicyRuleDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <rule-id>",
		Short: "Delete a Gateway rule",
		Long: `Delete a Gateway rule.

Deleting a rule stops it from filtering traffic immediately and cannot be
undone. To keep the rule but stop enforcing it, use
"cf gateway policy rule disable" instead.

Examples:

  cf gateway policy rule delete 3a1b2c4d-0000-4000-8000-000000000000 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("rule ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Gateway rule %s? It stops filtering traffic immediately.", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: gatewayPolicyRulePath(accountID, args[0])}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- Zero Trust lists ------------------------------------------------------

type gatewayPolicyListRow struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Count       *float64 `json:"count"`
	Description string   `json:"description"`
}

type gatewayPolicyListItemRow struct {
	Value       string `json:"value"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
}

func newGatewayPolicyListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Manage Zero Trust lists used by Gateway rule expressions",
	}
	cmd.AddCommand(
		newGatewayPolicyListListCmd(g),
		newGatewayPolicyListGetCmd(g),
		newGatewayPolicyListCreateCmd(g),
		newGatewayPolicyListUpdateCmd(g),
		newGatewayPolicyListDeleteCmd(g),
		newGatewayPolicyListItemCmd(g),
	)
	return cmd
}

func newGatewayPolicyListListCmd(g *globalOpts) *cobra.Command {
	var listType string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Zero Trust lists",
		Long: `List the account's Zero Trust lists.

Examples:

  cf gateway policy list list
  cf gateway policy list list --type DOMAIN`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("type") {
				canonical, err := gatewayPolicyCanonical("type", listType, gatewayPolicyListTypes)
				if err != nil {
					return err
				}
				q.Set("type", canonical)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayPolicyListsPath(accountID), Query: q}
			return runGatewayPolicyCollection(cmd, g, client, req, gatewayPolicyListTable)
		},
	}
	cmd.Flags().StringVar(&listType, "type", "", "filter by list type ("+strings.Join(gatewayPolicyListTypes, ", ")+")")
	return cmd
}

func gatewayPolicyListTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var lists []gatewayPolicyListRow
	if err := json.Unmarshal(raw, &lists); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(lists))
	for _, l := range lists {
		count := ""
		if l.Count != nil {
			count = output.Cell(*l.Count)
		}
		rows = append(rows, []string{l.ID, output.Cell(l.Name), l.Type, count, output.Cell(l.Description)})
	}
	return []string{"ID", "NAME", "TYPE", "ITEMS", "DESCRIPTION"}, rows, true
}

func newGatewayPolicyListGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <list-id>",
		Short: "Show one Zero Trust list",
		Long: `Show one Zero Trust list.

Use "cf gateway policy list item list" to page through the list's entries.

Examples:

  cf gateway policy list get 7f3e1a90-0000-4000-8000-000000000000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("list ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayPolicyListPath(accountID, args[0])}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newGatewayPolicyListCreateCmd(g *globalOpts) *cobra.Command {
	var listType, description string
	var items []string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Zero Trust list",
		Long: `Create a Zero Trust list.

Rules reference a list as $<list name> inside a wirefilter expression, e.g.
any(dns.domains[*] in $blocked_domains).

Examples:

  cf gateway policy list create blocked_domains --type DOMAIN
  cf gateway policy list create blocked_domains --type DOMAIN \
    --description "Known-bad domains" --item evil.example --item worse.example`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireArg("list name", args[0]); err != nil {
				return err
			}
			canonical, err := gatewayPolicyCanonical("type", listType, gatewayPolicyListTypes)
			if err != nil {
				return err
			}
			entries, err := gatewayPolicyItemEntries("item", items, "")
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			payload := map[string]any{"name": args[0], "type": canonical}
			if cmd.Flags().Changed("description") {
				payload["description"] = description
			}
			if len(entries) > 0 {
				payload["items"] = entries
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: gatewayPolicyListsPath(accountID), Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&listType, "type", "", "list type ("+strings.Join(gatewayPolicyListTypes, ", ")+")")
	cmd.Flags().StringVar(&description, "description", "", "list description")
	cmd.Flags().StringArrayVar(&items, "item", nil, "initial list entry (repeatable)")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newGatewayPolicyListUpdateCmd(g *globalOpts) *cobra.Command {
	var name, description string
	cmd := &cobra.Command{
		Use:   "update <list-id>",
		Short: "Rename a Zero Trust list or change its description",
		Long: `Update a Zero Trust list's name or description.

The list API requires the name on every update, so this command reads the
current list first and sends the merged object back; --dry-run performs that
read before printing the request. A list's type is fixed at creation, and its
entries are managed with "cf gateway policy list item add|remove", so neither
is ever sent by this command.

Examples:

  cf gateway policy list update 7f3e1a90-0000-4000-8000-000000000000 --name blocked_domains
  cf gateway policy list update 7f3e1a90-0000-4000-8000-000000000000 \
    --description "Reviewed 2026-08-12"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("list ID", args[0]); err != nil {
				return err
			}
			changes := map[string]any{}
			if cmd.Flags().Changed("name") {
				if strings.TrimSpace(name) == "" {
					return errors.New("--name must not be empty")
				}
				changes["name"] = name
			}
			if cmd.Flags().Changed("description") {
				changes["description"] = description
			}
			if len(changes) == 0 {
				return errors.New("nothing to update: pass --name or --description")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			path := gatewayPolicyListPath(accountID, args[0])
			current, err := gatewayPolicyReadObject(cmd, client, path, "Zero Trust list "+args[0])
			if err != nil {
				return err
			}
			body, err := gatewayPolicyReplacementBody(current, gatewayPolicyListServerFields, changes)
			if err != nil {
				return err
			}
			if err := gatewayPolicyValidateListBody(body); err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: path, Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new list name")
	cmd.Flags().StringVar(&description, "description", "", "new list description")
	return cmd
}

// gatewayPolicyValidateListBody checks the one field the list write schema
// marks required.
func gatewayPolicyValidateListBody(body []byte) error {
	var list struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("build list update: %w", err)
	}
	if strings.TrimSpace(list.Name) == "" {
		return errors.New("the stored list has no name; pass --name to set one")
	}
	return nil
}

func newGatewayPolicyListDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <list-id>",
		Short: "Delete a Zero Trust list",
		Long: `Delete a Zero Trust list and every entry in it.

Gateway rules that reference the list by name stop matching its entries, so
check "cf gateway policy rule list" before deleting a list in use.

Examples:

  cf gateway policy list delete 7f3e1a90-0000-4000-8000-000000000000 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("list ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Zero Trust list %s and all of its entries?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: gatewayPolicyListPath(accountID, args[0])}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- Zero Trust list items -------------------------------------------------

func newGatewayPolicyListItemCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "item",
		Short: "Manage the entries of a Zero Trust list",
	}
	cmd.AddCommand(
		newGatewayPolicyListItemListCmd(g),
		newGatewayPolicyListItemAddCmd(g),
		newGatewayPolicyListItemRemoveCmd(g),
	)
	return cmd
}

func newGatewayPolicyListItemListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <list-id>",
		Short: "List the entries of a Zero Trust list",
		Long: `List every entry of a Zero Trust list.

Examples:

  cf gateway policy list item list 7f3e1a90-0000-4000-8000-000000000000`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("list ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayPolicyListItemsPath(accountID, args[0])}
			return runGatewayPolicyCollection(cmd, g, client, req, gatewayPolicyListItemTable)
		},
	}
	return cmd
}

func gatewayPolicyListItemTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var items []gatewayPolicyListItemRow
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{output.Cell(it.Value), output.Cell(it.Description), it.CreatedAt})
	}
	return []string{"VALUE", "DESCRIPTION", "CREATED"}, rows, true
}

// gatewayPolicyItemEntries validates entry values and builds the item objects
// the list API takes. description is attached to every entry when set.
func gatewayPolicyItemEntries(what string, values []string, description string) ([]map[string]any, error) {
	entries := make([]map[string]any, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%s values must not be empty", what)
		}
		if seen[v] {
			return nil, fmt.Errorf("duplicate %s value %q", what, v)
		}
		seen[v] = true
		entry := map[string]any{"value": v}
		if description != "" {
			entry["description"] = description
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func newGatewayPolicyListItemAddCmd(g *globalOpts) *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "add <list-id> <value>...",
		Short: "Add entries to a Zero Trust list",
		Long: `Add one or more entries to a Zero Trust list.

Entries are appended; existing entries are left alone.

Examples:

  cf gateway policy list item add 7f3e1a90-0000-4000-8000-000000000000 evil.example
  cf gateway policy list item add 7f3e1a90-0000-4000-8000-000000000000 \
    evil.example worse.example --description "From the 2026-08 incident"`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("list ID", args[0]); err != nil {
				return err
			}
			entries, err := gatewayPolicyItemEntries("entry", args[1:], description)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]any{"append": entries})
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: gatewayPolicyListPath(accountID, args[0]), Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "description applied to every entry added")
	return cmd
}

func newGatewayPolicyListItemRemoveCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <list-id> <value>...",
		Short: "Remove entries from a Zero Trust list",
		Long: `Remove one or more entries from a Zero Trust list by value.

Removing an entry takes effect on the next policy evaluation and cannot be
undone; re-add the value to restore it.

Examples:

  cf gateway policy list item remove 7f3e1a90-0000-4000-8000-000000000000 \
    evil.example --force`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayPolicyRequireID("list ID", args[0]); err != nil {
				return err
			}
			values, err := gatewayPolicyRemovalValues(args[1:])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := gatewayPolicyAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				noun := "entries"
				if len(values) == 1 {
					noun = "entry"
				}
				if !confirm(fmt.Sprintf("Remove %d %s from Zero Trust list %s?", len(values), noun, args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			body, err := json.Marshal(map[string]any{"remove": values})
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: gatewayPolicyListPath(accountID, args[0]), Body: body}
			return runGatewayPolicyRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// gatewayPolicyRemovalValues validates the values to strip from a list.
func gatewayPolicyRemovalValues(values []string) ([]string, error) {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) == "" {
			return nil, errors.New("entry values must not be empty")
		}
		if seen[v] {
			return nil, fmt.Errorf("duplicate entry value %q", v)
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}
