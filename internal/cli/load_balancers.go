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
	"net/url"
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
	lbSteeringPolicies  = []string{"off", "geo", "random", "dynamic_latency", "proximity", "least_outstanding_requests", "least_connections"}
	lbSessionAffinities = []string{"none", "cookie", "ip_cookie", "header"}
	lbMonitorTypes      = []string{"http", "https", "tcp", "udp_icmp", "icmp_ping", "smtp"}
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
		Args:  cobra.ExactArgs(1),
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
		Args: cobra.ExactArgs(1),
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
				v, err := lbNormalizeEnum("session-affinity", affinity, lbSessionAffinities)
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
	cmd.Flags().StringVar(&affinity, "session-affinity", "", "session affinity: "+strings.Join(lbSessionAffinities, ", "))
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
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				patch["name"] = name
			}
			if cmd.Flags().Changed("default-pool") {
				if err := lbRequireNonEmpty("default-pool", defaultPools); err != nil {
					return err
				}
				patch["default_pools"] = defaultPools
			}
			if cmd.Flags().Changed("fallback-pool") {
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
				v, err := lbNormalizeEnum("session-affinity", affinity, lbSessionAffinities)
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
	cmd.Flags().StringVar(&affinity, "session-affinity", "", "session affinity: "+strings.Join(lbSessionAffinities, ", "))
	return cmd
}

func newLBDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <load-balancer-id>",
		Short: "Delete a load balancer",
		Long:  "Delete a load balancer. Pools and monitors are account-level and are left alone.\n\nExamples:\n\n  cf load-balancers delete 699d98642c564d2e855e9661899b7252 --zone example.com --force",
		Args:  cobra.ExactArgs(1),
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
		Args:  cobra.ExactArgs(1),
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
		Args: cobra.ExactArgs(1),
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
				if err := lbRequireNonEmpty("check-region", checkRegions); err != nil {
					return err
				}
				body["check_regions"] = checkRegions
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
	cmd.Flags().StringArrayVar(&checkRegions, "check-region", nil, "region to health check from, e.g. WEU (repeatable)")
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
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
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
				if err := lbRequireNonEmpty("check-region", checkRegions); err != nil {
					return err
				}
				patch["check_regions"] = checkRegions
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
	cmd.Flags().StringArrayVar(&checkRegions, "check-region", nil, "region to health check from, e.g. WEU (repeatable; replaces the list)")
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
		Args:  cobra.ExactArgs(1),
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

// lbPoolHealth is the shape of the pool health endpoint: per-PoP health, each
// with a list of single-entry maps keyed by origin address.
type lbPoolHealth struct {
	PoolID    string `json:"pool_id"`
	PopHealth map[string]struct {
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
		Short: "Show origin health for a pool, per PoP",
		Long: `Show the latest health check result for each origin, from each Cloudflare
PoP that probes the pool. This is the view to reach for when a pool is
unhealthy and you need to know which origin is failing and why.

Examples:

  cf load-balancers pool health 17b5962d775c646f3f9725cbc7a53df4
  cf load-balancers pool health 17b5962d775c646f3f9725cbc7a53df4 --output json`,
		Args: cobra.ExactArgs(1),
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
				[]string{"POP", "POP HEALTHY", "ORIGIN", "ORIGIN HEALTHY", "RTT", "CODE", "FAILURE REASON"},
				lbPoolHealthRows(health))
		},
	}
	return cmd
}

// lbPoolHealthRows flattens per-PoP health into stable, sorted table rows. The
// API returns maps, so sorting is what makes the output diffable.
func lbPoolHealthRows(health lbPoolHealth) [][]string {
	pops := make([]string, 0, len(health.PopHealth))
	for pop := range health.PopHealth {
		pops = append(pops, pop)
	}
	sort.Strings(pops)

	rows := [][]string{}
	for _, pop := range pops {
		ph := health.PopHealth[pop]
		type originRow struct {
			addr          string
			healthy       bool
			rtt           string
			failureReason string
			responseCode  int
		}
		origins := make([]originRow, 0, len(ph.Origins))
		for _, entry := range ph.Origins {
			for addr, o := range entry {
				origins = append(origins, originRow{addr, o.Healthy, o.RTT, o.FailureReason, o.ResponseCode})
			}
		}
		sort.Slice(origins, func(i, j int) bool { return origins[i].addr < origins[j].addr })
		if len(origins) == 0 {
			rows = append(rows, []string{pop, strconv.FormatBool(ph.Healthy), "", "", "", "", ""})
			continue
		}
		for _, o := range origins {
			code := ""
			if o.responseCode != 0 {
				code = strconv.Itoa(o.responseCode)
			}
			rows = append(rows, []string{
				pop,
				strconv.FormatBool(ph.Healthy),
				output.Cell(o.addr),
				strconv.FormatBool(o.healthy),
				o.rtt,
				code,
				output.Cell(o.failureReason),
			})
		}
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
		Args:  cobra.ExactArgs(1),
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

--expected-codes is only sent for http and https monitors, where the API
requires it; it defaults to 2xx.

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
			body := map[string]any{"type": t}
			if t == "http" || t == "https" || cmd.Flags().Changed("expected-codes") {
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

--header replaces the whole header set.

Examples:

  cf load-balancers monitor update f1aba936b94213e5b8dca0c0dbf1f9cc --interval 30
  cf load-balancers monitor update f1aba936b94213e5b8dca0c0dbf1f9cc \
    --path /healthz --expected-codes 200`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			patch := map[string]any{}
			if cmd.Flags().Changed("type") {
				t, err := lbNormalizeEnum("type", mtype, lbMonitorTypes)
				if err != nil {
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
		Args:  cobra.ExactArgs(1),
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

// lbApplyMonitorNumbers copies the numeric monitor flags that were explicitly
// set into body, rejecting values the API would.
func lbApplyMonitorNumbers(cmd *cobra.Command, body map[string]any, port, interval, timeout, retries int) error {
	for _, f := range []struct {
		flag  string
		field string
		value int
	}{
		{"port", "port", port},
		{"interval", "interval", interval},
		{"timeout", "timeout", timeout},
		{"retries", "retries", retries},
	} {
		if !cmd.Flags().Changed(f.flag) {
			continue
		}
		if f.value < 0 {
			return fmt.Errorf("--%s must be zero or greater", f.flag)
		}
		if f.flag == "port" && f.value > 65535 {
			return errors.New("--port must be between 1 and 65535")
		}
		body[f.field] = f.value
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
