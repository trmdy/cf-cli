package cli

// Devices fleet porcelain: the WARP device fleet an account has enrolled, and
// the device settings ("WARP") profiles applied to it.
//
// Everything here is account-scoped (`/accounts/{id}/devices/...`), the
// surface the Zero Trust dashboard drives. Two deliberate scope lines:
//
//   - Devices are read plus revoke. Registration-level operations (unrevoke,
//     per-registration delete, override codes) and device deletion stay on
//     `cf api devices`.
//   - Profiles expose every scalar setting as a flag. The list-valued
//     sub-resources — Split Tunnel include/exclude, Local Domain Fallback,
//     DNS search suffixes, virtual networks, and China Global Acceleration —
//     are whole-list replacements with their own item schemas; they stay on
//     `cf api devices` (policy-include-replace, policy-exclude-replace,
//     policy-fallback-domains-replace, and friends).
//
// No command here reads before it writes: both profile endpoints are true
// partial PATCHes (the pinned spec marks no field required on either), so
// --dry-run performs no network I/O at all.
//
// Everything hangs off a single `fleet` command because this shard registers
// exactly one constructor in the devices group scaffold, alongside the posture
// shard's `cf devices posture`.
//
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
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

// Enums below come from the pinned spec's query parameter schemas for
// GET /accounts/{account_id}/devices/physical-devices.
var (
	devicesFleetSortFields = []string{
		"name", "id", "client_version", "last_seen_user.email",
		"last_seen_at", "active_registrations", "created_at",
	}
	devicesFleetSortOrders          = []string{"asc", "desc"}
	devicesFleetRegistrationFilters = []string{"include", "only", "exclude"}
)

// devicesFleetProfileInclude is the one value the spec documents for the
// `include` query parameter on the device endpoints; --with-profile sends it.
const devicesFleetProfileInclude = "last_seen_registration.policy"

// Length bounds are the maxLength values in the pinned spec, which JSON Schema
// counts in Unicode code points — devicesFleetLen, not len(). The spec puts no
// bounds on the numeric profile settings (precedence, auto_connect,
// captive_portal, lan_allow_minutes, lan_allow_subnet_size, service mode
// port), so this porcelain does not invent any.
const (
	devicesFleetMaxProfileName = 100
	devicesFleetMaxMatch       = 500
	devicesFleetMaxDescription = 500
	devicesFleetMaxProfileID   = 36
)

// devicesFleetLen counts a value the way the API's maxLength does: in Unicode
// code points, so a name of accented or CJK characters is not rejected for
// being multibyte in UTF-8.
func devicesFleetLen(s string) int { return utf8.RuneCountInString(s) }

func newDevicesFleetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet",
		Short: "Inspect enrolled WARP devices and manage device settings profiles",
	}
	cmd.AddCommand(
		newDevicesFleetListCmd(g),
		newDevicesFleetGetCmd(g),
		newDevicesFleetRevokeCmd(g),
		newDevicesFleetProfileCmd(g),
	)
	return cmd
}

// --- shared helpers --------------------------------------------------------

// devicesFleetAccountID validates the resolved account scope. Every command in
// this file is account-scoped.
func devicesFleetAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

// devicesFleetPath builds an account-scoped devices path, e.g.
// /accounts/<id>/devices/physical-devices.
func devicesFleetPath(accountID, resource string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/devices/" + resource
}

func runDevicesFleetRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// runDevicesFleetList sends a list request and renders it as a table, falling
// back to JSON when the caller's decoder cannot read the result. paginate
// selects the shared cursor/page follower; endpoints without pagination
// parameters pass false.
func runDevicesFleetList(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request, paginate bool,
	table func(json.RawMessage) ([]string, [][]string, bool)) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return g.renderValue(cmd, dump, output.JSON)
	}
	do := client.Do
	if paginate {
		do = client.DoAutoPaginate
	}
	env, err := do(cmd.Context(), req)
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

