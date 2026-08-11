package cli

// Spectrum porcelain: zone-scoped application CRUD for Spectrum (TCP/UDP proxy).
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
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

// Enumerations mirror the Spectrum OpenAPI schemas (spectrum-config_*).
var (
	spectrumDNSTypes         = []string{"CNAME", "ADDRESS"}
	spectrumTrafficTypes     = []string{"direct", "http", "https"}
	spectrumProxyProtocols   = []string{"off", "v1", "v2", "simple"}
	spectrumTLSModes         = []string{"off", "flexible", "full", "strict"}
	spectrumEdgeIPSTypes     = []string{"dynamic", "static"}
	spectrumEdgeConnectivity = []string{"all", "ipv4", "ipv6"}
	spectrumListOrders       = []string{"protocol", "app_id", "created_on", "modified_on", "dns"}
	spectrumListDirections   = []string{"asc", "desc"}
)

// Documented bounds from the Spectrum app schemas.
const (
	spectrumOriginPortMin   = 1
	spectrumOriginPortMax   = 65535
	spectrumOriginDNSTTLMin = 600
	spectrumListPerPage     = 100 // API maximum; used for transparent pagination
	spectrumListMaxPages    = 1000
)

// spectrumApp is the subset of an application used for table list rows.
type spectrumApp struct {
	ID           string             `json:"id,omitempty"`
	Protocol     string             `json:"protocol,omitempty"`
	DNS          spectrumDNS        `json:"dns"`
	TrafficType  string             `json:"traffic_type,omitempty"`
	OriginDirect []string           `json:"origin_direct,omitempty"`
	OriginDNS    *spectrumOriginDNS `json:"origin_dns,omitempty"`
}

type spectrumDNS struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

type spectrumOriginDNS struct {
	Name string `json:"name,omitempty"`
	TTL  *int   `json:"ttl,omitempty"`
	Type string `json:"type,omitempty"`
}

// spectrumAppFlags holds create/update flag state.
type spectrumAppFlags struct {
	protocol, dnsName, dnsType, trafficType string
	originDirect                            []string
	originDNSName, originDNSType            string
	originDNSTTL                            int
	originPort                              string
	proxyProtocol, tls                      string
	edgeIPSType, edgeConnectivity           string
	edgeIPs                                 []string
	edgeIPsJSON                             string
	virtualNetworkID                        string
	argoSmartRouting, ipFirewall            bool
}

func newSpectrumCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spectrum",
		Short: "Manage Spectrum applications",
	}
	cmd.AddCommand(
		newSpectrumListCmd(g),
		newSpectrumGetCmd(g),
		newSpectrumCreateCmd(g),
		newSpectrumUpdateCmd(g),
		newSpectrumDeleteCmd(g),
	)
	return cmd
}

func spectrumAppsPath(zoneID string) string {
	return "/zones/" + zoneID + "/spectrum/apps"
}

func spectrumAppPath(zoneID, appID string) string {
	return spectrumAppsPath(zoneID) + "/" + url.PathEscape(appID)
}

// spectrumListQuery builds list query params. Spectrum requires page when any
// pagination parameter is set, so we always send page + per_page together.
func spectrumListQuery(page int, order, direction string) url.Values {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(spectrumListPerPage))
	if order != "" {
		q.Set("order", order)
	}
	if direction != "" {
		q.Set("direction", direction)
	}
	return q
}

// listAllSpectrumApps pages GET /zones/{id}/spectrum/apps starting at page=1.
// DoAutoPaginate is not used: seeding page would pin it as a single call, and
// omitting page leaves result_info/pagination undefined for this API.
func listAllSpectrumApps(ctx context.Context, client *api.Client, zoneID, order, direction string) ([]spectrumApp, []byte, error) {
	var all []json.RawMessage
	var apps []spectrumApp
	for page := 1; page <= spectrumListMaxPages; page++ {
		req := api.Request{
			Method: "GET",
			Path:   spectrumAppsPath(zoneID),
			Query:  spectrumListQuery(page, order, direction),
		}
		env, err := client.Do(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(env.Result, &items); err != nil {
			if page == 1 {
				return nil, env.Result, nil
			}
			return nil, nil, fmt.Errorf("list Spectrum applications page %d: unexpected response", page)
		}
		all = append(all, items...)
		for _, raw := range items {
			var a spectrumApp
			if err := json.Unmarshal(raw, &a); err != nil {
				continue
			}
			apps = append(apps, a)
		}
		if len(items) == 0 {
			break
		}
		if env.ResultInfo != nil && env.ResultInfo.TotalPages > 0 && page >= env.ResultInfo.TotalPages {
			break
		}
		if len(items) < spectrumListPerPage && (env.ResultInfo == nil || env.ResultInfo.TotalPages == 0) {
			break
		}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return nil, nil, err
	}
	return apps, raw, nil
}

func newSpectrumListCmd(g *globalOpts) *cobra.Command {
	var zone, order, direction string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Spectrum applications in a zone",
		Long: `List Spectrum applications in a zone.

Pagination always sends page + per_page together (the Spectrum API requires
page when using other pagination parameters).

Examples:

  cf spectrum list --zone example.com
  cf spectrum list --zone example.com --order created_on --direction desc`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			orderVal := ""
			if cmd.Flags().Changed("order") {
				if err := validateSpectrumEnum("order", order, spectrumListOrders); err != nil {
					return err
				}
				orderVal = order
			}
			directionVal := ""
			if cmd.Flags().Changed("direction") {
				if err := validateSpectrumEnum("direction", direction, spectrumListDirections); err != nil {
					return err
				}
				directionVal = direction
			}

			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			if g.DryRun {
				req := api.Request{
					Method: "GET",
					Path:   spectrumAppsPath(zoneID),
					Query:  spectrumListQuery(1, orderVal, directionVal),
				}
				dump, err := client.Dump(req)
				if err != nil {
					return err
				}
				return g.renderValue(cmd, dump, output.JSON)
			}
			apps, raw, err := listAllSpectrumApps(cmd.Context(), client, zoneID, orderVal, directionVal)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, raw, output.JSON)
			}
			rows := make([][]string, 0, len(apps))
			for _, a := range apps {
				rows = append(rows, []string{
					a.ID,
					a.Protocol,
					output.Cell(a.DNS.Name),
					a.DNS.Type,
					a.TrafficType,
					output.Cell(spectrumOriginLabel(a)),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "PROTOCOL", "DNS", "DNS TYPE", "TRAFFIC", "ORIGIN"}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&order, "order", "", "sort field: protocol, app_id, created_on, modified_on, or dns")
	cmd.Flags().StringVar(&direction, "direction", "", "sort direction: asc or desc")
	return cmd
}

