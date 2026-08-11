package cli

// Turnstile porcelain: widget CRUD plus secret rotation.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

// turnstileWidget is the subset of the widget object the porcelain reads or
// writes. Pointers mark tri-state booleans so "unset" survives a merge.
type turnstileWidget struct {
	SiteKey        string   `json:"sitekey,omitempty"`
	Name           string   `json:"name,omitempty"`
	Domains        []string `json:"domains,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	ClearanceLevel string   `json:"clearance_level,omitempty"`
	BotFightMode   *bool    `json:"bot_fight_mode,omitempty"`
	EphemeralID    *bool    `json:"ephemeral_id,omitempty"`
	Offlabel       *bool    `json:"offlabel,omitempty"`
	Region         string   `json:"region,omitempty"`
	CreatedOn      string   `json:"created_on,omitempty"`
	ModifiedOn     string   `json:"modified_on,omitempty"`
}

var (
	turnstileModes           = []string{"managed", "non-interactive", "invisible"}
	turnstileClearanceLevels = []string{"no_clearance", "jschallenge", "managed", "interactive"}
	turnstileOrders          = []string{"id", "sitekey", "name", "created_on", "modified_on"}
	turnstileDirections      = []string{"asc", "desc"}
	turnstileRegions         = []string{"world", "china"}
	// turnstileFilterFields are the widget fields `--filter field:value` can
	// match on.
	turnstileFilterFields = []string{"name", "sitekey"}
)

// Schema bounds the API enforces on a widget. Checking them here turns a
// round-trip 400 into an immediate, specific error.
const (
	turnstileMaxNameLen = 254
	turnstileMaxDomains = 10
)

func newTurnstileCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "turnstile",
		Short: "Manage Turnstile widgets",
	}
	cmd.AddCommand(
		newTurnstileListCmd(g),
		newTurnstileGetCmd(g),
		newTurnstileCreateCmd(g),
		newTurnstileUpdateCmd(g),
		newTurnstileDeleteCmd(g),
		newTurnstileRotateSecretCmd(g),
	)
	return cmd
}

// turnstileAccountID returns the resolved account ID or an actionable error.
func turnstileAccountID(cfg config.Resolved) (string, error) {
	if cfg.AccountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return cfg.AccountID, nil
}

func turnstilePath(accountID string) string {
	return "/accounts/" + accountID + "/challenges/widgets"
}

func turnstileWidgetPath(accountID, sitekey string) string {
	return turnstilePath(accountID) + "/" + url.PathEscape(sitekey)
}

// validateTurnstileEnum checks a flag value against the API's accepted set and
// names the alternatives in the error.
func validateTurnstileEnum(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

// parseTurnstileFilter validates the API's `field:value` filter grammar and
// returns the query value to send. An explicitly empty --filter means "no
// filter", exactly as omitting the flag does.
func parseTurnstileFilter(filter string) (value string, send bool, err error) {
	if filter == "" {
		return "", false, nil
	}
	field, term, ok := strings.Cut(filter, ":")
	field = strings.ToLower(strings.TrimSpace(field))
	if !ok || term == "" {
		return "", false, turnstileFilterError(filter)
	}
	for _, f := range turnstileFilterFields {
		if field == f {
			return field + ":" + term, true, nil
		}
	}
	return "", false, turnstileFilterError(filter)
}

func turnstileFilterError(filter string) error {
	return fmt.Errorf("--filter %q is not valid: use field:value, where field is one of %s (for example --filter name:checkout)",
		filter, strings.Join(turnstileFilterFields, ", "))
}

// validateTurnstileName enforces the API's 1..254 character bound on a widget
// name.
func validateTurnstileName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("widget name is empty: pass a non-empty name, e.g. `cf turnstile create \"checkout form\" --domain example.com`")
	}
	if n := utf8.RuneCountInString(name); n > turnstileMaxNameLen {
		return fmt.Errorf("widget name is %d characters; the API allows at most %d", n, turnstileMaxNameLen)
	}
	return nil
}

// validateTurnstileDomains enforces the API's 1..10 domain bound and rejects
// blank entries.
func validateTurnstileDomains(domains []string) error {
	if len(domains) == 0 {
		return errors.New("no domains specified: pass at least one --domain")
	}
	if len(domains) > turnstileMaxDomains {
		return fmt.Errorf("%d domains given; the API allows at most %d per widget", len(domains), turnstileMaxDomains)
	}
	return validateNonEmptyStrings("domain", domains)
}

// validateTurnstileWidgetBounds checks a fully-built widget body, so the
// merged update body is held to the same bounds as a create.
func validateTurnstileWidgetBounds(w turnstileWidget) error {
	if err := validateTurnstileName(w.Name); err != nil {
		return err
	}
	return validateTurnstileDomains(w.Domains)
}

