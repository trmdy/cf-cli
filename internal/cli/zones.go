package cli

// Zones porcelain: zone lifecycle (list/get/create/delete), pause/resume, and
// the handful of zone settings people actually toggle from a terminal.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type zoneSummary struct {
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Type   string `json:"type,omitempty"`
	Paused bool   `json:"paused,omitempty"`
	Plan   struct {
		Name string `json:"name,omitempty"`
	} `json:"plan"`
}

// zoneSettingValue is the shape of a single `/zones/{id}/settings/{setting}`
// result. Value is `any` because settings are not all strings.
type zoneSettingValue struct {
	ID         string `json:"id"`
	Value      any    `json:"value"`
	Editable   any    `json:"editable"`
	ModifiedOn string `json:"modified_on"`
}

// zoneSettingDef maps a CLI-friendly setting name onto the API setting ID and
// the values the API accepts. It is the single source of truth for both
// `zones settings get --setting` and the `zones settings set` flags.
type zoneSettingDef struct {
	Flag   string
	ID     string
	Values []string
	Usage  string
}

var zoneSettingDefs = []zoneSettingDef{
	{
		Flag:   "development-mode",
		ID:     "development_mode",
		Values: []string{"on", "off"},
		Usage:  "Development Mode: bypass the cache for 3 hours (on, off)",
	},
	{
		Flag:   "ssl",
		ID:     "ssl",
		Values: []string{"off", "flexible", "full", "strict"},
		Usage:  "SSL mode used towards the origin (off, flexible, full, strict)",
	},
	{
		Flag:   "always-use-https",
		ID:     "always_use_https",
		Values: []string{"on", "off"},
		Usage:  "Always Use HTTPS: redirect http:// requests to https:// (on, off)",
	},
}

// zoneTypes are the zone types the API accepts on create.
var zoneTypes = []string{"full", "partial", "secondary", "internal"}

// zoneStatuses are the values the list endpoint accepts for --status.
var zoneStatuses = []string{"initializing", "pending", "active", "moved"}

func newZonesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "zones",
		Short: "Manage zones and their common settings",
	}
	cmd.AddCommand(
		newZonesListCmd(g),
		newZonesGetCmd(g),
		newZonesCreateCmd(g),
		newZonesDeleteCmd(g),
		newZonesPauseCmd(g),
		newZonesResumeCmd(g),
		newZonesSettingsCmd(g),
	)
	return cmd
}

func zonePath(zoneID string) string { return "/zones/" + url.PathEscape(zoneID) }

func zoneSettingPath(zoneID, settingID string) string {
	return zonePath(zoneID) + "/settings/" + url.PathEscape(settingID)
}

func newZonesListCmd(g *globalOpts) *cobra.Command {
	var name, status string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List zones on the account",
		Long: `List zones visible to the current API token.

Examples:

  cf zones list
  cf zones list --status active
  cf zones list --name example.com
  cf zones list --name contains:example --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if name != "" {
				q.Set("name", name)
			}
			if status != "" {
				s, err := validateZoneChoice("--status", status, zoneStatuses)
				if err != nil {
					return err
				}
				q.Set("status", s)
			}
			q.Set("per_page", "100")
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: "/zones", Query: q}
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
			var zones []zoneSummary
			if err := json.Unmarshal(env.Result, &zones); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(zones))
			for _, z := range zones {
				rows = append(rows, []string{
					z.ID,
					output.Cell(z.Name),
					z.Status,
					output.Cell(z.Plan.Name),
					strconv.FormatBool(z.Paused),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "STATUS", "PLAN", "PAUSED"}, rows)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter by zone name (exact, or contains:<substring>)")
	cmd.Flags().StringVar(&status, "status", "", "filter by zone status (initializing, pending, active, moved)")
	return cmd
}

func newZonesGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show one zone",
		Long: `Show the details of a zone.

Examples:

  cf zones get --zone example.com
  cf zones get --zone 023e105f4ecef8ad9ca31a8372d0c353
  cf zones get --zone example.com --query .status`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			return runZonesRequest(cmd, g, client, api.Request{Method: "GET", Path: zonePath(zoneID)})
		},
	}
	addZoneFlag(cmd, &zone)
	return cmd
}

func newZonesCreateCmd(g *globalOpts) *cobra.Command {
	var zoneType string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a zone on the account",
		Long: `Create a zone. The account comes from --account-id, CLOUDFLARE_ACCOUNT_ID,
or the active profile.

Examples:

  cf zones create example.com
  cf zones create example.com --type partial
  cf zones create example.com --account-id 023e105f4ecef8ad9ca31a8372d0c353`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			body, err := buildZoneCreateBody(args[0], cfg.AccountID, zoneType)
			if err != nil {
				return err
			}
			return runZonesRequest(cmd, g, client, api.Request{Method: "POST", Path: "/zones", Body: body})
		},
	}
	cmd.Flags().StringVar(&zoneType, "type", "full", "zone type (full, partial, secondary, internal)")
	return cmd
}

// buildZoneCreateBody validates the create inputs and returns the JSON body.
func buildZoneCreateBody(name, accountID, zoneType string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("zone name is empty: pass a domain, for example `cf zones create example.com`")
	}
	if accountID == "" {
		return nil, errors.New("no account ID found: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	t, err := validateZoneChoice("--type", zoneType, zoneTypes)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{
		"name":    strings.TrimSpace(name),
		"account": map[string]string{"id": accountID},
		"type":    t,
	})
}

func newZonesDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a zone",
		Long: `Delete a zone and everything configured on it. This cannot be undone.

Examples:

  cf zones delete --zone example.com
  cf zones delete --zone example.com --force`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete zone %s and all of its configuration? This cannot be undone.", zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runZonesRequest(cmd, g, client, api.Request{Method: "DELETE", Path: zonePath(zoneID)})
		},
	}
	addZoneFlag(cmd, &zone)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newZonesPauseCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "pause",
		Short: "Pause Cloudflare on a zone",
		Long: `Pause Cloudflare for a zone: traffic bypasses the network and goes straight
to the origin. Undo it with ` + "`cf zones resume`" + `.

Examples:

  cf zones pause --zone example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runZonePausedUpdate(cmd, g, zone, true)
		},
	}
	addZoneFlag(cmd, &zone)
	return cmd
}

func newZonesResumeCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "resume",
		Short: "Resume Cloudflare on a paused zone",
		Long: `Resume Cloudflare for a zone that was paused.

Examples:

  cf zones resume --zone example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runZonePausedUpdate(cmd, g, zone, false)
		},
	}
	addZoneFlag(cmd, &zone)
	return cmd
}

// runZonePausedUpdate flips the zone-level `paused` flag, which is how the API
// models pause/resume.
func runZonePausedUpdate(cmd *cobra.Command, g *globalOpts, zone string, paused bool) error {
	client, cfg, err := g.client(true)
	if err != nil {
		return err
	}
	zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]bool{"paused": paused})
	if err != nil {
		return err
	}
	return runZonesRequest(cmd, g, client, api.Request{Method: "PATCH", Path: zonePath(zoneID), Body: body})
}

func newZonesSettingsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Read and change common zone settings",
	}
	cmd.AddCommand(
		newZonesSettingsGetCmd(g),
		newZonesSettingsSetCmd(g),
	)
	return cmd
}

func newZonesSettingsGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	var settings []string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show common zone settings",
		Long: `Show zone settings. With no --setting flag it reports every setting
` + "`cf zones settings set`" + ` can change, one row (or JSON array element) each.

Examples:

  cf zones settings get --zone example.com
  cf zones settings get --zone example.com --setting ssl
  cf zones settings get --zone example.com --setting ssl --setting always-use-https`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			defs, err := resolveZoneSettingDefs(settings)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			reqs := make([]api.Request, 0, len(defs))
			for _, d := range defs {
				reqs = append(reqs, api.Request{Method: "GET", Path: zoneSettingPath(zoneID, d.ID)})
			}
			if g.DryRun {
				return dumpZonesRequests(cmd, g, client, reqs)
			}
			raw, err := doZoneSettingRequests(cmd, client, defs, reqs)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, raw, output.JSON)
			}
			var values []zoneSettingValue
			if err := json.Unmarshal(raw, &values); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
			}
			rows := make([][]string, 0, len(values))
			for i, v := range values {
				id := v.ID
				if id == "" {
					id = defs[i].ID
				}
				rows = append(rows, []string{id, output.Cell(v.Value), output.Cell(v.Editable), v.ModifiedOn})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"SETTING", "VALUE", "EDITABLE", "MODIFIED"}, rows)
		},
	}
	addZoneFlag(cmd, &zone)
	cmd.Flags().StringArrayVar(&settings, "setting", nil,
		"setting to read: "+zoneSettingNameList()+" (repeatable; default: all of them)")
	return cmd
}

func newZonesSettingsSetCmd(g *globalOpts) *cobra.Command {
	var zone string
	values := make(map[string]*string, len(zoneSettingDefs))
	for _, d := range zoneSettingDefs {
		values[d.Flag] = new(string)
	}
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Change common zone settings",
		Long: `Change one or more zone settings. Each setting is a separate API call,
applied in the order listed below.