// devicesFleetEnumValue matches value case-insensitively against allowed and
// returns the canonical spelling that goes on the wire.
func devicesFleetEnumValue(flag, value string, allowed []string) (string, error) {
	for _, a := range allowed {
		if strings.EqualFold(a, value) {
			return a, nil
		}
	}
	return "", fmt.Errorf("unknown --%s %q (expected one of: %s)", flag, value, strings.Join(allowed, ", "))
}

// devicesFleetRequireID rejects empty path arguments before any client work.
func devicesFleetRequireID(what, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s must not be empty", what)
	}
	return nil
}

// --- devices ---------------------------------------------------------------

type devicesFleetDevice struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	DeviceType          string `json:"device_type"`
	OSVersion           string `json:"os_version"`
	ClientVersion       string `json:"client_version"`
	LastSeenAt          string `json:"last_seen_at"`
	ActiveRegistrations int    `json:"active_registrations"`
	LastSeenUser        *struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"last_seen_user"`
	LastSeenRegistration *struct {
		Policy *struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"policy"`
	} `json:"last_seen_registration"`
}

// devicesFleetListOpts holds the device list filters. seenAfter/seenBefore are
// typed as plain strings in the pinned spec, so they are passed through
// unvalidated rather than pinned to a timestamp format this CLI made up.
type devicesFleetListOpts struct {
	search              string
	userEmail           string
	activeRegistrations string
	seenAfter           string
	seenBefore          string
	sortBy              string
	sortOrder           string
	perPage             int
	withProfile         bool
	perPageSet          bool
}

// buildDevicesFleetListQuery validates the filter set and builds the query
// string. It runs before client construction so bad input never opens a
// connection.
func buildDevicesFleetListQuery(o devicesFleetListOpts) (url.Values, error) {
	q := url.Values{}
	if o.search != "" {
		q.Set("search", o.search)
	}
	if o.userEmail != "" {
		q.Set("last_seen_user.email", o.userEmail)
	}
	if o.activeRegistrations != "" {
		v, err := devicesFleetEnumValue("active-registrations", o.activeRegistrations, devicesFleetRegistrationFilters)
		if err != nil {
			return nil, err
		}
		q.Set("active_registrations", v)
	}
	if o.seenAfter != "" {
		q.Set("seen_after", o.seenAfter)
	}
	if o.seenBefore != "" {
		q.Set("seen_before", o.seenBefore)
	}
	if o.sortBy != "" {
		v, err := devicesFleetEnumValue("sort-by", o.sortBy, devicesFleetSortFields)
		if err != nil {
			return nil, err
		}
		q.Set("sort_by", v)
	}
	if o.sortOrder != "" {
		v, err := devicesFleetEnumValue("sort-order", o.sortOrder, devicesFleetSortOrders)
		if err != nil {
			return nil, err
		}
		q.Set("sort_order", v)
	}
	if o.perPageSet {
		if o.perPage < 1 {
			return nil, fmt.Errorf("--per-page must be a positive number of devices, got %d", o.perPage)
		}
		q.Set("per_page", strconv.Itoa(o.perPage))
	}
	if o.withProfile {
		q.Set("include", devicesFleetProfileInclude)
	}
	return q, nil
}

func devicesFleetDeviceTable(withProfile bool) func(json.RawMessage) ([]string, [][]string, bool) {
	return func(raw json.RawMessage) ([]string, [][]string, bool) {
		var devices []devicesFleetDevice
		if err := json.Unmarshal(raw, &devices); err != nil {
			return nil, nil, false
		}
		headers := []string{"ID", "NAME", "USER", "OS", "CLIENT", "LAST SEEN", "REGISTRATIONS"}
		if withProfile {
			headers = append(headers, "PROFILE")
		}
		rows := make([][]string, 0, len(devices))
		for _, d := range devices {
			user := ""
			if d.LastSeenUser != nil {
				user = d.LastSeenUser.Email
				if user == "" {
					user = d.LastSeenUser.Name
				}
			}
			os := strings.TrimSpace(d.DeviceType + " " + d.OSVersion)
			row := []string{
				d.ID,
				output.Cell(d.Name),
				output.Cell(user),
				output.Cell(os),
				d.ClientVersion,
				d.LastSeenAt,
				strconv.Itoa(d.ActiveRegistrations),
			}
			if withProfile {
				profile := ""
				if d.LastSeenRegistration != nil && d.LastSeenRegistration.Policy != nil {
					profile = d.LastSeenRegistration.Policy.Name
					if profile == "" {
						profile = d.LastSeenRegistration.Policy.ID
					}
				}
				row = append(row, output.Cell(profile))
			}
			rows = append(rows, row)
		}
		return headers, rows, true
	}
}