func spectrumOriginLabel(a spectrumApp) string {
	if len(a.OriginDirect) > 0 {
		return strings.Join(a.OriginDirect, ",")
	}
	if a.OriginDNS != nil && a.OriginDNS.Name != "" {
		return a.OriginDNS.Name
	}
	return ""
}

func newSpectrumGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get <app-id>",
		Short: "Show one Spectrum application",
		Long:  "Show one Spectrum application.\n\nExample:\n\n  cf spectrum get ea95132c15732412d22c1476fa83f27a --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("app id must not be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: spectrumAppPath(zoneID, args[0])}
			return runSpectrumRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newSpectrumCreateCmd(g *globalOpts) *cobra.Command {
	var zone string
	var f spectrumAppFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Spectrum application",
		Long: `Create a Spectrum application that proxies TCP or UDP traffic through Cloudflare.

Provide an origin with either --origin-direct (repeatable) or --origin-dns
(with --origin-port). Edge IPs default to dynamic anycast when omitted.

Examples:

  cf spectrum create --zone example.com --protocol tcp/22 --dns-name ssh.example.com --dns-type CNAME --traffic-type direct --origin-direct tcp://192.0.2.1:22
  cf spectrum create --zone example.com --protocol tcp/22 --dns-name spectrum-cname.example.com --dns-type CNAME --origin-dns cname-to-origin.example.com --origin-port 22 --tls off --proxy-protocol off
  cf spectrum create --zone example.com --protocol tcp/22 --dns-name ssh.example.com --dns-type CNAME --origin-direct tcp://10.0.0.5:22 --virtual-network-id f70ff985-a4ef-4643-bbbc-4a0ed4fc8415 --proxy-protocol off`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildSpectrumCreateBody(cmd, f)
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
			req := api.Request{Method: "POST", Path: spectrumAppsPath(zoneID), Body: body}
			return runSpectrumRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindSpectrumAppFlags(cmd, &f, true)
	return cmd
}

func newSpectrumUpdateCmd(g *globalOpts) *cobra.Command {
	var zone string
	var f spectrumAppFlags
	cmd := &cobra.Command{
		Use:   "update <app-id>",
		Short: "Update a Spectrum application",
		Long: `Update fields of a Spectrum application.

The Spectrum API replaces the whole application on write, so this command
first reads the application as a raw JSON object and re-sends it with your
changes applied; fields you do not pass keep their current values, including
API fields this CLI does not model. --dry-run performs that read but never
sends the write.

Setting --origin-direct clears origin_dns/origin_port; setting --origin-dns
clears origin_direct. Setting any edge-IP flag rebuilds the edge_ips object.

Examples:

  cf spectrum update ea95132c15732412d22c1476fa83f27a --zone example.com --tls full
  cf spectrum update ea95132c15732412d22c1476fa83f27a --zone example.com --origin-direct tcp://192.0.2.9:22 --proxy-protocol off`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("app id must not be empty")
			}
			overrides, err := spectrumOverridesFromFlags(cmd, f)
			if err != nil {
				return err
			}
			if overrides.empty() {
				return errors.New("nothing to update: pass at least one application flag")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			path := spectrumAppPath(zoneID, args[0])
			env, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: path})
			if err != nil {
				return fmt.Errorf("read Spectrum application %s before update: %w", args[0], err)
			}
			// Decode as a generic object so unknown API fields survive the PUT.
			var cur map[string]any
			if err := json.Unmarshal(env.Result, &cur); err != nil || cur == nil {
				return fmt.Errorf("read Spectrum application %s before update: unexpected response", args[0])
			}
			next, err := mergeSpectrumAppMap(cur, overrides)
			if err != nil {
				return err
			}
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: path, Body: body}
			return runSpectrumRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindSpectrumAppFlags(cmd, &f, false)
	return cmd
}

func newSpectrumDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <app-id>",
		Short: "Delete a Spectrum application",
		Long:  "Delete a Spectrum application.\n\nExample:\n\n  cf spectrum delete ea95132c15732412d22c1476fa83f27a --zone example.com --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("app id must not be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Spectrum application %s from zone %s?", args[0], zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: spectrumAppPath(zoneID, args[0])}
			return runSpectrumRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func bindSpectrumAppFlags(cmd *cobra.Command, f *spectrumAppFlags, create bool) {
	flags := cmd.Flags()
	flags.StringVar(&f.protocol, "protocol", "", "edge port config, e.g. tcp/22 or tcp/1000-2000")
	flags.StringVar(&f.dnsName, "dns-name", "", "DNS hostname for the Spectrum application")
	flags.StringVar(&f.dnsType, "dns-type", "", "DNS record type: CNAME or ADDRESS")
	flags.StringVar(&f.trafficType, "traffic-type", "", "how traffic reaches the origin: direct, http, or https")
	flags.StringArrayVar(&f.originDirect, "origin-direct", nil, "origin address URI, e.g. tcp://192.0.2.1:22 (repeatable)")
	flags.StringVar(&f.originDNSName, "origin-dns", "", "origin hostname (sets origin_dns.name)")
	flags.StringVar(&f.originDNSType, "origin-dns-type", "", `origin DNS type: "", A, AAAA, or SRV (empty means A+AAAA)`)
	flags.IntVar(&f.originDNSTTL, "origin-dns-ttl", 0, "origin DNS TTL in seconds (minimum 600)")
	flags.StringVar(&f.originPort, "origin-port", "", "origin port or range (e.g. 22 or 1000-2000); use with --origin-dns")
	flags.StringVar(&f.proxyProtocol, "proxy-protocol", "", "proxy protocol to origin: off, v1, v2, or simple")
	flags.StringVar(&f.tls, "tls", "", "edge TLS termination: off, flexible, full, or strict")
	flags.BoolVar(&f.argoSmartRouting, "argo-smart-routing", false, "enable Argo Smart Routing (direct traffic only)")
	flags.BoolVar(&f.ipFirewall, "ip-firewall", false, "enable IP Access Rules for this application (TCP only)")
	flags.StringVar(&f.edgeIPSType, "edge-ips-type", "", "edge IP allocation: dynamic or static")
	flags.StringVar(&f.edgeConnectivity, "edge-ips-connectivity", "", "dynamic edge IP connectivity: all, ipv4, or ipv6")
	flags.StringArrayVar(&f.edgeIPs, "edge-ip", nil, "static edge anycast IP (repeatable; requires --edge-ips-type static)")
	flags.StringVar(&f.edgeIPsJSON, "edge-ips", "", `edge_ips object as JSON, e.g. '{"type":"dynamic","connectivity":"all"}'`)
	flags.StringVar(&f.virtualNetworkID, "virtual-network-id", "", "virtual network UUID for private origin routing")
	if create {
		_ = cmd.MarkFlagRequired("protocol")
		_ = cmd.MarkFlagRequired("dns-name")
		_ = cmd.MarkFlagRequired("dns-type")
	}
}

