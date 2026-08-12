package cli

// Addressing porcelain covers the common BYOIP and Address Map workflows.
// LOA uploads and downloads stream through api.Client.DoStream: documents can
// be up to 10 MiB, and downloads are raw PDF bytes rather than JSON.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

const addressingIDMaxLength = 32
const addressingLOADocumentMaxBytes = 10 << 20

func newAddressingCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "addressing", Short: "Manage BYOIP prefixes, Address Maps, and LOA documents"}
	cmd.AddCommand(newAddressingPrefixCmd(g), newAddressingAdvertisementCmd(g), newAddressingMapCmd(g), newAddressingLOADocumentCmd(g))
	return cmd
}

func addressingBasePath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/addressing"
}

func addressingPrefixesPath(accountID string) string {
	return addressingBasePath(accountID) + "/prefixes"
}
func addressingPrefixPath(accountID, prefixID string) string {
	return addressingPrefixesPath(accountID) + "/" + url.PathEscape(prefixID)
}
func addressingAdvertisementPath(accountID, prefixID string) string {
	return addressingPrefixPath(accountID, prefixID) + "/bgp/status"
}
func addressingMapsPath(accountID string) string {
	return addressingBasePath(accountID) + "/address_maps"
}
func addressingMapPath(accountID, mapID string) string {
	return addressingMapsPath(accountID) + "/" + url.PathEscape(mapID)
}
func addressingLOADocumentsPath(accountID string) string {
	return addressingBasePath(accountID) + "/loa_documents"
}
func addressingLOADocumentDownloadPath(accountID, documentID string) string {
	return addressingLOADocumentsPath(accountID) + "/" + url.PathEscape(documentID) + "/download"
}

func addressingAccountID(accountID string) (string, error) {
	return addressingIdentifier("account ID", accountID)
}

// addressingIdentifier validates the documented maximum length in Unicode code
// points for IDs used in Addressing paths. It deliberately does not assume IDs
// are hex; url.PathEscape safely encodes every path value.
func addressingIdentifier(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s cannot be empty", name)
	}
	if utf8.RuneCountInString(value) > addressingIDMaxLength {
		return "", fmt.Errorf("%s must be at most %d characters", name, addressingIDMaxLength)
	}
	return value, nil
}

func addressingIP(value string) (string, error) {
	value = strings.TrimSpace(value)
	if _, err := netip.ParseAddr(value); err != nil {
		return "", fmt.Errorf("--ip must be an IPv4 or IPv6 address: %q", value)
	}
	return value, nil
}

func addressingClient(g *globalOpts) (*api.Client, string, error) {
	client, cfg, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	accountID, err := addressingAccountID(cfg.AccountID)
	if err != nil {
		return nil, "", err
	}
	return client, accountID, nil
}

func runAddressingRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// Prefixes -----------------------------------------------------------------

type addressingPrefix struct {
	ID       string `json:"id"`
	CIDR     string `json:"cidr"`
	ASN      int64  `json:"asn"`
	Approved string `json:"approved"`
}

func newAddressingPrefixCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "prefix", Short: "Inspect BYOIP prefixes"}
	cmd.AddCommand(newAddressingPrefixListCmd(g), newAddressingPrefixGetCmd(g))
	return cmd
}

func newAddressingPrefixListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List BYOIP prefixes",
		Long:  "List BYOIP prefixes owned by the account.\n\nExample:\n\n  cf addressing prefix list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: addressingPrefixesPath(accountID)}
			if g.DryRun {
				return runAddressingRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var prefixes []addressingPrefix
			if err := json.Unmarshal(env.Result, &prefixes); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(prefixes))
			for _, prefix := range prefixes {
				rows = append(rows, []string{prefix.ID, prefix.CIDR, fmt.Sprint(prefix.ASN), prefix.Approved})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "CIDR", "ASN", "APPROVED"}, rows)
		},
	}
}

func newAddressingPrefixGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <prefix-id>",
		Short: "Show a BYOIP prefix",
		Long:  "Show the full status of one BYOIP prefix.\n\nExample:\n\n  cf addressing prefix get 0123456789abcdef0123456789abcdef",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefixID, err := addressingIdentifier("prefix ID", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			return runAddressingRequest(cmd, g, client, api.Request{Method: "GET", Path: addressingPrefixPath(accountID, prefixID)})
		},
	}
}

// Advertisement ------------------------------------------------------------

func newAddressingAdvertisementCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "advertisement", Short: "View or change a prefix advertisement"}
	cmd.AddCommand(newAddressingAdvertisementGetCmd(g), newAddressingAdvertisementSetCmd(g))
	return cmd
}

func newAddressingAdvertisementGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <prefix-id>",
		Short: "Show dynamic advertisement status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefixID, err := addressingIdentifier("prefix ID", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			return runAddressingRequest(cmd, g, client, api.Request{Method: "GET", Path: addressingAdvertisementPath(accountID, prefixID)})
		},
	}
}

func newAddressingAdvertisementSetCmd(g *globalOpts) *cobra.Command {
	var advertised bool
	cmd := &cobra.Command{
		Use:   "set <prefix-id>",
		Short: "Advertise or withdraw a prefix",
		Long: `Set the legacy dynamic advertisement status for a BYOIP prefix.
True advertises the BGP route to the Internet; false withdraws it.

Examples:

  cf addressing advertisement set <prefix-id> --advertised=true
  cf addressing advertisement set <prefix-id> --advertised=false`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prefixID, err := addressingIdentifier("prefix ID", args[0])
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("advertised") {
				return errors.New("missing --advertised=true or --advertised=false")
			}
			body, err := json.Marshal(map[string]bool{"advertised": advertised})
			if err != nil {
				return err
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			return runAddressingRequest(cmd, g, client, api.Request{Method: "PATCH", Path: addressingAdvertisementPath(accountID, prefixID), Body: body})
		},
	}
	cmd.Flags().BoolVar(&advertised, "advertised", false, "advertise the prefix (false withdraws it)")
	return cmd
}

// Address Maps -------------------------------------------------------------

type addressingMap struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
}

func newAddressingMapCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "map", Short: "Manage Address Maps and their memberships"}
	cmd.AddCommand(newAddressingMapListCmd(g), newAddressingMapGetCmd(g), newAddressingMapCreateCmd(g), newAddressingMapUpdateCmd(g), newAddressingMapDeleteCmd(g), newAddressingMapIPCmd(g), newAddressingMapAccountCmd(g), newAddressingMapZoneCmd(g))
	return cmd
}

func newAddressingMapListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Address Maps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: addressingMapsPath(accountID)}
			if g.DryRun {
				return runAddressingRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var maps []addressingMap
			if err := json.Unmarshal(env.Result, &maps); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(maps))
			for _, m := range maps {
				enabled := ""
				if m.Enabled != nil {
					enabled = fmt.Sprint(*m.Enabled)
				}
				rows = append(rows, []string{m.ID, output.Cell(m.Description), enabled})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "DESCRIPTION", "ENABLED"}, rows)
		},
	}
}

func newAddressingMapGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <map-id>",
		Short: "Show an Address Map",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapID, err := addressingIdentifier("Address Map ID", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			return runAddressingRequest(cmd, g, client, api.Request{Method: "GET", Path: addressingMapPath(accountID, mapID)})
		},
	}
}

type addressingMapCreateOptions struct {
	Description string
	Enabled     bool
	EnabledSet  bool
	IPs         []string
	Accounts    []string
}

func buildAddressingMapCreateBody(o addressingMapCreateOptions, zones []string) ([]byte, error) {
	body := map[string]any{}
	if o.Description != "" {
		body["description"] = o.Description
	}
	if o.EnabledSet {
		body["enabled"] = o.Enabled
	}
	if len(o.IPs) > 0 {
		ips := make([]string, 0, len(o.IPs))
		for _, value := range o.IPs {
			ip, err := addressingIP(value)
			if err != nil {
				return nil, err
			}
			ips = append(ips, ip)
		}
		body["ips"] = ips
	}
	memberships := make([]map[string]string, 0, len(o.Accounts)+len(zones))
	for _, value := range o.Accounts {
		id, err := addressingIdentifier("member account ID", value)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, map[string]string{"identifier": id, "kind": "account"})
	}
	for _, zoneID := range zones {
		memberships = append(memberships, map[string]string{"identifier": zoneID, "kind": "zone"})
	}
	if len(memberships) > 0 {
		body["memberships"] = memberships
	}
	return json.Marshal(body)
}