func newDevicesFleetListCmd(g *globalOpts) *cobra.Command {
	var opts devicesFleetListOpts
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List enrolled WARP devices",
		Long: `List the WARP devices enrolled in an account.

By default the API returns only devices that still have an active
registration; pass --active-registrations include to also see devices whose
registrations were revoked or deleted.

Examples:

  cf devices fleet list
  cf devices fleet list --search macbook --sort-by last_seen_at --sort-order desc
  cf devices fleet list --user-email alice@example.com --with-profile`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.perPageSet = cmd.Flags().Changed("per-page")
			q, err := buildDevicesFleetListQuery(opts)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: devicesFleetPath(accountID, "physical-devices"), Query: q}
			// This endpoint is cursor-paginated (result_info.cursor), which
			// the shared paginator follows.
			return runDevicesFleetList(cmd, g, client, req, true, devicesFleetDeviceTable(opts.withProfile))
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.search, "search", "", "search device name, user, serial number, and other details")
	f.StringVar(&opts.userEmail, "user-email", "", "filter by the last seen user's email")
	f.StringVar(&opts.activeRegistrations, "active-registrations", "", "registration filter: "+strings.Join(devicesFleetRegistrationFilters, ", ")+" (default: only)")
	f.StringVar(&opts.seenAfter, "seen-after", "", "only devices last seen after this timestamp (RFC3339)")
	f.StringVar(&opts.seenBefore, "seen-before", "", "only devices last seen before this timestamp (RFC3339)")
	f.StringVar(&opts.sortBy, "sort-by", "", "order results by: "+strings.Join(devicesFleetSortFields, ", "))
	f.StringVar(&opts.sortOrder, "sort-order", "", "sort direction: "+strings.Join(devicesFleetSortOrders, ", "))
	f.IntVar(&opts.perPage, "per-page", 0, "devices to request per page (all pages are fetched)")
	f.BoolVar(&opts.withProfile, "with-profile", false, "include the device settings profile of the last seen registration")
	return cmd
}