// buildSpectrumCreateBody validates the full create contract before any
// network I/O and returns the POST body.
func buildSpectrumCreateBody(cmd *cobra.Command, f spectrumAppFlags) ([]byte, error) {
	if strings.TrimSpace(f.protocol) == "" {
		return nil, errors.New("--protocol is required (e.g. tcp/22)")
	}
	if _, err := spectrumProtocolPortWidth(f.protocol); err != nil {
		return nil, err
	}
	if strings.TrimSpace(f.dnsName) == "" {
		return nil, errors.New("--dns-name is required")
	}
	dnsType, err := normalizeSpectrumEnum("dns-type", f.dnsType, spectrumDNSTypes)
	if err != nil {
		return nil, err
	}
	trafficType := "direct"
	if cmd.Flags().Changed("traffic-type") || strings.TrimSpace(f.trafficType) != "" {
		trafficType, err = normalizeSpectrumEnum("traffic-type", f.trafficType, spectrumTrafficTypes)
		if err != nil {
			return nil, err
		}
	}

	body := map[string]any{
		"protocol":     f.protocol,
		"dns":          map[string]any{"name": f.dnsName, "type": dnsType},
		"traffic_type": trafficType,
	}

	if err := addSpectrumOrigin(cmd, body, f, true); err != nil {
		return nil, err
	}
	if err := addSpectrumOptionalFields(cmd, body, f); err != nil {
		return nil, err
	}
	if err := validateSpectrumAppConfig(body); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// spectrumOverrides is the set of fields an update should rewrite.
type spectrumOverrides struct {
	protocol, trafficType, proxyProtocol, tls, virtualNetworkID *string
	dnsName, dnsType                                            *string
	originDirect                                                []string
	originDirectSet                                             bool
	originDNSName, originDNSType                                *string
	originDNSTTL                                                *int
	originPort                                                  any // int or string after parse
	originPortSet                                               bool
	argoSmartRouting, ipFirewall                                *bool
	edgeIPs                                                     map[string]any
	edgeIPsSet                                                  bool
	clearVirtualNetworkID                                       bool
}

func (o spectrumOverrides) empty() bool {
	return o.protocol == nil && o.trafficType == nil && o.proxyProtocol == nil && o.tls == nil &&
		o.virtualNetworkID == nil && !o.clearVirtualNetworkID &&
		o.dnsName == nil && o.dnsType == nil &&
		!o.originDirectSet && o.originDNSName == nil && o.originDNSType == nil && o.originDNSTTL == nil &&
		!o.originPortSet && o.argoSmartRouting == nil && o.ipFirewall == nil && !o.edgeIPsSet
}

func spectrumOverridesFromFlags(cmd *cobra.Command, f spectrumAppFlags) (spectrumOverrides, error) {
	var o spectrumOverrides
	if cmd.Flags().Changed("protocol") {
		if strings.TrimSpace(f.protocol) == "" {
			return o, errors.New("--protocol must not be empty")
		}
		if _, err := spectrumProtocolPortWidth(f.protocol); err != nil {
			return o, err
		}
		o.protocol = &f.protocol
	}
	if cmd.Flags().Changed("dns-name") {
		if strings.TrimSpace(f.dnsName) == "" {
			return o, errors.New("--dns-name must not be empty")
		}
		o.dnsName = &f.dnsName
	}
	if cmd.Flags().Changed("dns-type") {
		v, err := normalizeSpectrumEnum("dns-type", f.dnsType, spectrumDNSTypes)
		if err != nil {
			return o, err
		}
		o.dnsType = &v
	}
	if cmd.Flags().Changed("traffic-type") {
		v, err := normalizeSpectrumEnum("traffic-type", f.trafficType, spectrumTrafficTypes)
		if err != nil {
			return o, err
		}
		o.trafficType = &v
	}
	if cmd.Flags().Changed("origin-direct") {
		if err := validateSpectrumOriginDirect(f.originDirect); err != nil {
			return o, err
		}
		o.originDirect = f.originDirect
		o.originDirectSet = true
	}
	if cmd.Flags().Changed("origin-dns") {
		if strings.TrimSpace(f.originDNSName) == "" {
			return o, errors.New("--origin-dns must not be empty")
		}
		o.originDNSName = &f.originDNSName
	}
	if cmd.Flags().Changed("origin-dns-type") {
		v, err := normalizeSpectrumOriginDNSType(f.originDNSType)
		if err != nil {
			return o, err
		}
		o.originDNSType = &v
	}
	if cmd.Flags().Changed("origin-dns-ttl") {
		if f.originDNSTTL < spectrumOriginDNSTTLMin {
			return o, fmt.Errorf("--origin-dns-ttl must be at least %d", spectrumOriginDNSTTLMin)
		}
		o.originDNSTTL = &f.originDNSTTL
	}
	if cmd.Flags().Changed("origin-port") {
		port, err := parseSpectrumOriginPort(f.originPort)
		if err != nil {
			return o, err
		}
		o.originPort = port
		o.originPortSet = true
	}
	if cmd.Flags().Changed("proxy-protocol") {
		v, err := normalizeSpectrumEnum("proxy-protocol", f.proxyProtocol, spectrumProxyProtocols)
		if err != nil {
			return o, err
		}
		o.proxyProtocol = &v
	}
	if cmd.Flags().Changed("tls") {
		v, err := normalizeSpectrumEnum("tls", f.tls, spectrumTLSModes)
		if err != nil {
			return o, err
		}
		o.tls = &v
	}
	if cmd.Flags().Changed("argo-smart-routing") {
		o.argoSmartRouting = &f.argoSmartRouting
	}
	if cmd.Flags().Changed("ip-firewall") {
		o.ipFirewall = &f.ipFirewall
	}
	if cmd.Flags().Changed("virtual-network-id") {
		if strings.TrimSpace(f.virtualNetworkID) == "" {
			o.clearVirtualNetworkID = true
		} else {
			if err := validateSpectrumUUID("--virtual-network-id", f.virtualNetworkID); err != nil {
				return o, err
			}
			o.virtualNetworkID = &f.virtualNetworkID
		}
	}

	edge, edgeSet, err := buildSpectrumEdgeIPs(cmd, f)
	if err != nil {
		return o, err
	}
	if edgeSet {
		o.edgeIPs = edge
		o.edgeIPsSet = true
	}

	if o.originDirectSet && (o.originDNSName != nil || o.originDNSType != nil || o.originDNSTTL != nil || o.originPortSet) {
		return o, errors.New("--origin-direct cannot be combined with --origin-dns/--origin-port on the same update")
	}
	return o, nil
}

// mergeSpectrumAppMap applies overrides onto the raw GET object so unknown API
// fields survive the full PUT replacement. Read-only identifiers are stripped.
func mergeSpectrumAppMap(cur map[string]any, o spectrumOverrides) (map[string]any, error) {
	next := cloneSpectrumMap(cur)
	delete(next, "id")
	delete(next, "created_on")
	delete(next, "modified_on")

	if o.protocol != nil {
		next["protocol"] = *o.protocol
	}
	if o.trafficType != nil {
		next["traffic_type"] = *o.trafficType
	}
	if o.proxyProtocol != nil {
		next["proxy_protocol"] = *o.proxyProtocol
	}
	if o.tls != nil {
		next["tls"] = *o.tls
	}
	if o.argoSmartRouting != nil {
		next["argo_smart_routing"] = *o.argoSmartRouting
	}
	if o.ipFirewall != nil {
		next["ip_firewall"] = *o.ipFirewall
	}
	if o.clearVirtualNetworkID {
		delete(next, "virtual_network_id")
	} else if o.virtualNetworkID != nil {
		next["virtual_network_id"] = *o.virtualNetworkID
	}

	dns := spectrumMapField(next, "dns")
	if o.dnsName != nil {
		dns["name"] = *o.dnsName
	}
	if o.dnsType != nil {
		dns["type"] = *o.dnsType
	}
	if len(dns) > 0 {
		next["dns"] = dns
	}

	if o.originDirectSet {
		next["origin_direct"] = o.originDirect
		delete(next, "origin_dns")
		delete(next, "origin_port")
	}
	if o.originDNSName != nil || o.originDNSType != nil || o.originDNSTTL != nil {
		originDNS := spectrumMapField(next, "origin_dns")
		if o.originDNSName != nil {
			originDNS["name"] = *o.originDNSName
		}
		if o.originDNSType != nil {
			originDNS["type"] = *o.originDNSType
		}
		if o.originDNSTTL != nil {
			originDNS["ttl"] = *o.originDNSTTL
		}
		next["origin_dns"] = originDNS
		delete(next, "origin_direct")
	}
	if o.originPortSet {
		next["origin_port"] = o.originPort
		delete(next, "origin_direct")
	}
	if o.edgeIPsSet {
		next["edge_ips"] = o.edgeIPs
	}

	if err := validateSpectrumAppConfig(next); err != nil {
		return nil, err
	}
	if _, ok := next["protocol"].(string); !ok || next["protocol"] == "" {
		return nil, errors.New("application is missing protocol and cannot be updated")
	}
	if _, ok := next["dns"].(map[string]any); !ok {
		return nil, errors.New("application is missing dns and cannot be updated")
	}
	return next, nil
}

func cloneSpectrumMap(in map[string]any) map[string]any {
	// JSON round-trip deep-copies nested maps/slices without sharing state.
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		out = make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
	}
	return out
}

