package cli

// Access application porcelain: the applications Cloudflare Access protects
// and the policies attached to each one. Applications exist under both an
// account and a zone, so every command takes --scope like logpush does.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// accessAppTypes are the application types the API accepts.
var accessAppTypes = []string{
	"self_hosted", "saas", "ssh", "vnc", "app_launcher", "warp", "biso",
	"bookmark", "dash_sso", "infrastructure", "rdp", "mcp", "mcp_portal",
	"proxy_endpoint",
}

// accessAppUnsupportedTypes need request fields this porcelain does not model
// (a full SaaS app configuration, infrastructure target criteria). They stay
// reachable through the generated plumbing.
var accessAppUnsupportedTypes = map[string]string{
	"saas":           "a saas_app configuration",
	"infrastructure": "target_criteria",
	"rdp":            "target_criteria",
}

// accessAppDomainRequiredTypes must be created with a domain; the remaining
// accessAppDomainTypes may carry one. For every other type the domain is
// read-only — Cloudflare assigns it.
var (
	accessAppDomainRequiredTypes = []string{"self_hosted", "ssh", "vnc"}
	accessAppDomainTypes         = []string{"self_hosted", "ssh", "vnc", "bookmark", "mcp_portal", "proxy_endpoint"}
)

// accessPolicyDecisions are the actions a policy can take on a match.
var accessPolicyDecisions = []string{"allow", "deny", "non_identity", "bypass"}

// Access replaces the whole object on update, so `update` reads the current
// one and sends it back with the changes applied. These fields are assigned by
// the API and are dropped from that round trip.
var (
	accessAppReadOnlyFields    = []string{"id", "uid", "aud", "created_at", "updated_at", "policies"}
	accessPolicyReadOnlyFields = []string{"id", "uid", "created_at", "updated_at", "account_id", "zone_id", "app_count", "reusable"}
)

// accessAppsMinPrecedence is the first slot in a policy evaluation order; the
// API numbers policies from 1.
const accessAppsMinPrecedence = 1

type accessApp struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Type   string `json:"type,omitempty"`
	Domain string `json:"domain,omitempty"`
	AUD    string `json:"aud,omitempty"`
}

type accessAppPolicy struct {
	ID              string `json:"id,omitempty"`
	Name            string `json:"name,omitempty"`
	Decision        string `json:"decision,omitempty"`
	Precedence      int    `json:"precedence,omitempty"`
	SessionDuration string `json:"session_duration,omitempty"`
}

func newAccessAppsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "app",
		Short: "Manage Access applications and their policies",
	}
	cmd.AddCommand(
		newAccessAppsListCmd(g),
		newAccessAppsGetCmd(g),
		newAccessAppsCreateCmd(g),
		newAccessAppsUpdateCmd(g),
		newAccessAppsDeleteCmd(g),
		newAccessAppsPolicyCmd(g),
	)
	return cmd
}

func newAccessAppsPolicyCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage the policies of one Access application",
	}
	cmd.AddCommand(
		newAccessAppsPolicyListCmd(g),
		newAccessAppsPolicyGetCmd(g),
		newAccessAppsPolicyCreateCmd(g),
		newAccessAppsPolicyUpdateCmd(g),
		newAccessAppsPolicyDeleteCmd(g),
	)
	return cmd
}

// accessAppsScope selects the account or zone the applications belong to.
type accessAppsScope struct {
	scope string
	zone  string
}

func addAccessAppsScopeFlags(cmd *cobra.Command, scope *accessAppsScope) {
	cmd.Flags().StringVar(&scope.scope, "scope", "account", "resource scope: account or zone")
	cmd.Flags().StringVar(&scope.zone, "zone", "", "zone name or ID (used with --scope zone; default: configured zone, or pick one interactively)")
}

// validateAccessAppsScope checks the scope flags on their own — no config, no
// client, no network — so a bad --scope or a --zone in the wrong scope is
// reported before anything is built.
func validateAccessAppsScope(scope accessAppsScope) error {
	switch scope.scope {
	case "account":
		if scope.zone != "" {
			return errors.New("--zone requires --scope zone")
		}
		return nil
	case "zone":
		return nil
	default:
		return fmt.Errorf("--scope must be account or zone (got %q)", scope.scope)
	}
}