func newDevicesFleetGetCmd(g *globalOpts) *cobra.Command {
	var withProfile bool
	cmd := &cobra.Command{
		Use:   "get <device-id>",
		Short: "Show one WARP device",
		Long: `Show one WARP device.

Examples:

  cf devices fleet get 8b7a6f5e-4d3c-2b1a-0f9e-8d7c6b5a4f3e
  cf devices fleet get 8b7a6f5e-4d3c-2b1a-0f9e-8d7c6b5a4f3e --with-profile`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := devicesFleetRequireID("device ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			q := url.Values{}
			if withProfile {
				q.Set("include", devicesFleetProfileInclude)
			}
			req := api.Request{
				Method: "GET",
				Path:   devicesFleetPath(accountID, "physical-devices") + "/" + url.PathEscape(args[0]),
				Query:  q,
			}
			return runDevicesFleetRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&withProfile, "with-profile", false, "include the device settings profile of the last seen registration")
	return cmd
}

func newDevicesFleetRevokeCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "revoke <device-id>",
		Short: "Revoke every WARP registration of a device",
		Long: `Revoke every WARP registration belonging to a device.

The device stops connecting through Zero Trust as soon as the revocation
propagates, and its user has to sign in to WARP again to re-register. The
device record itself is kept, so this is reversible: re-enable a revoked
registration with ` + "`cf api devices registrations-unrevoke`" + `.

Examples:

  cf devices fleet revoke 8b7a6f5e-4d3c-2b1a-0f9e-8d7c6b5a4f3e
  cf devices fleet revoke 8b7a6f5e-4d3c-2b1a-0f9e-8d7c6b5a4f3e --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := devicesFleetRequireID("device ID", args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				prompt := fmt.Sprintf("Revoke all WARP registrations for device %s? It disconnects from Zero Trust and its user must sign in to WARP again.", args[0])
				if !confirm(prompt) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{
				Method: "POST",
				Path:   devicesFleetPath(accountID, "physical-devices") + "/" + url.PathEscape(args[0]) + "/revoke",
			}
			return runDevicesFleetRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- device settings profiles ----------------------------------------------

type devicesFleetProfile struct {
	PolicyID   string   `json:"policy_id"`
	Name       string   `json:"name"`
	Default    bool     `json:"default"`
	Enabled    *bool    `json:"enabled"`
	Precedence *float64 `json:"precedence"`
	Match      string   `json:"match"`
}

func newDevicesFleetProfileCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage WARP device settings profiles",
	}
	cmd.AddCommand(
		newDevicesFleetProfileListCmd(g),
		newDevicesFleetProfileGetCmd(g),
		newDevicesFleetProfileCreateCmd(g),
		newDevicesFleetProfileUpdateCmd(g),
		newDevicesFleetProfileDeleteCmd(g),
	)
	return cmd
}

// devicesFleetProfilePath returns the endpoint for a profile. An empty
// profileID addresses the account's default profile, which lives at its own
// path and has no ID of its own.
func devicesFleetProfilePath(accountID, profileID string) string {
	p := devicesFleetPath(accountID, "policy")
	if profileID != "" {
		p += "/" + url.PathEscape(profileID)
	}
	return p
}

// devicesFleetValidateProfileID checks a profile ID against the spec's 36
// character bound before any client work.
func devicesFleetValidateProfileID(id string) error {
	if err := devicesFleetRequireID("profile ID", id); err != nil {
		return err
	}
	if devicesFleetLen(id) > devicesFleetMaxProfileID {
		return fmt.Errorf("profile ID must be at most %d characters, got %d", devicesFleetMaxProfileID, devicesFleetLen(id))
	}
	return nil
}

// devicesFleetProfileArg reads the optional <profile-id> argument. No argument
// means the account's default profile.
func devicesFleetProfileArg(args []string) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	if err := devicesFleetValidateProfileID(args[0]); err != nil {
		return "", err
	}
	return args[0], nil
}

func devicesFleetProfileTable(raw json.RawMessage) ([]string, [][]string, bool) {
	var profiles []devicesFleetProfile
	if err := json.Unmarshal(raw, &profiles); err != nil {
		return nil, nil, false
	}
	headers := []string{"ID", "NAME", "DEFAULT", "ENABLED", "PRECEDENCE", "MATCH"}
	rows := make([][]string, 0, len(profiles))
	for _, p := range profiles {
		enabled := ""
		if p.Enabled != nil {
			enabled = strconv.FormatBool(*p.Enabled)
		}
		precedence := ""
		if p.Precedence != nil {
			precedence = strconv.FormatFloat(*p.Precedence, 'f', -1, 64)
		}
		rows = append(rows, []string{
			p.PolicyID,
			output.Cell(p.Name),
			strconv.FormatBool(p.Default),
			enabled,
			precedence,
			output.Cell(p.Match),
		})
	}
	return headers, rows, true
}

func newDevicesFleetProfileListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List WARP device settings profiles",
		Long: `List the device settings profiles configured for an account, including the
default profile. Profiles are evaluated in ascending precedence order.

Example:

  cf devices fleet profile list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: devicesFleetPath(accountID, "policies")}
			// This endpoint takes no pagination parameters and returns the
			// whole collection, so it is fetched with a single request.
			return runDevicesFleetList(cmd, g, client, req, false, devicesFleetProfileTable)
		},
	}
	return cmd
}

func newDevicesFleetProfileGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [profile-id]",
		Short: "Show one WARP device settings profile",
		Long: `Show one device settings profile. With no profile ID, the account's default
profile is shown.

Examples:

  cf devices fleet profile get
  cf devices fleet profile get 699d98642c564d2e855e9661899b7252`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := devicesFleetProfileArg(args)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: devicesFleetProfilePath(accountID, profileID)}
			return runDevicesFleetRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// devicesFleetProfileOpts carries every scalar profile setting this porcelain
// exposes. changed reports whether a flag was given, so an unset boolean is
// never confused with an explicit false.
type devicesFleetProfileOpts struct {
	name        string
	match       string
	description string
	precedence  int
	enabled     bool

	serviceMode string
	proxyPort   int

	autoConnect        int
	captivePortal      int
	lanAllowMinutes    int
	lanAllowSubnetSize int

	allowModeSwitch            bool
	allowUpdates               bool
	allowedToLeave             bool
	switchLocked               bool
	excludeOfficeIPs           bool
	disableAutoFallback        bool
	registerInterfaceIPWithDNS bool
	sccmVPNBoundarySupport     bool

	supportURL     string
	tunnelProtocol string

	changed func(string) bool
}

func (o devicesFleetProfileOpts) isChanged(flag string) bool {
	return o.changed != nil && o.changed(flag)
}

// devicesFleetCustomOnlyFlags exist only on custom profiles. The default
// profile's PATCH schema in the pinned spec has no name, match, precedence,
// description, or enabled field — it always applies to every device that no
// custom profile matched.
var devicesFleetCustomOnlyFlags = []string{"name", "match", "precedence", "description", "enabled"}

// devicesFleetSharedBoolFlags map one-to-one onto boolean profile settings and
// are accepted on both the default and custom profiles.
var devicesFleetSharedBoolFlags = []struct {
	flag string
	key  string
	get  func(devicesFleetProfileOpts) bool
}{
	{"allow-mode-switch", "allow_mode_switch", func(o devicesFleetProfileOpts) bool { return o.allowModeSwitch }},
	{"allow-updates", "allow_updates", func(o devicesFleetProfileOpts) bool { return o.allowUpdates }},
	{"allowed-to-leave", "allowed_to_leave", func(o devicesFleetProfileOpts) bool { return o.allowedToLeave }},
	{"switch-locked", "switch_locked", func(o devicesFleetProfileOpts) bool { return o.switchLocked }},
	{"exclude-office-ips", "exclude_office_ips", func(o devicesFleetProfileOpts) bool { return o.excludeOfficeIPs }},
	{"disable-auto-fallback", "disable_auto_fallback", func(o devicesFleetProfileOpts) bool { return o.disableAutoFallback }},
	{"register-interface-ip-with-dns", "register_interface_ip_with_dns", func(o devicesFleetProfileOpts) bool { return o.registerInterfaceIPWithDNS }},
	{"sccm-vpn-boundary-support", "sccm_vpn_boundary_support", func(o devicesFleetProfileOpts) bool { return o.sccmVPNBoundarySupport }},
}

// devicesFleetSharedIntFlags map one-to-one onto numeric profile settings.
var devicesFleetSharedIntFlags = []struct {
	flag string
	key  string
	get  func(devicesFleetProfileOpts) int
}{
	{"auto-connect", "auto_connect", func(o devicesFleetProfileOpts) int { return o.autoConnect }},
	{"captive-portal", "captive_portal", func(o devicesFleetProfileOpts) int { return o.captivePortal }},
	{"lan-allow-minutes", "lan_allow_minutes", func(o devicesFleetProfileOpts) int { return o.lanAllowMinutes }},
	{"lan-allow-subnet-size", "lan_allow_subnet_size", func(o devicesFleetProfileOpts) int { return o.lanAllowSubnetSize }},
}

// devicesFleetSharedStringFlags map one-to-one onto string profile settings.
// Both accept an empty value, which is the API's own default.
var devicesFleetSharedStringFlags = []struct {
	flag string
	key  string
	get  func(devicesFleetProfileOpts) string
}{
	{"support-url", "support_url", func(o devicesFleetProfileOpts) string { return o.supportURL }},
	{"tunnel-protocol", "tunnel_protocol", func(o devicesFleetProfileOpts) string { return o.tunnelProtocol }},
}

