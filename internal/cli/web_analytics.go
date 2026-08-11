package cli

// Web Analytics (RUM) porcelain: account-scoped site CRUD and ruleset rules.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

// Schema bounds documented by the RUM OpenAPI. Checked locally so bad input
// fails before a client is built or any network call is made.
const (
	webAnalyticsMaxHostLen = 253
	webAnalyticsIDLen      = 32
)

// webAnalyticsHostPattern mirrors the structural hostname pattern the validate
// endpoint documents (label syntax only; no wildcards).
var webAnalyticsHostPattern = regexp.MustCompile(`^(([a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9\-]*[a-zA-Z0-9]?)\.){1,}([A-Za-z0-9]|[A-Za-z0-9][A-Za-z0-9\-]*[A-Za-z0-9]?){2,}$`)

var webAnalyticsOrderBy = []string{"host", "created"}

// webAnalyticsSite is the subset of a RUM site the porcelain reads for tables
// and JSON round-trips.
type webAnalyticsSite struct {
	SiteTag     string               `json:"site_tag,omitempty"`
	SiteToken   string               `json:"site_token,omitempty"`
	Host        string               `json:"host,omitempty"`
	AutoInstall *bool                `json:"auto_install,omitempty"`
	Created     string               `json:"created,omitempty"`
	Snippet     string               `json:"snippet,omitempty"`
	Ruleset     *webAnalyticsRuleset `json:"ruleset,omitempty"`
	Rules       []webAnalyticsRule   `json:"rules,omitempty"`
}

