package cli

// Access identity porcelain: identity providers, Access groups, service
// tokens, and Zero Trust users — the "who gets in" half of Cloudflare Access.
// Applications and their policies live in the sibling access-apps shard.
//
// Everything here is account-scoped (`/accounts/{id}/access/...`), the surface
// the Zero Trust dashboard drives. The legacy zone-scoped mirrors
// (`/zones/{id}/access/...`) and the endpoints this porcelain deliberately
// leaves out (service token refresh, SCIM resource listings, per-user failed
// logins and MFA devices) stay reachable through `cf api access`.
//
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// accessIdentityProviderTypes are the identity provider types the Access API
// accepts, in their canonical wire spelling. `--type` is matched
// case-insensitively but always serialized as the canonical value.
var accessIdentityProviderTypes = []string{
	"azureAD", "centrify", "facebook", "github", "google", "google-apps",
	"linkedin", "oidc", "okta", "onelogin", "onetimepin", "pingone",
	"saml", "yandex",
}

// accessIdentityDurationPattern mirrors the service token duration format the
// API documents: one or more <number><unit> pairs using ms, s, m, or h.
var accessIdentityDurationPattern = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?(ms|s|m|h))+$`)

func newAccessIdentityCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Manage Access identity providers, groups, service tokens, and users",
	}
	cmd.AddCommand(
		newAccessIdentityProviderCmd(g),
		newAccessIdentityGroupCmd(g),
		newAccessIdentityServiceTokenCmd(g),
		newAccessIdentityUserCmd(g),
	)
	return cmd
}

// --- shared helpers --------------------------------------------------------

// accessIdentityAccountID validates the resolved account scope. Every command
// in this file is account-scoped.
func accessIdentityAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

// accessIdentityPath builds an account-scoped Access collection path, e.g.
// /accounts/<id>/access/service_tokens.
func accessIdentityPath(accountID, resource string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/access/" + resource
}

// accessIdentityItemPath appends an escaped resource ID to a collection path.
func accessIdentityItemPath(accountID, resource, id string) string {
	return accessIdentityPath(accountID, resource) + "/" + url.PathEscape(id)
}

func runAccessIdentityRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// runAccessIdentityList sends a paginated list request and renders it as a
// table, falling back to JSON when the caller's decoder cannot read the
// result.
func runAccessIdentityList(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request,
	table func(json.RawMessage) ([]string, [][]string, bool)) error {
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
	headers, rows, ok := table(env.Result)
	if !ok {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
	}
	return output.RenderTable(cmd.OutOrStdout(), headers, rows)
}

// accessIdentityReadArg resolves a flag value that may be inline, @file, or @-
// for stdin. Errors name the flag so they say how to fix the invocation.
func accessIdentityReadArg(flag, value string, stdin io.Reader) ([]byte, error) {
	switch {
	case value == "":
		return nil, fmt.Errorf("--%s is required", flag)
	case value == "@-":
		if stdin == nil {
			return nil, fmt.Errorf("read --%s from stdin: no stdin available", flag)
		}
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

// accessIdentityOneStdin rejects invocations where more than one flag wants to
// read stdin, which would silently give the second one an empty value.
func accessIdentityOneStdin(names, values []string) error {
	var used []string
	for i, v := range values {
		if v == "@-" {
			used = append(used, "--"+names[i])
		}
	}
	if len(used) > 1 {
		return fmt.Errorf("only one flag can read stdin (@-); got %s", strings.Join(used, " and "))
	}
	return nil
}

// accessIdentityDecodeJSON decodes JSON into a generic value, keeping numbers
// verbatim so a config value is sent exactly as the user wrote it.
func accessIdentityDecodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("unexpected trailing data after the JSON value")
	}
	return v, nil
}

// accessIdentityJSONObject decodes a flag value that must be a JSON object.
// null, arrays, and scalars all decode without error, so the shape is checked
// rather than trusting the decode.
func accessIdentityJSONObject(flag string, raw []byte) (map[string]any, error) {
	v, err := accessIdentityDecodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("--%s must be a JSON object: %w", flag, err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("--%s must be a JSON object, for example '{\"client_id\":\"...\"}'", flag)
	}
	return obj, nil
}

// accessIdentityRules decodes an Access rule list: a JSON array of non-empty
// rule objects such as [{"email_domain":{"domain":"example.com"}}].
func accessIdentityRules(flag string, raw []byte) ([]any, error) {
	v, err := accessIdentityDecodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array of rule objects: %w", flag, err)
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("--%s must be a JSON array of rule objects, for example '[{\"email_domain\":{\"domain\":\"example.com\"}}]'", flag)
	}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("--%s rule %d must be a JSON object", flag, i+1)
		}
		if len(obj) == 0 {
			return nil, fmt.Errorf("--%s rule %d is empty; every rule needs a type, for example {\"everyone\":{}}", flag, i+1)
		}
	}
	return items, nil
}

// canonicalAccessIdentityProviderType maps case-insensitive user input onto
// the provider type spelling the API expects.
func canonicalAccessIdentityProviderType(s string) (string, bool) {
	for _, t := range accessIdentityProviderTypes {
		if strings.EqualFold(t, s) {
			return t, true
		}
	}
	return "", false
}

// validateAccessIdentityDuration enforces the service token lifetime format
// the API documents, so a typo fails locally instead of as a 400.
func validateAccessIdentityDuration(d string) error {
	if !accessIdentityDurationPattern.MatchString(d) {
		return fmt.Errorf("invalid --duration %q: use a value like 300ms, 90s, 30m, or 8760h", d)
	}
	parsed, err := time.ParseDuration(d)
	if err != nil {
		return fmt.Errorf("invalid --duration %q: %w", d, err)
	}
	if parsed <= 0 {
		return fmt.Errorf("--duration must be greater than zero, got %q", d)
	}
	return nil
}

// validateAccessIdentityEmail checks the address shape locally; the API
// identifies users by email and silently does nothing for a malformed one.
func validateAccessIdentityEmail(email string) error {
	e := strings.TrimSpace(email)
	if e == "" {
		return errors.New("user email must not be empty")
	}
	if strings.ContainsAny(e, " \t\r\n") {
		return fmt.Errorf("invalid email %q: it must not contain whitespace", email)
	}
	at := strings.Index(e, "@")
	if at <= 0 || at != strings.LastIndex(e, "@") || at == len(e)-1 {
		return fmt.Errorf("invalid email %q: expected a single address like user@example.com", email)
	}
	return nil
}

// --- identity providers ----------------------------------------------------

const accessIdentityProvidersResource = "identity_providers"

type accessIdentityProvider struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	ScimConfig struct {
		Enabled bool `json:"enabled"`
	} `json:"scim_config"`
}

func newAccessIdentityProviderCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provider",
		Short: "Manage Access identity providers",
	}
	cmd.AddCommand(
		newAccessIdentityProviderListCmd(g),
		newAccessIdentityProviderGetCmd(g),
		newAccessIdentityProviderCreateCmd(g),
		newAccessIdentityProviderUpdateCmd(g),
		newAccessIdentityProviderDeleteCmd(g),
	)
	return cmd
}

func accessIdentityProviderTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var items []accessIdentityProvider
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(items))
	for _, p := range items {
		rows = append(rows, []string{p.ID, output.Cell(p.Name), p.Type, strconv.FormatBool(p.ScimConfig.Enabled)})
	}
	return []string{"ID", "NAME", "TYPE", "SCIM"}, rows, true
}

func newAccessIdentityProviderListCmd(g *globalOpts) *cobra.Command {
	var scimEnabled bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Access identity providers",
		Long: `List the identity providers configured for Access.

