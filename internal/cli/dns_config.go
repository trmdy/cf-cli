package cli

// DNS configuration porcelain: the zone-level DNS knobs that live next to the
// records in internal/cli/dns.go, plus the account's DNS Firewall clusters.
//
//	cf dns config get|set            zone DNS settings  (zone-scoped)
//	cf dns config dnssec ...         DNSSEC state       (zone-scoped)
//	cf dns config firewall ...       DNS Firewall clusters (account-scoped)
//
// Scope notes, so the boundaries are explicit rather than accidental:
//
//   - Zone DNS settings PATCH is a true partial update at the top level, so
//     scalar flags are sent on their own. Its `soa` and `nameservers` members
//     are whole objects the API replaces (the read schema requires every SOA
//     component and a nameserver `type`), so changing any of those fields is
//     read-merge-write: read the current settings, merge the changed subfields
//     onto the raw nested object — preserving fields this CLI does not model —
//     and validate the complete object before writing. That read runs under
//     --dry-run too, because the request being previewed cannot be built
//     without it. Purely scalar `set` invocations never read.
//   - The deprecated `foundation_dns` setting and the `internal_dns` reference
//     zone stay on `cf api dns`; so do DNS views, account-level DNS settings,
//     `DELETE /zones/{id}/dnssec`, the ZSK listing, and DNS Firewall analytics
//     and reverse-DNS configuration.
//   - DNS Firewall PATCH carries no required fields in the pinned spec, so
//     `firewall update` sends only what changed and never reads first.
//     `--no-ratelimit` exists because the spec documents null as "disable rate
//     limiting"; `negative_cache_ttl` is merely nullable with no documented
//     null semantics, so clearing it stays on `cf api dns-firewall`.
//
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// dnsConfigZoneModes are the zone_mode values the zone DNS settings API
// accepts.
var dnsConfigZoneModes = []string{"standard", "cdn_only", "dns_only"}

// dnsConfigNameserverTypes are the nameservers.type values the zone DNS
// settings API accepts.
var dnsConfigNameserverTypes = []string{
	"cloudflare.standard", "cloudflare.advanced",
	"custom.account", "custom.tenant", "custom.zone",
}

// dnsConfigSOAFields are the SOA components the read schema marks required.
// A merged `soa` object must carry all of them before it is written back.
var dnsConfigSOAFields = []string{"mname", "rname", "refresh", "retry", "expire", "min_ttl", "ttl"}

func newDNSConfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage zone DNS settings, DNSSEC, and DNS Firewall clusters",
		Long: `Manage DNS configuration.

` + "`get`" + ` and ` + "`set`" + ` read and change a zone's DNS settings, ` + "`dnssec`" + ` controls
DNSSEC signing for a zone, and ` + "`firewall`" + ` manages the account's DNS Firewall
clusters. Zone-scoped commands take --zone (name or ID); firewall commands use
the account from --account-id, the environment, or the profile.`,
	}
	cmd.AddCommand(
		newDNSConfigGetCmd(g),
		newDNSConfigSetCmd(g),
		newDNSConfigDNSSECCmd(g),
		newDNSConfigFirewallCmd(g),
	)
	return cmd
}

// --- shared helpers --------------------------------------------------------

func dnsConfigSettingsPath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/dns_settings"
}

func dnsConfigDNSSECPath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/dnssec"
}

func dnsConfigFirewallPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/dns_firewall"
}

func dnsConfigFirewallItemPath(accountID, clusterID string) string {
	return dnsConfigFirewallPath(accountID) + "/" + url.PathEscape(clusterID)
}

// dnsConfigFirewallIDMax is the identifier length the pinned spec allows for
// a DNS Firewall cluster (dns-firewall_identifier, maxLength 32).
const dnsConfigFirewallIDMax = 32

// dnsConfigFirewallNameMax is the cluster name length the pinned spec allows.
// JSON Schema measures maxLength in Unicode code points, not bytes.
const dnsConfigFirewallNameMax = 160