func newTurnstileListCmd(g *globalOpts) *cobra.Command {
	var filter, order, direction string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Turnstile widgets",
		Long: `List the Turnstile widgets on an account.

--filter takes field:value and matches the field on a case-insensitive
substring; the field is name or sitekey.

Examples:

  cf turnstile list
  cf turnstile list --filter name:checkout
  cf turnstile list --filter sitekey:0x4AAAAAAA
  cf turnstile list --order created_on --direction desc`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			filterValue, sendFilter, err := parseTurnstileFilter(filter)
			if err != nil {
				return err
			}
			if sendFilter {
				q.Set("filter", filterValue)
			}
			if cmd.Flags().Changed("order") {
				if err := validateTurnstileEnum("order", order, turnstileOrders); err != nil {
					return err
				}
				q.Set("order", order)
			}
			if cmd.Flags().Changed("direction") {
				if err := validateTurnstileEnum("direction", direction, turnstileDirections); err != nil {
					return err
				}
				q.Set("direction", direction)
			}
			q.Set("per_page", "100")
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := turnstileAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: turnstilePath(accountID), Query: q}
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
			var widgets []turnstileWidget
			if err := json.Unmarshal(env.Result, &widgets); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(widgets))
			for _, w := range widgets {
				rows = append(rows, []string{
					w.SiteKey,
					output.Cell(w.Name),
					w.Mode,
					output.Cell(strings.Join(w.Domains, ",")),
					w.CreatedOn,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"SITEKEY", "NAME", "MODE", "DOMAINS", "CREATED"}, rows)
		},
	}
	cmd.Flags().StringVar(&filter, "filter", "", "match a field on a substring, as field:value (field: name or sitekey)")
	cmd.Flags().StringVar(&order, "order", "", "sort field: id, sitekey, name, created_on, or modified_on")
	cmd.Flags().StringVar(&direction, "direction", "", "sort direction: asc or desc")
	return cmd
}

func newTurnstileGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <sitekey>",
		Short: "Show one Turnstile widget",
		Long:  "Show one Turnstile widget, including its secret key.\n\nExample:\n\n  cf turnstile get 0x4AAAAAAADnPIDROrmt1Wwj",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := turnstileAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: turnstileWidgetPath(accountID, args[0])}
			return runTurnstileRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newTurnstileCreateCmd(g *globalOpts) *cobra.Command {
	var mode, clearanceLevel, region string
	var domains []string
	var botFightMode, ephemeralID, offlabel bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Turnstile widget",
		Long: `Create a Turnstile widget. The sitekey and secret are in the response.

--region is fixed at creation: the API has no way to move a widget between
regions afterwards, so ` + "`cf turnstile update`" + ` has no --region flag.

Examples:

  cf turnstile create "checkout form" --domain example.com
  cf turnstile create signup --domain example.com --domain www.example.com --mode invisible
  cf turnstile create login --domain example.com --mode managed --clearance-level interactive
  cf turnstile create checkout --domain example.cn --region china`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildTurnstileCreateBody(turnstileCreateOpts{
				Name:           args[0],
				Domains:        domains,
				Mode:           mode,
				ClearanceLevel: clearanceLevel,
				Region:         region,
				BotFightMode:   turnstileFlag(cmd, "bot-fight-mode", botFightMode),
				EphemeralID:    turnstileFlag(cmd, "ephemeral-id", ephemeralID),
				Offlabel:       turnstileFlag(cmd, "offlabel", offlabel),
			})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := turnstileAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: turnstilePath(accountID), Body: body}
			return runTurnstileRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringArrayVar(&domains, "domain", nil, "domain the widget may be used on (repeatable)")
	cmd.Flags().StringVar(&mode, "mode", "managed", "widget mode: managed, non-interactive, or invisible")
	cmd.Flags().StringVar(&clearanceLevel, "clearance-level", "", "clearance level: no_clearance, jschallenge, managed, or interactive")
	cmd.Flags().StringVar(&region, "region", "", "region the widget serves, fixed at creation: world or china (default: world)")
	cmd.Flags().BoolVar(&botFightMode, "bot-fight-mode", false, "issue challenges to requests from known bots")
	cmd.Flags().BoolVar(&ephemeralID, "ephemeral-id", false, "return an ephemeral ID in the siteverify response")
	cmd.Flags().BoolVar(&offlabel, "offlabel", false, "hide Cloudflare branding on the widget")
	_ = cmd.MarkFlagRequired("domain")
	// name is the positional argument; mode has a default. Both are required
	// by the API, so nothing else is marked required here.
	return cmd
}

// turnstileCreateOpts is the flag state `cf turnstile create` turns into a
// request body. Region lives here and not in turnstileOverrides because the
// API fixes it at creation.
type turnstileCreateOpts struct {
	Name           string
	Domains        []string
	Mode           string
	ClearanceLevel string
	Region         string
	BotFightMode   *bool
	EphemeralID    *bool
	Offlabel       *bool
}