func spectrumMapField(parent map[string]any, key string) map[string]any {
	if m, ok := parent[key].(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}

func addSpectrumOrigin(cmd *cobra.Command, body map[string]any, f spectrumAppFlags, create bool) error {
	directSet := cmd.Flags().Changed("origin-direct") || len(f.originDirect) > 0
	dnsSet := cmd.Flags().Changed("origin-dns") || strings.TrimSpace(f.originDNSName) != ""
	portSet := cmd.Flags().Changed("origin-port") || strings.TrimSpace(f.originPort) != ""
	if create && !directSet && !dnsSet {
		return errors.New("origin is required: pass --origin-direct or --origin-dns")
	}
	if directSet && dnsSet {
		return errors.New("--origin-direct and --origin-dns are mutually exclusive")
	}
	if portSet && !dnsSet && !directSet {
		return errors.New("--origin-port requires --origin-dns")
	}
	if directSet {
		if err := validateSpectrumOriginDirect(f.originDirect); err != nil {
			return err
		}
		body["origin_direct"] = f.originDirect
		if portSet {
			return errors.New("--origin-port is only valid with --origin-dns")
		}
		return nil
	}
	if dnsSet {
		if strings.TrimSpace(f.originDNSName) == "" {
			return errors.New("--origin-dns must not be empty")
		}
		originDNS := map[string]any{"name": f.originDNSName}
		if cmd.Flags().Changed("origin-dns-type") {
			t, err := normalizeSpectrumOriginDNSType(f.originDNSType)
			if err != nil {
				return err
			}
			originDNS["type"] = t
		}
		if cmd.Flags().Changed("origin-dns-ttl") {
			if f.originDNSTTL < spectrumOriginDNSTTLMin {
				return fmt.Errorf("--origin-dns-ttl must be at least %d", spectrumOriginDNSTTLMin)
			}
			originDNS["ttl"] = f.originDNSTTL
		}
		body["origin_dns"] = originDNS
		if portSet {
			port, err := parseSpectrumOriginPort(f.originPort)
			if err != nil {
				return err
			}
			body["origin_port"] = port
		}
	}
	return nil
}

func addSpectrumOptionalFields(cmd *cobra.Command, body map[string]any, f spectrumAppFlags) error {
	if cmd.Flags().Changed("proxy-protocol") {
		v, err := normalizeSpectrumEnum("proxy-protocol", f.proxyProtocol, spectrumProxyProtocols)
		if err != nil {
			return err
		}
		body["proxy_protocol"] = v
	}
	if cmd.Flags().Changed("tls") {
		v, err := normalizeSpectrumEnum("tls", f.tls, spectrumTLSModes)
		if err != nil {
			return err
		}
		body["tls"] = v
	}
	if cmd.Flags().Changed("argo-smart-routing") {
		body["argo_smart_routing"] = f.argoSmartRouting
	}
	if cmd.Flags().Changed("ip-firewall") {
		body["ip_firewall"] = f.ipFirewall
	}
	if cmd.Flags().Changed("virtual-network-id") {
		if err := validateSpectrumUUID("--virtual-network-id", f.virtualNetworkID); err != nil {
			return err
		}
		body["virtual_network_id"] = f.virtualNetworkID
	}
	edge, edgeSet, err := buildSpectrumEdgeIPs(cmd, f)
	if err != nil {
		return err
	}
	if edgeSet {
		body["edge_ips"] = edge
	}
	return nil
}

// buildSpectrumEdgeIPs returns the edge_ips object when any edge flag is set.
// --edge-ips (JSON) is exclusive with the decomposed flags.
func buildSpectrumEdgeIPs(cmd *cobra.Command, f spectrumAppFlags) (map[string]any, bool, error) {
	jsonSet := cmd.Flags().Changed("edge-ips")
	decompSet := cmd.Flags().Changed("edge-ips-type") || cmd.Flags().Changed("edge-ips-connectivity") || cmd.Flags().Changed("edge-ip")
	if !jsonSet && !decompSet {
		return nil, false, nil
	}
	if jsonSet && decompSet {
		return nil, false, errors.New("--edge-ips cannot be combined with --edge-ips-type, --edge-ips-connectivity, or --edge-ip")
	}
	if jsonSet {
		obj, err := parseSpectrumEdgeIPsJSON(f.edgeIPsJSON)
		if err != nil {
			return nil, false, err
		}
		return obj, true, nil
	}

	edgeType := strings.ToLower(strings.TrimSpace(f.edgeIPSType))
	if edgeType == "" {
		if cmd.Flags().Changed("edge-ip") {
			return nil, false, errors.New("--edge-ip requires --edge-ips-type static")
		}
		edgeType = "dynamic"
	}
	edgeType, err := normalizeSpectrumEnum("edge-ips-type", edgeType, spectrumEdgeIPSTypes)
	if err != nil {
		return nil, false, err
	}
	switch edgeType {
	case "dynamic":
		if len(f.edgeIPs) > 0 {
			return nil, false, errors.New("--edge-ip is only valid with --edge-ips-type static")
		}
		connectivity := "all"
		if cmd.Flags().Changed("edge-ips-connectivity") || strings.TrimSpace(f.edgeConnectivity) != "" {
			connectivity, err = normalizeSpectrumEnum("edge-ips-connectivity", f.edgeConnectivity, spectrumEdgeConnectivity)
			if err != nil {
				return nil, false, err
			}
		}
		return map[string]any{"type": "dynamic", "connectivity": connectivity}, true, nil
	case "static":
		if cmd.Flags().Changed("edge-ips-connectivity") {
			return nil, false, errors.New("--edge-ips-connectivity is only valid with --edge-ips-type dynamic")
		}
		if len(f.edgeIPs) == 0 {
			return nil, false, errors.New("--edge-ips-type static requires at least one --edge-ip")
		}
		for i, ip := range f.edgeIPs {
			if strings.TrimSpace(ip) == "" {
				return nil, false, fmt.Errorf("--edge-ip #%d is empty", i+1)
			}
		}
		return map[string]any{"type": "static", "ips": f.edgeIPs}, true, nil
	default:
		return nil, false, fmt.Errorf("--edge-ips-type must be one of: %s", strings.Join(spectrumEdgeIPSTypes, ", "))
	}
}

// parseSpectrumEdgeIPsJSON requires a non-null JSON object. null, arrays,
// scalars, and malformed JSON are rejected (wave-2 contract).
func parseSpectrumEdgeIPsJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New(`--edge-ips must be a JSON object, for example '{"type":"dynamic","connectivity":"all"}'`)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, errors.New(`--edge-ips must be a JSON object, for example '{"type":"dynamic","connectivity":"all"}'`)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New(`--edge-ips must be a JSON object, for example '{"type":"dynamic","connectivity":"all"}'`)
	}
	if t, has := obj["type"]; has {
		ts, ok := t.(string)
		if !ok {
			return nil, errors.New(`--edge-ips "type" must be a string: "dynamic" or "static"`)
		}
		canon, err := normalizeSpectrumEnum("edge-ips type", ts, spectrumEdgeIPSTypes)
		if err != nil {
			return nil, err
		}
		obj["type"] = canon
		if canon == "dynamic" {
			if c, has := obj["connectivity"]; has {
				cs, ok := c.(string)
				if !ok {
					return nil, errors.New(`--edge-ips "connectivity" must be a string: all, ipv4, or ipv6`)
				}
				cc, err := normalizeSpectrumEnum("edge-ips connectivity", cs, spectrumEdgeConnectivity)
				if err != nil {
					return nil, err
				}
				obj["connectivity"] = cc
			}
		}
		if canon == "static" {
			ips, has := obj["ips"]
			if !has {
				return nil, errors.New(`--edge-ips static object requires "ips" array`)
			}
			arr, ok := ips.([]any)
			if !ok {
				return nil, errors.New(`--edge-ips "ips" must be a JSON array of strings`)
			}
			if len(arr) == 0 {
				return nil, errors.New(`--edge-ips "ips" must contain at least one IP`)
			}
			out := make([]string, 0, len(arr))
			for i, item := range arr {
				s, ok := item.(string)
				if !ok || strings.TrimSpace(s) == "" {
					return nil, fmt.Errorf(`--edge-ips "ips"[%d] must be a non-empty string`, i)
				}
				out = append(out, s)
			}
			obj["ips"] = out
		}
	}
	return obj, nil
}