Examples:

  cf access identity provider list
  cf access identity provider list --scim-enabled
  cf access identity provider list --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("scim-enabled") {
				q.Set("scim_enabled", strconv.FormatBool(scimEnabled))
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityPath(accountID, accessIdentityProvidersResource), Query: q}
			return runAccessIdentityList(cmd, g, client, req, accessIdentityProviderTable)
		},
	}
	cmd.Flags().BoolVar(&scimEnabled, "scim-enabled", false, "only providers with SCIM provisioning enabled (or, when false, disabled)")
	return cmd
}

func newAccessIdentityProviderGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <provider-id>",
		Short: "Show one Access identity provider",
		Long:  "Show the full configuration of an Access identity provider.\n\nExamples:\n\n  cf access identity provider get f174e90a-fafe-4643-bbbc-4a0ed4fc8415",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityItemPath(accountID, accessIdentityProvidersResource, args[0])}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// accessIdentityProviderOpts carries the flag values shared by provider
// create and update. Empty strings mean "not given".
type accessIdentityProviderOpts struct {
	name       string
	idpType    string
	config     string
	scimConfig string
	stdin      io.Reader
}

// buildAccessIdentityProviderBody validates the provider contract and returns
// the request body. The API's update is a full replacement, so create and
// update send the same shape.
func buildAccessIdentityProviderBody(o accessIdentityProviderOpts) ([]byte, error) {
	name := strings.TrimSpace(o.name)
	if name == "" {
		return nil, errors.New("identity provider name must not be empty")
	}
	idpType, ok := canonicalAccessIdentityProviderType(strings.TrimSpace(o.idpType))
	if !ok {
		return nil, fmt.Errorf("unknown --type %q (expected one of: %s)", o.idpType, strings.Join(accessIdentityProviderTypes, ", "))
	}
	if err := accessIdentityOneStdin([]string{"config", "scim-config"}, []string{o.config, o.scimConfig}); err != nil {
		return nil, err
	}

	config := map[string]any{}
	if o.config != "" {
		raw, err := accessIdentityReadArg("config", o.config, o.stdin)
		if err != nil {
			return nil, err
		}
		config, err = accessIdentityJSONObject("config", raw)
		if err != nil {
			return nil, err
		}
	}
	if idpType == "onetimepin" {
		if len(config) > 0 {
			return nil, errors.New("--type onetimepin takes no --config: the one-time PIN provider has no settings")
		}
	} else if o.config == "" {
		return nil, fmt.Errorf("--config is required for --type %s: pass the provider settings as JSON", idpType)
	}

	body := map[string]any{"name": name, "type": idpType, "config": config}
	if o.scimConfig != "" {
		raw, err := accessIdentityReadArg("scim-config", o.scimConfig, o.stdin)
		if err != nil {
			return nil, err
		}
		scim, err := accessIdentityJSONObject("scim-config", raw)
		if err != nil {
			return nil, err
		}
		body["scim_config"] = scim
	}
	return json.Marshal(body)
}