func newAddressingMapCreateCmd(g *globalOpts) *cobra.Command {
	var o addressingMapCreateOptions
	var zones []string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an Address Map",
		Long: `Create an Address Map. --zone accepts a zone name or ID and is resolved
before the request; dry-run may read zones when a name is used.

Examples:

  cf addressing map create --description "edge addresses" --ip 192.0.2.1 --zone example.com
  cf addressing map create --enabled --account 0123456789abcdef0123456789abcdef`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o.EnabledSet = cmd.Flags().Changed("enabled")
			// Validate every entirely local input before config/client work.
			if _, err := buildAddressingMapCreateBody(o, nil); err != nil {
				return err
			}
			for _, zone := range zones {
				if strings.TrimSpace(zone) == "" {
					return errors.New("--zone cannot be empty")
				}
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := addressingAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			zoneIDs := make([]string, 0, len(zones))
			for _, zone := range zones {
				zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
				if err != nil {
					return err
				}
				zoneIDs = append(zoneIDs, zoneID)
			}
			body, err := buildAddressingMapCreateBody(o, zoneIDs)
			if err != nil {
				return err
			}
			return runAddressingRequest(cmd, g, client, api.Request{Method: "POST", Path: addressingMapsPath(accountID), Body: body})
		},
	}
	cmd.Flags().StringVar(&o.Description, "description", "", "optional description")
	cmd.Flags().BoolVar(&o.Enabled, "enabled", false, "enable the Address Map")
	cmd.Flags().StringSliceVar(&o.IPs, "ip", nil, "IP address to add (repeatable)")
	cmd.Flags().StringSliceVar(&o.Accounts, "account", nil, "account ID to add as a member (repeatable)")
	cmd.Flags().StringSliceVar(&zones, "zone", nil, "zone name or ID to add as a member (repeatable)")
	return cmd
}

type addressingMapUpdateOptions struct {
	DefaultSNI     string
	Description    string
	Enabled        bool
	DefaultSet     bool
	DescriptionSet bool
	EnabledSet     bool
}

func buildAddressingMapUpdateBody(o addressingMapUpdateOptions) ([]byte, error) {
	next := map[string]any{}
	if o.DefaultSet {
		next["default_sni"] = o.DefaultSNI
	}
	if o.DescriptionSet {
		next["description"] = o.Description
	}
	if o.EnabledSet {
		next["enabled"] = o.Enabled
	}
	return json.Marshal(next)
}

