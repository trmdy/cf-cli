package cli

// Tunnel porcelain: Cloudflare Tunnel (cloudflared) lifecycle, connector
// tokens, remotely-managed configuration, and private network routes.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

type tunnelSummary struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name,omitempty"`
	Status      string           `json:"status,omitempty"`
	CreatedAt   string           `json:"created_at,omitempty"`
	DeletedAt   string           `json:"deleted_at,omitempty"`
	Connections []map[string]any `json:"connections,omitempty"`
}

type tunnelRoute struct {
	ID               string `json:"id,omitempty"`
	Network          string `json:"network,omitempty"`
	TunnelID         string `json:"tunnel_id,omitempty"`
	TunnelName       string `json:"tunnel_name,omitempty"`
	VirtualNetworkID string `json:"virtual_network_id,omitempty"`
	Comment          string `json:"comment,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
}

// tunnelStatuses is the status filter vocabulary accepted by the API.
var tunnelStatuses = []string{"inactive", "degraded", "healthy", "down"}

func newTunnelCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tunnel",
		Short: "Manage Cloudflare Tunnels",
	}
	cmd.AddCommand(
		newTunnelListCmd(g),
		newTunnelGetCmd(g),
		newTunnelCreateCmd(g),
		newTunnelDeleteCmd(g),
		newTunnelTokenCmd(g),
		newTunnelConfigCmd(g),
		newTunnelRouteCmd(g),
	)
	return cmd
}

func tunnelPath(accountID string) string { return "/accounts/" + accountID + "/cfd_tunnel" }

func tunnelIDPath(accountID, tunnelID string) string {
	return tunnelPath(accountID) + "/" + url.PathEscape(tunnelID)
}

func tunnelRoutesPath(accountID string) string {
	return "/accounts/" + accountID + "/teamnet/routes"
}

// tunnelAccountID returns the resolved account ID or an actionable error.
// Tunnel porcelain is account-scoped, so every command needs one.
func tunnelAccountID(cfg config.Resolved) (string, error) {
	if cfg.AccountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return cfg.AccountID, nil
}

// isTunnelID reports whether s looks like a tunnel ID: a UUID, with or
// without dashes.
func isTunnelID(s string) bool {
	switch len(s) {
	case 32:
	case 36:
		if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
			return false
		}
	default:
		return false
	}
	for i, c := range s {
		if len(s) == 36 && (i == 8 || i == 13 || i == 18 || i == 23) {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// resolveTunnelID accepts a tunnel ID or a tunnel name (looked up via the
// API among the account's live tunnels).
func resolveTunnelID(ctx context.Context, client *api.Client, accountID, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("no tunnel specified: pass a tunnel name or ID")
	}
	if isTunnelID(ref) {
		return ref, nil
	}
	q := url.Values{}
	q.Set("name", ref)
	q.Set("is_deleted", "false")
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: tunnelPath(accountID), Query: q})
	if err != nil {
		return "", fmt.Errorf("look up tunnel %q: %w", ref, err)
	}
	var tunnels []tunnelSummary
	if err := json.Unmarshal(env.Result, &tunnels); err != nil {
		return "", fmt.Errorf("look up tunnel %q: unexpected response", ref)
	}
	matches := make([]tunnelSummary, 0, len(tunnels))
	for _, t := range tunnels {
		if t.Name == ref && t.ID != "" {
			matches = append(matches, t)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("tunnel %q not found in account %s", ref, accountID)
	case 1:
		return matches[0].ID, nil
	default:
		return "", fmt.Errorf("multiple tunnels named %q; pass the tunnel ID instead", ref)
	}
}

func newTunnelListCmd(g *globalOpts) *cobra.Command {
	var name, status string
	var includeDeleted bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Cloudflare Tunnels in an account",
		Long: `List Cloudflare Tunnels in an account. Deleted tunnels are hidden unless
--include-deleted is passed.

Examples:

  cf tunnel list
  cf tunnel list --status healthy
  cf tunnel list --name prod-tunnel --include-deleted`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := buildTunnelListQuery(name, status, includeDeleted)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: tunnelPath(accountID), Query: q}
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
			var tunnels []tunnelSummary
			if err := json.Unmarshal(env.Result, &tunnels); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(tunnels))
			for _, t := range tunnels {
				rows = append(rows, []string{
					t.ID,
					output.Cell(t.Name),
					t.Status,
					strconv.Itoa(len(t.Connections)),
					t.CreatedAt,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "STATUS", "CONNS", "CREATED"}, rows)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter by exact tunnel name")
	cmd.Flags().StringVar(&status, "status", "", "filter by status: inactive, degraded, healthy, or down")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include deleted tunnels")
	return cmd
}

// buildTunnelListQuery validates the list filters and builds the query.
func buildTunnelListQuery(name, status string, includeDeleted bool) (url.Values, error) {
	q := url.Values{}
	if name != "" {
		q.Set("name", name)
	}
	if status != "" {
		s := strings.ToLower(strings.TrimSpace(status))
		if !slices.Contains(tunnelStatuses, s) {
			return nil, fmt.Errorf("--status must be one of %s", strings.Join(tunnelStatuses, ", "))
		}
		q.Set("status", s)
	}
	if !includeDeleted {
		q.Set("is_deleted", "false")
	}
	q.Set("per_page", "100")
	return q, nil
}

func newTunnelGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <tunnel>",
		Short: "Show one Cloudflare Tunnel",
		Long: `Show one Cloudflare Tunnel. The tunnel may be given by name or ID.

Examples:

  cf tunnel get prod-tunnel
  cf tunnel get 6d6b1e0a-4c7d-4e2a-9f0c-1a2b3c4d5e6f`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID, err := resolveTunnelID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: tunnelIDPath(accountID, tunnelID)}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newTunnelCreateCmd(g *globalOpts) *cobra.Command {
	var secret, configSrc string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Cloudflare Tunnel",
		Long: `Create a Cloudflare Tunnel. By default the tunnel is remotely managed
(--config-src cloudflare), so its ingress rules live in Cloudflare and can be
edited with "cf tunnel config set".

Examples:

  cf tunnel create prod-tunnel
  cf tunnel create prod-tunnel --config-src local
  cf tunnel create prod-tunnel --secret "$(openssl rand -base64 32)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildTunnelCreateBody(args[0], secret, configSrc)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: tunnelPath(accountID), Body: body}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&secret, "secret", "", "base64-encoded tunnel secret, 32-64 bytes (default: generated by Cloudflare)")
	cmd.Flags().StringVar(&configSrc, "config-src", "cloudflare", "where ingress config lives: cloudflare or local")
	return cmd
}