func validateSpectrumOriginDirect(addrs []string) error {
	if len(addrs) == 0 {
		return errors.New("--origin-direct requires at least one address (e.g. tcp://192.0.2.1:22)")
	}
	for i, a := range addrs {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("--origin-direct #%d is empty", i+1)
		}
	}
	return nil
}

// parseSpectrumOriginPort turns a CLI string into the wire value: a JSON
// number for a single port, or a string for a range. Enforces 1..65535 on
// both sides of a range.
func parseSpectrumOriginPort(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("--origin-port must not be empty")
	}
	if strings.Contains(raw, "-") {
		lo, hi, err := spectrumParsePortRange(raw)
		if err != nil {
			return nil, fmt.Errorf("--origin-port: %w", err)
		}
		return fmt.Sprintf("%d-%d", lo, hi), nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return nil, errors.New("--origin-port must be an integer or a range like 1000-2000")
	}
	if port < spectrumOriginPortMin || port > spectrumOriginPortMax {
		return nil, fmt.Errorf("--origin-port must be between %d and %d", spectrumOriginPortMin, spectrumOriginPortMax)
	}
	return port, nil
}

func spectrumParsePortRange(raw string) (lo, hi int, err error) {
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return 0, 0, errors.New(`range must look like "1000-2000"`)
	}
	lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, errors.New(`range must look like "1000-2000"`)
	}
	if lo < spectrumOriginPortMin || lo > spectrumOriginPortMax || hi < spectrumOriginPortMin || hi > spectrumOriginPortMax {
		return 0, 0, fmt.Errorf("ports must be between %d and %d", spectrumOriginPortMin, spectrumOriginPortMax)
	}
	if lo > hi {
		return 0, 0, errors.New("range start must be <= end")
	}
	return lo, hi, nil
}