// resolveAccessAppsPath turns the explicit porcelain scope into the API prefix
// for applications. Zone scope goes through resolveZoneInteractive, the
// standard resolver for zone-scoped porcelain: explicit --zone, then the
// configured zone, then an inline picker on a terminal.
func resolveAccessAppsPath(cmd *cobra.Command, g *globalOpts, scope accessAppsScope) (*api.Client, string, error) {
	if err := validateAccessAppsScope(scope); err != nil {
		return nil, "", err
	}
	client, cfg, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	if scope.scope == "zone" {
		zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, scope.zone)
		if err != nil {
			return nil, "", err
		}
		return client, "/zones/" + url.PathEscape(zoneID) + "/access/apps", nil
	}
	if cfg.AccountID == "" {
		return nil, "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return client, "/accounts/" + url.PathEscape(cfg.AccountID) + "/access/apps", nil
}

func accessAppPath(prefix, appID string) string {
	return prefix + "/" + url.PathEscape(appID)
}

func accessAppPoliciesPath(prefix, appID string) string {
	return accessAppPath(prefix, appID) + "/policies"
}

func accessAppPolicyPath(prefix, appID, policyID string) string {
	return accessAppPoliciesPath(prefix, appID) + "/" + url.PathEscape(policyID)
}

// accessAppsID checks a positional ID before it is spliced into a URL, so a
// stray argument fails with a message instead of a 404.
func accessAppsID(kind, value string) (string, error) {
	id := strings.TrimSpace(value)
	if id == "" {
		return "", fmt.Errorf("%s ID must not be empty", kind)
	}
	if strings.ContainsAny(id, " \t\n/") {
		return "", fmt.Errorf("invalid %s ID %q: expected a UUID", kind, value)
	}
	return id, nil
}

// accessAppsListFilterFlags are the `list` filters the account endpoint
// accepts as query parameters. The zone endpoint takes none of them.
var accessAppsListFilterFlags = []string{"name", "domain", "aud", "search", "exact"}

// rejectAccessAppsZoneFilters fails a zone-scoped listing that was given an
// account-only filter, instead of sending a parameter the endpoint ignores and
// returning a list the caller believes was filtered.
func rejectAccessAppsZoneFilters(cmd *cobra.Command, scope accessAppsScope) error {
	if scope.scope != "zone" {
		return nil
	}
	for _, flag := range accessAppsListFilterFlags {
		if cmd.Flags().Changed(flag) {
			return fmt.Errorf("--%s is not supported with --scope zone: the zone application list endpoint takes no filters (use --scope account, or filter the output with --query)", flag)
		}
	}
	return nil
}

func newAccessAppsListCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var name, domain, aud, search string
	var exact bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Access applications",
		Long: `List the Access applications in an account or zone.

The filters below are account-scoped: the zone endpoint takes no query
parameters, so under --scope zone they are rejected rather than silently
ignored. Filter a zone listing with --query instead.

--exact narrows --name and --domain to exact string matches, so it needs one
of them.

Examples:

  cf access app list --account-id $ACCOUNT_ID
  cf access app list --name "Internal wiki" --exact
  cf access app list --scope zone --zone example.com
  cf access app list --scope zone --zone example.com --query '[.[] | select(.type == "self_hosted")]'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateAccessAppsScope(scope); err != nil {
				return err
			}
			if err := rejectAccessAppsZoneFilters(cmd, scope); err != nil {
				return err
			}
			q := url.Values{}
			for _, f := range []struct{ flag, value string }{
				{"name", name},
				{"domain", domain},
				{"aud", aud},
				{"search", search},
			} {
				if cmd.Flags().Changed(f.flag) {
					if strings.TrimSpace(f.value) == "" {
						return fmt.Errorf("--%s must not be empty", f.flag)
					}
					q.Set(f.flag, f.value)
				}
			}
			if cmd.Flags().Changed("exact") {
				if !cmd.Flags().Changed("name") && !cmd.Flags().Changed("domain") {
					return errors.New("--exact filters --name or --domain: pass one of them")
				}
				q.Set("exact", strconv.FormatBool(exact))
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: prefix, Query: q}
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
			var apps []accessApp
			if err := json.Unmarshal(env.Result, &apps); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(apps))
			for _, app := range apps {
				rows = append(rows, []string{app.ID, output.Cell(app.Name), app.Type, output.Cell(app.Domain), app.AUD})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "TYPE", "DOMAIN", "AUD"}, rows)
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	cmd.Flags().StringVar(&name, "name", "", "filter by application name (--scope account only)")
	cmd.Flags().StringVar(&domain, "domain", "", "filter by application domain (--scope account only)")
	cmd.Flags().StringVar(&aud, "aud", "", "filter by application audience (AUD) tag (--scope account only)")
	cmd.Flags().StringVar(&search, "search", "", "search applications by name or domain (--scope account only)")
	cmd.Flags().BoolVar(&exact, "exact", false, "match --name and --domain exactly instead of by substring (--scope account only)")
	return cmd
}

func newAccessAppsGetCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	cmd := &cobra.Command{
		Use:   "get <app-id>",
		Short: "Show one Access application",
		Long:  "Show one Access application.\n\nExamples:\n\n  cf access app get f174e90a-fafe-4643-bbbc-4a0ed4fc8415\n  cf access app get f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --scope zone --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, err := accessAppsID("application", args[0])
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "GET", Path: accessAppPath(prefix, appID)})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	return cmd
}

// accessAppOptions carries the application flags shared by create and update.
type accessAppOptions struct {
	appType                 string
	name                    string
	domain                  string
	sessionDuration         string
	allowedIDPs             []string
	tags                    []string
	logoURL                 string
	customDenyMessage       string
	customDenyURL           string
	appLauncherVisible      bool
	autoRedirectToIdentity  bool
	enableBindingCookie     bool
	httpOnlyCookieAttribute bool
	skipInterstitial        bool
}

func addAccessAppFlags(cmd *cobra.Command, o *accessAppOptions, create bool) {
	flags := cmd.Flags()
	if create {
		flags.StringVar(&o.appType, "type", "self_hosted", "application type: "+strings.Join(accessAppTypes, ", "))
	} else {
		flags.StringVar(&o.name, "name", "", "application name")
	}
	flags.StringVar(&o.domain, "domain", "", "primary hostname and path secured by Access")
	flags.StringVar(&o.sessionDuration, "session-duration", "", "how long issued tokens stay valid, for example 24h or 2h45m")
	flags.StringArrayVar(&o.allowedIDPs, "allowed-idp", nil, "identity provider ID users may pick (repeatable; sets the whole list)")
	flags.StringArrayVar(&o.tags, "tag", nil, "App Launcher tag (repeatable; sets the whole list)")
	flags.StringVar(&o.logoURL, "logo-url", "", "image URL for the App Launcher logo")
	flags.StringVar(&o.customDenyMessage, "custom-deny-message", "", "message shown to users who are denied access")
	flags.StringVar(&o.customDenyURL, "custom-deny-url", "", "URL users are redirected to when denied access")
	flags.BoolVar(&o.appLauncherVisible, "app-launcher-visible", false, "show the application in the App Launcher")
	flags.BoolVar(&o.autoRedirectToIdentity, "auto-redirect-to-identity", false, "skip the identity provider selection screen")
	flags.BoolVar(&o.enableBindingCookie, "enable-binding-cookie", false, "enable the binding cookie for extra CSRF protection")
	flags.BoolVar(&o.httpOnlyCookieAttribute, "http-only-cookie-attribute", false, "set the HttpOnly attribute on the Access cookie")
	flags.BoolVar(&o.skipInterstitial, "skip-interstitial", false, "authenticate through cloudflared without the interstitial page")
}

// accessAppFieldChanges validates and collects every application field the
// caller actually passed. Name, type, and domain are handled by the callers,
// which apply different rules on create and update.
func accessAppFieldChanges(cmd *cobra.Command, o accessAppOptions) (map[string]any, error) {
	changes := map[string]any{}
	if cmd.Flags().Changed("session-duration") {
		if err := validateAccessAppsDuration("session-duration", o.sessionDuration); err != nil {
			return nil, err
		}
		changes["session_duration"] = o.sessionDuration
	}
	if cmd.Flags().Changed("allowed-idp") {
		if err := validateNonEmptyStrings("allowed-idp", o.allowedIDPs); err != nil {
			return nil, err
		}
		changes["allowed_idps"] = o.allowedIDPs
	}
	if cmd.Flags().Changed("tag") {
		if err := validateNonEmptyStrings("tag", o.tags); err != nil {
			return nil, err
		}
		changes["tags"] = o.tags
	}
	for _, f := range []struct{ flag, key, value string }{
		{"logo-url", "logo_url", o.logoURL},
		{"custom-deny-url", "custom_deny_url", o.customDenyURL},
	} {
		if cmd.Flags().Changed(f.flag) {
			if err := validateAccessAppsURL(f.flag, f.value); err != nil {
				return nil, err
			}
			changes[f.key] = f.value
		}
	}
	if cmd.Flags().Changed("custom-deny-message") {
		changes["custom_deny_message"] = o.customDenyMessage
	}
	for _, f := range []struct {
		flag, key string
		value     bool
	}{
		{"app-launcher-visible", "app_launcher_visible", o.appLauncherVisible},
		{"auto-redirect-to-identity", "auto_redirect_to_identity", o.autoRedirectToIdentity},
		{"enable-binding-cookie", "enable_binding_cookie", o.enableBindingCookie},
		{"http-only-cookie-attribute", "http_only_cookie_attribute", o.httpOnlyCookieAttribute},
		{"skip-interstitial", "skip_interstitial", o.skipInterstitial},
	} {
		if cmd.Flags().Changed(f.flag) {
			changes[f.key] = f.value
		}
	}
	return changes, nil
}

// validateAccessAppsDuration enforces the session duration format the API
// documents (300ms, 2h45m; units ns, us, ms, s, m, h).
func validateAccessAppsDuration(flag, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("--%s must not be empty", flag)
	}
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return fmt.Errorf("--%s must be a positive duration such as 24h or 2h45m, got %q", flag, value)
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("--%s must be a duration such as 300ms, 24h, or 2h45m (units ns, us, ms, s, m, h), got %q", flag, value)
	}
	if d <= 0 {
		return fmt.Errorf("--%s must be a positive duration such as 24h or 2h45m, got %q", flag, value)
	}
	return nil
}

// validateAccessAppsURL accepts an empty value (which clears the field) and
// otherwise requires an absolute http(s) URL.
func validateAccessAppsURL(flag, value string) error {
	if value == "" {
		return nil
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("--%s must be an absolute http or https URL, got %q", flag, value)
	}
	return nil
}

// buildAccessAppCreateBody assembles the create body and enforces the type
// rules the API documents: some types need a domain and some assign their own.
func buildAccessAppCreateBody(cmd *cobra.Command, name string, o accessAppOptions) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("application name must not be empty")
	}
	if !accessAppsContains(accessAppTypes, o.appType) {
		return nil, fmt.Errorf("unknown --type %q (expected one of: %s)", o.appType, strings.Join(accessAppTypes, ", "))
	}
	if needs, ok := accessAppUnsupportedTypes[o.appType]; ok {
		return nil, fmt.Errorf("--type %s needs %s, which this command does not build; use `cf api access apps-create-account` (or apps-create-zone) instead", o.appType, needs)
	}
	changes, err := accessAppFieldChanges(cmd, o)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name, "type": o.appType}
	if err := applyAccessAppDomain(body, o.appType, o.domain, cmd.Flags().Changed("domain"), true); err != nil {
		return nil, err
	}
	for k, v := range changes {
		body[k] = v
	}
	return json.Marshal(body)
}

// applyAccessAppDomain writes the domain into target when the type accepts
// one, and reports the types that require or reject it. On create a missing
// domain is an error for the types that need one; on update the stored
// application already has it. An unknown type (an application shape this
// build does not know) is left to the API.
func applyAccessAppDomain(target map[string]any, appType, domain string, changed, create bool) error {
	required := accessAppsContains(accessAppDomainRequiredTypes, appType)
	known := accessAppsContains(accessAppTypes, appType)
	allowed := accessAppsContains(accessAppDomainTypes, appType) || !known
	if !changed {
		if create && required {
			return fmt.Errorf("--domain is required for %s applications", appType)
		}
		return nil
	}
	if !allowed {
		return fmt.Errorf("--domain is not supported for %s applications: Cloudflare assigns that application's domain", appType)
	}
	if required && strings.TrimSpace(domain) == "" {
		return fmt.Errorf("--domain must not be empty for %s applications", appType)
	}
	target["domain"] = domain
	return nil
}

func newAccessAppsCreateCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var o accessAppOptions
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an Access application",
		Long: `Create an Access application.