func newAccessIdentityProviderCreateCmd(g *globalOpts) *cobra.Command {
	var opts accessIdentityProviderOpts
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Add an Access identity provider",
		Long: `Add an identity provider to Access.

--config carries the provider-specific settings as JSON (inline, @file, or @-
for stdin); every type except onetimepin needs one. --scim-config turns on SCIM
provisioning for providers that support it.

Examples:

  cf access identity provider create "One-time PIN" --type onetimepin
  cf access identity provider create Okta --type okta --config '{"client_id":"abc","client_secret":"s3cret","okta_account":"https://example.okta.com"}'
  cf access identity provider create Entra --type azureAD --config @entra.json --scim-config '{"enabled":true,"user_deprovision":true}'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.name = args[0]
			opts.stdin = cmd.InOrStdin()
			body, err := buildAccessIdentityProviderBody(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: accessIdentityPath(accountID, accessIdentityProvidersResource), Body: body}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&opts.idpType, "type", "", "provider type: "+strings.Join(accessIdentityProviderTypes, ", "))
	cmd.Flags().StringVar(&opts.config, "config", "", "provider settings as JSON: inline, @file, or @- for stdin")
	cmd.Flags().StringVar(&opts.scimConfig, "scim-config", "", "SCIM provisioning settings as JSON: inline, @file, or @- for stdin")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newAccessIdentityProviderUpdateCmd(g *globalOpts) *cobra.Command {
	var opts accessIdentityProviderOpts
	cmd := &cobra.Command{
		Use:   "update <provider-id>",
		Short: "Replace an Access identity provider",
		Long: `Replace an Access identity provider.

The API replaces the whole provider, so --name, --type, and --config (except
for onetimepin) are all required — send the settings you want the provider to
end up with, not just the ones that changed. Omitting --scim-config clears the
SCIM configuration.

Examples:

  cf access identity provider update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 \
    --name Okta --type okta --config @okta.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.stdin = cmd.InOrStdin()
			body, err := buildAccessIdentityProviderBody(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: accessIdentityItemPath(accountID, accessIdentityProvidersResource, args[0]), Body: body}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&opts.name, "name", "", "provider display name")
	cmd.Flags().StringVar(&opts.idpType, "type", "", "provider type: "+strings.Join(accessIdentityProviderTypes, ", "))
	cmd.Flags().StringVar(&opts.config, "config", "", "provider settings as JSON: inline, @file, or @- for stdin")
	cmd.Flags().StringVar(&opts.scimConfig, "scim-config", "", "SCIM provisioning settings as JSON: inline, @file, or @- for stdin")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newAccessIdentityProviderDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <provider-id>",
		Short: "Delete an Access identity provider",
		Long:  "Delete an Access identity provider. Applications that only accept this provider stop letting anyone in.\n\nExamples:\n\n  cf access identity provider delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Access identity provider %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: accessIdentityItemPath(accountID, accessIdentityProvidersResource, args[0])}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- Access groups ---------------------------------------------------------