// buildTunnelCreateBody validates the create flags and builds the request
// body.
func buildTunnelCreateBody(name, secret, configSrc string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("tunnel name must not be empty")
	}
	src := strings.ToLower(strings.TrimSpace(configSrc))
	if src == "" {
		src = "cloudflare"
	}
	if src != "cloudflare" && src != "local" {
		return nil, errors.New("--config-src must be cloudflare or local")
	}
	body := map[string]any{"name": name, "config_src": src}
	if secret != "" {
		decoded, err := base64.StdEncoding.DecodeString(secret)
		if err != nil {
			return nil, errors.New("--secret must be base64-encoded (for example: openssl rand -base64 32)")
		}
		if len(decoded) < 32 || len(decoded) > 64 {
			return nil, fmt.Errorf("--secret must decode to 32-64 bytes, got %d", len(decoded))
		}
		body["tunnel_secret"] = secret
	}
	return json.Marshal(body)
}

func newTunnelDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <tunnel>",
		Short: "Delete a Cloudflare Tunnel",
		Long: `Delete a Cloudflare Tunnel. The tunnel must have no active connections;
stop cloudflared (or run "cf api tunnel connections-delete") first.

Examples:

  cf tunnel delete prod-tunnel
  cf tunnel delete 6d6b1e0a-4c7d-4e2a-9f0c-1a2b3c4d5e6f --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID, err := resolveTunnelID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete tunnel %s from account %s?", tunnelID, accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: tunnelIDPath(accountID, tunnelID)}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newTunnelTokenCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token <tunnel>",
		Short: "Print the connector token for a tunnel",
		Long: `Print the connector token for a tunnel — the value cloudflared needs to
run it. The token is printed as a JSON string; pipe through "jq -r" for the
bare value.

Examples:

  cf tunnel token prod-tunnel
  cloudflared tunnel run --token "$(cf tunnel token prod-tunnel | jq -r)"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID, err := resolveTunnelID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: tunnelIDPath(accountID, tunnelID) + "/token"}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newTunnelConfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage the remotely-managed configuration of a tunnel",
	}
	cmd.AddCommand(
		newTunnelConfigGetCmd(g),
		newTunnelConfigSetCmd(g),
	)
	return cmd
}

func newTunnelConfigGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <tunnel>",
		Short: "Show the configuration of a tunnel",
		Long: `Show the remotely-managed configuration (ingress rules, warp routing,
origin request settings) of a tunnel.

Examples:

  cf tunnel config get prod-tunnel
  cf tunnel config get prod-tunnel --query '.config.ingress'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID, err := resolveTunnelID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: tunnelIDPath(accountID, tunnelID) + "/configurations"}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newTunnelConfigSetCmd(g *globalOpts) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "set <tunnel>",
		Short: "Replace the configuration of a tunnel",
		Long: `Replace the remotely-managed configuration of a tunnel from a JSON file.