// spectrumProtocolPortWidth returns the number of edge ports described by a
// protocol string (tcp/22 → 1, tcp/1000-2000 → 1001).
func spectrumProtocolPortWidth(protocol string) (int, error) {
	protocol = strings.TrimSpace(protocol)
	if protocol == "" {
		return 0, errors.New("--protocol must not be empty")
	}
	scheme, ports, ok := strings.Cut(protocol, "/")
	if !ok || strings.TrimSpace(scheme) == "" || strings.TrimSpace(ports) == "" {
		return 0, errors.New("--protocol must look like tcp/22 or tcp/1000-2000")
	}
	scheme = strings.ToLower(scheme)
	if scheme != "tcp" && scheme != "udp" {
		// HTTP/HTTPS traffic types still use tcp/... protocols in Spectrum;
		// reject unknown schemes early.
		return 0, errors.New("--protocol scheme must be tcp or udp (e.g. tcp/22)")
	}
	return spectrumPortSpecWidth(ports, "--protocol")
}

func spectrumPortSpecWidth(spec, flag string) (int, error) {
	spec = strings.TrimSpace(spec)
	if strings.Contains(spec, "-") {
		lo, hi, err := spectrumParsePortRange(spec)
		if err != nil {
			return 0, fmt.Errorf("%s: %w", flag, err)
		}
		return hi - lo + 1, nil
	}
	port, err := strconv.Atoi(spec)
	if err != nil {
		return 0, fmt.Errorf("%s port must be an integer or range", flag)
	}
	if port < spectrumOriginPortMin || port > spectrumOriginPortMax {
		return 0, fmt.Errorf("%s port must be between %d and %d", flag, spectrumOriginPortMin, spectrumOriginPortMax)
	}
	return 1, nil
}

func spectrumOriginPortWidth(v any) (int, error) {
	switch t := v.(type) {
	case float64:
		// JSON numbers decode as float64 in map[string]any.
		port := int(t)
		if float64(port) != t {
			return 0, errors.New("origin_port must be an integer or range string")
		}
		if port < spectrumOriginPortMin || port > spectrumOriginPortMax {
			return 0, fmt.Errorf("origin_port must be between %d and %d", spectrumOriginPortMin, spectrumOriginPortMax)
		}
		return 1, nil
	case int:
		if t < spectrumOriginPortMin || t > spectrumOriginPortMax {
			return 0, fmt.Errorf("origin_port must be between %d and %d", spectrumOriginPortMin, spectrumOriginPortMax)
		}
		return 1, nil
	case json.Number:
		port, err := t.Int64()
		if err != nil {
			return 0, errors.New("origin_port must be an integer or range string")
		}
		if port < spectrumOriginPortMin || port > spectrumOriginPortMax {
			return 0, fmt.Errorf("origin_port must be between %d and %d", spectrumOriginPortMin, spectrumOriginPortMax)
		}
		return 1, nil
	case string:
		return spectrumPortSpecWidth(t, "origin_port")
	default:
		return 0, errors.New("origin_port must be an integer or range string")
	}
}

// validateSpectrumAppConfig enforces cross-field Spectrum schema rules on a
// fully built create/update body (before client work and after merge).
func validateSpectrumAppConfig(body map[string]any) error {
	protocol, _ := body["protocol"].(string)
	trafficType, _ := body["traffic_type"].(string)
	dns, _ := body["dns"].(map[string]any)
	dnsType, _ := dns["type"].(string)

	if protocol != "" {
		if _, err := spectrumProtocolPortWidth(protocol); err != nil {
			return err
		}
	}

	// origin_port only with a complete origin_dns (name present).
	if _, hasPort := body["origin_port"]; hasPort {
		originDNS, _ := body["origin_dns"].(map[string]any)
		name, _ := originDNS["name"].(string)
		if strings.TrimSpace(name) == "" {
			return errors.New("origin_port requires origin_dns with a name")
		}
	}

	// Equal-width rule: protocol port count must match origin_port / origin_direct ranges.
	if protocol != "" {
		edgeWidth, err := spectrumProtocolPortWidth(protocol)
		if err != nil {
			return err
		}
		if op, has := body["origin_port"]; has {
			ow, err := spectrumOriginPortWidth(op)
			if err != nil {
				return err
			}
			if ow != edgeWidth {
				return fmt.Errorf("origin_port range width (%d) must match protocol port range width (%d)", ow, edgeWidth)
			}
		}
		if err := validateSpectrumOriginDirectWidths(body["origin_direct"], edgeWidth); err != nil {
			return err
		}
	}

	// edge_ips vs dns.type: dynamic ↔ CNAME, static ↔ ADDRESS.
	if edge, ok := body["edge_ips"].(map[string]any); ok {
		edgeType, _ := edge["type"].(string)
		switch edgeType {
		case "dynamic":
			if dnsType != "" && !strings.EqualFold(dnsType, "CNAME") {
				return errors.New(`edge_ips type "dynamic" requires dns type CNAME`)
			}
		case "static":
			if dnsType != "" && !strings.EqualFold(dnsType, "ADDRESS") {
				return errors.New(`edge_ips type "static" requires dns type ADDRESS`)
			}
		}
	}

	// argo_smart_routing only for tcp/udp + traffic_type direct.
	if argo, ok := body["argo_smart_routing"].(bool); ok && argo {
		if trafficType != "" && trafficType != "direct" {
			return errors.New("--argo-smart-routing requires --traffic-type direct")
		}
		if protocol != "" && !spectrumProtocolIsTCPOrUDP(protocol) {
			return errors.New("--argo-smart-routing requires a tcp/... or udp/... protocol")
		}
	}

	// ip_firewall only for TCP applications.
	if ipfw, ok := body["ip_firewall"].(bool); ok && ipfw {
		if protocol != "" && !spectrumProtocolIsTCP(protocol) {
			return errors.New("--ip-firewall is only valid for TCP applications")
		}
	}

	return validateSpectrumVirtualNetwork(body)
}