func newAddressingMapUpdateCmd(g *globalOpts) *cobra.Command {
	var o addressingMapUpdateOptions
	cmd := &cobra.Command{
		Use:   "update <map-id>",
		Short: "Update an Address Map",
		Long: `Update Address Map properties. Only the fields explicitly passed are
sent in the API's partial PATCH request, so --dry-run is read-free.

Example:

  cf addressing map update <map-id> --enabled=false --default-sni legacy.example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapID, err := addressingIdentifier("Address Map ID", args[0])
			if err != nil {
				return err
			}
			o.DefaultSet = cmd.Flags().Changed("default-sni")
			o.DescriptionSet = cmd.Flags().Changed("description")
			o.EnabledSet = cmd.Flags().Changed("enabled")
			if !o.DefaultSet && !o.DescriptionSet && !o.EnabledSet {
				return errors.New("nothing to update: pass --default-sni, --description, or --enabled")
			}
			body, err := buildAddressingMapUpdateBody(o)
			if err != nil {
				return err
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			path := addressingMapPath(accountID, mapID)
			return runAddressingRequest(cmd, g, client, api.Request{Method: "PATCH", Path: path, Body: body})
		},
	}
	cmd.Flags().StringVar(&o.DefaultSNI, "default-sni", "", "default SNI for clients without SNI")
	cmd.Flags().StringVar(&o.Description, "description", "", "optional description")
	cmd.Flags().BoolVar(&o.Enabled, "enabled", false, "enable the Address Map")
	return cmd
}

func newAddressingMapDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <map-id>",
		Short: "Delete an Address Map",
		Long:  "Delete an Address Map. Address Maps no longer serve their assigned IPs.\n\nExample:\n\n  cf addressing map delete <map-id> --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapID, err := addressingIdentifier("Address Map ID", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete Address Map %s?", mapID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runAddressingRequest(cmd, g, client, api.Request{Method: "DELETE", Path: addressingMapPath(accountID, mapID)})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newAddressingMapIPCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "ip", Short: "Manage IP membership of an Address Map"}
	cmd.AddCommand(newAddressingMapIPMembershipCmd(g, "add", "PUT"), newAddressingMapIPMembershipCmd(g, "remove", "DELETE"))
	return cmd
}

func newAddressingMapIPMembershipCmd(g *globalOpts, verb, method string) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   verb + " <map-id> <ip-address>",
		Short: strings.Title(verb) + " an IP " + map[bool]string{true: "to", false: "from"}[verb == "add"] + " an Address Map",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapID, err := addressingIdentifier("Address Map ID", args[0])
			if err != nil {
				return err
			}
			ip, err := addressingIP(args[1])
			if err != nil {
				return err
			}
			if verb == "remove" && !force && !g.DryRun && !confirm(fmt.Sprintf("Remove IP %s from Address Map %s?", ip, mapID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			path := addressingMapPath(accountID, mapID) + "/ips/" + url.PathEscape(ip)
			return runAddressingRequest(cmd, g, client, api.Request{Method: method, Path: path})
		},
	}
	if verb == "remove" {
		cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	}
	return cmd
}

func newAddressingMapAccountCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "account", Short: "Manage account membership of an Address Map"}
	cmd.AddCommand(newAddressingMapAccountMembershipCmd(g, "add", "PUT"), newAddressingMapAccountMembershipCmd(g, "remove", "DELETE"))
	return cmd
}

func newAddressingMapAccountMembershipCmd(g *globalOpts, verb, method string) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   verb + " <map-id> <member-account-id>",
		Short: strings.Title(verb) + " an account " + map[bool]string{true: "to", false: "from"}[verb == "add"] + " an Address Map",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapID, err := addressingIdentifier("Address Map ID", args[0])
			if err != nil {
				return err
			}
			memberID, err := addressingIdentifier("member account ID", args[1])
			if err != nil {
				return err
			}
			if verb == "remove" && !force && !g.DryRun && !confirm(fmt.Sprintf("Remove account %s from Address Map %s?", memberID, mapID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			path := addressingMapPath(accountID, mapID) + "/accounts/" + url.PathEscape(memberID)
			return runAddressingRequest(cmd, g, client, api.Request{Method: method, Path: path})
		},
	}
	if verb == "remove" {
		cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	}
	return cmd
}

func newAddressingMapZoneCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "zone", Short: "Manage zone membership of an Address Map"}
	cmd.AddCommand(newAddressingMapZoneMembershipCmd(g, "add", "PUT"), newAddressingMapZoneMembershipCmd(g, "remove", "DELETE"))
	return cmd
}

func newAddressingMapZoneMembershipCmd(g *globalOpts, verb, method string) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   verb + " <map-id>",
		Short: strings.Title(verb) + " a zone " + map[bool]string{true: "to", false: "from"}[verb == "add"] + " an Address Map",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mapID, err := addressingIdentifier("Address Map ID", args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(zone) == "" {
				return errors.New("missing --zone")
			}
			if verb == "remove" && !force && !g.DryRun && !confirm(fmt.Sprintf("Remove zone %s from Address Map %s?", zone, mapID)) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := addressingAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			path := addressingMapPath(accountID, mapID) + "/zones/" + url.PathEscape(zoneID)
			return runAddressingRequest(cmd, g, client, api.Request{Method: method, Path: path})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID")
	if verb == "remove" {
		cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	}
	return cmd
}

// LOA Documents ------------------------------------------------------------

type addressingLOADocumentUpload struct{ FilePath string }

func buildAddressingLOADocumentUpload(file string) (addressingLOADocumentUpload, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return addressingLOADocumentUpload{}, errors.New("missing --file")
	}
	info, err := os.Stat(file)
	if err != nil {
		return addressingLOADocumentUpload{}, fmt.Errorf("read --file: %w", err)
	}
	if info.IsDir() {
		return addressingLOADocumentUpload{}, fmt.Errorf("--file %q is a directory", file)
	}
	if info.Size() > addressingLOADocumentMaxBytes {
		return addressingLOADocumentUpload{}, errors.New("--file must not exceed 10 MiB")
	}
	if !strings.EqualFold(filepath.Ext(file), ".pdf") {
		return addressingLOADocumentUpload{}, errors.New("--file must be a PDF document")
	}
	f, err := os.Open(file)
	if err != nil {
		return addressingLOADocumentUpload{}, fmt.Errorf("open --file: %w", err)
	}
	var signature [5]byte
	if _, err := io.ReadFull(f, signature[:]); err != nil || string(signature[:]) != "%PDF-" {
		_ = f.Close()
		return addressingLOADocumentUpload{}, errors.New("--file must begin with the PDF signature %PDF-")
	}
	if err := f.Close(); err != nil {
		return addressingLOADocumentUpload{}, fmt.Errorf("close --file: %w", err)
	}
	return addressingLOADocumentUpload{FilePath: file}, nil
}

func writeAddressingLOADocumentPart(writer *multipart.Writer, filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open --file: %w", err)
	}
	defer file.Close()
	part, err := writer.CreateFormFile("loa_document", filepath.Base(filePath))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)
	return err
}

// buildAddressingLOADocumentFixture is deterministic for a useful --dry-run
// result. The live pipe uses the same part writer below.
func buildAddressingLOADocumentFixture(filePath string) ([]byte, string, error) {
	const boundary = "cf-cli-addressing-loa-dry-run"
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, "", err
	}
	if err := writeAddressingLOADocumentPart(writer, filePath); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func newAddressingLOADocumentStream(filePath string) (io.Reader, string) {
	reader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	go func() {
		writeErr := writeAddressingLOADocumentPart(writer, filePath)
		if err := writer.Close(); writeErr == nil {
			writeErr = err
		}
		_ = pipeWriter.CloseWithError(writeErr)
	}()
	return reader, writer.FormDataContentType()
}

func newAddressingLOADocumentCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "loa", Short: "Upload and download LOA documents"}
	cmd.AddCommand(newAddressingLOADocumentUploadCmd(g), newAddressingLOADocumentDownloadCmd(g))
	return cmd
}

func newAddressingLOADocumentUploadCmd(g *globalOpts) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload a PDF LOA document",
		Long: `Upload a Letter of Authorization PDF. The API accepts PDF files up to
10 MiB. Dry-run uses a deterministic multipart body; live upload streams the
same multipart structure without buffering the document.

Example:

  cf addressing loa upload --file ./letter-of-authorization.pdf`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			upload, err := buildAddressingLOADocumentUpload(file)
			if err != nil {
				return err
			}
			if g.DryRun {
				body, contentType, err := buildAddressingLOADocumentFixture(upload.FilePath)
				if err != nil {
					return err
				}
				client, accountID, err := addressingClient(g)
				if err != nil {
					return err
				}
				return runAddressingRequest(cmd, g, client, api.Request{Method: "POST", Path: addressingLOADocumentsPath(accountID), Body: body, ContentType: contentType})
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			body, contentType := newAddressingLOADocumentStream(upload.FilePath)
			resp, err := client.DoStream(cmd.Context(), api.Request{Method: "POST", Path: addressingLOADocumentsPath(accountID), ContentType: contentType}, body)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			var env api.Envelope
			if err := json.Unmarshal(raw, &env); err != nil || env.Result == nil {
				return g.renderResult(cmd, raw, output.JSON)
			}
			if !env.Success {
				return &api.APIError{StatusCode: resp.StatusCode, Errors: env.Errors, RawBody: string(raw)}
			}
			return g.renderResult(cmd, env.Result, output.JSON)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "PDF LOA document to upload (maximum 10 MiB)")
	return cmd
}

func newAddressingLOADocumentDownloadCmd(g *globalOpts) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "download <loa-document-id>",
		Short: "Download a LOA document",
		Long: `Download a LOA document as raw PDF bytes. --output and --query do not
apply because this endpoint does not return JSON. Use --file to save the PDF;
an existing file is overwritten.

Example:

  cf addressing loa download <loa-document-id> --file ./letter.pdf`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			documentID, err := addressingIdentifier("LOA document ID", args[0])
			if err != nil {
				return err
			}
			if g.Query != "" {
				return errors.New("--query is not supported for LOA download: the response is raw PDF content, not JSON")
			}
			if g.Output != "" && !g.DryRun {
				return errors.New("--output is not supported for LOA download: the response is raw PDF content, not JSON")
			}
			if cmd.Flags().Changed("file") && strings.TrimSpace(file) == "" {
				return errors.New("--file path cannot be empty")
			}
			client, accountID, err := addressingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: addressingLOADocumentDownloadPath(accountID, documentID)}
			if g.DryRun {
				return runAddressingRequest(cmd, g, client, req)
			}
			resp, err := client.DoStream(cmd.Context(), req, nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			dest := strings.TrimSpace(file)
			if dest == "" {
				_, err := io.Copy(cmd.OutOrStdout(), resp.Body)
				return err
			}
			f, err := os.Create(dest)
			if err != nil {
				return fmt.Errorf("write --file: %w", err)
			}
			if _, err := io.Copy(f, resp.Body); err != nil {
				_ = f.Close()
				return fmt.Errorf("write --file %q: %w", dest, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("write --file %q: %w", dest, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "write the PDF to this file instead of stdout")
	return cmd
}