// dnsConfigFirewallClusterID checks a cluster identifier against the spec's
// length limit. Callers run it before building a client so a typo fails
// locally instead of as a request the API would reject anyway.
func dnsConfigFirewallClusterID(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("cluster ID must not be empty")
	}
	if n := utf8.RuneCountInString(id); n > dnsConfigFirewallIDMax {
		return fmt.Errorf("invalid cluster ID %q: it must be at most %d characters, got %d", id, dnsConfigFirewallIDMax, n)
	}
	return nil
}

// dnsConfigAccountID validates the resolved account scope for the
// account-scoped DNS Firewall commands.
func dnsConfigAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

func runDNSConfigRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// dnsConfigFetchObject reads a resource as a raw JSON object so unmodelled
// fields survive a read-merge-write.
func dnsConfigFetchObject(ctx context.Context, client *api.Client, path, label string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s before update: %w", label, err)
	}
	var value any
	if err := json.Unmarshal(env.Result, &value); err != nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	return obj, nil
}

// dnsConfigNestedObject copies the named nested object out of a raw resource so
// a partial change can be merged onto it without dropping unknown fields.
func dnsConfigNestedObject(obj map[string]any, key, label string) (map[string]any, error) {
	raw, ok := obj[key]
	if !ok || raw == nil {
		return nil, fmt.Errorf("read %s before update: the response had no %q object to merge into", label, key)
	}
	cur, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("read %s before update: %q was not an object", label, key)
	}
	out := make(map[string]any, len(cur))
	for k, v := range cur {
		out[k] = v
	}
	return out, nil
}

// dnsConfigEnum matches a flag value case-insensitively against the values the
// API documents and returns the canonical spelling.
func dnsConfigEnum(flag, value string, allowed []string) (string, error) {
	for _, a := range allowed {
		if strings.EqualFold(a, value) {
			return a, nil
		}
	}
	return "", fmt.Errorf("invalid --%s %q (expected one of: %s)", flag, value, strings.Join(allowed, ", "))
}

// dnsConfigRange enforces an inclusive numeric bound from the pinned spec.
func dnsConfigRange(flag string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("--%s must be between %d and %d, got %d", flag, min, max, value)
	}
	return nil
}

// --- zone DNS settings -----------------------------------------------------

func newDNSConfigGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show a zone's DNS settings",
		Long: `Show a zone's DNS settings: CNAME flattening, multi-provider mode, nameserver
configuration, and the SOA record components.

Examples:

  cf dns config get --zone example.com
  cf dns config get --zone example.com --query .soa`,
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
			req := api.Request{Method: "GET", Path: dnsConfigSettingsPath(zoneID)}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

// dnsConfigSettingsFlags holds the `set` flag values. Which of them were
// actually given is read from the command's flag set, so "false" and "unset"
// stay distinguishable.
type dnsConfigSettingsFlags struct {
	zone               string
	flattenAllCNAMEs   bool
	multiProvider      bool
	secondaryOverrides bool
	nsTTL              int
	zoneMode           string
	nameserverType     string
	nsSet              int
	soaMName           string
	soaRName           string
	soaRefresh         int
	soaRetry           int
	soaExpire          int
	soaMinTTL          int
	soaTTL             int
}