Self-hosted, ssh, and vnc applications need --domain; bookmark, mcp_portal,
and proxy_endpoint applications may have one. Cloudflare assigns the domain
for the remaining types, so --domain is rejected there.

Policies are created separately, with ` + "`cf access app policy create`" + `.

Examples:

  cf access app create "Internal wiki" --domain wiki.example.com
  cf access app create "SSH bastion" --type ssh --domain ssh.example.com --session-duration 2h45m
  cf access app create Runbooks --type bookmark --domain https://runbooks.example.com --scope zone --zone example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildAccessAppCreateBody(cmd, args[0], o)
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "POST", Path: prefix, Body: body})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	addAccessAppFlags(cmd, &o, true)
	return cmd
}

func newAccessAppsUpdateCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var o accessAppOptions
	cmd := &cobra.Command{
		Use:   "update <app-id>",
		Short: "Update fields of an Access application",
		Long: `Update fields of an Access application.

The API replaces the whole application, so this command first reads it and
re-sends it with your changes applied; fields you do not pass keep their
current values. Its policies are left alone. The application type cannot be
changed: delete the application and create it again instead. --dry-run
performs the read but never sends the write.

Examples:

  cf access app update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --session-duration 12h
  cf access app update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --name "Internal wiki" --app-launcher-visible=false`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, err := accessAppsID("application", args[0])
			if err != nil {
				return err
			}
			changes, err := accessAppFieldChanges(cmd, o)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("name") {
				if strings.TrimSpace(o.name) == "" {
					return errors.New("--name must not be empty")
				}
				changes["name"] = o.name
			}
			if len(changes) == 0 && !cmd.Flags().Changed("domain") {
				return errors.New("nothing to update: pass at least one application flag")
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			path := accessAppPath(prefix, appID)
			current, err := accessAppsReadObject(cmd, client, path, "application "+appID)
			if err != nil {
				return err
			}
			// The domain rules depend on the application's type, which only
			// the stored application knows.
			appType, _ := current["type"].(string)
			if err := applyAccessAppDomain(changes, appType, o.domain, cmd.Flags().Changed("domain"), false); err != nil {
				return err
			}
			body, err := accessAppsReplacementBody(current, accessAppReadOnlyFields, changes)
			if err != nil {
				return err
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: body})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	addAccessAppFlags(cmd, &o, false)
	return cmd
}

func newAccessAppsDeleteCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <app-id>",
		Short: "Delete an Access application",
		Long:  "Delete an Access application and every policy attached to it.\n\nExamples:\n\n  cf access app delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, err := accessAppsID("application", args[0])
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Access application %s and its policies?", appID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "DELETE", Path: accessAppPath(prefix, appID)})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newAccessAppsPolicyListCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	cmd := &cobra.Command{
		Use:   "list <app-id>",
		Short: "List the policies of an Access application",
		Long:  "List the policies attached to an Access application, in evaluation order.\n\nExamples:\n\n  cf access app policy list f174e90a-fafe-4643-bbbc-4a0ed4fc8415",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, err := accessAppsID("application", args[0])
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessAppPoliciesPath(prefix, appID)}
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
			var policies []accessAppPolicy
			if err := json.Unmarshal(env.Result, &policies); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(policies))
			for _, p := range policies {
				precedence := ""
				if p.Precedence != 0 {
					precedence = strconv.Itoa(p.Precedence)
				}
				rows = append(rows, []string{p.ID, output.Cell(p.Name), p.Decision, precedence, p.SessionDuration})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "DECISION", "PRECEDENCE", "SESSION"}, rows)
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	return cmd
}

func newAccessAppsPolicyGetCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	cmd := &cobra.Command{
		Use:   "get <app-id> <policy-id>",
		Short: "Show one policy of an Access application",
		Long:  "Show one policy of an Access application.\n\nExamples:\n\n  cf access app policy get f174e90a-fafe-4643-bbbc-4a0ed4fc8415 699d98642c564d2e855e9661899b7252",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, policyID, err := accessAppsPolicyArgs(args)
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "GET", Path: accessAppPolicyPath(prefix, appID, policyID)})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	return cmd
}

func accessAppsPolicyArgs(args []string) (appID, policyID string, err error) {
	appID, err = accessAppsID("application", args[0])
	if err != nil {
		return "", "", err
	}
	policyID, err = accessAppsID("policy", args[1])
	if err != nil {
		return "", "", err
	}
	return appID, policyID, nil
}

// accessPolicyOptions carries the policy flags shared by create and update.
type accessPolicyOptions struct {
	name                         string
	decision                     string
	include                      string
	exclude                      string
	require                      string
	includeEveryone              bool
	includeEmails                []string
	includeEmailDomains          []string
	includeGroups                []string
	precedence                   int
	sessionDuration              string
	purposeJustificationRequired bool
	purposeJustificationPrompt   string
	approvalRequired             bool
	isolationRequired            bool
	stdin                        io.Reader
}