Examples:

  cf zones settings set --zone example.com --development-mode on
  cf zones settings set --zone example.com --ssl full
  cf zones settings set --zone example.com --ssl strict --always-use-https on`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			defs, bodies, err := buildZoneSettingUpdates(cmd, values)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			reqs := make([]api.Request, 0, len(defs))
			for i, d := range defs {
				reqs = append(reqs, api.Request{Method: "PATCH", Path: zoneSettingPath(zoneID, d.ID), Body: bodies[i]})
			}
			if g.DryRun {
				return dumpZonesRequests(cmd, g, client, reqs)
			}
			raw, err := doZoneSettingRequests(cmd, client, defs, reqs)
			if err != nil {
				return err
			}
			return g.renderResult(cmd, raw, output.JSON)
		},
	}
	addZoneFlag(cmd, &zone)
	for _, d := range zoneSettingDefs {
		cmd.Flags().StringVar(values[d.Flag], d.Flag, "", d.Usage)
	}
	return cmd
}

// buildZoneSettingUpdates turns the changed setting flags into validated
// request bodies, in the fixed order of zoneSettingDefs.
func buildZoneSettingUpdates(cmd *cobra.Command, values map[string]*string) ([]zoneSettingDef, [][]byte, error) {
	var defs []zoneSettingDef
	var bodies [][]byte
	for _, d := range zoneSettingDefs {
		if !cmd.Flags().Changed(d.Flag) {
			continue
		}
		v, err := validateZoneChoice("--"+d.Flag, *values[d.Flag], d.Values)
		if err != nil {
			return nil, nil, err
		}
		body, err := json.Marshal(map[string]string{"value": v})
		if err != nil {
			return nil, nil, err
		}
		defs = append(defs, d)
		bodies = append(bodies, body)
	}
	if len(defs) == 0 {
		return nil, nil, fmt.Errorf("nothing to set: pass at least one of %s", zoneSettingFlagList())
	}
	return defs, bodies, nil
}

// doZoneSettingRequests runs one request per setting and returns the results
// as a JSON array, so --query and --output see a single value.
func doZoneSettingRequests(cmd *cobra.Command, client *api.Client, defs []zoneSettingDef, reqs []api.Request) ([]byte, error) {
	results := make([]json.RawMessage, 0, len(reqs))
	for i, req := range reqs {
		env, err := client.Do(cmd.Context(), req)
		if err != nil {
			return nil, fmt.Errorf("setting %s: %w", defs[i].ID, err)
		}
		results = append(results, env.Result)
	}
	return json.Marshal(results)
}

// resolveZoneSettingDefs maps --setting values onto known settings, defaulting
// to all of them. Names are accepted in either CLI (always-use-https) or API
// (always_use_https) spelling.
func resolveZoneSettingDefs(names []string) ([]zoneSettingDef, error) {
	if len(names) == 0 {
		return zoneSettingDefs, nil
	}
	var defs []zoneSettingDef
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		key := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(n)), "_", "-")
		var match *zoneSettingDef
		for i := range zoneSettingDefs {
			if zoneSettingDefs[i].Flag == key {
				match = &zoneSettingDefs[i]
				break
			}
		}
		if match == nil {
			return nil, fmt.Errorf("unknown setting %q: expected one of %s", n, zoneSettingNameList())
		}
		if seen[match.Flag] {
			continue
		}
		seen[match.Flag] = true
		defs = append(defs, *match)
	}
	return defs, nil
}

// validateZoneChoice normalizes a flag value and checks it against the allowed
// set, naming the flag and the options in the error.
func validateZoneChoice(flag, value string, allowed []string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return "", fmt.Errorf("%s is empty: expected one of %s", flag, strings.Join(allowed, ", "))
	}
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", fmt.Errorf("invalid %s value %q: expected one of %s", flag, value, strings.Join(allowed, ", "))
}

func zoneSettingNameList() string {
	names := make([]string, 0, len(zoneSettingDefs))
	for _, d := range zoneSettingDefs {
		names = append(names, d.Flag)
	}
	return strings.Join(names, ", ")
}

func zoneSettingFlagList() string {
	names := make([]string, 0, len(zoneSettingDefs))
	for _, d := range zoneSettingDefs {
		names = append(names, "--"+d.Flag)
	}
	return strings.Join(names, ", ")
}

func addZoneFlag(cmd *cobra.Command, zone *string) {
	cmd.Flags().StringVar(zone, "zone", "", "zone name or ID (default: configured zone)")
}

func runZonesRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// dumpZonesRequests renders the --dry-run form of a multi-request command as a
// JSON array, one entry per request.
func dumpZonesRequests(cmd *cobra.Command, g *globalOpts, client *api.Client, reqs []api.Request) error {
	dumps := make([]*api.RequestDump, 0, len(reqs))
	for _, req := range reqs {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		dumps = append(dumps, dump)
	}
	return g.renderValue(cmd, dumps, output.JSON)
}