func validateSpectrumOriginDirectWidths(v any, edgeWidth int) error {
	var addrs []string
	switch t := v.(type) {
	case []string:
		addrs = t
	case []any:
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return errors.New("origin_direct entries must be strings")
			}
			addrs = append(addrs, s)
		}
	case nil:
		return nil
	default:
		return nil
	}
	for i, a := range addrs {
		if !spectrumOriginURIHasPortRange(a) {
			if edgeWidth != 1 {
				// Single origin port with multi-port edge is only OK if the
				// URI has no explicit range (API may map by offset); when the
				// URI lacks a range we do not invent one. Skip width check.
				continue
			}
			continue
		}
		portPart := a[strings.LastIndex(a, ":")+1:]
		w, err := spectrumPortSpecWidth(portPart, fmt.Sprintf("origin_direct #%d", i+1))
		if err != nil {
			return err
		}
		if w != edgeWidth {
			return fmt.Errorf("origin_direct #%d port range width (%d) must match protocol port range width (%d)", i+1, w, edgeWidth)
		}
	}
	return nil
}

func spectrumProtocolIsTCP(protocol string) bool {
	scheme, _, ok := strings.Cut(strings.ToLower(protocol), "/")
	return ok && scheme == "tcp"
}

func spectrumProtocolIsTCPOrUDP(protocol string) bool {
	scheme, _, ok := strings.Cut(strings.ToLower(protocol), "/")
	return ok && (scheme == "tcp" || scheme == "udp")
}

func validateSpectrumVirtualNetwork(body map[string]any) error {
	vni, has := body["virtual_network_id"]
	if !has {
		return nil
	}
	id, _ := vni.(string)
	if err := validateSpectrumUUID("--virtual-network-id", id); err != nil {
		return err
	}
	if _, hasDNS := body["origin_dns"]; hasDNS {
		return errors.New("--virtual-network-id requires --origin-direct (hostname origins are not supported)")
	}
	if direct, ok := body["origin_direct"].([]string); ok {
		if len(direct) != 1 {
			return errors.New("--virtual-network-id requires exactly one --origin-direct address")
		}
		if spectrumOriginURIHasPortRange(direct[0]) {
			return errors.New("--virtual-network-id does not support origin port ranges")
		}
	} else if directAny, ok := body["origin_direct"].([]any); ok {
		if len(directAny) != 1 {
			return errors.New("--virtual-network-id requires exactly one --origin-direct address")
		}
		if s, ok := directAny[0].(string); ok && spectrumOriginURIHasPortRange(s) {
			return errors.New("--virtual-network-id does not support origin port ranges")
		}
	} else if _, hasDirect := body["origin_direct"]; !hasDirect {
		return errors.New("--virtual-network-id requires --origin-direct")
	}
	if pp, ok := body["proxy_protocol"].(string); ok && pp != "off" {
		return errors.New("--virtual-network-id requires --proxy-protocol off")
	}
	if tt, ok := body["traffic_type"].(string); ok && tt != "direct" {
		return errors.New("--virtual-network-id requires --traffic-type direct")
	}
	return nil
}

func spectrumOriginURIHasPortRange(uri string) bool {
	idx := strings.LastIndex(uri, ":")
	if idx < 0 || idx+1 >= len(uri) {
		return false
	}
	portPart := uri[idx+1:]
	return strings.Contains(portPart, "-")
}

// isSpectrumUUID reports whether s has the shape of a UUID (with or without dashes).
func isSpectrumUUID(s string) bool {
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

func validateSpectrumUUID(flag, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", flag)
	}
	if !isSpectrumUUID(value) {
		return fmt.Errorf("%s must be a UUID", flag)
	}
	return nil
}

func validateSpectrumEnum(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

// normalizeSpectrumEnum matches case-insensitively and returns the canonical
// allowed entry (wire value).
func normalizeSpectrumEnum(flag, value string, allowed []string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
	}
	for _, a := range allowed {
		if strings.EqualFold(value, a) {
			return a, nil
		}
	}
	return "", fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

func normalizeSpectrumOriginDNSType(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return normalizeSpectrumEnum("origin-dns-type", value, []string{"A", "AAAA", "SRV"})
}

func runSpectrumRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