func addAccessPolicyFlags(cmd *cobra.Command, o *accessPolicyOptions) {
	flags := cmd.Flags()
	flags.StringVar(&o.name, "name", "", "policy name")
	flags.StringVar(&o.decision, "decision", "", "action on a match: "+strings.Join(accessPolicyDecisions, ", "))
	flags.StringVar(&o.include, "include", "", "include rules as a JSON array: inline, @file, or @- for stdin")
	flags.StringVar(&o.exclude, "exclude", "", "exclude rules as a JSON array: inline, @file, or @- for stdin")
	flags.StringVar(&o.require, "require", "", "require rules as a JSON array: inline, @file, or @- for stdin")
	flags.BoolVar(&o.includeEveryone, "include-everyone", false, "add an include rule matching every user")
	flags.StringArrayVar(&o.includeEmails, "include-email", nil, "add an include rule for one email address (repeatable)")
	flags.StringArrayVar(&o.includeEmailDomains, "include-email-domain", nil, "add an include rule for one email domain (repeatable)")
	flags.StringArrayVar(&o.includeGroups, "include-group", nil, "add an include rule for one Access group ID (repeatable)")
	flags.IntVar(&o.precedence, "precedence", 0, "evaluation order within the application (1 is evaluated first)")
	flags.StringVar(&o.sessionDuration, "session-duration", "", "how long issued tokens stay valid, for example 24h or 2h45m")
	flags.BoolVar(&o.purposeJustificationRequired, "purpose-justification-required", false, "ask users to justify why they need access")
	flags.StringVar(&o.purposeJustificationPrompt, "purpose-justification-prompt", "", "message shown on the purpose justification screen")
	flags.BoolVar(&o.approvalRequired, "approval-required", false, "require an administrator to approve each session")
	flags.BoolVar(&o.isolationRequired, "isolation-required", false, "serve the application in an isolated browser")
}

// accessPolicyFieldChanges validates and collects every policy field the
// caller passed, including the rule lists assembled from --include, its
// shorthands, --exclude, and --require.
func accessPolicyFieldChanges(cmd *cobra.Command, o accessPolicyOptions) (map[string]any, error) {
	if err := validateAccessPolicyStdinUse(cmd, o); err != nil {
		return nil, err
	}
	changes := map[string]any{}
	if cmd.Flags().Changed("name") {
		if strings.TrimSpace(o.name) == "" {
			return nil, errors.New("--name must not be empty")
		}
		changes["name"] = o.name
	}
	if cmd.Flags().Changed("decision") {
		if !accessAppsContains(accessPolicyDecisions, o.decision) {
			return nil, fmt.Errorf("unknown --decision %q (expected one of: %s)", o.decision, strings.Join(accessPolicyDecisions, ", "))
		}
		changes["decision"] = o.decision
	}
	for _, kind := range []string{"include", "exclude", "require"} {
		rules, set, err := buildAccessPolicyRules(cmd, o, kind)
		if err != nil {
			return nil, err
		}
		if !set {
			continue
		}
		if kind == "include" && len(rules) == 0 {
			return nil, errors.New("--include must contain at least one rule")
		}
		changes[kind] = rules
	}
	if cmd.Flags().Changed("precedence") {
		if o.precedence < accessAppsMinPrecedence {
			return nil, fmt.Errorf("--precedence must be %d or greater, got %d", accessAppsMinPrecedence, o.precedence)
		}
		changes["precedence"] = o.precedence
	}
	if cmd.Flags().Changed("session-duration") {
		if err := validateAccessAppsDuration("session-duration", o.sessionDuration); err != nil {
			return nil, err
		}
		changes["session_duration"] = o.sessionDuration
	}
	if cmd.Flags().Changed("purpose-justification-prompt") {
		changes["purpose_justification_prompt"] = o.purposeJustificationPrompt
	}
	for _, f := range []struct {
		flag, key string
		value     bool
	}{
		{"purpose-justification-required", "purpose_justification_required", o.purposeJustificationRequired},
		{"approval-required", "approval_required", o.approvalRequired},
		{"isolation-required", "isolation_required", o.isolationRequired},
	} {
		if cmd.Flags().Changed(f.flag) {
			changes[f.key] = f.value
		}
	}
	return changes, nil
}