type webAnalyticsRuleset struct {
	ID       string `json:"id,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
	ZoneName string `json:"zone_name,omitempty"`
	ZoneTag  string `json:"zone_tag,omitempty"`
}

type webAnalyticsRule struct {
	ID        string   `json:"id,omitempty"`
	Created   string   `json:"created,omitempty"`
	Host      string   `json:"host,omitempty"`
	Inclusive *bool    `json:"inclusive,omitempty"`
	IsPaused  *bool    `json:"is_paused,omitempty"`
	Paths     []string `json:"paths,omitempty"`
	Priority  *float64 `json:"priority,omitempty"`
}

// webAnalyticsRulesList is the list-rules result shape: an object wrapping the
// rules array, not a bare array, so auto-pagination does not apply.
type webAnalyticsRulesList struct {
	Rules   []webAnalyticsRule   `json:"rules,omitempty"`
	Ruleset *webAnalyticsRuleset `json:"ruleset,omitempty"`
}

func newWebAnalyticsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "web-analytics",
		Short: "Manage Web Analytics (RUM) sites and rules",
	}
	cmd.AddCommand(
		newWebAnalyticsListCmd(g),
		newWebAnalyticsGetCmd(g),
		newWebAnalyticsCreateCmd(g),
		newWebAnalyticsUpdateCmd(g),
		newWebAnalyticsDeleteCmd(g),
		newWebAnalyticsRuleCmd(g),
	)
	return cmd
}

func webAnalyticsAccountID(cfg config.Resolved) (string, error) {
	if cfg.AccountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return cfg.AccountID, nil
}

func webAnalyticsSitesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/rum/site_info"
}

func webAnalyticsSitesListPath(accountID string) string {
	return webAnalyticsSitesPath(accountID) + "/list"
}

func webAnalyticsSitePath(accountID, siteID string) string {
	return webAnalyticsSitesPath(accountID) + "/" + url.PathEscape(siteID)
}

func webAnalyticsRulesPath(accountID, rulesetID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/rum/v2/" + url.PathEscape(rulesetID) + "/rules"
}

func webAnalyticsRuleCollectionPath(accountID, rulesetID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/rum/v2/" + url.PathEscape(rulesetID) + "/rule"
}

func webAnalyticsRulePath(accountID, rulesetID, ruleID string) string {
	return webAnalyticsRuleCollectionPath(accountID, rulesetID) + "/" + url.PathEscape(ruleID)
}

func validateWebAnalyticsEnum(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

// validateWebAnalyticsHost enforces the documented hostname length and
// structural pattern (including both sides of the 253-character bound).
func validateWebAnalyticsHost(host string) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("--host must not be empty")
	}
	if strings.Contains(host, "*") {
		return errors.New("--host must not contain wildcards")
	}
	if n := len(host); n > webAnalyticsMaxHostLen {
		return fmt.Errorf("--host is %d characters; the API allows at most %d", n, webAnalyticsMaxHostLen)
	}
	if !webAnalyticsHostPattern.MatchString(host) {
		return fmt.Errorf("--host %q is not a valid hostname (e.g. example.com)", host)
	}
	return nil
}

// validateWebAnalyticsZoneTag checks a zone identifier length bound.
func validateWebAnalyticsZoneTag(zoneTag string) error {
	if strings.TrimSpace(zoneTag) == "" {
		return errors.New("--zone-tag must not be empty")
	}
	if n := len(zoneTag); n != webAnalyticsIDLen {
		return fmt.Errorf("--zone-tag is %d characters; zone identifiers are %d hex characters", n, webAnalyticsIDLen)
	}
	for _, c := range zoneTag {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return fmt.Errorf("--zone-tag %q is not a hex zone identifier", zoneTag)
		}
	}
	return nil
}

func validateWebAnalyticsPaths(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	return validateNonEmptyStrings("path", paths)
}

// parseWebAnalyticsJSONStringArray decodes a JSON array of strings and rejects
// null, objects, scalars, and non-string elements.
func parseWebAnalyticsJSONStringArray(flag, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("--%s must be a JSON array of strings", flag)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array of strings", flag)
	}
	if v == nil {
		return nil, fmt.Errorf("--%s must be a JSON array of strings, not null", flag)
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("--%s must be a JSON array of strings", flag)
	}
	out := make([]string, 0, len(arr))
	for i, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("--%s element at index %d must be a string", flag, i)
		}
		out = append(out, s)
	}
	return out, nil
}

// parseWebAnalyticsRulesJSON decodes the bulk --rules body: a JSON array of
// objects. null and non-arrays are rejected.
func parseWebAnalyticsRulesJSON(raw string) ([]map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("--rules must be a JSON array of rule objects")
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, errors.New("--rules must be a JSON array of rule objects")
	}
	if v == nil {
		return nil, errors.New("--rules must be a JSON array of rule objects, not null")
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, errors.New("--rules must be a JSON array of rule objects")
	}
	out := make([]map[string]any, 0, len(arr))
	for i, item := range arr {
		if item == nil {
			return nil, fmt.Errorf("--rules element at index %d must be an object, not null", i)
		}
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("--rules element at index %d must be an object", i)
		}
		out = append(out, obj)
	}
	return out, nil
}

func webAnalyticsBoolFlag(cmd *cobra.Command, name string, value bool) *bool {
	if !cmd.Flags().Changed(name) {
		return nil
	}
	v := value
	return &v
}

// --- sites -----------------------------------------------------------------

type webAnalyticsCreateOpts struct {
	Host        string
	ZoneTag     string
	AutoInstall *bool
}

func buildWebAnalyticsCreateBody(o webAnalyticsCreateOpts) ([]byte, error) {
	if o.Host == "" && o.ZoneTag == "" {
		return nil, errors.New("create requires --host and/or --zone-tag")
	}
	body := map[string]any{}
	if o.Host != "" {
		if err := validateWebAnalyticsHost(o.Host); err != nil {
			return nil, err
		}
		body["host"] = o.Host
	}
	if o.ZoneTag != "" {
		if err := validateWebAnalyticsZoneTag(o.ZoneTag); err != nil {
			return nil, err
		}
		body["zone_tag"] = o.ZoneTag
	}
	if o.AutoInstall != nil {
		body["auto_install"] = *o.AutoInstall
	}
	return json.Marshal(body)
}

type webAnalyticsUpdateOpts struct {
	Host        *string
	ZoneTag     *string
	AutoInstall *bool
	Enabled     *bool
	Lite        *bool
}

func (o webAnalyticsUpdateOpts) empty() bool {
	return o.Host == nil && o.ZoneTag == nil && o.AutoInstall == nil && o.Enabled == nil && o.Lite == nil
}

func buildWebAnalyticsUpdateBody(o webAnalyticsUpdateOpts) ([]byte, error) {
	if o.empty() {
		return nil, errors.New("nothing to update: pass at least one of --host, --zone-tag, --auto-install, --enabled, --lite")
	}
	// enabled is only valid when auto_install is true (API contract).
	if o.Enabled != nil && o.AutoInstall != nil && !*o.AutoInstall {
		return nil, errors.New("--enabled can only be used when --auto-install is true")
	}
	body := map[string]any{}
	if o.Host != nil {
		if err := validateWebAnalyticsHost(*o.Host); err != nil {
			return nil, err
		}
		body["host"] = *o.Host
	}
	if o.ZoneTag != nil {
		if err := validateWebAnalyticsZoneTag(*o.ZoneTag); err != nil {
			return nil, err
		}
		body["zone_tag"] = *o.ZoneTag
	}
	if o.AutoInstall != nil {
		body["auto_install"] = *o.AutoInstall
	}
	if o.Enabled != nil {
		body["enabled"] = *o.Enabled
	}
	if o.Lite != nil {
		body["lite"] = *o.Lite
	}
	return json.Marshal(body)
}

func newWebAnalyticsListCmd(g *globalOpts) *cobra.Command {
	var orderBy string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Web Analytics sites",
		Long: `List Web Analytics (RUM) sites on an account.

Examples:

  cf web-analytics list
  cf web-analytics list --order-by created
  cf web-analytics list --order-by host --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("order-by") {
				if err := validateWebAnalyticsEnum("order-by", orderBy, webAnalyticsOrderBy); err != nil {
					return err
				}
				q.Set("order_by", orderBy)
			}
			q.Set("per_page", "100")
			// Validate local input fully before constructing a client.
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: webAnalyticsSitesListPath(accountID), Query: q}
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
			var sites []webAnalyticsSite
			if err := json.Unmarshal(env.Result, &sites); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(sites))
			for _, s := range sites {
				host := s.Host
				if host == "" && s.Ruleset != nil {
					host = s.Ruleset.ZoneName
				}
				auto := ""
				if s.AutoInstall != nil {
					auto = fmt.Sprintf("%t", *s.AutoInstall)
				}
				rulesetID := ""
				if s.Ruleset != nil {
					rulesetID = s.Ruleset.ID
				}
				rows = append(rows, []string{s.SiteTag, output.Cell(host), auto, rulesetID, s.Created})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"SITE_TAG", "HOST", "AUTO_INSTALL", "RULESET", "CREATED"}, rows)
		},
	}
	cmd.Flags().StringVar(&orderBy, "order-by", "", "sort field: host or created")
	return cmd
}

func newWebAnalyticsGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <site-id>",
		Short: "Show one Web Analytics site",
		Long:  "Show one Web Analytics site, including its token, snippet, and rules.\n\nExample:\n\n  cf web-analytics get 023e105f4ecef8ad9ca31a8372d0c353",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("site-id must not be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: webAnalyticsSitePath(accountID, args[0])}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newWebAnalyticsCreateCmd(g *globalOpts) *cobra.Command {
	var host, zoneTag string
	var autoInstall bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Web Analytics site",
		Long: `Create a Web Analytics (RUM) site.

Gray-clouded sites take --host and receive a JS snippet. Orange-clouded zones
take --zone-tag (and usually --auto-install) so Cloudflare injects the beacon.

Examples:

  cf web-analytics create --host example.com
  cf web-analytics create --zone-tag 023e105f4ecef8ad9ca31a8372d0c353 --auto-install
  cf web-analytics create --host blog.example.com --zone-tag 023e105f4ecef8ad9ca31a8372d0c353 --auto-install`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildWebAnalyticsCreateBody(webAnalyticsCreateOpts{
				Host:        host,
				ZoneTag:     zoneTag,
				AutoInstall: webAnalyticsBoolFlag(cmd, "auto-install", autoInstall),
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: webAnalyticsSitesPath(accountID), Body: body}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "hostname for a gray-clouded site (e.g. example.com)")
	cmd.Flags().StringVar(&zoneTag, "zone-tag", "", "zone identifier for an orange-clouded site")
	cmd.Flags().BoolVar(&autoInstall, "auto-install", false, "automatically inject the beacon on orange-clouded zones")
	return cmd
}