// buildTurnstileCreateBody validates the widget fields and marshals the create
// request body.
func buildTurnstileCreateBody(o turnstileCreateOpts) ([]byte, error) {
	if err := validateTurnstileName(o.Name); err != nil {
		return nil, err
	}
	if err := validateTurnstileDomains(o.Domains); err != nil {
		return nil, err
	}
	if err := validateTurnstileEnum("mode", o.Mode, turnstileModes); err != nil {
		return nil, err
	}
	if o.ClearanceLevel != "" {
		if err := validateTurnstileEnum("clearance-level", o.ClearanceLevel, turnstileClearanceLevels); err != nil {
			return nil, err
		}
	}
	if o.Region != "" {
		if err := validateTurnstileEnum("region", o.Region, turnstileRegions); err != nil {
			return nil, err
		}
	}
	w := turnstileWidget{
		Name:           o.Name,
		Domains:        o.Domains,
		Mode:           o.Mode,
		ClearanceLevel: o.ClearanceLevel,
		Region:         o.Region,
		BotFightMode:   o.BotFightMode,
		EphemeralID:    o.EphemeralID,
		Offlabel:       o.Offlabel,
	}
	return json.Marshal(w)
}

// turnstileOverrides carries the update flags the user actually set; nil
// fields mean "leave the current value alone".
type turnstileOverrides struct {
	Name           *string
	Domains        []string
	Mode           *string
	ClearanceLevel *string
	BotFightMode   *bool
	EphemeralID    *bool
	Offlabel       *bool
}

func (o turnstileOverrides) empty() bool {
	return o.Name == nil && o.Domains == nil && o.Mode == nil && o.ClearanceLevel == nil &&
		o.BotFightMode == nil && o.EphemeralID == nil && o.Offlabel == nil
}

// validate checks the enum-valued overrides before any network call.
func (o turnstileOverrides) validate() error {
	if o.empty() {
		return errors.New("nothing to update: pass at least one of --name, --domain, --mode, --clearance-level, --bot-fight-mode, --ephemeral-id, --offlabel")
	}
	if o.Name != nil {
		if err := validateTurnstileName(*o.Name); err != nil {
			return err
		}
	}
	if o.Domains != nil {
		if err := validateTurnstileDomains(o.Domains); err != nil {
			return err
		}
	}
	if o.Mode != nil {
		if err := validateTurnstileEnum("mode", *o.Mode, turnstileModes); err != nil {
			return err
		}
	}
	if o.ClearanceLevel != nil {
		if err := validateTurnstileEnum("clearance-level", *o.ClearanceLevel, turnstileClearanceLevels); err != nil {
			return err
		}
	}
	return nil
}

// mergeTurnstileWidget applies the overrides onto the widget as it exists
// today. The API replaces the whole widget on update, so every field that is
// not being changed has to be sent back unchanged.
func mergeTurnstileWidget(cur turnstileWidget, o turnstileOverrides) turnstileWidget {
	next := turnstileWidget{
		Name:           cur.Name,
		Domains:        cur.Domains,
		Mode:           cur.Mode,
		ClearanceLevel: cur.ClearanceLevel,
		BotFightMode:   cur.BotFightMode,
		EphemeralID:    cur.EphemeralID,
		Offlabel:       cur.Offlabel,
	}
	if o.Name != nil {
		next.Name = *o.Name
	}
	if o.Domains != nil {
		next.Domains = o.Domains
	}
	if o.Mode != nil {
		next.Mode = *o.Mode
	}
	if o.ClearanceLevel != nil {
		next.ClearanceLevel = *o.ClearanceLevel
	}
	if o.BotFightMode != nil {
		next.BotFightMode = o.BotFightMode
	}
	if o.EphemeralID != nil {
		next.EphemeralID = o.EphemeralID
	}
	if o.Offlabel != nil {
		next.Offlabel = o.Offlabel
	}
	return next
}