// devicesFleetUpdatableFlags is every flag `profile update` accepts on a
// custom profile, used to build the "nothing to update" error.
func devicesFleetUpdatableFlags(isDefault bool) []string {
	var flags []string
	if !isDefault {
		flags = append(flags, devicesFleetCustomOnlyFlags...)
	}
	flags = append(flags, "service-mode", "proxy-port")
	for _, f := range devicesFleetSharedIntFlags {
		flags = append(flags, f.flag)
	}
	for _, f := range devicesFleetSharedBoolFlags {
		flags = append(flags, f.flag)
	}
	for _, f := range devicesFleetSharedStringFlags {
		flags = append(flags, f.flag)
	}
	return flags
}

// buildDevicesFleetProfileBody builds a create (POST) or partial update
// (PATCH) body. create requires the spec's three required fields; isDefault
// selects the account default profile, whose schema has no identity fields.
func buildDevicesFleetProfileBody(o devicesFleetProfileOpts, create, isDefault bool) ([]byte, error) {
	if isDefault {
		for _, flag := range devicesFleetCustomOnlyFlags {
			if o.isChanged(flag) {
				return nil, fmt.Errorf("--%s applies to custom profiles only: the default profile has no name, match expression, precedence, description, or enabled toggle; pass a profile ID to update a custom profile", flag)
			}
		}
	}

	body := map[string]any{}

	if create {
		name := strings.TrimSpace(o.name)
		if name == "" {
			return nil, errors.New("--name is required: the display name of the device settings profile")
		}
		if devicesFleetLen(name) > devicesFleetMaxProfileName {
			return nil, fmt.Errorf("--name must be at most %d characters, got %d", devicesFleetMaxProfileName, devicesFleetLen(name))
		}
		if strings.TrimSpace(o.match) == "" {
			return nil, errors.New(`--match is required: a wirefilter expression selecting the devices this profile applies to, for example 'identity.email == "alice@example.com"'`)
		}
		if devicesFleetLen(o.match) > devicesFleetMaxMatch {
			return nil, fmt.Errorf("--match must be at most %d characters, got %d", devicesFleetMaxMatch, devicesFleetLen(o.match))
		}
		if !o.isChanged("precedence") {
			return nil, errors.New("--precedence is required: lower values are evaluated first")
		}
		body["name"] = name
		body["match"] = o.match
		body["precedence"] = o.precedence
		if o.isChanged("description") {
			if devicesFleetLen(o.description) > devicesFleetMaxDescription {
				return nil, fmt.Errorf("--description must be at most %d characters, got %d", devicesFleetMaxDescription, devicesFleetLen(o.description))
			}
			body["description"] = o.description
		}
		if o.isChanged("enabled") {
			body["enabled"] = o.enabled
		}
	} else if !isDefault {
		if o.isChanged("name") {
			name := strings.TrimSpace(o.name)
			if name == "" {
				return nil, errors.New("--name must not be empty")
			}
			if devicesFleetLen(name) > devicesFleetMaxProfileName {
				return nil, fmt.Errorf("--name must be at most %d characters, got %d", devicesFleetMaxProfileName, devicesFleetLen(name))
			}
			body["name"] = name
		}
		if o.isChanged("match") {
			if strings.TrimSpace(o.match) == "" {
				return nil, errors.New("--match must not be empty")
			}
			if devicesFleetLen(o.match) > devicesFleetMaxMatch {
				return nil, fmt.Errorf("--match must be at most %d characters, got %d", devicesFleetMaxMatch, devicesFleetLen(o.match))
			}
			body["match"] = o.match
		}
		if o.isChanged("precedence") {
			body["precedence"] = o.precedence
		}
		if o.isChanged("description") {
			if devicesFleetLen(o.description) > devicesFleetMaxDescription {
				return nil, fmt.Errorf("--description must be at most %d characters, got %d", devicesFleetMaxDescription, devicesFleetLen(o.description))
			}
			body["description"] = o.description
		}
		if o.isChanged("enabled") {
			body["enabled"] = o.enabled
		}
	}

	// service_mode_v2 carries mode and port, both optional in the pinned spec's
	// PATCH schemas, so either may be sent on its own.
	if o.isChanged("service-mode") || o.isChanged("proxy-port") {
		serviceMode := map[string]any{}
		if o.isChanged("service-mode") {
			mode := strings.TrimSpace(o.serviceMode)
			if mode == "" {
				return nil, errors.New("--service-mode must not be empty")
			}
			serviceMode["mode"] = mode
		}
		if o.isChanged("proxy-port") {
			serviceMode["port"] = o.proxyPort
		}
		body["service_mode_v2"] = serviceMode
	}

	for _, f := range devicesFleetSharedIntFlags {
		if o.isChanged(f.flag) {
			body[f.key] = f.get(o)
		}
	}
	for _, f := range devicesFleetSharedBoolFlags {
		if o.isChanged(f.flag) {
			body[f.key] = f.get(o)
		}
	}
	for _, f := range devicesFleetSharedStringFlags {
		if o.isChanged(f.flag) {
			body[f.key] = f.get(o)
		}
	}

	if !create && len(body) == 0 {
		return nil, fmt.Errorf("nothing to update: pass at least one of --%s", strings.Join(devicesFleetUpdatableFlags(isDefault), ", --"))
	}
	return json.Marshal(body)
}