// dnsConfigSettingsPatch splits the changed flags into the top-level PATCH
// body and the two nested objects that have to be merged onto current state.
// It validates every bound and enum before any of them is used.
func dnsConfigSettingsPatch(cmd *cobra.Command, f dnsConfigSettingsFlags) (top, soa, nameservers map[string]any, err error) {
	changed := cmd.Flags().Changed
	top = map[string]any{}
	soa = map[string]any{}
	nameservers = map[string]any{}

	if changed("flatten-all-cnames") {
		top["flatten_all_cnames"] = f.flattenAllCNAMEs
	}
	if changed("multi-provider") {
		top["multi_provider"] = f.multiProvider
	}
	if changed("secondary-overrides") {
		top["secondary_overrides"] = f.secondaryOverrides
	}
	if changed("ns-ttl") {
		if err := dnsConfigRange("ns-ttl", f.nsTTL, 30, 86400); err != nil {
			return nil, nil, nil, err
		}
		top["ns_ttl"] = f.nsTTL
	}
	if changed("zone-mode") {
		mode, err := dnsConfigEnum("zone-mode", f.zoneMode, dnsConfigZoneModes)
		if err != nil {
			return nil, nil, nil, err
		}
		top["zone_mode"] = mode
	}

	if changed("nameserver-type") {
		nsType, err := dnsConfigEnum("nameserver-type", f.nameserverType, dnsConfigNameserverTypes)
		if err != nil {
			return nil, nil, nil, err
		}
		nameservers["type"] = nsType
	}
	if changed("ns-set") {
		if err := dnsConfigRange("ns-set", f.nsSet, 1, 5); err != nil {
			return nil, nil, nil, err
		}
		nameservers["ns_set"] = f.nsSet
	}

	if changed("soa-mname") {
		if strings.TrimSpace(f.soaMName) == "" {
			return nil, nil, nil, errors.New("--soa-mname must not be empty: pass the primary nameserver hostname")
		}
		soa["mname"] = f.soaMName
	}
	if changed("soa-rname") {
		if strings.TrimSpace(f.soaRName) == "" {
			return nil, nil, nil, errors.New("--soa-rname must not be empty: pass the zone administrator address, e.g. admin.example.com")
		}
		soa["rname"] = f.soaRName
	}
	for _, n := range []struct {
		flag, key string
		value     int
		min, max  int
		set       bool
	}{
		{"soa-refresh", "refresh", f.soaRefresh, 600, 86400, changed("soa-refresh")},
		{"soa-retry", "retry", f.soaRetry, 600, 86400, changed("soa-retry")},
		{"soa-expire", "expire", f.soaExpire, 86400, 2419200, changed("soa-expire")},
		{"soa-min-ttl", "min_ttl", f.soaMinTTL, 60, 86400, changed("soa-min-ttl")},
		{"soa-ttl", "ttl", f.soaTTL, 300, 86400, changed("soa-ttl")},
	} {
		if !n.set {
			continue
		}
		if err := dnsConfigRange(n.flag, n.value, n.min, n.max); err != nil {
			return nil, nil, nil, err
		}
		soa[n.key] = n.value
	}

	if len(top)+len(soa)+len(nameservers) == 0 {
		return nil, nil, nil, errors.New("nothing to update: pass at least one of --flatten-all-cnames, --multi-provider, --secondary-overrides, --ns-ttl, --zone-mode, --nameserver-type, --ns-set, or a --soa-* flag")
	}
	return top, soa, nameservers, nil
}

