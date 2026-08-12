package cli

// Secondary DNS porcelain covers the usual transfer configuration, peer, and
// TSIG workflows. The generated cf api secondary-dns surface remains
// available for ACLs and less-common operations.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

const secondaryDNSDefaultAutoRefreshSeconds = 86400

type secondaryDNSPeer struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IP         string `json:"ip"`
	Port       any    `json:"port"`
	TSIGID     string `json:"tsig_id"`
	IXFREnable *bool  `json:"ixfr_enable"`
}

type secondaryDNSTSIG struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Algo string `json:"algo"`
}

func newSecondaryDNSCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secondary-dns",
		Short: "Manage Secondary DNS zone transfers",
		Long:  "Manage Secondary DNS zone transfers, account peers, and TSIG keys.",
	}
	cmd.AddCommand(
		newSecondaryDNSIncomingCmd(g),
		newSecondaryDNSOutgoingCmd(g),
		newSecondaryDNSPeersCmd(g),
		newSecondaryDNSTSIGsCmd(g),
	)
	return cmd
}

func secondaryDNSAccountPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/secondary_dns"
}

func secondaryDNSPeersPath(accountID string) string {
	return secondaryDNSAccountPath(accountID) + "/peers"
}
func secondaryDNSTSIGsPath(accountID string) string {
	return secondaryDNSAccountPath(accountID) + "/tsigs"
}

func secondaryDNSZonePath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/secondary_dns"
}

func secondaryDNSRequireAccountID(accountID string) error {
	if strings.TrimSpace(accountID) == "" {
		return errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return nil
}

func secondaryDNSRequireIdentifier(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	return nil
}

// secondaryDNSAccountClient resolves local configuration and validates the
// account before constructing a client. Callers validate command input first.
func secondaryDNSAccountClient(g *globalOpts) (*api.Client, string, error) {
	cfg, err := g.resolve()
	if err != nil {
		return nil, "", err
	}
	if err := secondaryDNSRequireAccountID(cfg.AccountID); err != nil {
		return nil, "", err
	}
	if !g.DryRun && cfg.Token == "" {
		return nil, "", errors.New("no API token found; run `cf auth login`, set CLOUDFLARE_API_TOKEN, or pass --token")
	}
	return api.New(g.BaseURL, cfg.Token, Version), cfg.AccountID, nil
}

// secondaryDNSZoneClient uses the shared interactive zone resolver. The
// resolver may read during --dry-run only when a zone name must become its ID.
func secondaryDNSZoneClient(cmd *cobra.Command, g *globalOpts, zone string) (*api.Client, string, error) {
	cfg, err := g.resolve()
	if err != nil {
		return nil, "", err
	}
	if !g.DryRun && cfg.Token == "" {
		return nil, "", errors.New("no API token found; run `cf auth login`, set CLOUDFLARE_API_TOKEN, or pass --token")
	}
	client := api.New(g.BaseURL, cfg.Token, Version)
	zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
	if err != nil {
		return nil, "", err
	}
	return client, zoneID, nil
}

func runSecondaryDNSRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func secondaryDNSListRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) (*api.Envelope, bool, error) {
	if g.DryRun {
		if err := runSecondaryDNSRequest(cmd, g, client, req); err != nil {
			return nil, true, err
		}
		return nil, true, nil
	}
	// These endpoints report page/total_pages metadata but do not expose the
	// usual generated list query parameters. Keep their page loop local so its
	// continuation semantics are explicit and do not rely on another product's
	// pagination contract.
	if req.Query == nil {
		req.Query = url.Values{}
	}
	var merged []json.RawMessage
	for requestPage := 0; requestPage < 1000; requestPage++ {
		env, err := client.Do(cmd.Context(), req)
		if err != nil {
			return env, false, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(env.Result, &items); err != nil {
			if requestPage == 0 {
				return env, false, nil
			}
			return nil, false, fmt.Errorf("Secondary DNS pagination: page %d result was not an array", requestPage+1)
		}
		merged = append(merged, items...)
		if env.ResultInfo == nil || env.ResultInfo.TotalPages == 0 || env.ResultInfo.Page >= env.ResultInfo.TotalPages {
			result, err := json.Marshal(merged)
			if err != nil {
				return nil, false, err
			}
			return &api.Envelope{Success: true, Result: result, Messages: env.Messages}, false, nil
		}
		nextPage := env.ResultInfo.Page + 1
		if env.ResultInfo.Page == 0 {
			nextPage = requestPage + 2
		}
		req.Query.Set("page", strconv.Itoa(nextPage))
	}
	return nil, false, errors.New("Secondary DNS pagination exceeded 1000 pages")
}

func newSecondaryDNSPeersCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "peers", Short: "Manage Secondary DNS peers"}
	cmd.AddCommand(
		newSecondaryDNSPeersListCmd(g),
		newSecondaryDNSPeersGetCmd(g),
		newSecondaryDNSPeersCreateCmd(g),
		newSecondaryDNSPeersUpdateCmd(g),
		newSecondaryDNSPeersDeleteCmd(g),
	)
	return cmd
}

func newSecondaryDNSPeersListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Secondary DNS peers",
		Long:  "List Secondary DNS peers.\n\nExample:\n\n  cf secondary-dns peers list --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			env, dryRun, err := secondaryDNSListRequest(cmd, g, client, api.Request{Method: "GET", Path: secondaryDNSPeersPath(accountID)})
			if err != nil || dryRun {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var peers []secondaryDNSPeer
			if err := json.Unmarshal(env.Result, &peers); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(peers))
			for _, peer := range peers {
				port := ""
				if peer.Port != nil {
					port = fmt.Sprint(peer.Port)
				}
				ixfr := ""
				if peer.IXFREnable != nil {
					ixfr = strconv.FormatBool(*peer.IXFREnable)
				}
				rows = append(rows, []string{peer.ID, peer.Name, peer.IP, port, peer.TSIGID, ixfr})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "IP", "PORT", "TSIG ID", "IXFR"}, rows)
		},
	}
}

func newSecondaryDNSPeersGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <peer-id>",
		Short: "Show a Secondary DNS peer",
		Long:  "Show a Secondary DNS peer.\n\nExample:\n\n  cf secondary-dns peers get 23ff594956f20c2a721606e94745a8aa",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secondaryDNSRequireIdentifier("peer ID", args[0]); err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "GET", Path: secondaryDNSPeersPath(accountID) + "/" + url.PathEscape(args[0])})
		},
	}
}

func newSecondaryDNSPeersCreateCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Secondary DNS peer",
		Long:  "Create a Secondary DNS peer. Configure its address and transfer settings with update.\n\nExample:\n\n  cf secondary-dns peers create primary-ns --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := secondaryDNSBuildPeerCreateBody(args[0])
			if err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "POST", Path: secondaryDNSPeersPath(accountID), Body: body})
		},
	}
}

type secondaryDNSPeerFlags struct {
	name, ip, tsigID string
	port             int
	ixfrEnable       bool
}

func newSecondaryDNSPeersUpdateCmd(g *globalOpts) *cobra.Command {
	var f secondaryDNSPeerFlags
	cmd := &cobra.Command{
		Use:   "update <peer-id>",
		Short: "Update a Secondary DNS peer",
		Long: `Update selected fields of a Secondary DNS peer.

The API uses a full-object PUT. This command reads the current peer, preserves
unknown writable fields, removes the read-only ID, then applies your flags.
--dry-run performs that required read but never sends the PUT.

Example:

  cf secondary-dns peers update 23ff594956f20c2a721606e94745a8aa --ip 192.0.2.53 --port 53 --ixfr-enable`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secondaryDNSRequireIdentifier("peer ID", args[0]); err != nil {
				return err
			}
			patch, err := secondaryDNSPeerPatchFromFlags(cmd, f)
			if err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			path := secondaryDNSPeersPath(accountID) + "/" + url.PathEscape(args[0])
			cur, err := secondaryDNSReadObject(cmd.Context(), client, path, "peer "+args[0])
			if err != nil {
				return err
			}
			delete(cur, "id")
			next := secondaryDNSMergeObject(cur, patch)
			if err := secondaryDNSValidatePeerObject(next); err != nil {
				return err
			}
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: body})
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "peer name")
	cmd.Flags().StringVar(&f.ip, "ip", "", "primary or secondary nameserver IP address")
	cmd.Flags().IntVar(&f.port, "port", 0, "DNS port")
	cmd.Flags().StringVar(&f.tsigID, "tsig-id", "", "TSIG key ID used for transfers")
	cmd.Flags().BoolVar(&f.ixfrEnable, "ixfr-enable", false, "enable IXFR instead of AXFR for this peer")
	return cmd
}

func newSecondaryDNSPeersDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <peer-id>",
		Short: "Delete a Secondary DNS peer",
		Long:  "Delete a Secondary DNS peer.\n\nExample:\n\n  cf secondary-dns peers delete 23ff594956f20c2a721606e94745a8aa --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secondaryDNSRequireIdentifier("peer ID", args[0]); err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete Secondary DNS peer %s from account %s?", args[0], accountID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "DELETE", Path: secondaryDNSPeersPath(accountID) + "/" + url.PathEscape(args[0])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func secondaryDNSBuildPeerCreateBody(name string) ([]byte, error) {
	if err := secondaryDNSRequireIdentifier("peer name", name); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"name": name})
}

func secondaryDNSPeerPatchFromFlags(cmd *cobra.Command, f secondaryDNSPeerFlags) (map[string]any, error) {
	patch := map[string]any{}
	if cmd.Flags().Changed("name") {
		if err := secondaryDNSRequireIdentifier("peer name", f.name); err != nil {
			return nil, err
		}
		patch["name"] = f.name
	}
	if cmd.Flags().Changed("ip") {
		if err := secondaryDNSValidatePeerIP(f.ip); err != nil {
			return nil, err
		}
		patch["ip"] = f.ip
	}
	if cmd.Flags().Changed("port") {
		patch["port"] = f.port
	}
	if cmd.Flags().Changed("tsig-id") {
		if err := secondaryDNSRequireIdentifier("--tsig-id", f.tsigID); err != nil {
			return nil, err
		}
		patch["tsig_id"] = f.tsigID
	}
	if cmd.Flags().Changed("ixfr-enable") {
		patch["ixfr_enable"] = f.ixfrEnable
	}
	if len(patch) == 0 {
		return nil, errors.New("nothing to update: pass at least one peer field")
	}
	return patch, nil
}

func secondaryDNSValidatePeerObject(obj map[string]any) error {
	name, ok := obj["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("peer is missing name and cannot be updated")
	}
	if value, exists := obj["ip"]; exists {
		ip, ok := value.(string)
		if !ok {
			return errors.New("peer has an invalid IP address and cannot be updated")
		}
		if err := secondaryDNSValidatePeerIP(ip); err != nil {
			return fmt.Errorf("peer has an invalid IP address and cannot be updated: %w", err)
		}
	}
	return nil
}

func secondaryDNSValidatePeerIP(value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New("--ip must not be empty")
	}
	if _, err := netip.ParseAddr(value); err != nil {
		return fmt.Errorf("--ip must be a valid IPv4 or IPv6 address")
	}
	return nil
}

func newSecondaryDNSTSIGsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "tsigs", Short: "Manage Secondary DNS TSIG keys"}
	cmd.AddCommand(
		newSecondaryDNSTSIGsListCmd(g),
		newSecondaryDNSTSIGsGetCmd(g),
		newSecondaryDNSTSIGsCreateCmd(g),
		newSecondaryDNSTSIGsUpdateCmd(g),
		newSecondaryDNSTSIGsDeleteCmd(g),
	)
	return cmd
}

func newSecondaryDNSTSIGsListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Secondary DNS TSIG keys",
		Long:  "List Secondary DNS TSIG keys.\n\nExample:\n\n  cf secondary-dns tsigs list --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			env, dryRun, err := secondaryDNSListRequest(cmd, g, client, api.Request{Method: "GET", Path: secondaryDNSTSIGsPath(accountID)})
			if err != nil || dryRun {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var tsigs []secondaryDNSTSIG
			if err := json.Unmarshal(env.Result, &tsigs); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(tsigs))
			for _, tsig := range tsigs {
				rows = append(rows, []string{tsig.ID, tsig.Name, tsig.Algo})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "ALGORITHM"}, rows)
		},
	}
}

func newSecondaryDNSTSIGsGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <tsig-id>",
		Short: "Show a Secondary DNS TSIG key",
		Long:  "Show a Secondary DNS TSIG key.\n\nExample:\n\n  cf secondary-dns tsigs get 69cd1e104af3e6ed3cb344f263fd0d5a",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secondaryDNSRequireIdentifier("TSIG ID", args[0]); err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "GET", Path: secondaryDNSTSIGsPath(accountID) + "/" + url.PathEscape(args[0])})
		},
	}
}

type secondaryDNSTSIGFlags struct{ name, secret, algo string }

func newSecondaryDNSTSIGsCreateCmd(g *globalOpts) *cobra.Command {
	var f secondaryDNSTSIGFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Secondary DNS TSIG key",
		Long:  "Create a Secondary DNS TSIG key.\n\nExample:\n\n  cf secondary-dns tsigs create tsig.customer.cf. --secret \"$TSIG_SECRET\" --algo hmac-sha512.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.name = args[0]
			body, err := secondaryDNSBuildTSIGCreateBody(f)
			if err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "POST", Path: secondaryDNSTSIGsPath(accountID), Body: body})
		},
	}
	cmd.Flags().StringVar(&f.secret, "secret", "", "TSIG secret")
	cmd.Flags().StringVar(&f.algo, "algo", "", "TSIG algorithm")
	_ = cmd.MarkFlagRequired("secret")
	_ = cmd.MarkFlagRequired("algo")
	return cmd
}