func newWebAnalyticsUpdateCmd(g *globalOpts) *cobra.Command {
	var host, zoneTag string
	var autoInstall, enabled, lite bool
	cmd := &cobra.Command{
		Use:   "update <site-id>",
		Short: "Update fields of a Web Analytics site",
		Long: `Update fields of a Web Analytics site.

--enabled only applies when auto_install is true (API contract). --lite skips
beacon injection for visitors from the EU.

Examples:

  cf web-analytics update 023e105f4ecef8ad9ca31a8372d0c353 --host www.example.com
  cf web-analytics update 023e105f4ecef8ad9ca31a8372d0c353 --auto-install --enabled
  cf web-analytics update 023e105f4ecef8ad9ca31a8372d0c353 --lite`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("site-id must not be empty")
			}
			o := webAnalyticsUpdateOpts{
				AutoInstall: webAnalyticsBoolFlag(cmd, "auto-install", autoInstall),
				Enabled:     webAnalyticsBoolFlag(cmd, "enabled", enabled),
				Lite:        webAnalyticsBoolFlag(cmd, "lite", lite),
			}
			if cmd.Flags().Changed("host") {
				h := host
				o.Host = &h
			}
			if cmd.Flags().Changed("zone-tag") {
				z := zoneTag
				o.ZoneTag = &z
			}
			body, err := buildWebAnalyticsUpdateBody(o)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: webAnalyticsSitePath(accountID, args[0]), Body: body}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "hostname for a gray-clouded site")
	cmd.Flags().StringVar(&zoneTag, "zone-tag", "", "zone identifier for an orange-clouded site")
	cmd.Flags().BoolVar(&autoInstall, "auto-install", false, "automatically inject the beacon on orange-clouded zones")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "enable or disable RUM (only when auto-install is true)")
	cmd.Flags().BoolVar(&lite, "lite", false, "do not inject the beacon for visitors from the EU")
	return cmd
}

func newWebAnalyticsDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <site-id>",
		Short: "Delete a Web Analytics site",
		Long:  "Delete a Web Analytics site and stop collecting RUM for it.\n\nExample:\n\n  cf web-analytics delete 023e105f4ecef8ad9ca31a8372d0c353 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("site-id must not be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Web Analytics site %s from account %s?", args[0], accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: webAnalyticsSitePath(accountID, args[0])}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- rules -----------------------------------------------------------------

func newWebAnalyticsRuleCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rule",
		Short: "Manage Web Analytics rules",
	}
	cmd.AddCommand(
		newWebAnalyticsRuleListCmd(g),
		newWebAnalyticsRuleCreateCmd(g),
		newWebAnalyticsRuleUpdateCmd(g),
		newWebAnalyticsRuleDeleteCmd(g),
		newWebAnalyticsRuleApplyCmd(g),
	)
	return cmd
}

type webAnalyticsRuleOpts struct {
	Host      string
	Inclusive *bool
	IsPaused  *bool
	Paths     []string
}

func buildWebAnalyticsRuleBody(o webAnalyticsRuleOpts, requireSomething bool) ([]byte, error) {
	if err := validateWebAnalyticsPaths(o.Paths); err != nil {
		return nil, err
	}
	if o.Host != "" {
		if err := validateWebAnalyticsHost(o.Host); err != nil {
			return nil, err
		}
	}
	body := map[string]any{}
	if o.Host != "" {
		body["host"] = o.Host
	}
	if o.Inclusive != nil {
		body["inclusive"] = *o.Inclusive
	}
	if o.IsPaused != nil {
		body["is_paused"] = *o.IsPaused
	}
	if o.Paths != nil {
		// Explicit empty list is a legal "clear paths" signal when the flag was set.
		body["paths"] = o.Paths
	}
	if requireSomething && len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one of --host, --inclusive, --paused, --path, or --paths")
	}
	return json.Marshal(body)
}

// resolveWebAnalyticsRulePaths merges --path (repeatable) and --paths (JSON
// array). Both may not be used together. JSON null/wrong shapes are rejected.
func resolveWebAnalyticsRulePaths(cmd *cobra.Command, pathFlags []string, pathsJSON string) ([]string, bool, error) {
	pathChanged := cmd.Flags().Changed("path")
	pathsChanged := cmd.Flags().Changed("paths")
	if pathChanged && pathsChanged {
		return nil, false, errors.New("use either --path or --paths, not both")
	}
	if pathsChanged {
		paths, err := parseWebAnalyticsJSONStringArray("paths", pathsJSON)
		if err != nil {
			return nil, false, err
		}
		if err := validateWebAnalyticsPaths(paths); err != nil {
			return nil, false, err
		}
		return paths, true, nil
	}
	if pathChanged {
		if err := validateWebAnalyticsPaths(pathFlags); err != nil {
			return nil, false, err
		}
		return pathFlags, true, nil
	}
	return nil, false, nil
}