// devicesFleetProfileFlags registers the settings that create and update share
// verbatim. The identity flags (name, match, precedence, description, enabled)
// differ between the two commands and are registered by each of them.
func devicesFleetProfileFlags(cmd *cobra.Command, o *devicesFleetProfileOpts) {
	f := cmd.Flags()
	f.StringVar(&o.serviceMode, "service-mode", "", "WARP client service mode (for example warp, proxy, or postquantum — see the Zero Trust docs)")
	f.IntVar(&o.proxyPort, "proxy-port", 0, "listening port used by proxy service modes")
	f.IntVar(&o.autoConnect, "auto-connect", 0, "seconds before WARP reconnects after the user disables it (0 = never auto-reconnect)")
	f.IntVar(&o.captivePortal, "captive-portal", 0, "seconds of captive portal detection allowed before WARP reconnects")
	f.IntVar(&o.lanAllowMinutes, "lan-allow-minutes", 0, "minutes of local network access allowed (0 = until the next WARP reconnection)")
	f.IntVar(&o.lanAllowSubnetSize, "lan-allow-subnet-size", 0, "subnet prefix size allowed for local network access")
	f.BoolVar(&o.allowModeSwitch, "allow-mode-switch", false, "let users switch WARP between modes")
	f.BoolVar(&o.allowUpdates, "allow-updates", false, "notify users when a new WARP client version is available")
	f.BoolVar(&o.allowedToLeave, "allowed-to-leave", false, "let users disconnect the device from the organization")
	f.BoolVar(&o.switchLocked, "switch-locked", false, "prevent users from turning the WARP switch off")
	f.BoolVar(&o.excludeOfficeIPs, "exclude-office-ips", false, "add Microsoft Office IPs to the Split Tunnel exclude list")
	f.BoolVar(&o.disableAutoFallback, "disable-auto-fallback", false, "stop falling back to system DNS for fallback domains without a dns_server")
	f.BoolVar(&o.registerInterfaceIPWithDNS, "register-interface-ip-with-dns", false, "register the WARP interface IP with on-premises DNS")
	f.BoolVar(&o.sccmVPNBoundarySupport, "sccm-vpn-boundary-support", false, "report the device as inside a VPN boundary to SCCM (Windows only)")
	f.StringVar(&o.supportURL, "support-url", "", "URL opened by the WARP client's Send Feedback button")
	f.StringVar(&o.tunnelProtocol, "tunnel-protocol", "", "tunnel protocol the WARP client uses (empty = account default)")
}