The file may hold the bare configuration object or one wrapped in
{"config": ...}; either way the full configuration is replaced.

Examples:

  cf tunnel config set prod-tunnel --file tunnel-config.json
  cf tunnel config set prod-tunnel --file - < tunnel-config.json
  cf tunnel config get prod-tunnel | cf tunnel config set staging-tunnel --file -`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := readTunnelConfigFile(file, cmd.InOrStdin())
			if err != nil {
				return err
			}
			body, err := buildTunnelConfigBody(raw)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID, err := resolveTunnelID(cmd.Context(), client, accountID, args[0])
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: tunnelIDPath(accountID, tunnelID) + "/configurations", Body: body}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "JSON configuration file, or - to read stdin")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// readTunnelConfigFile reads the configuration from a path, or from stdin
// when the path is "-".
func readTunnelConfigFile(file string, stdin io.Reader) ([]byte, error) {
	if file == "" {
		return nil, errors.New("no configuration given: pass --file <path> or --file - to read stdin")
	}
	if file == "-" {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read configuration from stdin: %w", err)
		}
		return raw, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read configuration file: %w", err)
	}
	return raw, nil
}

// buildTunnelConfigBody normalizes a configuration document into the
// {"config": ...} body the API expects. A document that already carries a
// top-level "config" key (as returned by `cf tunnel config get`) is unwrapped
// first, so get | set round-trips.
func buildTunnelConfigBody(raw []byte) ([]byte, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errors.New("configuration is empty: expected a JSON object")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("configuration must be a JSON object: %w", err)
	}
	if probe == nil {
		return nil, errors.New("configuration must be a JSON object, not null")
	}
	inner, ok := probe["config"]
	if !ok {
		inner = json.RawMessage(raw)
	}
	var innerProbe map[string]json.RawMessage
	if err := json.Unmarshal(inner, &innerProbe); err != nil {
		return nil, fmt.Errorf("configuration must be a JSON object: %w", err)
	}
	if innerProbe == nil {
		return nil, errors.New("configuration must be a JSON object, not null")
	}
	return json.Marshal(map[string]json.RawMessage{"config": inner})
}

func newTunnelRouteCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "route",
		Short: "Manage private network routes through tunnels",
	}
	cmd.AddCommand(
		newTunnelRouteListCmd(g),
		newTunnelRouteAddCmd(g),
		newTunnelRouteRemoveCmd(g),
	)
	return cmd
}

func newTunnelRouteListCmd(g *globalOpts) *cobra.Command {
	var tunnelRef, virtualNetwork string
	var includeDeleted bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List private network routes",
		Long: `List the private network (CIDR) routes of an account. Deleted routes are
hidden unless --include-deleted is passed.

Examples:

  cf tunnel route list
  cf tunnel route list --tunnel prod-tunnel
  cf tunnel route list --virtual-network-id 5f2c1a80-1c9e-4f77-8b4d-2f8a37f3b0d1`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID := ""
			if tunnelRef != "" {
				tunnelID, err = resolveTunnelID(cmd.Context(), client, accountID, tunnelRef)
				if err != nil {
					return err
				}
			}
			req := api.Request{
				Method: "GET",
				Path:   tunnelRoutesPath(accountID),
				Query:  buildTunnelRouteListQuery(tunnelID, virtualNetwork, includeDeleted),
			}
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
			var routes []tunnelRoute
			if err := json.Unmarshal(env.Result, &routes); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(routes))
			for _, r := range routes {
				tunnel := r.TunnelName
				if tunnel == "" {
					tunnel = r.TunnelID
				}
				rows = append(rows, []string{
					r.ID,
					r.Network,
					output.Cell(tunnel),
					r.VirtualNetworkID,
					output.Cell(r.Comment),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NETWORK", "TUNNEL", "VNET", "COMMENT"}, rows)
		},
	}
	cmd.Flags().StringVar(&tunnelRef, "tunnel", "", "filter by tunnel name or ID")
	cmd.Flags().StringVar(&virtualNetwork, "virtual-network-id", "", "filter by virtual network ID")
	cmd.Flags().BoolVar(&includeDeleted, "include-deleted", false, "include deleted routes")
	return cmd
}

func buildTunnelRouteListQuery(tunnelID, virtualNetwork string, includeDeleted bool) url.Values {
	q := url.Values{}
	if tunnelID != "" {
		q.Set("tunnel_id", tunnelID)
	}
	if virtualNetwork != "" {
		q.Set("virtual_network_id", virtualNetwork)
	}
	if !includeDeleted {
		q.Set("is_deleted", "false")
	}
	q.Set("per_page", "100")
	return q
}

func newTunnelRouteAddCmd(g *globalOpts) *cobra.Command {
	var tunnelRef, comment, virtualNetwork string
	cmd := &cobra.Command{
		Use:   "add <network>",
		Short: "Route a private network through a tunnel",
		Long: `Route a private network (in CIDR notation) through a tunnel, so WARP