func newWebAnalyticsRuleListCmd(g *globalOpts) *cobra.Command {
	var rulesetID string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List rules in a Web Analytics ruleset",
		Long: `List rules in a Web Analytics ruleset.

The ruleset ID is returned on each site as ruleset.id.

Examples:

  cf web-analytics rule list --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(rulesetID) == "" {
				return errors.New("missing --ruleset-id")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: webAnalyticsRulesPath(accountID, rulesetID)}
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
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var res webAnalyticsRulesList
			if err := json.Unmarshal(env.Result, &res); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(res.Rules))
			for _, r := range res.Rules {
				inclusive, paused := "", ""
				if r.Inclusive != nil {
					inclusive = fmt.Sprintf("%t", *r.Inclusive)
				}
				if r.IsPaused != nil {
					paused = fmt.Sprintf("%t", *r.IsPaused)
				}
				rows = append(rows, []string{
					r.ID,
					output.Cell(r.Host),
					inclusive,
					paused,
					output.Cell(strings.Join(r.Paths, ",")),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "HOST", "INCLUSIVE", "PAUSED", "PATHS"}, rows)
		},
	}
	cmd.Flags().StringVar(&rulesetID, "ruleset-id", "", "Web Analytics ruleset identifier")
	_ = cmd.MarkFlagRequired("ruleset-id")
	return cmd
}

func newWebAnalyticsRuleCreateCmd(g *globalOpts) *cobra.Command {
	var rulesetID, host, pathsJSON string
	var paths []string
	var inclusive, paused bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Web Analytics rule",
		Long: `Create a rule in a Web Analytics ruleset.

--path is repeatable. --paths accepts a JSON string array (not null).

Examples:

  cf web-analytics rule create --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --host example.com --path '*' --inclusive
  cf web-analytics rule create --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --host example.com --paths '["/app/*","/api/*"]'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(rulesetID) == "" {
				return errors.New("missing --ruleset-id")
			}
			resolvedPaths, pathsSet, err := resolveWebAnalyticsRulePaths(cmd, paths, pathsJSON)
			if err != nil {
				return err
			}
			o := webAnalyticsRuleOpts{
				Host:      host,
				Inclusive: webAnalyticsBoolFlag(cmd, "inclusive", inclusive),
				IsPaused:  webAnalyticsBoolFlag(cmd, "paused", paused),
			}
			if pathsSet {
				o.Paths = resolvedPaths
			}
			body, err := buildWebAnalyticsRuleBody(o, false)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: webAnalyticsRuleCollectionPath(accountID, rulesetID), Body: body}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&rulesetID, "ruleset-id", "", "Web Analytics ruleset identifier")
	cmd.Flags().StringVar(&host, "host", "", "hostname the rule applies to")
	cmd.Flags().BoolVar(&inclusive, "inclusive", false, "include (true) or exclude (false) matching traffic from measurement")
	cmd.Flags().BoolVar(&paused, "paused", false, "pause the rule without deleting it")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "path pattern the rule applies to (repeatable)")
	cmd.Flags().StringVar(&pathsJSON, "paths", "", `JSON array of path patterns (e.g. '["*"]')`)
	_ = cmd.MarkFlagRequired("ruleset-id")
	return cmd
}

func newWebAnalyticsRuleUpdateCmd(g *globalOpts) *cobra.Command {
	var rulesetID, host, pathsJSON string
	var paths []string
	var inclusive, paused bool
	cmd := &cobra.Command{
		Use:   "update <rule-id>",
		Short: "Update a Web Analytics rule",
		Long: `Update fields of a Web Analytics rule.

Examples:

  cf web-analytics rule update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --path '/blog/*'
  cf web-analytics rule update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --paused`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("rule-id must not be empty")
			}
			if strings.TrimSpace(rulesetID) == "" {
				return errors.New("missing --ruleset-id")
			}
			resolvedPaths, pathsSet, err := resolveWebAnalyticsRulePaths(cmd, paths, pathsJSON)
			if err != nil {
				return err
			}
			o := webAnalyticsRuleOpts{
				Inclusive: webAnalyticsBoolFlag(cmd, "inclusive", inclusive),
				IsPaused:  webAnalyticsBoolFlag(cmd, "paused", paused),
			}
			if cmd.Flags().Changed("host") {
				o.Host = host
			}
			if pathsSet {
				o.Paths = resolvedPaths
			}
			// For update, host may be explicitly set empty only via --host "".
			// Treat Changed("host") with empty as an error via validate when non-empty required pattern.
			if cmd.Flags().Changed("host") && host == "" {
				return errors.New("--host must not be empty")
			}
			body, err := buildWebAnalyticsRuleBody(o, true)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: webAnalyticsRulePath(accountID, rulesetID, args[0]), Body: body}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&rulesetID, "ruleset-id", "", "Web Analytics ruleset identifier")
	cmd.Flags().StringVar(&host, "host", "", "hostname the rule applies to")
	cmd.Flags().BoolVar(&inclusive, "inclusive", false, "include (true) or exclude (false) matching traffic from measurement")
	cmd.Flags().BoolVar(&paused, "paused", false, "pause the rule without deleting it")
	cmd.Flags().StringArrayVar(&paths, "path", nil, "path pattern the rule applies to (repeatable; replaces the current list)")
	cmd.Flags().StringVar(&pathsJSON, "paths", "", `JSON array of path patterns (e.g. '["*"]')`)
	_ = cmd.MarkFlagRequired("ruleset-id")
	return cmd
}