func newDevicesFleetProfileCreateCmd(g *globalOpts) *cobra.Command {
	var opts devicesFleetProfileOpts
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a WARP device settings profile",
		Long: `Create a custom device settings profile.

--match is a wirefilter expression over identity.email, identity.groups.id,
identity.groups.name, identity.groups.email, identity.service_token_uuid,
identity.saml_attributes, network, os.name, and os.version. Lower --precedence
values are evaluated first; devices matching no custom profile get the default
profile.

Split Tunnel routes, Local Domain Fallback, DNS search suffixes, and virtual
networks are whole-list replacements — set them with ` + "`cf api devices`" + `.

Examples:

  cf devices fleet profile create --name Contractors --precedence 100 \
    --match 'identity.groups.name == "contractors"'
  cf devices fleet profile create --name Kiosks --precedence 50 \
    --match 'os.name == "windows"' --switch-locked --service-mode warp`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.changed = cmd.Flags().Changed
			body, err := buildDevicesFleetProfileBody(opts, true, false)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: devicesFleetProfilePath(accountID, ""), Body: body}
			return runDevicesFleetRequest(cmd, g, client, req)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.name, "name", "", "display name of the profile")
	f.StringVar(&opts.match, "match", "", "wirefilter expression selecting the devices this profile applies to")
	f.IntVar(&opts.precedence, "precedence", 0, "evaluation order; lower values are evaluated first")
	f.StringVar(&opts.description, "description", "", "description of the profile")
	f.BoolVar(&opts.enabled, "enabled", true, "apply the profile to matching devices")
	devicesFleetProfileFlags(cmd, &opts)
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("match")
	_ = cmd.MarkFlagRequired("precedence")
	return cmd
}

func newDevicesFleetProfileUpdateCmd(g *globalOpts) *cobra.Command {
	var opts devicesFleetProfileOpts
	cmd := &cobra.Command{
		Use:   "update [profile-id]",
		Short: "Update fields of a WARP device settings profile",
		Long: `Update a device settings profile in place. Only the flags you pass are sent.

With no profile ID the account's default profile is updated. The default
profile has no name, match expression, precedence, description, or enabled
toggle, so those flags need a profile ID.

Examples:

  cf devices fleet profile update --switch-locked --captive-portal 180
  cf devices fleet profile update 699d98642c564d2e855e9661899b7252 --precedence 20
  cf devices fleet profile update 699d98642c564d2e855e9661899b7252 --service-mode proxy --proxy-port 3128`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := devicesFleetProfileArg(args)
			if err != nil {
				return err
			}
			opts.changed = cmd.Flags().Changed
			body, err := buildDevicesFleetProfileBody(opts, false, profileID == "")
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: devicesFleetProfilePath(accountID, profileID), Body: body}
			return runDevicesFleetRequest(cmd, g, client, req)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.name, "name", "", "display name of the profile (custom profiles only)")
	f.StringVar(&opts.match, "match", "", "wirefilter expression selecting matching devices (custom profiles only)")
	f.IntVar(&opts.precedence, "precedence", 0, "evaluation order; lower values are evaluated first (custom profiles only)")
	f.StringVar(&opts.description, "description", "", "description of the profile (custom profiles only)")
	f.BoolVar(&opts.enabled, "enabled", false, "apply the profile to matching devices (custom profiles only)")
	devicesFleetProfileFlags(cmd, &opts)
	return cmd
}

func newDevicesFleetProfileDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <profile-id>",
		Short: "Delete a WARP device settings profile",
		Long: `Delete a custom device settings profile.

Devices that matched it fall through to the next matching profile, or to the
account's default profile if none match. The account default profile cannot be
deleted.

Examples:

  cf devices fleet profile delete 699d98642c564d2e855e9661899b7252
  cf devices fleet profile delete 699d98642c564d2e855e9661899b7252 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := devicesFleetValidateProfileID(args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := devicesFleetAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				prompt := fmt.Sprintf("Delete WARP profile %s? Devices matching it fall back to the next matching profile, or the account default.", args[0])
				if !confirm(prompt) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: devicesFleetProfilePath(accountID, args[0])}
			return runDevicesFleetRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}