const accessIdentityGroupsResource = "groups"

type accessIdentityGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault *bool  `json:"is_default"`
	CreatedAt string `json:"created_at"`
}

func newAccessIdentityGroupCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "group",
		Short: "Manage Access groups",
	}
	cmd.AddCommand(
		newAccessIdentityGroupListCmd(g),
		newAccessIdentityGroupGetCmd(g),
		newAccessIdentityGroupCreateCmd(g),
		newAccessIdentityGroupUpdateCmd(g),
		newAccessIdentityGroupDeleteCmd(g),
	)
	return cmd
}

func accessIdentityGroupTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var items []accessIdentityGroup
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(items))
	for _, grp := range items {
		isDefault := ""
		if grp.IsDefault != nil {
			isDefault = strconv.FormatBool(*grp.IsDefault)
		}
		rows = append(rows, []string{grp.ID, output.Cell(grp.Name), isDefault, grp.CreatedAt})
	}
	return []string{"ID", "NAME", "DEFAULT", "CREATED"}, rows, true
}

func newAccessIdentityGroupListCmd(g *globalOpts) *cobra.Command {
	var name, search string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Access groups",
		Long: `List the reusable Access groups on an account.

Examples:

  cf access identity group list
  cf access identity group list --name Engineering
  cf access identity group list --search contractor --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("name") {
				q.Set("name", name)
			}
			if cmd.Flags().Changed("search") {
				q.Set("search", search)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityPath(accountID, accessIdentityGroupsResource), Query: q}
			return runAccessIdentityList(cmd, g, client, req, accessIdentityGroupTable)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter by exact group name")
	cmd.Flags().StringVar(&search, "search", "", "filter by a substring of the group name")
	return cmd
}

func newAccessIdentityGroupGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <group-id>",
		Short: "Show one Access group",
		Long:  "Show an Access group with its include, exclude, and require rules.\n\nExamples:\n\n  cf access identity group get 699d98642c564d2e855e9661899b7252",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityItemPath(accountID, accessIdentityGroupsResource, args[0])}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// accessIdentityGroupOpts carries the flag values shared by group create and
// update. Empty rule strings mean "not given".
type accessIdentityGroupOpts struct {
	name         string
	include      string
	exclude      string
	require      string
	isDefault    bool
	isDefaultSet bool
	stdin        io.Reader
}

// buildAccessIdentityGroupBody validates the group contract and returns the
// request body. The API's update is a full replacement, so create and update
// send the same shape.
func buildAccessIdentityGroupBody(o accessIdentityGroupOpts) ([]byte, error) {
	name := strings.TrimSpace(o.name)
	if name == "" {
		return nil, errors.New("group name must not be empty")
	}
	if err := accessIdentityOneStdin(
		[]string{"include", "exclude", "require"},
		[]string{o.include, o.exclude, o.require}); err != nil {
		return nil, err
	}
	if o.include == "" {
		return nil, errors.New("--include is required: an Access group needs at least one include rule")
	}
	raw, err := accessIdentityReadArg("include", o.include, o.stdin)
	if err != nil {
		return nil, err
	}
	include, err := accessIdentityRules("include", raw)
	if err != nil {
		return nil, err
	}
	if len(include) == 0 {
		return nil, errors.New("--include must contain at least one rule")
	}

	body := map[string]any{"name": name, "include": include}
	for _, f := range []struct{ flag, value string }{{"exclude", o.exclude}, {"require", o.require}} {
		if f.value == "" {
			continue
		}
		raw, err := accessIdentityReadArg(f.flag, f.value, o.stdin)
		if err != nil {
			return nil, err
		}
		rules, err := accessIdentityRules(f.flag, raw)
		if err != nil {
			return nil, err
		}
		body[f.flag] = rules
	}
	if o.isDefaultSet {
		body["is_default"] = o.isDefault
	}
	return json.Marshal(body)
}

// accessIdentityGroupRuleHelp documents the rule grammar once; both create and
// update repeat it because each is read on its own.
const accessIdentityGroupRuleHelp = `Rules are JSON arrays of rule objects and accept an inline string, @file, or @-
for stdin. Common rule types:

  {"everyone":{}}
  {"email":{"email":"jane@example.com"}}
  {"email_domain":{"domain":"example.com"}}
  {"ip":{"ip":"198.51.100.4/32"}}
  {"group":{"id":"699d98642c564d2e855e9661899b7252"}}`

func newAccessIdentityGroupCreateCmd(g *globalOpts) *cobra.Command {
	var opts accessIdentityGroupOpts
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an Access group",
		Long: `Create a reusable Access group.

` + accessIdentityGroupRuleHelp + `

Examples:

  cf access identity group create Employees --include '[{"email_domain":{"domain":"example.com"}}]'
  cf access identity group create Contractors --include @include.json --exclude '[{"email":{"email":"former@example.com"}}]'
  cf access identity group create Everyone --include '[{"everyone":{}}]' --is-default`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.name = args[0]
			opts.isDefaultSet = cmd.Flags().Changed("is-default")
			opts.stdin = cmd.InOrStdin()
			body, err := buildAccessIdentityGroupBody(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: accessIdentityPath(accountID, accessIdentityGroupsResource), Body: body}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	accessIdentityGroupRuleFlags(cmd, &opts)
	return cmd
}

func newAccessIdentityGroupUpdateCmd(g *globalOpts) *cobra.Command {
	var opts accessIdentityGroupOpts
	cmd := &cobra.Command{
		Use:   "update <group-id>",
		Short: "Replace an Access group",
		Long: `Replace an Access group.

The API replaces the whole group, so --name and --include are required — send
the rules you want the group to end up with, not just the ones that changed.
Omitting --exclude or --require clears those rule lists.

` + accessIdentityGroupRuleHelp + `

Examples:

  cf access identity group update 699d98642c564d2e855e9661899b7252 \
    --name Employees --include '[{"email_domain":{"domain":"example.com"}}]'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.isDefaultSet = cmd.Flags().Changed("is-default")
			opts.stdin = cmd.InOrStdin()
			body, err := buildAccessIdentityGroupBody(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: accessIdentityItemPath(accountID, accessIdentityGroupsResource, args[0]), Body: body}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&opts.name, "name", "", "group name")
	accessIdentityGroupRuleFlags(cmd, &opts)
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// accessIdentityGroupRuleFlags registers the rule flags shared by group create
// and update.
func accessIdentityGroupRuleFlags(cmd *cobra.Command, opts *accessIdentityGroupOpts) {
	cmd.Flags().StringVar(&opts.include, "include", "", "include rules as a JSON array: inline, @file, or @- for stdin")
	cmd.Flags().StringVar(&opts.exclude, "exclude", "", "exclude rules as a JSON array: inline, @file, or @- for stdin")
	cmd.Flags().StringVar(&opts.require, "require", "", "require rules as a JSON array: inline, @file, or @- for stdin")
	cmd.Flags().BoolVar(&opts.isDefault, "is-default", false, "make this the default group for new applications")
	_ = cmd.MarkFlagRequired("include")
}

func newAccessIdentityGroupDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <group-id>",
		Short: "Delete an Access group",
		Long:  "Delete an Access group. Policies that reference it stop matching its members.\n\nExamples:\n\n  cf access identity group delete 699d98642c564d2e855e9661899b7252 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Access group %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: accessIdentityItemPath(accountID, accessIdentityGroupsResource, args[0])}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- service tokens --------------------------------------------------------

const accessIdentityServiceTokensResource = "service_tokens"

// accessIdentitySecretNote is printed to stderr after a command whose result
// contains a client secret the API will never show again. Stdout stays
// machine-parseable.
const accessIdentitySecretNote = "note: the client secret is in the result above and is shown only once — store it now"

type accessIdentityServiceToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ClientID  string `json:"client_id"`
	ExpiresAt string `json:"expires_at"`
}

func newAccessIdentityServiceTokenCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service-token",
		Short: "Manage Access service tokens",
	}
	cmd.AddCommand(
		newAccessIdentityServiceTokenListCmd(g),
		newAccessIdentityServiceTokenGetCmd(g),
		newAccessIdentityServiceTokenCreateCmd(g),
		newAccessIdentityServiceTokenUpdateCmd(g),
		newAccessIdentityServiceTokenRotateCmd(g),
		newAccessIdentityServiceTokenDeleteCmd(g),
	)
	return cmd
}

func accessIdentityServiceTokenTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var items []accessIdentityServiceToken
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(items))
	for _, t := range items {
		rows = append(rows, []string{t.ID, output.Cell(t.Name), t.ClientID, t.ExpiresAt})
	}
	return []string{"ID", "NAME", "CLIENT ID", "EXPIRES"}, rows, true
}

func newAccessIdentityServiceTokenListCmd(g *globalOpts) *cobra.Command {
	var name, search string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Access service tokens",
		Long: `List the Access service tokens on an account. Client secrets are never
returned by this endpoint.

Examples:

  cf access identity service-token list
  cf access identity service-token list --search ci --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("name") {
				q.Set("name", name)
			}
			if cmd.Flags().Changed("search") {
				q.Set("search", search)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityPath(accountID, accessIdentityServiceTokensResource), Query: q}
			return runAccessIdentityList(cmd, g, client, req, accessIdentityServiceTokenTable)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter by exact token name")
	cmd.Flags().StringVar(&search, "search", "", "filter by a substring of the token name")
	return cmd
}

func newAccessIdentityServiceTokenGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <token-id>",
		Short: "Show one Access service token",
		Long:  "Show an Access service token's metadata. The client secret is not part of the result.\n\nExamples:\n\n  cf access identity service-token get f174e90a-fafe-4643-bbbc-4a0ed4fc8415",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityItemPath(accountID, accessIdentityServiceTokensResource, args[0])}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// buildAccessIdentityServiceTokenBody validates the service token contract and
// returns the request body. duration is only sent when the user set it, so the
// API's own default applies otherwise.
func buildAccessIdentityServiceTokenBody(name, duration string, durationSet bool) ([]byte, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return nil, errors.New("service token name must not be empty")
	}
	body := map[string]any{"name": n}
	if durationSet {
		if err := validateAccessIdentityDuration(duration); err != nil {
			return nil, err
		}
		body["duration"] = duration
	}
	return json.Marshal(body)
}

func newAccessIdentityServiceTokenCreateCmd(g *globalOpts) *cobra.Command {
	var duration string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an Access service token",
		Long: `Create an Access service token for machine-to-machine access.

The result contains the client secret; the API never shows it again, so store
it when the command runs. --duration takes a value like 300ms, 90s, 30m, or
8760h and defaults to the API's one year.

Examples:

  cf access identity service-token create ci-deploy
  cf access identity service-token create ci-deploy --duration 720h
  cf access identity service-token create ci-deploy --query .client_secret`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildAccessIdentityServiceTokenBody(args[0], duration, cmd.Flags().Changed("duration"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: accessIdentityPath(accountID, accessIdentityServiceTokensResource), Body: body}
			if err := runAccessIdentityRequest(cmd, g, client, req); err != nil {
				return err
			}
			if !g.DryRun {
				fmt.Fprintln(cmd.ErrOrStderr(), accessIdentitySecretNote)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&duration, "duration", "", "token lifetime, e.g. 8760h (default: the API's one year)")
	return cmd
}

func newAccessIdentityServiceTokenUpdateCmd(g *globalOpts) *cobra.Command {
	var name, duration string
	cmd := &cobra.Command{
		Use:   "update <token-id>",
		Short: "Update an Access service token",
		Long: `Update an Access service token's name and lifetime.

The API replaces the token's settings, so pass --duration as well whenever the
token should keep a non-default lifetime. Updating never changes the client ID
or secret; use ` + "`rotate`" + ` for that.

Examples:

  cf access identity service-token update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --name ci-deploy
  cf access identity service-token update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --name ci-deploy --duration 720h`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildAccessIdentityServiceTokenBody(name, duration, cmd.Flags().Changed("duration"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: accessIdentityItemPath(accountID, accessIdentityServiceTokensResource, args[0]), Body: body}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "token name")
	cmd.Flags().StringVar(&duration, "duration", "", "token lifetime, e.g. 8760h")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

func newAccessIdentityServiceTokenRotateCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rotate <token-id>",
		Short: "Rotate an Access service token's client secret",
		Long: `Generate a new client secret for an Access service token.

The previous secret stops working immediately, so every client using it must be
updated. The new secret is in the result and is shown only once.

Examples:

  cf access identity service-token rotate f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Rotate Access service token %s? The current client secret stops working.", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{
				Method: "POST",
				Path:   accessIdentityItemPath(accountID, accessIdentityServiceTokensResource, args[0]) + "/rotate",
			}
			if err := runAccessIdentityRequest(cmd, g, client, req); err != nil {
				return err
			}
			if !g.DryRun {
				fmt.Fprintln(cmd.ErrOrStderr(), accessIdentitySecretNote)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newAccessIdentityServiceTokenDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <token-id>",
		Short: "Delete an Access service token",
		Long:  "Delete an Access service token. Clients using its credentials are locked out immediately.\n\nExamples:\n\n  cf access identity service-token delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Access service token %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: accessIdentityItemPath(accountID, accessIdentityServiceTokensResource, args[0])}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- users -----------------------------------------------------------------

const accessIdentityUsersResource = "users"

// accessIdentityRevokeResource is the organization-level endpoint that revokes
// every Access token a user holds; it identifies the user by email.
const accessIdentityRevokeResource = "organizations/revoke_user"

type accessIdentityUser struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Email               string `json:"email"`
	ActiveDeviceCount   int    `json:"active_device_count"`
	LastSuccessfulLogin string `json:"last_successful_login"`
}

func newAccessIdentityUserCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Inspect Zero Trust users and revoke their sessions",
	}
	cmd.AddCommand(
		newAccessIdentityUserListCmd(g),
		newAccessIdentityUserRevokeSessionsCmd(g),
	)
	return cmd
}

func accessIdentityUserTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var items []accessIdentityUser
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, false
	}
	rows := make([][]string, 0, len(items))
	for _, u := range items {
		rows = append(rows, []string{
			u.ID,
			output.Cell(u.Name),
			output.Cell(u.Email),
			strconv.Itoa(u.ActiveDeviceCount),
			u.LastSuccessfulLogin,
		})
	}
	return []string{"ID", "NAME", "EMAIL", "DEVICES", "LAST LOGIN"}, rows, true
}

func newAccessIdentityUserListCmd(g *globalOpts) *cobra.Command {
	var email, name, search string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Zero Trust users",
		Long: `List the users known to Zero Trust on an account.

Examples:

  cf access identity user list
  cf access identity user list --email jane@example.com
  cf access identity user list --search example.com --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if cmd.Flags().Changed("email") {
				q.Set("email", email)
			}
			if cmd.Flags().Changed("name") {
				q.Set("name", name)
			}
			if cmd.Flags().Changed("search") {
				q.Set("search", search)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: accessIdentityPath(accountID, accessIdentityUsersResource), Query: q}
			return runAccessIdentityList(cmd, g, client, req, accessIdentityUserTable)
		},
	}
	cmd.Flags().StringVar(&email, "email", "", "filter by exact user email")
	cmd.Flags().StringVar(&name, "name", "", "filter by exact user name")
	cmd.Flags().StringVar(&search, "search", "", "filter by a substring of the name or email")
	return cmd
}

// buildAccessIdentityRevokeBody validates the address and returns the revoke
// body, which identifies the user by email rather than by ID. devices is part
// of the body, not the query string, and is only sent when the user set it so
// the API's own default applies otherwise.
func buildAccessIdentityRevokeBody(email string, devices, devicesSet bool) ([]byte, error) {
	if err := validateAccessIdentityEmail(email); err != nil {
		return nil, err
	}
	body := map[string]any{"email": strings.TrimSpace(email)}
	if devicesSet {
		body["devices"] = devices
	}
	return json.Marshal(body)
}

func newAccessIdentityUserRevokeSessionsCmd(g *globalOpts) *cobra.Command {
	var devices, force bool
	cmd := &cobra.Command{
		Use:   "revoke-sessions <email>",
		Short: "Revoke every Access session a user holds",
		Long: `Revoke all Access tokens for a user, signing them out of every application.

The user can sign in again through their identity provider unless a policy
stops them. --devices additionally revokes the user's registered WARP devices.

Examples:

  cf access identity user revoke-sessions jane@example.com --force
  cf access identity user revoke-sessions jane@example.com --devices --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildAccessIdentityRevokeBody(args[0], devices, cmd.Flags().Changed("devices"))
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := accessIdentityAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Revoke all Access sessions for %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{
				Method: "POST",
				Path:   accessIdentityPath(accountID, accessIdentityRevokeResource),
				Body:   body,
			}
			return runAccessIdentityRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&devices, "devices", false, "also revoke the user's registered devices")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}