clients can reach it.

Examples:

  cf tunnel route add 10.0.0.0/8 --tunnel prod-tunnel
  cf tunnel route add 192.168.4.0/24 --tunnel prod-tunnel --comment "office lan"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			network, err := normalizeTunnelNetwork(args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			tunnelID, err := resolveTunnelID(cmd.Context(), client, accountID, tunnelRef)
			if err != nil {
				return err
			}
			body, err := buildTunnelRouteBody(network, tunnelID, comment, virtualNetwork)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: tunnelRoutesPath(accountID), Body: body}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&tunnelRef, "tunnel", "", "tunnel name or ID to route the network through")
	cmd.Flags().StringVar(&comment, "comment", "", "route comment")
	cmd.Flags().StringVar(&virtualNetwork, "virtual-network-id", "", "virtual network ID (default: the account's default virtual network)")
	_ = cmd.MarkFlagRequired("tunnel")
	return cmd
}

// normalizeTunnelNetwork validates that s is a CIDR network and that it is written in
// canonical (masked) form, which is what the API stores.
func normalizeTunnelNetwork(s string) (string, error) {
	network := strings.TrimSpace(s)
	_, ipNet, err := net.ParseCIDR(network)
	if err != nil {
		return "", fmt.Errorf("invalid network %q: expected CIDR notation, for example 10.0.0.0/8", s)
	}
	if ipNet.String() != network {
		return "", fmt.Errorf("network %q has host bits set: did you mean %s?", s, ipNet.String())
	}
	return network, nil
}

func buildTunnelRouteBody(network, tunnelID, comment, virtualNetwork string) ([]byte, error) {
	if tunnelID == "" {
		return nil, errors.New("no tunnel specified: pass --tunnel with a tunnel name or ID")
	}
	body := map[string]any{"network": network, "tunnel_id": tunnelID}
	if comment != "" {
		body["comment"] = comment
	}
	if virtualNetwork != "" {
		body["virtual_network_id"] = virtualNetwork
	}
	return json.Marshal(body)
}

func newTunnelRouteRemoveCmd(g *globalOpts) *cobra.Command {
	var virtualNetwork string
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <route-id|network>",
		Short: "Remove a private network route",
		Long: `Remove a private network route, by route ID or by the network itself (in
CIDR notation). When a network is given and it is routed on more than one
virtual network, disambiguate with --virtual-network-id.

Examples:

  cf tunnel route remove 10.0.0.0/8
  cf tunnel route remove 10.0.0.0/8 --virtual-network-id 5f2c1a80-1c9e-4f77-8b4d-2f8a37f3b0d1
  cf tunnel route remove 3f1d7b02-9a4c-4c6c-bb2a-70d2a0c1f0e5 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := tunnelAccountID(cfg)
			if err != nil {
				return err
			}
			routeID, err := resolveTunnelRouteID(cmd.Context(), client, accountID, args[0], virtualNetwork)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Remove route %s from account %s?", routeID, accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: tunnelRoutesPath(accountID) + "/" + url.PathEscape(routeID)}
			return runTunnelRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&virtualNetwork, "virtual-network-id", "", "virtual network ID, when a network is routed on more than one")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// resolveTunnelRouteID accepts a route ID or a CIDR network; a network is
// looked up among the account's live routes.
func resolveTunnelRouteID(ctx context.Context, client *api.Client, accountID, ref, virtualNetwork string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", errors.New("no route specified: pass a route ID or a network in CIDR notation")
	}
	if !strings.Contains(ref, "/") {
		return ref, nil
	}
	network, err := normalizeTunnelNetwork(ref)
	if err != nil {
		return "", err
	}
	q := buildTunnelRouteListQuery("", virtualNetwork, false)
	env, err := client.DoAutoPaginate(ctx, api.Request{Method: "GET", Path: tunnelRoutesPath(accountID), Query: q})
	if err != nil {
		return "", fmt.Errorf("look up route %q: %w", network, err)
	}
	var routes []tunnelRoute
	if err := json.Unmarshal(env.Result, &routes); err != nil {
		return "", fmt.Errorf("look up route %q: unexpected response", network)
	}
	matches := make([]tunnelRoute, 0, 1)
	for _, r := range routes {
		if r.Network == network && r.ID != "" {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no route for network %s in account %s", network, accountID)
	case 1:
		return matches[0].ID, nil
	default:
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, m.ID)
		}
		return "", fmt.Errorf("network %s is routed %d times (%s); pass a route ID or --virtual-network-id", network, len(matches), strings.Join(ids, ", "))
	}
}

func runTunnelRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