func newWebAnalyticsRuleDeleteCmd(g *globalOpts) *cobra.Command {
	var rulesetID string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <rule-id>",
		Short: "Delete a Web Analytics rule",
		Long:  "Delete a rule from a Web Analytics ruleset.\n\nExample:\n\n  cf web-analytics rule delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("rule-id must not be empty")
			}
			if strings.TrimSpace(rulesetID) == "" {
				return errors.New("missing --ruleset-id")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Web Analytics rule %s from ruleset %s?", args[0], rulesetID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: webAnalyticsRulePath(accountID, rulesetID, args[0])}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&rulesetID, "ruleset-id", "", "Web Analytics ruleset identifier")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	_ = cmd.MarkFlagRequired("ruleset-id")
	return cmd
}

func newWebAnalyticsRuleApplyCmd(g *globalOpts) *cobra.Command {
	var rulesetID, rulesJSON, deleteRulesJSON string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Bulk create, update, or delete Web Analytics rules",
		Long: `Apply a batch of rule creates/updates and deletes to a ruleset in one request.

--rules is a JSON array of rule objects (id optional for creates). --delete-rules
is a JSON array of rule ID strings. null and non-array shapes are rejected.

Examples:

  cf web-analytics rule apply --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 \
    --rules '[{"host":"example.com","inclusive":true,"paths":["*"]}]'
  cf web-analytics rule apply --ruleset-id f174e90a-fafe-4643-bbbc-4a0ed4fc8415 \
    --delete-rules '["f174e90a-fafe-4643-bbbc-4a0ed4fc8415"]'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(rulesetID) == "" {
				return errors.New("missing --ruleset-id")
			}
			body, err := buildWebAnalyticsRuleApplyBody(
				cmd.Flags().Changed("rules"), rulesJSON,
				cmd.Flags().Changed("delete-rules"), deleteRulesJSON,
			)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := webAnalyticsAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: webAnalyticsRulesPath(accountID, rulesetID), Body: body}
			return runWebAnalyticsRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&rulesetID, "ruleset-id", "", "Web Analytics ruleset identifier")
	cmd.Flags().StringVar(&rulesJSON, "rules", "", "JSON array of rules to create or update")
	cmd.Flags().StringVar(&deleteRulesJSON, "delete-rules", "", "JSON array of rule IDs to delete")
	_ = cmd.MarkFlagRequired("ruleset-id")
	return cmd
}

func buildWebAnalyticsRuleApplyBody(rulesSet bool, rulesJSON string, deleteSet bool, deleteJSON string) ([]byte, error) {
	if !rulesSet && !deleteSet {
		return nil, errors.New("nothing to apply: pass --rules and/or --delete-rules")
	}
	body := map[string]any{}
	if rulesSet {
		rules, err := parseWebAnalyticsRulesJSON(rulesJSON)
		if err != nil {
			return nil, err
		}
		body["rules"] = rules
	}
	if deleteSet {
		ids, err := parseWebAnalyticsJSONStringArray("delete-rules", deleteJSON)
		if err != nil {
			return nil, err
		}
		if err := validateNonEmptyStrings("delete-rules", ids); err != nil {
			return nil, err
		}
		body["delete_rules"] = ids
	}
	return json.Marshal(body)
}

func runWebAnalyticsRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