// dnsConfigValidateSOA checks that a merged SOA object still carries every
// component the API requires, so an incomplete write fails locally with a
// useful message instead of as a 400.
func dnsConfigValidateSOA(soa map[string]any) error {
	var missing []string
	for _, k := range dnsConfigSOAFields {
		if _, ok := soa[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the zone's current SOA record is missing %s; set every component explicitly with the --soa-* flags", strings.Join(missing, ", "))
	}
	return nil
}

// dnsConfigValidateNameservers checks that a merged nameservers object carries
// the type the API requires.
func dnsConfigValidateNameservers(ns map[string]any) error {
	t, ok := ns["type"].(string)
	if !ok || strings.TrimSpace(t) == "" {
		return errors.New("the zone's current nameserver configuration has no type; pass --nameserver-type as well")
	}
	return nil
}

// buildDNSConfigSettingsBody assembles the PATCH body. When a nested `soa` or
// `nameservers` field changes it reads the current settings first (including
// under --dry-run) because the API replaces those objects wholesale.
func buildDNSConfigSettingsBody(cmd *cobra.Command, client *api.Client, zoneID string, f dnsConfigSettingsFlags) ([]byte, error) {
	top, soaPatch, nsPatch, err := dnsConfigSettingsPatch(cmd, f)
	if err != nil {
		return nil, err
	}
	if len(soaPatch) > 0 || len(nsPatch) > 0 {
		label := "DNS settings for zone " + zoneID
		cur, err := dnsConfigFetchObject(cmd.Context(), client, dnsConfigSettingsPath(zoneID), label)
		if err != nil {
			return nil, err
		}
		if len(soaPatch) > 0 {
			merged, err := dnsConfigNestedObject(cur, "soa", label)
			if err != nil {
				return nil, err
			}
			for k, v := range soaPatch {
				merged[k] = v
			}
			if err := dnsConfigValidateSOA(merged); err != nil {
				return nil, err
			}
			top["soa"] = merged
		}
		if len(nsPatch) > 0 {
			merged, err := dnsConfigNestedObject(cur, "nameservers", label)
			if err != nil {
				return nil, err
			}
			for k, v := range nsPatch {
				merged[k] = v
			}
			if err := dnsConfigValidateNameservers(merged); err != nil {
				return nil, err
			}
			top["nameservers"] = merged
		}
	}
	return json.Marshal(top)
}

func newDNSConfigSetCmd(g *globalOpts) *cobra.Command {
	var f dnsConfigSettingsFlags
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Change a zone's DNS settings",
		Long: `Change a zone's DNS settings. Only the flags you pass are sent.

The SOA record and the nameserver configuration are single objects on the API,
so changing one --soa-* or nameserver field first reads the zone's current
settings and merges your change onto them; that read also happens under
--dry-run, since the request cannot be built without it.

Examples:

  cf dns config set --zone example.com --flatten-all-cnames
  cf dns config set --zone example.com --zone-mode dns_only --ns-ttl 300
  cf dns config set --zone example.com --soa-ttl 3600 --soa-min-ttl 300
  cf dns config set --zone example.com --nameserver-type cloudflare.advanced`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate the whole local input contract before building a
			// client or resolving the zone; the body builder repeats it
			// because it is also reachable directly from tests.
			if _, _, _, err := dnsConfigSettingsPatch(cmd, f); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, f.zone)
			if err != nil {
				return err
			}
			body, err := buildDNSConfigSettingsBody(cmd, client, zoneID, f)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: dnsConfigSettingsPath(zoneID), Body: body}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&f.zone, "zone", "", "zone name or ID (default: configured zone)")
	dnsConfigSettingFlags(cmd, &f)
	return cmd
}

// dnsConfigSettingFlags registers the zone DNS settings flags. It is separate
// from the command so the patch builder can be exercised on its own.
func dnsConfigSettingFlags(cmd *cobra.Command, f *dnsConfigSettingsFlags) {
	flags := cmd.Flags()
	flags.BoolVar(&f.flattenAllCNAMEs, "flatten-all-cnames", false, "flatten every CNAME record in the zone")
	flags.BoolVar(&f.multiProvider, "multi-provider", false, "keep the zone active while non-Cloudflare NS records exist")
	flags.BoolVar(&f.secondaryOverrides, "secondary-overrides", false, "allow override records and apex CNAME flattening on a secondary zone")
	flags.IntVar(&f.nsTTL, "ns-ttl", 0, "TTL of the zone's NS records, in seconds (30-86400)")
	flags.StringVar(&f.zoneMode, "zone-mode", "", "zone mode: "+strings.Join(dnsConfigZoneModes, ", "))
	flags.StringVar(&f.nameserverType, "nameserver-type", "", "nameserver type: "+strings.Join(dnsConfigNameserverTypes, ", "))
	flags.IntVar(&f.nsSet, "ns-set", 0, "configured nameserver set to use (1-5)")
	flags.StringVar(&f.soaMName, "soa-mname", "", "SOA primary nameserver")
	flags.StringVar(&f.soaRName, "soa-rname", "", "SOA administrator address, e.g. admin.example.com")
	flags.IntVar(&f.soaRefresh, "soa-refresh", 0, "SOA refresh interval in seconds (600-86400)")
	flags.IntVar(&f.soaRetry, "soa-retry", 0, "SOA retry interval in seconds (600-86400)")
	flags.IntVar(&f.soaExpire, "soa-expire", 0, "SOA expire time in seconds (86400-2419200)")
	flags.IntVar(&f.soaMinTTL, "soa-min-ttl", 0, "SOA negative caching TTL in seconds (60-86400)")
	flags.IntVar(&f.soaTTL, "soa-ttl", 0, "TTL of the SOA record itself, in seconds (300-86400)")
}

// --- DNSSEC ----------------------------------------------------------------

func newDNSConfigDNSSECCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dnssec",
		Short: "Manage DNSSEC for a zone",
	}
	cmd.AddCommand(
		newDNSConfigDNSSECGetCmd(g),
		newDNSConfigDNSSECEnableCmd(g),
		newDNSConfigDNSSECDisableCmd(g),
	)
	return cmd
}

func newDNSConfigDNSSECGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show a zone's DNSSEC status and DS record",
		Long: `Show a zone's DNSSEC status together with the DS record to publish at the
registrar. Status is one of active, pending, disabled, pending-disabled, or
error; it stays pending until the DS record is visible at the parent zone.

Examples:

  cf dns config dnssec get --zone example.com
  cf dns config dnssec get --zone example.com --query .ds`,
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
			req := api.Request{Method: "GET", Path: dnsConfigDNSSECPath(zoneID)}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

// buildDNSConfigDNSSECEnableBody builds the enable PATCH. The three boolean
// modes are only sent when given, so the zone keeps whatever it has otherwise.
func buildDNSConfigDNSSECEnableBody(cmd *cobra.Command, multiSigner, presigned, useNSEC3 bool) ([]byte, error) {
	body := map[string]any{"status": "active"}
	if cmd.Flags().Changed("multi-signer") {
		body["dnssec_multi_signer"] = multiSigner
	}
	if cmd.Flags().Changed("presigned") {
		body["dnssec_presigned"] = presigned
	}
	if cmd.Flags().Changed("use-nsec3") {
		body["dnssec_use_nsec3"] = useNSEC3
	}
	return json.Marshal(body)
}

func newDNSConfigDNSSECEnableCmd(g *globalOpts) *cobra.Command {
	var zone string
	var multiSigner, presigned, useNSEC3 bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Enable DNSSEC signing for a zone",
		Long: `Enable DNSSEC for a zone.

Cloudflare starts signing the zone immediately, but validation only takes
effect once the DS record from ` + "`cf dns config dnssec get`" + ` is published at the
registrar. The status stays pending until then.

Examples:

  cf dns config dnssec enable --zone example.com
  cf dns config dnssec enable --zone example.com --use-nsec3
  cf dns config dnssec enable --zone example.com --presigned --multi-signer`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildDNSConfigDNSSECEnableBody(cmd, multiSigner, presigned, useNSEC3)
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
			req := api.Request{Method: "PATCH", Path: dnsConfigDNSSECPath(zoneID), Body: body}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&multiSigner, "multi-signer", false, "allow several providers to serve this signed zone at once")
	cmd.Flags().BoolVar(&presigned, "presigned", false, "accept a zone transferred in already signed by an external provider")
	cmd.Flags().BoolVar(&useNSEC3, "use-nsec3", false, "use NSEC3 instead of NSEC for authenticated denial")
	return cmd
}

func newDNSConfigDNSSECDisableCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Disable DNSSEC signing for a zone",
		Long: `Disable DNSSEC for a zone.

Remove the DS record at the registrar FIRST and wait for it to expire from
caches. A published DS record for a zone that is no longer signed makes
validating resolvers treat every answer as bogus, and the domain stops
resolving for the users behind them.

Examples:

  cf dns config dnssec disable --zone example.com
  cf dns config dnssec disable --zone example.com --force`,
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
				if !confirm(fmt.Sprintf("Disable DNSSEC for zone %s? If the DS record is still published at the registrar, validating resolvers will stop resolving this domain.", zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			body, err := json.Marshal(map[string]any{"status": "disabled"})
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: dnsConfigDNSSECPath(zoneID), Body: body}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- DNS Firewall clusters -------------------------------------------------

// dnsConfigFirewallPerPage is the endpoint's documented maximum page size.
const dnsConfigFirewallPerPage = 100

type dnsConfigFirewallCluster struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	UpstreamIPs     []string `json:"upstream_ips"`
	DNSFirewallIPs  []string `json:"dns_firewall_ips"`
	MinimumCacheTTL int      `json:"minimum_cache_ttl"`
	MaximumCacheTTL int      `json:"maximum_cache_ttl"`
	Ratelimit       *float64 `json:"ratelimit"`
	ModifiedOn      string   `json:"modified_on"`
}

func newDNSConfigFirewallCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firewall",
		Short: "Manage DNS Firewall clusters on the account",
	}
	cmd.AddCommand(
		newDNSConfigFirewallListCmd(g),
		newDNSConfigFirewallGetCmd(g),
		newDNSConfigFirewallCreateCmd(g),
		newDNSConfigFirewallUpdateCmd(g),
		newDNSConfigFirewallDeleteCmd(g),
	)
	return cmd
}

func newDNSConfigFirewallListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DNS Firewall clusters",
		Long: `List the account's DNS Firewall clusters with the addresses to point resolvers
at and the upstream nameservers behind them.

Examples:

  cf dns config firewall list
  cf dns config firewall list --output json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dnsConfigAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			q := url.Values{}
			q.Set("per_page", strconv.Itoa(dnsConfigFirewallPerPage))
			req := api.Request{Method: "GET", Path: dnsConfigFirewallPath(accountID), Query: q}
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
			var clusters []dnsConfigFirewallCluster
			if err := json.Unmarshal(env.Result, &clusters); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(clusters))
			for _, c := range clusters {
				rows = append(rows, []string{
					c.ID,
					output.Cell(c.Name),
					output.Cell(strings.Join(c.DNSFirewallIPs, ", ")),
					output.Cell(strings.Join(c.UpstreamIPs, ", ")),
					dnsConfigRatelimitCell(c.Ratelimit),
					c.ModifiedOn,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "NAME", "FIREWALL IPS", "UPSTREAM IPS", "RATELIMIT", "MODIFIED"}, rows)
		},
	}
	return cmd
}

// dnsConfigRatelimitCell renders a nullable ratelimit; null means rate
// limiting is off.
func dnsConfigRatelimitCell(v *float64) string {
	if v == nil {
		return "off"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func newDNSConfigFirewallGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <cluster-id>",
		Short: "Show one DNS Firewall cluster",
		Long: `Show the full configuration of a DNS Firewall cluster.

