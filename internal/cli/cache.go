package cli

// Cache porcelain: purge workflows and Smart Tiered Cache get/set.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

func newCacheCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Purge cache and manage Smart Tiered Cache",
	}
	cmd.AddCommand(
		newCachePurgeCmd(g),
		newCacheSmartTieredCmd(g),
	)
	return cmd
}

func cachePurgePath(zoneID string) string {
	return "/zones/" + zoneID + "/purge_cache"
}

func cacheSmartTieredPath(zoneID string) string {
	return "/zones/" + zoneID + "/cache/tiered_cache_smart_topology_enable"
}

func newCachePurgeCmd(g *globalOpts) *cobra.Command {
	var zone string
	var everything, force bool
	var urls, tags, hosts, prefixes []string
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Purge cached content for a zone",
		Long: `Purge cached content for a zone. Exactly one purge mode is required.

Examples:

  cf cache purge --zone example.com --everything --force
  cf cache purge --zone example.com --url https://example.com/app.js --url https://example.com/style.css
  cf cache purge --zone example.com --tag product --tag images
  cf cache purge --zone example.com --host www.example.com
  cf cache purge --zone example.com --prefix www.example.com/static`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, summary, err := buildCachePurgeBody(everything, urls, tags, hosts, prefixes)
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
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Purge cache (%s) for zone %s?", summary, zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "POST", Path: cachePurgePath(zoneID), Body: body}
			return runCacheRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&everything, "everything", false, "purge all cached content for the zone")
	cmd.Flags().StringArrayVar(&urls, "url", nil, "URL to purge (repeatable)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "Cache-Tag to purge (repeatable)")
	cmd.Flags().StringArrayVar(&hosts, "host", nil, "hostname to purge (repeatable)")
	cmd.Flags().StringArrayVar(&prefixes, "prefix", nil, "URL prefix to purge (repeatable)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// buildCachePurgeBody validates mutually exclusive purge modes and returns the
// JSON body plus a short human summary for the confirmation prompt.
func buildCachePurgeBody(everything bool, urls, tags, hosts, prefixes []string) ([]byte, string, error) {
	modes := 0
	if everything {
		modes++
	}
	if len(urls) > 0 {
		modes++
	}
	if len(tags) > 0 {
		modes++
	}
	if len(hosts) > 0 {
		modes++
	}
	if len(prefixes) > 0 {
		modes++
	}
	if modes == 0 {
		return nil, "", errors.New("specify a purge mode: --everything, --url, --tag, --host, or --prefix")
	}
	if modes > 1 {
		return nil, "", errors.New("specify exactly one purge mode: --everything, --url, --tag, --host, or --prefix")
	}

	var body any
	var summary string
	switch {
	case everything:
		body = map[string]any{"purge_everything": true}
		summary = "everything"
	case len(urls) > 0:
		if err := validateNonEmptyStrings("url", urls); err != nil {
			return nil, "", err
		}
		body = map[string]any{"files": urls}
		summary = fmt.Sprintf("%d URL(s)", len(urls))
	case len(tags) > 0:
		if err := validateNonEmptyStrings("tag", tags); err != nil {
			return nil, "", err
		}
		body = map[string]any{"tags": tags}
		summary = fmt.Sprintf("%d tag(s)", len(tags))
	case len(hosts) > 0:
		if err := validateNonEmptyStrings("host", hosts); err != nil {
			return nil, "", err
		}
		body = map[string]any{"hosts": hosts}
		summary = fmt.Sprintf("%d host(s)", len(hosts))
	case len(prefixes) > 0:
		if err := validateNonEmptyStrings("prefix", prefixes); err != nil {
			return nil, "", err
		}
		body = map[string]any{"prefixes": prefixes}
		summary = fmt.Sprintf("%d prefix(es)", len(prefixes))
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	return raw, summary, nil
}

func validateNonEmptyStrings(flag string, values []string) error {
	for i, v := range values {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("--%s value at position %d is empty", flag, i+1)
		}
	}
	return nil
}

func newCacheSmartTieredCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "smart-tiered",
		Short: "Manage Smart Tiered Cache for a zone",
	}
	cmd.AddCommand(
		newCacheSmartTieredGetCmd(g),
		newCacheSmartTieredSetCmd(g),
	)
	return cmd
}

func newCacheSmartTieredGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show Smart Tiered Cache setting",
		Long:  "Show the Smart Tiered Cache setting for a zone.\n\nExamples:\n\n  cf cache smart-tiered get --zone example.com",
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
			req := api.Request{Method: "GET", Path: cacheSmartTieredPath(zoneID)}
			return runCacheRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newCacheSmartTieredSetCmd(g *globalOpts) *cobra.Command {
	var zone, value string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set Smart Tiered Cache on or off",
		Long: `Enable or disable Smart Tiered Cache for a zone.

Examples:

  cf cache smart-tiered set --zone example.com --value on
  cf cache smart-tiered set --zone example.com --value off`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildSmartTieredBody(value)
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
			req := api.Request{Method: "PATCH", Path: cacheSmartTieredPath(zoneID), Body: body}
			return runCacheRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&value, "value", "", "setting value: on or off")
	_ = cmd.MarkFlagRequired("value")
	return cmd
}

func buildSmartTieredBody(value string) ([]byte, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	if v != "on" && v != "off" {
		return nil, errors.New("--value must be on or off")
	}
	return json.Marshal(map[string]string{"value": v})
}

func runCacheRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
