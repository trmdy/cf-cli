package cli

// Load balancer porcelain: the three objects a real user assembles — monitors
// (how health is probed), pools (groups of origins), and load balancers (the
// zone hostname that steers traffic across pools) — plus the pool health view
// used when something is down. See docs/STYLE.md; internal/cli/dns.go is the
// shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// Enum values accepted by the API. Flag values are normalized (lower-cased,
// dashes folded to underscores) so `dynamic-latency` and `dynamic_latency`
// both work.
var (
	lbSteeringPolicies = []string{"off", "geo", "random", "dynamic_latency", "proximity", "least_outstanding_requests", "least_connections"}
	// `header` affinity additionally requires session_affinity_attributes.headers,
	// which this porcelain has no way to express, so it is rejected with a
	// pointer to the plumbing rather than sent as an incomplete request.
	lbSessionAffinities = []string{"none", "cookie", "ip_cookie"}
	lbMonitorTypes      = []string{"http", "https", "tcp", "udp_icmp", "icmp_ping", "smtp"}
	lbCheckRegions      = []string{"WNAM", "ENAM", "WEU", "EEU", "NSAM", "SSAM", "OC", "ME", "NAF", "SAF", "SAS", "SEAS", "NEAS", "ALL_REGIONS"}

	// Probe details Cloudflare only honors for http and https monitors.
	lbHTTPOnlyMonitorFlags = []string{"path", "expected-codes", "expected-body", "header", "follow-redirects", "allow-insecure"}
	// Monitor types with no default port, so one must be given explicitly.
	lbPortRequiredMonitorTypes = []string{"tcp", "udp_icmp", "smtp"}

	// Pool names are tags: alphanumerics, hyphens and underscores only.
	lbPoolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func newLoadBalancersCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "load-balancers",
		Aliases: []string{"lb"},
		Short:   "Manage load balancers, pools, and monitors",
		Long: `Manage load balancers, pools, and monitors.

Load balancers live in a zone and steer traffic across pools; pools and
monitors are account-level and shared between zones. A typical setup runs
bottom-up:

  cf load-balancers monitor create --type https --path /health
  cf load-balancers pool create eu-west --origin name=web1,address=203.0.113.1 --monitor <monitor-id>
  cf load-balancers create www.example.com --default-pool <pool-id> --fallback-pool <pool-id>
  cf load-balancers pool health <pool-id>`,
	}
	cmd.AddCommand(
		newLBListCmd(g),
		newLBGetCmd(g),
		newLBCreateCmd(g),
		newLBUpdateCmd(g),
		newLBDeleteCmd(g),
		newLBPoolCmd(g),
		newLBMonitorCmd(g),
	)
	return cmd
}

// lbZonePath is the zone-scoped load balancer collection.
func lbZonePath(zoneID string) string { return "/zones/" + zoneID + "/load_balancers" }

// lbPoolsPath and lbMonitorsPath are account-scoped: pools and monitors are
// shared across every zone on the account.
func lbPoolsPath(accountID string) string {
	return "/accounts/" + accountID + "/load_balancers/pools"
}

func lbMonitorsPath(accountID string) string {
	return "/accounts/" + accountID + "/load_balancers/monitors"
}

// lbIDArg accepts exactly one non-blank positional argument. A blank ID would
// otherwise build a collection path with a trailing slash — which for the
// delete commands means aiming a destructive call at the wrong resource.
func lbIDArg(label string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(1)(cmd, args); err != nil {
			return err
		}
		if strings.TrimSpace(args[0]) == "" {
			return fmt.Errorf("%s must not be empty", label)
		}
		return nil
	}
}

// lbPoolNameArg additionally enforces the pool name contract, so a name the
// API would reject fails before the request is built.
func lbPoolNameArg(cmd *cobra.Command, args []string) error {
	if err := lbIDArg("<name>")(cmd, args); err != nil {
		return err
	}
	return lbValidatePoolName(args[0])
}

func lbValidatePoolName(name string) error {
	if !lbPoolNamePattern.MatchString(name) {
		return fmt.Errorf("pool name %q is invalid: use only letters, digits, hyphens and underscores (for example: eu-west)", name)
	}
	return nil
}

// lbAccountID validates that an account is configured for the account-scoped
// pool and monitor commands.
func lbAccountID(configured string) (string, error) {
	if strings.TrimSpace(configured) == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return strings.TrimSpace(configured), nil
}

// --------------------------------------------------------------------------
// Load balancers (zone-scoped)
// --------------------------------------------------------------------------

type lbLoadBalancer struct {
	ID           string   `json:"id,omitempty"`
	Name         string   `json:"name,omitempty"`
	Enabled      *bool    `json:"enabled,omitempty"`
	Proxied      *bool    `json:"proxied,omitempty"`
	DefaultPools []string `json:"default_pools,omitempty"`
	FallbackPool string   `json:"fallback_pool,omitempty"`
	Steering     string   `json:"steering_policy,omitempty"`
}

func newLBListCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List load balancers in a zone",
		Long:  "List the load balancers in a zone.\n\nExamples:\n\n  cf load-balancers list --zone example.com\n  cf load-balancers list --zone example.com --output json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: lbZonePath(zoneID)}
			if g.DryRun {
				return lbDumpRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var lbs []lbLoadBalancer
			if err := json.Unmarshal(env.Result, &lbs); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(lbs))
			for _, lb := range lbs {
				rows = append(rows, []string{
					lb.ID,
					output.Cell(lb.Name),
					lbBoolCell(lb.Enabled),
					lbBoolCell(lb.Proxied),
					lb.Steering,
					output.Cell(strings.Join(lb.DefaultPools, ",")),
					lb.FallbackPool,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "NAME", "ENABLED", "PROXIED", "STEERING", "DEFAULT POOLS", "FALLBACK POOL"}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newLBGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get <load-balancer-id>",
		Short: "Show one load balancer",
		Long:  "Show the full configuration of one load balancer.\n\nExamples:\n\n  cf load-balancers get 699d98642c564d2e855e9661899b7252 --zone example.com",
		Args:  lbIDArg("<load-balancer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: lbZonePath(zoneID) + "/" + url.PathEscape(args[0])}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newLBCreateCmd(g *globalOpts) *cobra.Command {
	var zone, fallbackPool, description, steering, affinity string
	var defaultPools []string
	var ttl int
	var proxied, enabled bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a load balancer in a zone",
		Long: `Create a load balancer for a hostname in a zone.

The pools must already exist (see cf load-balancers pool create); the fallback
pool receives traffic when every default pool is unhealthy.

Examples:

  cf load-balancers create www.example.com --zone example.com \
    --default-pool 17b5962d775c646f3f9725cbc7a53df4 --fallback-pool 17b5962d775c646f3f9725cbc7a53df4
  cf load-balancers create www.example.com --zone example.com \
    --default-pool eu-pool-id --default-pool us-pool-id --fallback-pool us-pool-id \
    --steering-policy dynamic-latency --proxied`,
		Args: lbIDArg("<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := lbRequireNonEmpty("default-pool", defaultPools); err != nil {
				return err
			}
			if strings.TrimSpace(fallbackPool) == "" {
				return errors.New("--fallback-pool is required and must not be empty")
			}
			body := map[string]any{
				"name":          args[0],
				"default_pools": defaultPools,
				"fallback_pool": fallbackPool,
			}
			if description != "" {
				body["description"] = description
			}
			if cmd.Flags().Changed("ttl") {
				body["ttl"] = ttl
			}
			if cmd.Flags().Changed("proxied") {
				body["proxied"] = proxied
			}
			if cmd.Flags().Changed("enabled") {
				body["enabled"] = enabled
			}
			if cmd.Flags().Changed("steering-policy") {
				v, err := lbNormalizeEnum("steering-policy", steering, lbSteeringPolicies)
				if err != nil {
					return err
				}
				body["steering_policy"] = v
			}
			if cmd.Flags().Changed("session-affinity") {
				v, err := lbSessionAffinity(affinity, "create-zone")
				if err != nil {
					return err
				}
				body["session_affinity"] = v
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, zone)
			if err != nil {
				return err
			}
			return lbRunRequest(cmd, g, client, api.Request{Method: "POST", Path: lbZonePath(zoneID), Body: raw})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringArrayVar(&defaultPools, "default-pool", nil, "pool ID to serve traffic from, in failover order (repeatable)")
	cmd.Flags().StringVar(&fallbackPool, "fallback-pool", "", "pool ID used when every default pool is unhealthy")
	cmd.Flags().StringVar(&description, "description", "", "human-readable description")
	cmd.Flags().IntVar(&ttl, "ttl", 0, "DNS TTL in seconds (ignored when proxied)")
	cmd.Flags().BoolVar(&proxied, "proxied", false, "proxy the hostname through Cloudflare")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "serve traffic from this load balancer")
	cmd.Flags().StringVar(&steering, "steering-policy", "", "steering policy: "+strings.Join(lbSteeringPolicies, ", "))
	cmd.Flags().StringVar(&affinity, "session-affinity", "", "session affinity: "+strings.Join(lbSessionAffinities, ", ")+" (header affinity needs the plumbing command)")
	_ = cmd.MarkFlagRequired("default-pool")
	_ = cmd.MarkFlagRequired("fallback-pool")
	return cmd
}

func newLBUpdateCmd(g *globalOpts) *cobra.Command {
	var zone, name, fallbackPool, description, steering, affinity string
	var defaultPools []string
	var ttl int
	var proxied, enabled bool
	cmd := &cobra.Command{
		Use:   "update <load-balancer-id>",
		Short: "Update fields of a load balancer",
		Long: `Update a load balancer. Only the flags you pass are sent.

--default-pool replaces the whole default pool list.

Examples:

  cf load-balancers update 699d98642c564d2e855e9661899b7252 --zone example.com --enabled=false
  cf load-balancers update 699d98642c564d2e855e9661899b7252 --zone example.com \
    --default-pool eu-pool-id --default-pool us-pool-id --steering-policy geo`,
		Args: lbIDArg("<load-balancer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				if strings.TrimSpace(name) == "" {
					return errors.New("--name must not be empty")
				}
				patch["name"] = name
			}
			if cmd.Flags().Changed("default-pool") {
				if err := lbRequireNonEmpty("default-pool", defaultPools); err != nil {
					return err
				}
				patch["default_pools"] = defaultPools
			}
			if cmd.Flags().Changed("fallback-pool") {
				if strings.TrimSpace(fallbackPool) == "" {
					return errors.New("--fallback-pool must not be empty")
				}
				patch["fallback_pool"] = fallbackPool
			}
			if cmd.Flags().Changed("description") {
				patch["description"] = description
			}
			if cmd.Flags().Changed("ttl") {
				patch["ttl"] = ttl
			}
			if cmd.Flags().Changed("proxied") {
				patch["proxied"] = proxied
			}
			if cmd.Flags().Changed("enabled") {
				patch["enabled"] = enabled
			}
			if cmd.Flags().Changed("steering-policy") {
				v, err := lbNormalizeEnum("steering-policy", steering, lbSteeringPolicies)
				if err != nil {
					return err
				}
				patch["steering_policy"] = v
			}
			if cmd.Flags().Changed("session-affinity") {
				v, err := lbSessionAffinity(affinity, "update-zone")
				if err != nil {
					return err
				}
				patch["session_affinity"] = v
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --default-pool, --fallback-pool, --description, --ttl, --proxied, --enabled, --steering-policy, --session-affinity")
			}
			raw, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: lbZonePath(zoneID) + "/" + url.PathEscape(args[0]), Body: raw}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&name, "name", "", "hostname the load balancer serves")
	cmd.Flags().StringArrayVar(&defaultPools, "default-pool", nil, "pool ID to serve traffic from, in failover order (repeatable; replaces the list)")
	cmd.Flags().StringVar(&fallbackPool, "fallback-pool", "", "pool ID used when every default pool is unhealthy")
	cmd.Flags().StringVar(&description, "description", "", "human-readable description")
	cmd.Flags().IntVar(&ttl, "ttl", 0, "DNS TTL in seconds (ignored when proxied)")
	cmd.Flags().BoolVar(&proxied, "proxied", false, "proxy the hostname through Cloudflare")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "serve traffic from this load balancer")
	cmd.Flags().StringVar(&steering, "steering-policy", "", "steering policy: "+strings.Join(lbSteeringPolicies, ", "))
	cmd.Flags().StringVar(&affinity, "session-affinity", "", "session affinity: "+strings.Join(lbSessionAffinities, ", ")+" (header affinity needs the plumbing command)")
	return cmd
}

func newLBDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <load-balancer-id>",
		Short: "Delete a load balancer",
		Long:  "Delete a load balancer. Pools and monitors are account-level and are left alone.\n\nExamples:\n\n  cf load-balancers delete 699d98642c564d2e855e9661899b7252 --zone example.com --force",
		Args:  lbIDArg("<load-balancer-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete load balancer %s from zone %s?", args[0], zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: lbZonePath(zoneID) + "/" + url.PathEscape(args[0])}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --------------------------------------------------------------------------
// Pools (account-scoped)
// --------------------------------------------------------------------------

type lbPool struct {
	ID          string     `json:"id,omitempty"`
	Name        string     `json:"name,omitempty"`
	Enabled     *bool      `json:"enabled,omitempty"`
	Healthy     *bool      `json:"healthy,omitempty"`
	Monitor     string     `json:"monitor,omitempty"`
	Origins     []lbOrigin `json:"origins,omitempty"`
	Description string     `json:"description,omitempty"`
}

// lbOrigin is one backend server in a pool.
type lbOrigin struct {
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Enabled *bool    `json:"enabled,omitempty"`
	Weight  *float64 `json:"weight,omitempty"`
}

func newLBPoolCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pool",
		Short: "Manage origin pools (account-level)",
	}
	cmd.AddCommand(
		newLBPoolListCmd(g),
		newLBPoolGetCmd(g),
		newLBPoolCreateCmd(g),
		newLBPoolUpdateCmd(g),
		newLBPoolDeleteCmd(g),
		newLBPoolHealthCmd(g),
	)
	return cmd
}

func newLBPoolListCmd(g *globalOpts) *cobra.Command {
	var monitor string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List origin pools on the account",
		Long:  "List the origin pools on the account.\n\nExamples:\n\n  cf load-balancers pool list\n  cf load-balancers pool list --monitor f1aba936b94213e5b8dca0c0dbf1f9cc",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			q := url.Values{}
			if monitor != "" {
				q.Set("monitor", monitor)
			}
			req := api.Request{Method: "GET", Path: lbPoolsPath(accountID), Query: q}
			if g.DryRun {
				return lbDumpRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var pools []lbPool
			if err := json.Unmarshal(env.Result, &pools); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(pools))
			for _, p := range pools {
				rows = append(rows, []string{
					p.ID,
					output.Cell(p.Name),
					lbBoolCell(p.Enabled),
					lbBoolCell(p.Healthy),
					strconv.Itoa(len(p.Origins)),
					p.Monitor,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "NAME", "ENABLED", "HEALTHY", "ORIGINS", "MONITOR"}, rows)
		},
	}
	cmd.Flags().StringVar(&monitor, "monitor", "", "only pools attached to this monitor ID")
	return cmd
}

func newLBPoolGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <pool-id>",
		Short: "Show one origin pool",
		Long:  "Show the full configuration of one origin pool, including its origins.\n\nExamples:\n\n  cf load-balancers pool get 17b5962d775c646f3f9725cbc7a53df4",
		Args:  lbIDArg("<pool-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: lbPoolsPath(accountID) + "/" + url.PathEscape(args[0])}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newLBPoolCreateCmd(g *globalOpts) *cobra.Command {
	var description, monitor, notificationEmail string
	var originSpecs, checkRegions []string
	var minimumOrigins int
	var enabled bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an origin pool",
		Long: `Create an origin pool from one or more origins.

Each --origin is either a bare address (the name defaults to the address) or a
comma-separated list of name, address, weight and enabled:

  --origin 203.0.113.1
  --origin name=web1,address=203.0.113.1,weight=0.5,enabled=true

Examples:

  cf load-balancers pool create eu-west --origin 203.0.113.1 --origin 203.0.113.2
  cf load-balancers pool create eu-west \
    --origin name=web1,address=203.0.113.1,weight=0.7 \
    --origin name=web2,address=203.0.113.2,weight=0.3 \
    --monitor f1aba936b94213e5b8dca0c0dbf1f9cc --minimum-origins 1`,
		Args: lbPoolNameArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			origins, err := lbParseOrigins(originSpecs)
			if err != nil {
				return err
			}
			body := map[string]any{
				"name":    args[0],
				"origins": origins,
			}
			if description != "" {
				body["description"] = description
			}
			if monitor != "" {
				body["monitor"] = monitor
			}
			if notificationEmail != "" {
				body["notification_email"] = notificationEmail
			}
			if len(checkRegions) > 0 {
				regions, err := lbNormalizeCheckRegions(checkRegions)
				if err != nil {
					return err
				}
				body["check_regions"] = regions
			}
			if cmd.Flags().Changed("minimum-origins") {
				if minimumOrigins < 0 {
					return errors.New("--minimum-origins must be zero or greater")
				}
				body["minimum_origins"] = minimumOrigins
			}
			if cmd.Flags().Changed("enabled") {
				body["enabled"] = enabled
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			return lbRunRequest(cmd, g, client, api.Request{Method: "POST", Path: lbPoolsPath(accountID), Body: raw})
		},
	}
	lbAddOriginFlag(cmd, &originSpecs)
	cmd.Flags().StringVar(&description, "description", "", "human-readable description")
	cmd.Flags().StringVar(&monitor, "monitor", "", "monitor ID used to health check the origins")
	cmd.Flags().StringVar(&notificationEmail, "notification-email", "", "email address notified when the pool changes health")
	cmd.Flags().StringArrayVar(&checkRegions, "check-region", nil, "region to health check from: "+strings.Join(lbCheckRegions, ", ")+" (repeatable)")
	cmd.Flags().IntVar(&minimumOrigins, "minimum-origins", 0, "healthy origins required to keep the pool healthy (default 1)")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "serve traffic from this pool")
	_ = cmd.MarkFlagRequired("origin")
	return cmd
}

func newLBPoolUpdateCmd(g *globalOpts) *cobra.Command {
	var name, description, monitor, notificationEmail string
	var originSpecs, checkRegions []string
	var minimumOrigins int
	var enabled bool
	cmd := &cobra.Command{
		Use:   "update <pool-id>",
		Short: "Update fields of an origin pool",
		Long: `Update an origin pool. Only the flags you pass are sent.

--origin replaces the whole origin list, so pass every origin the pool should
keep. Use the same syntax as pool create.

Examples:

  cf load-balancers pool update 17b5962d775c646f3f9725cbc7a53df4 --enabled=false
  cf load-balancers pool update 17b5962d775c646f3f9725cbc7a53df4 \
    --origin name=web1,address=203.0.113.1 --origin name=web3,address=203.0.113.3`,
		Args: lbIDArg("<pool-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				if err := lbValidatePoolName(name); err != nil {
					return err
				}
				patch["name"] = name
			}
			if cmd.Flags().Changed("origin") {
				origins, err := lbParseOrigins(originSpecs)
				if err != nil {
					return err
				}
				patch["origins"] = origins
			}
			if cmd.Flags().Changed("description") {
				patch["description"] = description
			}
			if cmd.Flags().Changed("monitor") {
				patch["monitor"] = monitor
			}
			if cmd.Flags().Changed("notification-email") {
				patch["notification_email"] = notificationEmail
			}
			if cmd.Flags().Changed("check-region") {
				regions, err := lbNormalizeCheckRegions(checkRegions)
				if err != nil {
					return err
				}
				patch["check_regions"] = regions
			}
			if cmd.Flags().Changed("minimum-origins") {
				if minimumOrigins < 0 {
					return errors.New("--minimum-origins must be zero or greater")
				}
				patch["minimum_origins"] = minimumOrigins
			}
			if cmd.Flags().Changed("enabled") {
				patch["enabled"] = enabled
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --origin, --description, --monitor, --notification-email, --check-region, --minimum-origins, --enabled")
			}
			raw, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: lbPoolsPath(accountID) + "/" + url.PathEscape(args[0]), Body: raw}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	lbAddOriginFlag(cmd, &originSpecs)
	cmd.Flags().StringVar(&name, "name", "", "pool name")
	cmd.Flags().StringVar(&description, "description", "", "human-readable description")
	cmd.Flags().StringVar(&monitor, "monitor", "", "monitor ID used to health check the origins")
	cmd.Flags().StringVar(&notificationEmail, "notification-email", "", "email address notified when the pool changes health")
	cmd.Flags().StringArrayVar(&checkRegions, "check-region", nil, "region to health check from: "+strings.Join(lbCheckRegions, ", ")+" (repeatable; replaces the list)")
	cmd.Flags().IntVar(&minimumOrigins, "minimum-origins", 0, "healthy origins required to keep the pool healthy")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "serve traffic from this pool")
	return cmd
}

func newLBPoolDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <pool-id>",
		Short: "Delete an origin pool",
		Long:  "Delete an origin pool. Load balancers still referencing it will fail to serve.\n\nExamples:\n\n  cf load-balancers pool delete 17b5962d775c646f3f9725cbc7a53df4 --force",
		Args:  lbIDArg("<pool-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete origin pool %s from account %s?", args[0], accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: lbPoolsPath(accountID) + "/" + url.PathEscape(args[0])}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// lbPoolHealth is the shape of the pool health endpoint: one pop_health object
// holding the pool's overall health plus a list of single-entry maps keyed by
// origin address.
type lbPoolHealth struct {
	PoolID    string `json:"pool_id"`
	PopHealth struct {
		Healthy bool `json:"healthy"`
		Origins []map[string]struct {
			Healthy       bool   `json:"healthy"`
			RTT           string `json:"rtt"`
			FailureReason string `json:"failure_reason"`
			ResponseCode  int    `json:"response_code"`
		} `json:"origins"`
	} `json:"pop_health"`
}

func newLBPoolHealthCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "health <pool-id>",
		Short: "Show origin health for a pool",
		Long: `Show the latest health check result for each origin in a pool, alongside
the pool's overall health. This is the view to reach for when a pool is
unhealthy and you need to know which origin is failing and why.

Examples:

  cf load-balancers pool health 17b5962d775c646f3f9725cbc7a53df4
  cf load-balancers pool health 17b5962d775c646f3f9725cbc7a53df4 --output json`,
		Args: lbIDArg("<pool-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: lbPoolsPath(accountID) + "/" + url.PathEscape(args[0]) + "/health"}
			if g.DryRun {
				return lbDumpRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var health lbPoolHealth
			if err := json.Unmarshal(env.Result, &health); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"POOL HEALTHY", "ORIGIN", "ORIGIN HEALTHY", "RTT", "CODE", "FAILURE REASON"},
				lbPoolHealthRows(health))
		},
	}
	return cmd
}

// lbPoolHealthRows flattens the origin health entries into table rows. The
// pool's overall health is result data, so it is carried on every row rather
// than pushed to stderr. Each entry is a single-key map, so sorting by origin
// address is what makes the output stable between runs.
func lbPoolHealthRows(health lbPoolHealth) [][]string {
	poolHealthy := strconv.FormatBool(health.PopHealth.Healthy)
	type originRow struct {
		addr          string
		healthy       bool
		rtt           string
		failureReason string
		responseCode  int
	}
	origins := make([]originRow, 0, len(health.PopHealth.Origins))
	for _, entry := range health.PopHealth.Origins {
		addrs := make([]string, 0, len(entry))
		for addr := range entry {
			addrs = append(addrs, addr)
		}
		sort.Strings(addrs)
		for _, addr := range addrs {
			o := entry[addr]
			origins = append(origins, originRow{addr, o.Healthy, o.RTT, o.FailureReason, o.ResponseCode})
		}
	}
	sort.SliceStable(origins, func(i, j int) bool { return origins[i].addr < origins[j].addr })

	// A pool with no origin results still has a health verdict worth showing.
	if len(origins) == 0 {
		return [][]string{{poolHealthy, "", "", "", "", ""}}
	}
	rows := make([][]string, 0, len(origins))
	for _, o := range origins {
		code := ""
		if o.responseCode != 0 {
			code = strconv.Itoa(o.responseCode)
		}
		rows = append(rows, []string{
			poolHealthy,
			output.Cell(o.addr),
			strconv.FormatBool(o.healthy),
			o.rtt,
			code,
			output.Cell(o.failureReason),
		})
	}
	return rows
}

// --------------------------------------------------------------------------
// Monitors (account-scoped)
// --------------------------------------------------------------------------

type lbMonitor struct {
	ID          string `json:"id,omitempty"`
	Type        string `json:"type,omitempty"`
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Port        int    `json:"port,omitempty"`
	Interval    int    `json:"interval,omitempty"`
	Timeout     int    `json:"timeout,omitempty"`
	Retries     int    `json:"retries,omitempty"`
	Description string `json:"description,omitempty"`
}

func newLBMonitorCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "monitor",
		Short: "Manage health check monitors (account-level)",
	}
	cmd.AddCommand(
		newLBMonitorListCmd(g),
		newLBMonitorGetCmd(g),
		newLBMonitorCreateCmd(g),
		newLBMonitorUpdateCmd(g),
		newLBMonitorDeleteCmd(g),
	)
	return cmd
}

func newLBMonitorListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List health check monitors on the account",
		Long:  "List the health check monitors on the account.\n\nExamples:\n\n  cf load-balancers monitor list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: lbMonitorsPath(accountID)}
			if g.DryRun {
				return lbDumpRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var monitors []lbMonitor
			if err := json.Unmarshal(env.Result, &monitors); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(monitors))
			for _, m := range monitors {
				rows = append(rows, []string{
					m.ID,
					m.Type,
					m.Method,
					output.Cell(m.Path),
					lbIntCell(m.Port),
					lbIntCell(m.Interval),
					lbIntCell(m.Timeout),
					output.Cell(m.Description),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "TYPE", "METHOD", "PATH", "PORT", "INTERVAL", "TIMEOUT", "DESCRIPTION"}, rows)
		},
	}
	return cmd
}

func newLBMonitorGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <monitor-id>",
		Short: "Show one health check monitor",
		Long:  "Show the full configuration of one health check monitor.\n\nExamples:\n\n  cf load-balancers monitor get f1aba936b94213e5b8dca0c0dbf1f9cc",
		Args:  lbIDArg("<monitor-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: lbMonitorsPath(accountID) + "/" + url.PathEscape(args[0])}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newLBMonitorCreateCmd(g *globalOpts) *cobra.Command {
	var mtype, method, path, expectedCodes, expectedBody, description string
	var headers []string
	var port, interval, timeout, retries int
	var followRedirects, allowInsecure bool
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a health check monitor",
		Long: `Create a health check monitor for pools to use.

The HTTP probe details (--path, --expected-codes, --expected-body, --header,
--follow-redirects, --allow-insecure) only apply to http and https monitors.
--expected-codes defaults to 2xx and is sent only for those types. tcp,
udp-icmp and smtp monitors have no default port, so they need an explicit
--port.

Examples:

  cf load-balancers monitor create --type https --path /health
  cf load-balancers monitor create --type https --path /health \
    --expected-codes 200 --header "Host: www.example.com" --interval 30 --retries 2
  cf load-balancers monitor create --type tcp --port 5432 --description "postgres"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := lbNormalizeEnum("type", mtype, lbMonitorTypes)
			if err != nil {
				return err
			}
			if err := lbCheckMonitorFlagCompat(cmd, t); err != nil {
				return err
			}
			if lbMonitorTypeNeedsPort(t) && !cmd.Flags().Changed("port") {
				return fmt.Errorf("--port is required for --type %s: it has no default port (for example: --port 5432)", t)
			}
			body := map[string]any{"type": t}
			if lbMonitorTypeIsHTTP(t) {
				if strings.TrimSpace(expectedCodes) == "" {
					return errors.New("--expected-codes must not be empty (for example: 2xx or 200)")
				}
				body["expected_codes"] = expectedCodes
			}
			if method != "" {
				body["method"] = strings.ToUpper(method)
			}
			if path != "" {
				body["path"] = path
			}
			if expectedBody != "" {
				body["expected_body"] = expectedBody
			}
			if description != "" {
				body["description"] = description
			}
			if len(headers) > 0 {
				h, err := lbParseHeaders(headers)
				if err != nil {
					return err
				}
				body["header"] = h
			}
			if err := lbApplyMonitorNumbers(cmd, body, port, interval, timeout, retries); err != nil {
				return err
			}
			if cmd.Flags().Changed("follow-redirects") {
				body["follow_redirects"] = followRedirects
			}
			if cmd.Flags().Changed("allow-insecure") {
				body["allow_insecure"] = allowInsecure
			}
			raw, err := json.Marshal(body)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			return lbRunRequest(cmd, g, client, api.Request{Method: "POST", Path: lbMonitorsPath(accountID), Body: raw})
		},
	}
	lbAddMonitorFlags(cmd, &mtype, &method, &path, &expectedCodes, &expectedBody, &description,
		&headers, &port, &interval, &timeout, &retries, &followRedirects, &allowInsecure)
	_ = cmd.MarkFlagRequired("type")
	return cmd
}

func newLBMonitorUpdateCmd(g *globalOpts) *cobra.Command {
	var mtype, method, path, expectedCodes, expectedBody, description string
	var headers []string
	var port, interval, timeout, retries int
	var followRedirects, allowInsecure bool
	cmd := &cobra.Command{
		Use:   "update <monitor-id>",
		Short: "Update fields of a health check monitor",
		Long: `Update a health check monitor. Only the flags you pass are sent.

--header replaces the whole header set. The HTTP probe details (--path,
--expected-codes, --expected-body, --header, --follow-redirects,
--allow-insecure) are rejected when --type names a non-HTTP monitor; without
--type the monitor's current type is unknown here, so the API decides.

Examples:

  cf load-balancers monitor update f1aba936b94213e5b8dca0c0dbf1f9cc --interval 30
  cf load-balancers monitor update f1aba936b94213e5b8dca0c0dbf1f9cc \
    --path /healthz --expected-codes 200`,
		Args: lbIDArg("<monitor-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("type") {
				t, err := lbNormalizeEnum("type", mtype, lbMonitorTypes)
				if err != nil {
					return err
				}
				if err := lbCheckMonitorFlagCompat(cmd, t); err != nil {
					return err
				}
				patch["type"] = t
			}
			if cmd.Flags().Changed("expected-codes") {
				if strings.TrimSpace(expectedCodes) == "" {
					return errors.New("--expected-codes must not be empty (for example: 2xx or 200)")
				}
				patch["expected_codes"] = expectedCodes
			}
			if cmd.Flags().Changed("method") {
				patch["method"] = strings.ToUpper(method)
			}
			if cmd.Flags().Changed("path") {
				patch["path"] = path
			}
			if cmd.Flags().Changed("expected-body") {
				patch["expected_body"] = expectedBody
			}
			if cmd.Flags().Changed("description") {
				patch["description"] = description
			}
			if cmd.Flags().Changed("header") {
				h, err := lbParseHeaders(headers)
				if err != nil {
					return err
				}
				patch["header"] = h
			}
			if err := lbApplyMonitorNumbers(cmd, patch, port, interval, timeout, retries); err != nil {
				return err
			}
			if cmd.Flags().Changed("follow-redirects") {
				patch["follow_redirects"] = followRedirects
			}
			if cmd.Flags().Changed("allow-insecure") {
				patch["allow_insecure"] = allowInsecure
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --type, --method, --path, --expected-codes, --expected-body, --description, --header, --port, --interval, --timeout, --retries, --follow-redirects, --allow-insecure")
			}
			raw, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: lbMonitorsPath(accountID) + "/" + url.PathEscape(args[0]), Body: raw}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	lbAddMonitorFlags(cmd, &mtype, &method, &path, &expectedCodes, &expectedBody, &description,
		&headers, &port, &interval, &timeout, &retries, &followRedirects, &allowInsecure)
	return cmd
}

func newLBMonitorDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <monitor-id>",
		Short: "Delete a health check monitor",
		Long:  "Delete a health check monitor. Pools still referencing it stop being health checked.\n\nExamples:\n\n  cf load-balancers monitor delete f1aba936b94213e5b8dca0c0dbf1f9cc --force",
		Args:  lbIDArg("<monitor-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := lbAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete monitor %s from account %s?", args[0], accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: lbMonitorsPath(accountID) + "/" + url.PathEscape(args[0])}
			return lbRunRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// lbAddMonitorFlags registers the flag set shared by monitor create and
// update, so the two commands can never drift apart.
func lbAddMonitorFlags(cmd *cobra.Command, mtype, method, path, expectedCodes, expectedBody, description *string,
	headers *[]string, port, interval, timeout, retries *int, followRedirects, allowInsecure *bool) {
	cmd.Flags().StringVar(mtype, "type", "", "monitor type: "+strings.Join(lbMonitorTypes, ", "))
	cmd.Flags().StringVar(method, "method", "", "HTTP method to probe with (GET, HEAD, ...)")
	cmd.Flags().StringVar(path, "path", "", "endpoint path to probe, e.g. /health")
	cmd.Flags().StringVar(expectedCodes, "expected-codes", "2xx", "response codes considered healthy, e.g. 2xx or 200")
	cmd.Flags().StringVar(expectedBody, "expected-body", "", "substring the response body must contain")
	cmd.Flags().StringVar(description, "description", "", "human-readable description")
	cmd.Flags().StringArrayVar(headers, "header", nil, `request header as "Name: value" (repeatable; replaces the set)`)
	cmd.Flags().IntVar(port, "port", 0, "port to probe (defaults to the protocol port)")
	cmd.Flags().IntVar(interval, "interval", 0, "seconds between probes")
	cmd.Flags().IntVar(timeout, "timeout", 0, "seconds to wait for a response")
	cmd.Flags().IntVar(retries, "retries", 0, "retries before marking an origin unhealthy")
	cmd.Flags().BoolVar(followRedirects, "follow-redirects", false, "follow 3xx redirects when probing")
	cmd.Flags().BoolVar(allowInsecure, "allow-insecure", false, "skip TLS certificate verification when probing")
}

func lbMonitorTypeIsHTTP(t string) bool { return t == "http" || t == "https" }

// lbMonitorTypeNeedsPort reports whether a type has no default port, so one
// must be supplied at create time.
func lbMonitorTypeNeedsPort(t string) bool {
	for _, p := range lbPortRequiredMonitorTypes {
		if p == t {
			return true
		}
	}
	return false
}

// lbCheckMonitorFlagCompat rejects HTTP-only probe details on a non-HTTP
// monitor, naming every offending flag so one run reports them all.
func lbCheckMonitorFlagCompat(cmd *cobra.Command, t string) error {
	if lbMonitorTypeIsHTTP(t) {
		return nil
	}
	var offending []string
	for _, f := range lbHTTPOnlyMonitorFlags {
		if cmd.Flags().Changed(f) {
			offending = append(offending, "--"+f)
		}
	}
	if len(offending) == 0 {
		return nil
	}
	return fmt.Errorf("%s only valid for http and https monitors, not --type %s", strings.Join(offending, ", "), t)
}

// lbApplyMonitorNumbers copies the numeric monitor flags that were explicitly
// set into body, rejecting values the API would.
func lbApplyMonitorNumbers(cmd *cobra.Command, body map[string]any, port, interval, timeout, retries int) error {
	if cmd.Flags().Changed("port") {
		if port < 1 || port > 65535 {
			return errors.New("--port must be between 1 and 65535")
		}
		body["port"] = port
	}
	for _, f := range []struct {
		flag  string
		value int
	}{
		{"interval", interval},
		{"timeout", timeout},
		{"retries", retries},
	} {
		if !cmd.Flags().Changed(f.flag) {
			continue
		}
		if f.value < 0 {
			return fmt.Errorf("--%s must be zero or greater", f.flag)
		}
		body[f.flag] = f.value
	}
	return nil
}

// --------------------------------------------------------------------------
// Shared helpers
// --------------------------------------------------------------------------

func lbAddOriginFlag(cmd *cobra.Command, specs *[]string) {
	cmd.Flags().StringArrayVar(specs, "origin", nil,
		"origin as an address or name=...,address=...,weight=...,enabled=... (repeatable)")
}

// lbParseOrigins parses every --origin value, rejecting the whole set if any
// entry is malformed.
func lbParseOrigins(specs []string) ([]lbOrigin, error) {
	if len(specs) == 0 {
		return nil, errors.New("at least one --origin is required (for example: --origin 203.0.113.1)")
	}
	origins := make([]lbOrigin, 0, len(specs))
	for _, spec := range specs {
		o, err := lbParseOrigin(spec)
		if err != nil {
			return nil, err
		}
		origins = append(origins, o)
	}
	return origins, nil
}

// lbParseOrigin accepts either a bare address ("203.0.113.1") or a
// comma-separated key=value spec ("name=web1,address=203.0.113.1,weight=0.5").
func lbParseOrigin(spec string) (lbOrigin, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return lbOrigin{}, errors.New("--origin value is empty (for example: --origin 203.0.113.1)")
	}
	if !strings.Contains(s, "=") {
		return lbOrigin{Name: s, Address: s}, nil
	}
	var o lbOrigin
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return lbOrigin{}, fmt.Errorf("--origin %q: %q is not key=value (valid keys: name, address, weight, enabled)", spec, part)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "name":
			o.Name = value
		case "address":
			o.Address = value
		case "weight":
			w, err := strconv.ParseFloat(value, 64)
			if err != nil || w < 0 || w > 1 {
				return lbOrigin{}, fmt.Errorf("--origin %q: weight %q must be a number between 0 and 1", spec, value)
			}
			// The API documents weight as a multiple of 0.01, so finer
			// precision would be silently rounded server-side.
			if hundredths := w * 100; math.Abs(hundredths-math.Round(hundredths)) > 1e-9 {
				return lbOrigin{}, fmt.Errorf("--origin %q: weight %q must be a multiple of 0.01 (for example: 0.55)", spec, value)
			}
			o.Weight = &w
		case "enabled":
			b, err := strconv.ParseBool(value)
			if err != nil {
				return lbOrigin{}, fmt.Errorf("--origin %q: enabled %q must be true or false", spec, value)
			}
			o.Enabled = &b
		default:
			return lbOrigin{}, fmt.Errorf("--origin %q: unknown key %q (valid keys: name, address, weight, enabled)", spec, key)
		}
	}
	if o.Address == "" {
		return lbOrigin{}, fmt.Errorf("--origin %q: address is required (for example: --origin name=web1,address=203.0.113.1)", spec)
	}
	if o.Name == "" {
		o.Name = o.Address
	}
	return o, nil
}

// lbParseHeaders turns "Name: value" (or "Name=value") flags into the API's
// map of header name to values, keeping repeats of the same name.
func lbParseHeaders(values []string) (map[string][]string, error) {
	headers := map[string][]string{}
	for _, v := range values {
		s := strings.TrimSpace(v)
		name, value, ok := strings.Cut(s, ":")
		if !ok {
			name, value, ok = strings.Cut(s, "=")
		}
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("--header %q must be in \"Name: value\" form (for example: --header \"Host: www.example.com\")", v)
		}
		headers[name] = append(headers[name], strings.TrimSpace(value))
	}
	return headers, nil
}

// lbNormalizeEnum lower-cases a flag value and folds dashes to underscores so
// `dynamic-latency` and `dynamic_latency` both reach the API correctly.
func lbNormalizeEnum(flag, value string, allowed []string) (string, error) {
	v := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
	for _, a := range allowed {
		if a == v {
			return v, nil
		}
	}
	return "", fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

// lbSessionAffinity rejects `header` explicitly: the API needs
// session_affinity_attributes.headers alongside it, which this porcelain has
// no flag for, so silently sending an incomplete request would be worse than
// pointing at the plumbing. plumbingOp names the generated command that can
// send the full body from where the caller stands.
func lbSessionAffinity(value, plumbingOp string) (string, error) {
	if strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_") == "header" {
		return "", fmt.Errorf("--session-affinity header also needs session_affinity_attributes.headers, which this command cannot set: use `cf api load-balancers %s` for header affinity", plumbingOp)
	}
	return lbNormalizeEnum("session-affinity", value, lbSessionAffinities)
}

// lbNormalizeCheckRegions upper-cases and validates the documented health
// check regions, so a typo fails here instead of at the API.
func lbNormalizeCheckRegions(values []string) ([]string, error) {
	if err := lbRequireNonEmpty("check-region", values); err != nil {
		return nil, err
	}
	regions := make([]string, 0, len(values))
	for _, v := range values {
		r := strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(v)), "-", "_")
		ok := false
		for _, allowed := range lbCheckRegions {
			if allowed == r {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("--check-region %q is not a known region: use one of %s", v, strings.Join(lbCheckRegions, ", "))
		}
		regions = append(regions, r)
	}
	return regions, nil
}

func lbRequireNonEmpty(flag string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("--%s is required", flag)
	}
	for i, v := range values {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("--%s value at position %d is empty", flag, i+1)
		}
	}
	return nil
}

func lbBoolCell(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

func lbIntCell(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

func lbDumpRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	dump, err := client.Dump(req)
	if err != nil {
		return err
	}
	return g.renderValue(cmd, dump, output.JSON)
}

func lbRunRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	if g.DryRun {
		return lbDumpRequest(cmd, g, client, req)
	}
	env, err := client.Do(cmd.Context(), req)
	if err != nil {
		return err
	}
	return g.renderResult(cmd, env.Result, output.JSON)
}