Examples:

  cf dns config firewall get 023e105f4ecef8ad9ca31a8372d0c353`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dnsConfigFirewallClusterID(args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dnsConfigAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dnsConfigFirewallItemPath(accountID, args[0])}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	return cmd
}

// dnsConfigFirewallFlags holds the settings shared by firewall create and
// update. Which ones were given is read from the command's flag set.
type dnsConfigFirewallFlags struct {
	name                 string
	upstreamIPs          []string
	minimumCacheTTL      int
	maximumCacheTTL      int
	negativeCacheTTL     int
	ratelimit            int
	noRatelimit          bool
	retries              int
	deprecateAnyRequests bool
	ecsFallback          bool
	attackMitigation     bool
	attackOnlyUnhealthy  bool
	ipCount              int
}

// dnsConfigValidateUpstreamIPs enforces the documented contract: at least one
// address, each a literal IPv4 or IPv6 address, and no duplicates (the API
// models the field as a set).
func dnsConfigValidateUpstreamIPs(ips []string) ([]string, error) {
	if len(ips) == 0 {
		return nil, errors.New("--upstream-ip is required: a DNS Firewall cluster needs at least one upstream nameserver")
	}
	seen := make(map[netip.Addr]bool, len(ips))
	out := make([]string, 0, len(ips))
	for _, raw := range ips {
		s := strings.TrimSpace(raw)
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return nil, fmt.Errorf("invalid --upstream-ip %q: expected an IPv4 or IPv6 address", raw)
		}
		if seen[addr] {
			return nil, fmt.Errorf("duplicate --upstream-ip %q", raw)
		}
		seen[addr] = true
		out = append(out, s)
	}
	return out, nil
}

// dnsConfigFirewallFields validates the given flags and returns them as API
// fields. create is true for the POST body, where name and upstream IPs are
// required and the IP count may be set.
func dnsConfigFirewallFields(cmd *cobra.Command, f dnsConfigFirewallFlags, create bool) (map[string]any, error) {
	changed := cmd.Flags().Changed
	body := map[string]any{}

	if create || changed("name") {
		name := strings.TrimSpace(f.name)
		if name == "" {
			return nil, errors.New("cluster name must not be empty")
		}
		if n := utf8.RuneCountInString(name); n > dnsConfigFirewallNameMax {
			return nil, fmt.Errorf("cluster name must be at most %d characters, got %d", dnsConfigFirewallNameMax, n)
		}
		body["name"] = name
	}
	if create || changed("upstream-ip") {
		ips, err := dnsConfigValidateUpstreamIPs(f.upstreamIPs)
		if err != nil {
			return nil, err
		}
		body["upstream_ips"] = ips
	}
	for _, n := range []struct {
		flag, key string
		value     int
		min, max  int
	}{
		{"minimum-cache-ttl", "minimum_cache_ttl", f.minimumCacheTTL, 30, 36000},
		{"maximum-cache-ttl", "maximum_cache_ttl", f.maximumCacheTTL, 30, 36000},
		{"negative-cache-ttl", "negative_cache_ttl", f.negativeCacheTTL, 30, 36000},
		{"retries", "retries", f.retries, 0, 2},
	} {
		if !changed(n.flag) {
			continue
		}
		if err := dnsConfigRange(n.flag, n.value, n.min, n.max); err != nil {
			return nil, err
		}
		body[n.key] = n.value
	}
	if changed("ratelimit") && changed("no-ratelimit") {
		return nil, errors.New("--ratelimit and --no-ratelimit are mutually exclusive")
	}
	if changed("ratelimit") {
		if err := dnsConfigRange("ratelimit", f.ratelimit, 100, 1000000000); err != nil {
			return nil, err
		}
		body["ratelimit"] = f.ratelimit
	}
	if changed("no-ratelimit") {
		if !f.noRatelimit {
			return nil, errors.New("--no-ratelimit takes no false form: omit it to leave rate limiting unchanged")
		}
		body["ratelimit"] = nil
	}
	if changed("deprecate-any-requests") {
		body["deprecate_any_requests"] = f.deprecateAnyRequests
	}
	if changed("ecs-fallback") {
		body["ecs_fallback"] = f.ecsFallback
	}
	if changed("attack-mitigation-only-when-unhealthy") && !changed("attack-mitigation") {
		return nil, errors.New("--attack-mitigation-only-when-unhealthy needs --attack-mitigation as well: the API replaces the whole attack mitigation setting")
	}
	if changed("attack-mitigation") {
		am := map[string]any{"enabled": f.attackMitigation}
		if changed("attack-mitigation-only-when-unhealthy") {
			am["only_when_upstream_unhealthy"] = f.attackOnlyUnhealthy
		}
		body["attack_mitigation"] = am
	}
	if changed("dns-firewall-ip-count") {
		if !create {
			return nil, errors.New("--dns-firewall-ip-count can only be set when the cluster is created")
		}
		if err := dnsConfigRange("dns-firewall-ip-count", f.ipCount, 1, 10); err != nil {
			return nil, err
		}
		body["dns_firewall_ip_count"] = f.ipCount
	}
	return body, nil
}

// dnsConfigFirewallSettingFlags registers the settings shared by firewall
// create and update.
func dnsConfigFirewallSettingFlags(cmd *cobra.Command, f *dnsConfigFirewallFlags) {
	flags := cmd.Flags()
	flags.StringArrayVar(&f.upstreamIPs, "upstream-ip", nil, "upstream nameserver IPv4/IPv6 address (repeatable; replaces the list)")
	flags.IntVar(&f.minimumCacheTTL, "minimum-cache-ttl", 0, "lower bound on cached TTLs, in seconds (30-36000)")
	flags.IntVar(&f.maximumCacheTTL, "maximum-cache-ttl", 0, "upper bound on cached TTLs, in seconds (30-36000)")
	flags.IntVar(&f.negativeCacheTTL, "negative-cache-ttl", 0, "how long negative answers such as NXDOMAIN are cached, in seconds (30-36000)")
	flags.IntVar(&f.ratelimit, "ratelimit", 0, "maximum queries per second forwarded upstream, per server (100-1000000000)")
	flags.BoolVar(&f.noRatelimit, "no-ratelimit", false, "turn off upstream rate limiting")
	flags.IntVar(&f.retries, "retries", 0, "retries when an upstream nameserver does not answer (0-2)")
	flags.BoolVar(&f.deprecateAnyRequests, "deprecate-any-requests", false, "refuse to answer queries for the ANY type")
	flags.BoolVar(&f.ecsFallback, "ecs-fallback", false, "forward the resolver subnet when no EDNS Client Subnet is sent")
	flags.BoolVar(&f.attackMitigation, "attack-mitigation", false, "automatically mitigate random-prefix attacks on the upstream nameservers")
	flags.BoolVar(&f.attackOnlyUnhealthy, "attack-mitigation-only-when-unhealthy", false, "only mitigate attacks while upstream nameservers look unhealthy (needs --attack-mitigation)")
}

func newDNSConfigFirewallCreateCmd(g *globalOpts) *cobra.Command {
	var f dnsConfigFirewallFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a DNS Firewall cluster",
		Long: `Create a DNS Firewall cluster in front of your own nameservers. The cluster's