func newSecondaryDNSTSIGsUpdateCmd(g *globalOpts) *cobra.Command {
	var f secondaryDNSTSIGFlags
	cmd := &cobra.Command{
		Use:   "update <tsig-id>",
		Short: "Update a Secondary DNS TSIG key",
		Long: `Update selected fields of a Secondary DNS TSIG key.

The API uses a full-object PUT. This command reads the current key, preserves
unknown writable fields, removes its read-only ID, then applies your flags.
--dry-run performs that required read but never sends the PUT.

Example:

  cf secondary-dns tsigs update 69cd1e104af3e6ed3cb344f263fd0d5a --secret "$NEW_TSIG_SECRET"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secondaryDNSRequireIdentifier("TSIG ID", args[0]); err != nil {
				return err
			}
			patch, err := secondaryDNSTSIGPatchFromFlags(cmd, f)
			if err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			path := secondaryDNSTSIGsPath(accountID) + "/" + url.PathEscape(args[0])
			cur, err := secondaryDNSReadObject(cmd.Context(), client, path, "TSIG key "+args[0])
			if err != nil {
				return err
			}
			delete(cur, "id")
			next := secondaryDNSMergeObject(cur, patch)
			if err := secondaryDNSValidateTSIGObject(next); err != nil {
				return err
			}
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: body})
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "TSIG key name")
	cmd.Flags().StringVar(&f.secret, "secret", "", "TSIG secret")
	cmd.Flags().StringVar(&f.algo, "algo", "", "TSIG algorithm")
	return cmd
}

func newSecondaryDNSTSIGsDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <tsig-id>",
		Short: "Delete a Secondary DNS TSIG key",
		Long:  "Delete a Secondary DNS TSIG key.\n\nExample:\n\n  cf secondary-dns tsigs delete 69cd1e104af3e6ed3cb344f263fd0d5a --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := secondaryDNSRequireIdentifier("TSIG ID", args[0]); err != nil {
				return err
			}
			client, accountID, err := secondaryDNSAccountClient(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete Secondary DNS TSIG key %s from account %s?", args[0], accountID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "DELETE", Path: secondaryDNSTSIGsPath(accountID) + "/" + url.PathEscape(args[0])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func secondaryDNSBuildTSIGCreateBody(f secondaryDNSTSIGFlags) ([]byte, error) {
	if err := secondaryDNSValidateTSIGFields(f.name, f.secret, f.algo); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"name": f.name, "secret": f.secret, "algo": f.algo})
}

func secondaryDNSTSIGPatchFromFlags(cmd *cobra.Command, f secondaryDNSTSIGFlags) (map[string]any, error) {
	patch := map[string]any{}
	if cmd.Flags().Changed("name") {
		if err := secondaryDNSRequireIdentifier("TSIG key name", f.name); err != nil {
			return nil, err
		}
		patch["name"] = f.name
	}
	if cmd.Flags().Changed("secret") {
		if err := secondaryDNSRequireIdentifier("TSIG secret", f.secret); err != nil {
			return nil, err
		}
		patch["secret"] = f.secret
	}
	if cmd.Flags().Changed("algo") {
		if err := secondaryDNSRequireIdentifier("TSIG algorithm", f.algo); err != nil {
			return nil, err
		}
		patch["algo"] = f.algo
	}
	if len(patch) == 0 {
		return nil, errors.New("nothing to update: pass at least one TSIG field")
	}
	return patch, nil
}

func secondaryDNSValidateTSIGFields(name, secret, algo string) error {
	if err := secondaryDNSRequireIdentifier("TSIG key name", name); err != nil {
		return err
	}
	if err := secondaryDNSRequireIdentifier("TSIG secret", secret); err != nil {
		return err
	}
	return secondaryDNSRequireIdentifier("TSIG algorithm", algo)
}

func secondaryDNSValidateTSIGObject(obj map[string]any) error {
	name, nameOK := obj["name"].(string)
	secret, secretOK := obj["secret"].(string)
	algo, algoOK := obj["algo"].(string)
	if !nameOK || !secretOK || !algoOK {
		return errors.New("TSIG key is missing name, secret, or algo and cannot be updated")
	}
	return secondaryDNSValidateTSIGFields(name, secret, algo)
}

func newSecondaryDNSIncomingCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "incoming",
		Short: "Manage incoming Secondary DNS zone transfers",
		Long:  "Manage a secondary zone's incoming AXFR/IXFR configuration. All commands accept --zone; a zone name is resolved during --dry-run when needed.",
	}
	cmd.AddCommand(
		newSecondaryDNSConfigGetCmd(g, "incoming"),
		newSecondaryDNSConfigCreateCmd(g, "incoming"),
		newSecondaryDNSConfigUpdateCmd(g, "incoming"),
		newSecondaryDNSConfigDeleteCmd(g, "incoming"),
		newSecondaryDNSActionCmd(g, "force-axfr", "Force an incoming AXFR from the configured primary", "/force_axfr"),
	)
	return cmd
}

func newSecondaryDNSOutgoingCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "outgoing",
		Short: "Manage outgoing Secondary DNS zone transfers",
		Long:  "Manage a primary zone's outgoing transfer configuration. All commands accept --zone; a zone name is resolved during --dry-run when needed.",
	}
	cmd.AddCommand(
		newSecondaryDNSConfigGetCmd(g, "outgoing"),
		newSecondaryDNSConfigCreateCmd(g, "outgoing"),
		newSecondaryDNSConfigUpdateCmd(g, "outgoing"),
		newSecondaryDNSConfigDeleteCmd(g, "outgoing"),
		newSecondaryDNSActionCmd(g, "status", "Show outgoing zone transfer status", "/outgoing/status"),
		newSecondaryDNSActionCmd(g, "enable", "Enable outgoing zone transfers", "/outgoing/enable"),
		newSecondaryDNSActionCmd(g, "disable", "Disable outgoing zone transfers", "/outgoing/disable"),
		newSecondaryDNSActionCmd(g, "notify", "Force DNS NOTIFY to configured secondary peers", "/outgoing/force_notify"),
	)
	return cmd
}

func secondaryDNSConfigPath(zoneID, direction string) string {
	return secondaryDNSZonePath(zoneID) + "/" + direction
}

func newSecondaryDNSConfigGetCmd(g *globalOpts, direction string) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show " + direction + " zone transfer configuration",
		Long:  "Show " + direction + " zone transfer configuration.\n\nExample:\n\n  cf secondary-dns " + direction + " get --zone example.com",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, zoneID, err := secondaryDNSZoneClient(cmd, g, zone)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "GET", Path: secondaryDNSConfigPath(zoneID, direction)})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

type secondaryDNSConfigFlags struct {
	name               string
	peers              []string
	autoRefreshSeconds int
}

func newSecondaryDNSConfigCreateCmd(g *globalOpts, direction string) *cobra.Command {
	var zone string
	var f secondaryDNSConfigFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create " + direction + " zone transfer configuration",
		Long:  "Create " + direction + " zone transfer configuration.\n\nExample:\n\n  cf secondary-dns " + direction + " create --zone example.com --name example.com. --peer 23ff594956f20c2a721606e94745a8aa",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := secondaryDNSBuildConfigCreateBody(direction, f)
			if err != nil {
				return err
			}
			client, zoneID, err := secondaryDNSZoneClient(cmd, g, zone)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "POST", Path: secondaryDNSConfigPath(zoneID, direction), Body: body})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	secondaryDNSBindConfigFlags(cmd, &f, direction == "incoming", true)
	return cmd
}

func newSecondaryDNSConfigUpdateCmd(g *globalOpts, direction string) *cobra.Command {
	var zone string
	var f secondaryDNSConfigFlags
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update " + direction + " zone transfer configuration",
		Long: `Update selected fields of ` + direction + ` zone transfer configuration.

The API uses a full-object PUT. This command reads the current configuration,
preserves unknown writable fields, removes known read-only fields, and applies
your flags. --dry-run performs that required read but never sends the PUT.

Example:

  cf secondary-dns ` + direction + ` update --zone example.com --peer 23ff594956f20c2a721606e94745a8aa`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			patch, err := secondaryDNSConfigPatchFromFlags(cmd, direction, f)
			if err != nil {
				return err
			}
			client, zoneID, err := secondaryDNSZoneClient(cmd, g, zone)
			if err != nil {
				return err
			}
			path := secondaryDNSConfigPath(zoneID, direction)
			cur, err := secondaryDNSReadObject(cmd.Context(), client, path, direction+" zone transfer configuration")
			if err != nil {
				return err
			}
			secondaryDNSStripConfigReadOnly(cur, direction)
			next := secondaryDNSMergeObject(cur, patch)
			if err := secondaryDNSValidateConfigObject(next, direction); err != nil {
				return err
			}
			body, err := json.Marshal(next)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: body})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	secondaryDNSBindConfigFlags(cmd, &f, direction == "incoming", false)
	return cmd
}

func newSecondaryDNSConfigDeleteCmd(g *globalOpts, direction string) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete " + direction + " zone transfer configuration",
		Long:  "Delete " + direction + " zone transfer configuration.\n\nExample:\n\n  cf secondary-dns " + direction + " delete --zone example.com --force",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, zoneID, err := secondaryDNSZoneClient(cmd, g, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete %s zone transfer configuration for zone %s?", direction, zoneID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: "DELETE", Path: secondaryDNSConfigPath(zoneID, direction)})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newSecondaryDNSActionCmd(g *globalOpts, use, short, suffix string) *cobra.Command {
	var zone string
	method := "POST"
	if use == "status" {
		method = "GET"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  short + ".\n\nExample:\n\n  cf secondary-dns " + map[bool]string{true: "incoming", false: "outgoing"}[use == "force-axfr"] + " " + use + " --zone example.com",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, zoneID, err := secondaryDNSZoneClient(cmd, g, zone)
			if err != nil {
				return err
			}
			return runSecondaryDNSRequest(cmd, g, client, api.Request{Method: method, Path: secondaryDNSZonePath(zoneID) + suffix})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func secondaryDNSBindConfigFlags(cmd *cobra.Command, f *secondaryDNSConfigFlags, incoming, create bool) {
	flags := cmd.Flags()
	flags.StringVar(&f.name, "name", "", "zone name used by the Secondary DNS configuration")
	flags.StringSliceVar(&f.peers, "peer", []string{}, "peer ID (repeat to configure multiple peers)")
	if incoming {
		flags.IntVar(&f.autoRefreshSeconds, "auto-refresh-seconds", secondaryDNSDefaultAutoRefreshSeconds, "refresh interval in seconds (minimum 300)")
	}
	if create {
		_ = cmd.MarkFlagRequired("name")
	}
}

func secondaryDNSBuildConfigCreateBody(direction string, f secondaryDNSConfigFlags) ([]byte, error) {
	if err := secondaryDNSRequireIdentifier("zone name", f.name); err != nil {
		return nil, err
	}
	if err := secondaryDNSValidatePeers(f.peers); err != nil {
		return nil, err
	}
	body := map[string]any{"name": f.name, "peers": f.peers}
	if direction == "incoming" {
		if err := secondaryDNSValidateAutoRefresh(f.autoRefreshSeconds); err != nil {
			return nil, err
		}
		body["auto_refresh_seconds"] = f.autoRefreshSeconds
	}
	return json.Marshal(body)
}

func secondaryDNSConfigPatchFromFlags(cmd *cobra.Command, direction string, f secondaryDNSConfigFlags) (map[string]any, error) {
	patch := map[string]any{}
	if cmd.Flags().Changed("name") {
		if err := secondaryDNSRequireIdentifier("zone name", f.name); err != nil {
			return nil, err
		}
		patch["name"] = f.name
	}
	if cmd.Flags().Changed("peer") {
		if err := secondaryDNSValidatePeers(f.peers); err != nil {
			return nil, err
		}
		patch["peers"] = f.peers
	}
	if direction == "incoming" && cmd.Flags().Changed("auto-refresh-seconds") {
		if err := secondaryDNSValidateAutoRefresh(f.autoRefreshSeconds); err != nil {
			return nil, err
		}
		patch["auto_refresh_seconds"] = f.autoRefreshSeconds
	}
	if len(patch) == 0 {
		return nil, errors.New("nothing to update: pass at least one configuration field")
	}
	return patch, nil
}

func secondaryDNSValidatePeers(peers []string) error {
	for _, peer := range peers {
		if err := secondaryDNSRequireIdentifier("peer ID", peer); err != nil {
			return err
		}
	}
	return nil
}

func secondaryDNSValidateAutoRefresh(seconds int) error {
	if seconds < 300 {
		return errors.New("--auto-refresh-seconds must be at least 300")
	}
	return nil
}

func secondaryDNSStripConfigReadOnly(obj map[string]any, direction string) {
	delete(obj, "id")
	delete(obj, "checked_time")
	delete(obj, "created_time")
	delete(obj, "modified_time")
	delete(obj, "soa_serial")
	if direction == "outgoing" {
		delete(obj, "last_transferred_time")
	}
}

func secondaryDNSValidateConfigObject(obj map[string]any, direction string) error {
	name, ok := obj["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("zone transfer configuration is missing name and cannot be updated")
	}
	if err := secondaryDNSValidatePeerValue(obj["peers"]); err != nil {
		return err
	}
	if direction == "incoming" {
		value, ok := obj["auto_refresh_seconds"]
		if !ok {
			return errors.New("incoming configuration is missing auto_refresh_seconds and cannot be updated")
		}
		seconds, ok := secondaryDNSInt(value)
		if !ok {
			return errors.New("incoming configuration has an invalid auto_refresh_seconds")
		}
		return secondaryDNSValidateAutoRefresh(seconds)
	}
	return nil
}

func secondaryDNSValidatePeerValue(value any) error {
	switch peers := value.(type) {
	case []string:
		return secondaryDNSValidatePeers(peers)
	case []any:
		peerIDs := make([]string, len(peers))
		for i, peer := range peers {
			id, ok := peer.(string)
			if !ok {
				return errors.New("zone transfer configuration has an invalid peer ID")
			}
			peerIDs[i] = id
		}
		return secondaryDNSValidatePeers(peerIDs)
	default:
		return errors.New("zone transfer configuration is missing peers and cannot be updated")
	}
}

func secondaryDNSInt(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), int64(int(n)) == n
	case float64:
		return int(n), n == float64(int(n))
	case json.Number:
		parsed, err := n.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	default:
		return 0, false
	}
}

func secondaryDNSReadObject(ctx context.Context, client *api.Client, path, label string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s before update: %w", label, err)
	}
	var obj map[string]any
	if err := json.Unmarshal(env.Result, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	return obj, nil
}

func secondaryDNSMergeObject(base, patch map[string]any) map[string]any {
	next := make(map[string]any, len(base)+len(patch))
	for key, value := range base {
		next[key] = value
	}
	for key, value := range patch {
		next[key] = value
	}
	return next
}