func newTurnstileUpdateCmd(g *globalOpts) *cobra.Command {
	var name, mode, clearanceLevel string
	var domains []string
	var botFightMode, ephemeralID, offlabel bool
	cmd := &cobra.Command{
		Use:   "update <sitekey>",
		Short: "Update fields of a Turnstile widget",
		Long: `Update fields of a Turnstile widget.

The API replaces the whole widget, so this command first reads the widget and
re-sends it with your changes applied; fields you do not pass keep their
current values. --dry-run performs that read but never sends the write.

Examples:

  cf turnstile update 0x4AAAAAAADnPIDROrmt1Wwj --name "checkout form"
  cf turnstile update 0x4AAAAAAADnPIDROrmt1Wwj --domain example.com --domain www.example.com
  cf turnstile update 0x4AAAAAAADnPIDROrmt1Wwj --mode invisible --bot-fight-mode=false`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o := turnstileOverrides{
				Name:           turnstileStringFlag(cmd, "name", name),
				Mode:           turnstileStringFlag(cmd, "mode", mode),
				ClearanceLevel: turnstileStringFlag(cmd, "clearance-level", clearanceLevel),
				BotFightMode:   turnstileFlag(cmd, "bot-fight-mode", botFightMode),
				EphemeralID:    turnstileFlag(cmd, "ephemeral-id", ephemeralID),
				Offlabel:       turnstileFlag(cmd, "offlabel", offlabel),
			}
			if cmd.Flags().Changed("domain") {
				o.Domains = domains
			}
			if err := o.validate(); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := turnstileAccountID(cfg)
			if err != nil {
				return err
			}
			path := turnstileWidgetPath(accountID, args[0])
			env, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: path})
			if err != nil {
				return fmt.Errorf("read widget %s before update: %w", args[0], err)
			}
			var cur turnstileWidget
			if err := json.Unmarshal(env.Result, &cur); err != nil {
				return fmt.Errorf("read widget %s before update: unexpected response", args[0])
			}
			next := mergeTurnstileWidget(cur, o)
			// The merged body is a full replacement, so it has to satisfy the
			// same bounds as a create — including for fields the user left
			// alone but the API (or another client) set out of range.
			if err := validateTurnstileWidgetBounds(next); err != nil {
				return fmt.Errorf("widget %s cannot be updated: %w", args[0], err)
			}
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: path, Body: body}
			return runTurnstileRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "widget name")
	cmd.Flags().StringArrayVar(&domains, "domain", nil, "domain the widget may be used on (repeatable; replaces the current list)")
	cmd.Flags().StringVar(&mode, "mode", "", "widget mode: managed, non-interactive, or invisible")
	cmd.Flags().StringVar(&clearanceLevel, "clearance-level", "", "clearance level: no_clearance, jschallenge, managed, or interactive")
	cmd.Flags().BoolVar(&botFightMode, "bot-fight-mode", false, "issue challenges to requests from known bots")
	cmd.Flags().BoolVar(&ephemeralID, "ephemeral-id", false, "return an ephemeral ID in the siteverify response")
	cmd.Flags().BoolVar(&offlabel, "offlabel", false, "hide Cloudflare branding on the widget")
	return cmd
}

func newTurnstileDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <sitekey>",
		Short: "Delete a Turnstile widget",
		Long:  "Delete a Turnstile widget. Sites using its sitekey stop validating.\n\nExample:\n\n  cf turnstile delete 0x4AAAAAAADnPIDROrmt1Wwj --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := turnstileAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Turnstile widget %s from account %s?", args[0], accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: turnstileWidgetPath(accountID, args[0])}
			return runTurnstileRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newTurnstileRotateSecretCmd(g *globalOpts) *cobra.Command {
	var invalidateImmediately, force bool
	cmd := &cobra.Command{
		Use:   "rotate-secret <sitekey>",
		Short: "Rotate the secret key of a Turnstile widget",
		Long: `Rotate the secret key of a Turnstile widget.

By default the previous secret keeps working for two hours so servers can be
updated; --invalidate-immediately revokes it right away, which breaks
siteverify for anything still using it.

Examples:

  cf turnstile rotate-secret 0x4AAAAAAADnPIDROrmt1Wwj --force
  cf turnstile rotate-secret 0x4AAAAAAADnPIDROrmt1Wwj --invalidate-immediately --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := json.Marshal(map[string]bool{"invalidate_immediately": invalidateImmediately})
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := turnstileAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				prompt := fmt.Sprintf("Rotate the secret for Turnstile widget %s?", args[0])
				if invalidateImmediately {
					prompt = fmt.Sprintf("Rotate the secret for Turnstile widget %s and invalidate the old one immediately?", args[0])
				}
				if !confirm(prompt) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "POST", Path: turnstileWidgetPath(accountID, args[0]) + "/rotate_secret", Body: body}
			return runTurnstileRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&invalidateImmediately, "invalidate-immediately", false, "revoke the previous secret now instead of after the grace period")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// turnstileFlag returns a pointer to v when the flag was set, so unset boolean
// flags stay out of request bodies.
func turnstileFlag(cmd *cobra.Command, flag string, v bool) *bool {
	if !cmd.Flags().Changed(flag) {
		return nil
	}
	return &v
}

// turnstileStringFlag is the string counterpart of turnstileFlag.
func turnstileStringFlag(cmd *cobra.Command, flag string, v string) *string {
	if !cmd.Flags().Changed(flag) {
		return nil
	}
	return &v
}

func runTurnstileRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
