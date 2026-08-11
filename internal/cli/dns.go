package cli

// DNS is the porcelain exemplar: the pattern every product's high-level
// commands should follow. See docs/STYLE.md.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/TormodHaugland/cf-cli/internal/api"
	"github.com/TormodHaugland/cf-cli/internal/output"
)

type dnsRecord struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Content  string `json:"content,omitempty"`
	TTL      int    `json:"ttl,omitempty"`
	Proxied  *bool  `json:"proxied,omitempty"`
	Priority *int   `json:"priority,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

func newDNSCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Manage DNS records",
	}
	cmd.AddCommand(
		newDNSListCmd(g),
		newDNSGetCmd(g),
		newDNSCreateCmd(g),
		newDNSUpdateCmd(g),
		newDNSDeleteCmd(g),
	)
	return cmd
}

// resolveZoneID accepts a zone ID or a zone name (looked up via the API) and
// falls back to the configured zone.
func resolveZoneID(ctx context.Context, client *api.Client, configured, flagZone string) (string, error) {
	z := flagZone
	if z == "" {
		z = configured
	}
	if z == "" {
		return "", errors.New("no zone specified: pass --zone, set CLOUDFLARE_ZONE_ID, or configure a profile")
	}
	if isZoneID(z) {
		return z, nil
	}
	q := url.Values{}
	q.Set("name", z)
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: "/zones", Query: q})
	if err != nil {
		return "", fmt.Errorf("look up zone %q: %w", z, err)
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &zones); err != nil {
		return "", fmt.Errorf("look up zone %q: unexpected response", z)
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("zone %q not found on this account", z)
	}
	return zones[0].ID, nil
}

// isZoneID reports whether s looks like a Cloudflare zone ID (32 hex chars).
func isZoneID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func dnsPath(zoneID string) string { return "/zones/" + zoneID + "/dns_records" }

func newDNSListCmd(g *globalOpts) *cobra.Command {
	var zone, rtype, name string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List DNS records in a zone",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, zone)
			if err != nil {
				return err
			}
			q := url.Values{}
			if rtype != "" {
				q.Set("type", strings.ToUpper(rtype))
			}
			if name != "" {
				q.Set("name", name)
			}
			q.Set("per_page", "100")
			req := api.Request{Method: "GET", Path: dnsPath(zoneID), Query: q}
			if g.DryRun {
				dump, err := client.Dump(req)
				if err != nil {
					return err
				}
				return output.Render(cmd.OutOrStdout(), g.format(output.JSON), dump)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if format != output.Table {
				return output.RenderRaw(cmd.OutOrStdout(), format, env.Result)
			}
			var records []dnsRecord
			if err := json.Unmarshal(env.Result, &records); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(records))
			for _, r := range records {
				proxied := ""
				if r.Proxied != nil {
					proxied = strconv.FormatBool(*r.Proxied)
				}
				ttl := strconv.Itoa(r.TTL)
				if r.TTL == 1 {
					ttl = "auto"
				}
				rows = append(rows, []string{r.ID, r.Type, r.Name, output.Cell(r.Content), proxied, ttl})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "TYPE", "NAME", "CONTENT", "PROXIED", "TTL"}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&rtype, "type", "", "filter by record type (A, AAAA, CNAME, MX, TXT, ...)")
	cmd.Flags().StringVar(&name, "name", "", "filter by exact record name")
	return cmd
}

func newDNSGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get <record-id>",
		Short: "Show one DNS record",
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
			req := api.Request{Method: "GET", Path: dnsPath(zoneID) + "/" + url.PathEscape(args[0])}
			return runDNSRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newDNSCreateCmd(g *globalOpts) *cobra.Command {
	var zone, rtype, content, comment string
	var ttl, priority int
	var proxied bool
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a DNS record",
		Long:  "Create a DNS record. Examples:\n\n  cf dns create www.example.com --type A --content 192.0.2.1 --proxied\n  cf dns create example.com --type MX --content mail.example.com --priority 10",
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
			rec := dnsRecord{
				Name:    args[0],
				Type:    strings.ToUpper(rtype),
				Content: content,
				TTL:     ttl,
				Comment: comment,
			}
			if cmd.Flags().Changed("proxied") {
				rec.Proxied = &proxied
			}
			if cmd.Flags().Changed("priority") {
				rec.Priority = &priority
			}
			body, err := json.Marshal(rec)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: dnsPath(zoneID), Body: body}
			return runDNSRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&rtype, "type", "", "record type (A, AAAA, CNAME, MX, TXT, ...)")
	cmd.Flags().StringVar(&content, "content", "", "record content (IP address, target hostname, text, ...)")
	cmd.Flags().IntVar(&ttl, "ttl", 1, "TTL in seconds (1 = automatic)")
	cmd.Flags().IntVar(&priority, "priority", 0, "record priority (MX, SRV, URI)")
	cmd.Flags().BoolVar(&proxied, "proxied", false, "proxy through Cloudflare")
	cmd.Flags().StringVar(&comment, "comment", "", "record comment")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("content")
	return cmd
}

func newDNSUpdateCmd(g *globalOpts) *cobra.Command {
	var zone, rtype, name, content, comment string
	var ttl, priority int
	var proxied bool
	cmd := &cobra.Command{
		Use:   "update <record-id>",
		Short: "Update fields of a DNS record",
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
			patch := map[string]any{}
			if cmd.Flags().Changed("type") {
				patch["type"] = strings.ToUpper(rtype)
			}
			if cmd.Flags().Changed("name") {
				patch["name"] = name
			}
			if cmd.Flags().Changed("content") {
				patch["content"] = content
			}
			if cmd.Flags().Changed("ttl") {
				patch["ttl"] = ttl
			}
			if cmd.Flags().Changed("priority") {
				patch["priority"] = priority
			}
			if cmd.Flags().Changed("proxied") {
				patch["proxied"] = proxied
			}
			if cmd.Flags().Changed("comment") {
				patch["comment"] = comment
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --type, --name, --content, --ttl, --priority, --proxied, --comment")
			}
			body, err := json.Marshal(patch)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: dnsPath(zoneID) + "/" + url.PathEscape(args[0]), Body: body}
			return runDNSRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&rtype, "type", "", "record type")
	cmd.Flags().StringVar(&name, "name", "", "record name")
	cmd.Flags().StringVar(&content, "content", "", "record content")
	cmd.Flags().IntVar(&ttl, "ttl", 0, "TTL in seconds (1 = automatic)")
	cmd.Flags().IntVar(&priority, "priority", 0, "record priority")
	cmd.Flags().BoolVar(&proxied, "proxied", false, "proxy through Cloudflare")
	cmd.Flags().StringVar(&comment, "comment", "", "record comment")
	return cmd
}

func newDNSDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <record-id>",
		Short: "Delete a DNS record",
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
				if !confirm(fmt.Sprintf("Delete DNS record %s from zone %s?", args[0], zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: dnsPath(zoneID) + "/" + url.PathEscape(args[0])}
			return runDNSRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func runDNSRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
	if g.DryRun {
		dump, err := client.Dump(req)
		if err != nil {
			return err
		}
		return output.Render(cmd.OutOrStdout(), g.format(output.JSON), dump)
	}
	env, err := client.Do(cmd.Context(), req)
	if err != nil {
		return err
	}
	return output.RenderRaw(cmd.OutOrStdout(), g.format(output.JSON), env.Result)
}

// confirm prompts on stderr and reads y/N from stdin; it declines when stdin
// is not a terminal.
func confirm(prompt string) bool {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	var s string
	_, _ = fmt.Fscanln(os.Stdin, &s)
	s = strings.ToLower(strings.TrimSpace(s))
	return s == "y" || s == "yes"
}