// validateAccessPolicyStdinUse rejects two rule flags both reading stdin,
// which would give the second one an empty document.
func validateAccessPolicyStdinUse(cmd *cobra.Command, o accessPolicyOptions) error {
	var fromStdin []string
	for _, f := range []struct{ flag, value string }{
		{"include", o.include},
		{"exclude", o.exclude},
		{"require", o.require},
	} {
		if cmd.Flags().Changed(f.flag) && f.value == "@-" {
			fromStdin = append(fromStdin, "--"+f.flag)
		}
	}
	if len(fromStdin) > 1 {
		return fmt.Errorf("%s cannot all read stdin (@-): pass at most one as @-", strings.Join(fromStdin, " and "))
	}
	return nil
}

// buildAccessPolicyRules assembles one rule list. JSON rules come first, then
// the --include-* shorthands in a fixed order so the wire body is
// deterministic. The bool reports whether the caller supplied the list at all.
func buildAccessPolicyRules(cmd *cobra.Command, o accessPolicyOptions, kind string) ([]any, bool, error) {
	var value string
	switch kind {
	case "include":
		value = o.include
	case "exclude":
		value = o.exclude
	default:
		value = o.require
	}
	rules := []any{}
	set := false
	if cmd.Flags().Changed(kind) {
		raw, err := accessAppsReadArg(kind, value, cmd.InOrStdin())
		if err != nil {
			return nil, false, err
		}
		decoded, err := decodeAccessPolicyRules(kind, raw)
		if err != nil {
			return nil, false, err
		}
		rules = append(rules, decoded...)
		set = true
	}
	if kind != "include" {
		return rules, set, nil
	}
	if cmd.Flags().Changed("include-everyone") && o.includeEveryone {
		rules = append(rules, map[string]any{"everyone": map[string]any{}})
		set = true
	}
	for _, shorthand := range []struct {
		flag, key, field string
		values           []string
	}{
		{"include-email", "email", "email", o.includeEmails},
		{"include-email-domain", "email_domain", "domain", o.includeEmailDomains},
		{"include-group", "group", "id", o.includeGroups},
	} {
		if !cmd.Flags().Changed(shorthand.flag) {
			continue
		}
		for i, v := range shorthand.values {
			if err := validateAccessPolicyRuleValue(shorthand.flag, v, i); err != nil {
				return nil, false, err
			}
			rules = append(rules, map[string]any{shorthand.key: map[string]any{shorthand.field: v}})
			set = true
		}
	}
	return rules, set, nil
}

// validateAccessPolicyRuleValue checks one shorthand rule value so a typo
// fails locally instead of creating a policy that matches nobody.
func validateAccessPolicyRuleValue(flag, value string, index int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("--%s value at position %d is empty", flag, index+1)
	}
	if strings.ContainsAny(value, " \t\n") {
		return fmt.Errorf("--%s value %q must not contain whitespace", flag, value)
	}
	switch flag {
	case "include-email":
		at := strings.Index(value, "@")
		if at <= 0 || at == len(value)-1 {
			return fmt.Errorf("--%s value %q is not an email address", flag, value)
		}
	case "include-email-domain":
		if strings.Contains(value, "@") {
			return fmt.Errorf("--%s value %q is an email address; pass just the domain, for example example.com", flag, value)
		}
	}
	return nil
}

// decodeAccessPolicyRules requires a JSON array of non-empty objects, so
// null, a bare object, or a list of strings is rejected here rather than
// silently becoming an empty rule set.
func decodeAccessPolicyRules(flag string, raw []byte) ([]any, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array of rule objects: %w", flag, err)
	}
	list, ok := decoded.([]any)
	if !ok {
		return nil, fmt.Errorf(`--%s must be a JSON array of rule objects, for example '[{"email_domain":{"domain":"example.com"}}]'`, flag)
	}
	for i, item := range list {
		obj, ok := item.(map[string]any)
		if !ok || len(obj) == 0 {
			return nil, fmt.Errorf("--%s rule %d must be a non-empty JSON object", flag, i+1)
		}
	}
	return list, nil
}

func newAccessAppsPolicyCreateCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var o accessPolicyOptions
	cmd := &cobra.Command{
		Use:   "create <app-id>",
		Short: "Create a policy on an Access application",
		Long: `Create a policy on an Access application.

Include rules come from --include (a JSON array) plus the --include-*
shorthands, which are appended in the order everyone, email, email-domain,
group. A policy needs at least one include rule. --exclude and --require take
JSON arrays only; every rule flag also accepts @file or @- for stdin.

Examples:

  cf access app policy create f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --name Engineers --decision allow --include-email-domain example.com
  cf access app policy create f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --name Contractors --decision allow --precedence 2 \
    --include '[{"group":{"id":"aa0a4aab-672b-4bdb-bc33-a59f1130a11f"}}]' --require '[{"any_valid_service_token":{}}]'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, err := accessAppsID("application", args[0])
			if err != nil {
				return err
			}
			o.stdin = cmd.InOrStdin()
			changes, err := accessPolicyFieldChanges(cmd, o)
			if err != nil {
				return err
			}
			if _, ok := changes["include"]; !ok {
				return errors.New("a policy needs at least one include rule: pass --include or an --include-* shorthand")
			}
			body, err := json.Marshal(changes)
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "POST", Path: accessAppPoliciesPath(prefix, appID), Body: body})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	addAccessPolicyFlags(cmd, &o)
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("decision")
	return cmd
}

func newAccessAppsPolicyUpdateCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var o accessPolicyOptions
	cmd := &cobra.Command{
		Use:   "update <app-id> <policy-id>",
		Short: "Update fields of an Access application policy",
		Long: `Update fields of an Access application policy.

The API replaces the whole policy, so this command first reads it and re-sends
it with your changes applied; fields you do not pass keep their current values.
Passing any include source replaces the whole include list, and --exclude or
--require replace theirs. --dry-run performs the read but never sends the
write.

Examples:

  cf access app policy update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 699d98642c564d2e855e9661899b7252 --decision deny
  cf access app policy update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 699d98642c564d2e855e9661899b7252 --include-email-domain example.com --include-email oncall@example.com`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, policyID, err := accessAppsPolicyArgs(args)
			if err != nil {
				return err
			}
			o.stdin = cmd.InOrStdin()
			changes, err := accessPolicyFieldChanges(cmd, o)
			if err != nil {
				return err
			}
			if len(changes) == 0 {
				return errors.New("nothing to update: pass at least one policy flag")
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			path := accessAppPolicyPath(prefix, appID, policyID)
			current, err := accessAppsReadObject(cmd, client, path, "policy "+policyID)
			if err != nil {
				return err
			}
			body, err := accessAppsReplacementBody(current, accessPolicyReadOnlyFields, changes)
			if err != nil {
				return err
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: body})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	addAccessPolicyFlags(cmd, &o)
	return cmd
}

func newAccessAppsPolicyDeleteCmd(g *globalOpts) *cobra.Command {
	var scope accessAppsScope
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <app-id> <policy-id>",
		Short: "Delete a policy from an Access application",
		Long:  "Delete a policy from an Access application.\n\nExamples:\n\n  cf access app policy delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 699d98642c564d2e855e9661899b7252 --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			appID, policyID, err := accessAppsPolicyArgs(args)
			if err != nil {
				return err
			}
			client, prefix, err := resolveAccessAppsPath(cmd, g, scope)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete policy %s from Access application %s?", policyID, appID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runAccessAppsRequest(cmd, g, client, api.Request{Method: "DELETE", Path: accessAppPolicyPath(prefix, appID, policyID)})
		},
	}
	addAccessAppsScopeFlags(cmd, &scope)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// accessAppsReadObject fetches the current object an update has to re-send.
func accessAppsReadObject(cmd *cobra.Command, client *api.Client, path, what string) (map[string]any, error) {
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

// accessAppsReplacementBody drops the fields the API assigns and applies the
// caller's changes on top of the stored object.
func accessAppsReplacementBody(current map[string]any, readOnly []string, changes map[string]any) ([]byte, error) {
	for _, field := range readOnly {
		delete(current, field)
	}
	for k, v := range changes {
		current[k] = v
	}
	return json.Marshal(current)
}

// accessAppsReadArg resolves a flag value that may be inline, @file, or @- for
// stdin. Errors name the flag so they say how to fix the invocation.
func accessAppsReadArg(flag, value string, stdin io.Reader) ([]byte, error) {
	switch {
	case value == "":
		return nil, fmt.Errorf("--%s must not be empty", flag)
	case value == "@-":
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read --%s from stdin: %w", flag, err)
		}
		return raw, nil
	case strings.HasPrefix(value, "@"):
		path := strings.TrimPrefix(value, "@")
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --%s from %s: %w", flag, path, err)
		}
		return raw, nil
	default:
		return []byte(value), nil
	}
}

func accessAppsContains(allowed []string, v string) bool {
	for _, a := range allowed {
		if a == v {
			return true
		}
	}
	return false
}

func runAccessAppsRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