assigned addresses come back in the result; point your resolvers at those.

Examples:

  cf dns config firewall create edge-resolver --upstream-ip 192.0.2.1 --upstream-ip 2001:db8::1
  cf dns config firewall create edge-resolver --upstream-ip 192.0.2.1 --minimum-cache-ttl 60 --maximum-cache-ttl 900
  cf dns config firewall create edge-resolver --upstream-ip 192.0.2.1 --attack-mitigation --dns-firewall-ip-count 4`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.name = args[0]
			body, err := dnsConfigFirewallFields(cmd, f, true)
			if err != nil {
				return err
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dnsConfigAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: dnsConfigFirewallPath(accountID), Body: raw}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	dnsConfigFirewallSettingFlags(cmd, &f)
	cmd.Flags().IntVar(&f.ipCount, "dns-firewall-ip-count", 0, "number of IPv4 addresses to assign, set once at creation (1-10)")
	_ = cmd.MarkFlagRequired("upstream-ip")
	return cmd
}

func newDNSConfigFirewallUpdateCmd(g *globalOpts) *cobra.Command {
	var f dnsConfigFirewallFlags
	cmd := &cobra.Command{
		Use:   "update <cluster-id>",
		Short: "Update a DNS Firewall cluster",
		Long: `Update a DNS Firewall cluster. Only the flags you pass are sent; --upstream-ip
replaces the whole upstream list, so repeat it for every nameserver the cluster
should end up with. The address count is fixed when the cluster is created.

Examples:

  cf dns config firewall update 023e105f4ecef8ad9ca31a8372d0c353 --name edge-resolver
  cf dns config firewall update 023e105f4ecef8ad9ca31a8372d0c353 --upstream-ip 192.0.2.1 --upstream-ip 192.0.2.2
  cf dns config firewall update 023e105f4ecef8ad9ca31a8372d0c353 --no-ratelimit`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dnsConfigFirewallClusterID(args[0]); err != nil {
				return err
			}
			body, err := dnsConfigFirewallFields(cmd, f, false)
			if err != nil {
				return err
			}
			if len(body) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --upstream-ip, --minimum-cache-ttl, --maximum-cache-ttl, --negative-cache-ttl, --ratelimit, --no-ratelimit, --retries, --deprecate-any-requests, --ecs-fallback, or --attack-mitigation")
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dnsConfigAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: dnsConfigFirewallItemPath(accountID, args[0]), Body: raw}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "cluster name")
	dnsConfigFirewallSettingFlags(cmd, &f)
	return cmd
}

func newDNSConfigFirewallDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <cluster-id>",
		Short: "Delete a DNS Firewall cluster",
		Long: `Delete a DNS Firewall cluster. Its assigned addresses are released, so any
resolver or delegation still pointing at them stops getting answers.

Examples:

  cf dns config firewall delete 023e105f4ecef8ad9ca31a8372d0c353 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dnsConfigFirewallClusterID(args[0]); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dnsConfigAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete DNS Firewall cluster %s? Its DNS Firewall addresses are released and stop answering.", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: dnsConfigFirewallItemPath(accountID, args[0])}
			return runDNSConfigRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}
