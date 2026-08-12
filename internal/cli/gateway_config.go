package cli

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

// Gateway configuration porcelain covers the operational settings, DNS
// locations, and TLS-interception certificate workflows most commonly changed
// from a terminal. Less-common Gateway endpoints remain available via cf api.

type gatewayConfigLocationFlags struct {
	name                string
	networks            []string
	dnsDestinationIPsID string
	clientDefault       bool
	ecsSupport          bool
	maxTTLMode          string
	maxTTLSecs          int
}

func newGatewayConfigCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage Gateway locations, settings, and certificates",
	}
	cmd.AddCommand(
		newGatewayConfigLocationsCmd(g),
		newGatewayConfigSettingsCmd(g),
		newGatewayConfigCertificatesCmd(g),
	)
	return cmd
}

func gatewayConfigAccountPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/gateway"
}

func gatewayConfigLocationsPath(accountID string) string {
	return gatewayConfigAccountPath(accountID) + "/locations"
}

func gatewayConfigLocationPath(accountID, locationID string) string {
	return gatewayConfigLocationsPath(accountID) + "/" + url.PathEscape(locationID)
}

func gatewayConfigSettingsPath(accountID string) string {
	return gatewayConfigAccountPath(accountID) + "/configuration"
}

func gatewayConfigCertificatesPath(accountID string) string {
	return gatewayConfigAccountPath(accountID) + "/certificates"
}

func gatewayConfigCertificateActivatePath(accountID, certificateID string) string {
	return gatewayConfigCertificatesPath(accountID) + "/" + url.PathEscape(certificateID) + "/activate"
}

// gatewayConfigAccountID resolves the local profile before constructing a
// client, so every command can reject a missing account ID without network I/O.
func gatewayConfigAccountID(g *globalOpts) (string, error) {
	cfg, err := g.resolve()
	if err != nil {
		return "", err
	}
	accountID := strings.TrimSpace(cfg.AccountID)
	if accountID == "" {
		return "", errors.New("no account ID found: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

func gatewayConfigClient(g *globalOpts) (*api.Client, string, error) {
	accountID, err := gatewayConfigAccountID(g)
	if err != nil {
		return nil, "", err
	}
	client, _, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	return client, accountID, nil
}

func gatewayConfigRunRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func gatewayConfigFetchObject(ctx context.Context, client *api.Client, path, label string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", label, err)
	}
	var object map[string]any
	if err := json.Unmarshal(env.Result, &object); err != nil {
		return nil, fmt.Errorf("get %s: unexpected response", label)
	}
	return object, nil
}

func newGatewayConfigLocationsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "locations", Short: "Manage Gateway DNS locations"}
	cmd.AddCommand(
		newGatewayConfigLocationsListCmd(g),
		newGatewayConfigLocationsGetCmd(g),
		newGatewayConfigLocationsCreateCmd(g),
		newGatewayConfigLocationsUpdateCmd(g),
		newGatewayConfigLocationsDeleteCmd(g),
	)
	return cmd
}

func newGatewayConfigLocationsListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Gateway DNS locations",
		Long:  "List Gateway DNS locations.\n\nExample:\n\n  cf gateway config locations list --account-id $ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			accountID, err := gatewayConfigAccountID(g)
			if err != nil {
				return err
			}
			client, _, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayConfigLocationsPath(accountID)}
			if g.DryRun {
				return gatewayConfigRunRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			return gatewayConfigRenderLocations(cmd, env.Result)
		},
	}
}

func newGatewayConfigLocationsGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <location-id>",
		Short: "Show a Gateway DNS location",
		Long:  "Show a Gateway DNS location.\n\nExample:\n\n  cf gateway config locations get LOCATION_ID --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayConfigIdentifier("location-id", args[0], 0); err != nil {
				return err
			}
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "GET", Path: gatewayConfigLocationPath(accountID, args[0])})
		},
	}
}

func newGatewayConfigLocationsCreateCmd(g *globalOpts) *cobra.Command {
	f := gatewayConfigLocationFlags{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Gateway DNS location",
		Long: `Create a Gateway DNS location.

Examples:

  cf gateway config locations create office --network 192.0.2.0/24 --ecs-support
  cf gateway config locations create branch --max-ttl-mode override --max-ttl-secs 3600`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.name = args[0]
			body, err := gatewayConfigLocationFields(cmd, f, true)
			if err != nil {
				return err
			}
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "POST", Path: gatewayConfigLocationsPath(accountID), Body: body})
		},
	}
	gatewayConfigAddLocationFlags(cmd, &f, true)
	return cmd
}

func newGatewayConfigLocationsUpdateCmd(g *globalOpts) *cobra.Command {
	f := gatewayConfigLocationFlags{}
	cmd := &cobra.Command{
		Use:   "update <location-id>",
		Short: "Update a Gateway DNS location",
		Long: `Update selected Gateway DNS location fields.

This command reads the current location, preserves unknown writable fields, and
then sends the full PUT body. For correctness, --dry-run performs that read.

Example:

  cf gateway config locations update LOCATION_ID --client-default=false --max-ttl-mode inherit`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayConfigIdentifier("location-id", args[0], 0); err != nil {
				return err
			}
			if _, err := gatewayConfigLocationFields(cmd, f, false); err != nil {
				return err
			}
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			body, err := gatewayConfigLocationUpdateBody(cmd, client, accountID, args[0], f)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "PUT", Path: gatewayConfigLocationPath(accountID, args[0]), Body: body})
		},
	}
	gatewayConfigAddLocationFlags(cmd, &f, false)
	return cmd
}

func newGatewayConfigLocationsDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <location-id>",
		Short: "Delete a Gateway DNS location",
		Long:  "Delete a Gateway DNS location.\n\nExample:\n\n  cf gateway config locations delete LOCATION_ID --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayConfigIdentifier("location-id", args[0], 0); err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete Gateway DNS location %s?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "DELETE", Path: gatewayConfigLocationPath(accountID, args[0]), Body: []byte("{}")})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func gatewayConfigAddLocationFlags(cmd *cobra.Command, f *gatewayConfigLocationFlags, create bool) {
	if !create {
		cmd.Flags().StringVar(&f.name, "name", "", "new location name")
	}
	cmd.Flags().StringArrayVar(&f.networks, "network", nil, "IPv4 address or CIDR allowed at this location (repeatable; CIDRs no broader than /24)")
	cmd.Flags().StringVar(&f.dnsDestinationIPsID, "dns-destination-ips-id", "", "DNS destination IPv4 pair ID")
	cmd.Flags().BoolVar(&f.clientDefault, "client-default", false, "make this the default client location")
	cmd.Flags().BoolVar(&f.ecsSupport, "ecs-support", false, "enable EDNS Client Subnet support")
	cmd.Flags().StringVar(&f.maxTTLMode, "max-ttl-mode", "", "DNS TTL cap mode (inherit, override, disabled)")
	cmd.Flags().IntVar(&f.maxTTLSecs, "max-ttl-secs", 0, "location TTL cap in seconds when --max-ttl-mode=override (60-36000)")
}

func gatewayConfigLocationFields(cmd *cobra.Command, f gatewayConfigLocationFlags, create bool) ([]byte, error) {
	body := map[string]any{}
	if create {
		if strings.TrimSpace(f.name) == "" {
			return nil, errors.New("location name cannot be empty")
		}
		body["name"] = f.name
	} else if cmd.Flags().Changed("name") {
		if strings.TrimSpace(f.name) == "" {
			return nil, errors.New("--name cannot be empty")
		}
		body["name"] = f.name
	}
	if cmd.Flags().Changed("network") {
		networks, err := gatewayConfigNetworks(f.networks)
		if err != nil {
			return nil, err
		}
		body["networks"] = networks
	}
	if cmd.Flags().Changed("dns-destination-ips-id") {
		if strings.TrimSpace(f.dnsDestinationIPsID) == "" {
			return nil, errors.New("--dns-destination-ips-id cannot be empty")
		}
		body["dns_destination_ips_id"] = f.dnsDestinationIPsID
	}
	if cmd.Flags().Changed("client-default") {
		body["client_default"] = f.clientDefault
	}
	if cmd.Flags().Changed("ecs-support") {
		body["ecs_support"] = f.ecsSupport
	}
	if cmd.Flags().Changed("max-ttl-mode") || cmd.Flags().Changed("max-ttl-secs") {
		maxTTL, err := gatewayConfigMaxTTL(f.maxTTLMode, f.maxTTLSecs, cmd.Flags().Changed("max-ttl-mode"), cmd.Flags().Changed("max-ttl-secs"))
		if err != nil {
			return nil, err
		}
		body["max_ttl"] = maxTTL
	}
	if !create && len(body) == 0 {
		return nil, errors.New("provide at least one field to update")
	}
	return json.Marshal(body)
}

func gatewayConfigNetworks(values []string) ([]map[string]string, error) {
	if len(values) == 0 {
		return nil, errors.New("--network requires at least one IPv4 address or CIDR")
	}
	networks := make([]map[string]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("--network cannot contain an empty value")
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			if !prefix.Addr().Is4() || prefix.Bits() < 24 {
				return nil, fmt.Errorf("--network %q must be an IPv4 address or IPv4 CIDR no broader than /24", value)
			}
		} else if addr, err := netip.ParseAddr(value); err != nil || !addr.Is4() {
			return nil, fmt.Errorf("--network %q must be an IPv4 address or IPv4 CIDR no broader than /24", value)
		}
		networks = append(networks, map[string]string{"network": value})
	}
	return networks, nil
}

func gatewayConfigMaxTTL(mode string, seconds int, modeSet, secondsSet bool) (map[string]any, error) {
	if !modeSet {
		return nil, errors.New("--max-ttl-secs requires --max-ttl-mode=override")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "inherit" && mode != "override" && mode != "disabled" {
		return nil, fmt.Errorf("--max-ttl-mode must be inherit, override, or disabled (got %q)", mode)
	}
	if mode == "override" {
		if !secondsSet || seconds < 60 || seconds > 36000 {
			return nil, errors.New("--max-ttl-secs must be between 60 and 36000 when --max-ttl-mode=override")
		}
		return map[string]any{"mode": mode, "ttl_secs": seconds}, nil
	}
	if secondsSet {
		return nil, fmt.Errorf("--max-ttl-secs requires --max-ttl-mode=override, not %s", mode)
	}
	return map[string]any{"mode": mode}, nil
}

func gatewayConfigLocationUpdateBody(cmd *cobra.Command, client *api.Client, accountID, locationID string, f gatewayConfigLocationFlags) ([]byte, error) {
	patchRaw, err := gatewayConfigLocationFields(cmd, f, false)
	if err != nil {
		return nil, err
	}
	var patch map[string]any
	if err := json.Unmarshal(patchRaw, &patch); err != nil {
		return nil, err
	}
	current, err := gatewayConfigFetchObject(cmd.Context(), client, gatewayConfigLocationPath(accountID, locationID), "Gateway location "+locationID)
	if err != nil {
		return nil, err
	}
	gatewayConfigStripLocationReadOnly(current)
	for key, value := range patch {
		current[key] = value
	}
	gatewayConfigStripLocationReadOnly(current)
	if err := gatewayConfigValidateLocationObject(current); err != nil {
		return nil, fmt.Errorf("Gateway location %s cannot be updated: %w", locationID, err)
	}
	return json.Marshal(current)
}

func gatewayConfigStripLocationReadOnly(object map[string]any) {
	for _, key := range []string{"id", "created_at", "updated_at", "dns_destination_ipv6_block_id", "doh_subdomain", "ip", "ipv4_destination", "ipv4_destination_backup"} {
		delete(object, key)
	}
}

func gatewayConfigValidateLocationObject(object map[string]any) error {
	name, ok := object["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return errors.New("location name is required")
	}
	if maxTTL, ok := object["max_ttl"].(map[string]any); ok {
		mode, _ := maxTTL["mode"].(string)
		if mode == "override" {
			seconds, ok := gatewayConfigNumber(maxTTL["ttl_secs"])
			if !ok || seconds < 60 || seconds > 36000 {
				return errors.New("max_ttl.ttl_secs must be between 60 and 36000 when max_ttl.mode is override")
			}
		}
	}
	return nil
}

func gatewayConfigNumber(value any) (int, bool) {
	switch value := value.(type) {
	case float64:
		return int(value), value == float64(int(value))
	case int:
		return value, true
	case json.Number:
		n, err := strconv.Atoi(string(value))
		return n, err == nil
	default:
		return 0, false
	}
}

func newGatewayConfigSettingsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "settings", Short: "View and change common Gateway settings"}
	cmd.AddCommand(newGatewayConfigSettingsGetCmd(g), newGatewayConfigSettingsSetCmd(g))
	return cmd
}

func newGatewayConfigSettingsGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get",
		Short: "Show Gateway account settings",
		Long:  "Show Gateway account settings.\n\nExample:\n\n  cf gateway config settings get --account-id $ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "GET", Path: gatewayConfigSettingsPath(accountID)})
		},
	}
}

type gatewayConfigSettingsFlags struct {
	tlsDecrypt            bool
	activityLog           bool
	blockPage             bool
	browserIsolation      bool
	fipsTLS               bool
	antivirusDownload     bool
	antivirusUpload       bool
	antivirusFailClosed   bool
	extendedEmailMatching bool
	hostSelector          bool
	protocolDetection     bool
	sandbox               bool
	sandboxFallbackAction string
	inspectionMode        string
	bodyScanningMode      string
	maxTTLSecs            int
	clearMaxTTL           bool
}

func newGatewayConfigSettingsSetCmd(g *globalOpts) *cobra.Command {
	f := gatewayConfigSettingsFlags{}
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set common Gateway account settings",
		Long: `Set common Gateway account settings.

The configuration API requires a full replacement body. This command reads the
current settings, preserves unknown writable fields, and removes known
read-only fields before PUT. For correctness, --dry-run performs that read.

Examples:

  cf gateway config settings set --tls-decrypt --activity-log
  cf gateway config settings set --antivirus-download --antivirus-upload --antivirus-fail-closed
  cf gateway config settings set --max-ttl-secs 3600 --inspection-mode dynamic`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			patch, err := gatewayConfigSettingsPatch(cmd, f)
			if err != nil {
				return err
			}
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			body, err := gatewayConfigSettingsUpdateBody(cmd, client, accountID, patch)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "PUT", Path: gatewayConfigSettingsPath(accountID), Body: body})
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&f.tlsDecrypt, "tls-decrypt", false, "inspect encrypted HTTP traffic")
	flags.BoolVar(&f.activityLog, "activity-log", false, "enable Gateway activity logging")
	flags.BoolVar(&f.blockPage, "block-page", false, "enable the custom Gateway block page")
	flags.BoolVar(&f.browserIsolation, "browser-isolation", false, "enable Clientless Browser Isolation")
	flags.BoolVar(&f.fipsTLS, "fips-tls", false, "enforce FIPS-compliant TLS settings")
	flags.BoolVar(&f.antivirusDownload, "antivirus-download", false, "scan downloads for malware")
	flags.BoolVar(&f.antivirusUpload, "antivirus-upload", false, "scan uploads for malware")
	flags.BoolVar(&f.antivirusFailClosed, "antivirus-fail-closed", false, "block files that cannot be scanned")
	flags.BoolVar(&f.extendedEmailMatching, "extended-email-matching", false, "match + and . email variants in firewall policies")
	flags.BoolVar(&f.hostSelector, "host-selector", false, "enable host selection in egress policies")
	flags.BoolVar(&f.protocolDetection, "protocol-detection", false, "detect protocols from initial traffic bytes")
	flags.BoolVar(&f.sandbox, "sandbox", false, "enable sandbox scanning")
	flags.StringVar(&f.sandboxFallbackAction, "sandbox-fallback-action", "", "sandbox fallback action (allow or block)")
	flags.StringVar(&f.inspectionMode, "inspection-mode", "", "proxy inspection mode (static or dynamic)")
	flags.StringVar(&f.bodyScanningMode, "body-scanning-mode", "", "DLP inspection mode (deep or shallow)")
	flags.IntVar(&f.maxTTLSecs, "max-ttl-secs", 0, "account DNS TTL cap in seconds (60-36000)")
	flags.BoolVar(&f.clearMaxTTL, "clear-max-ttl", false, "remove the account DNS TTL cap")
	return cmd
}

func gatewayConfigSettingsPatch(cmd *cobra.Command, f gatewayConfigSettingsFlags) (map[string]any, error) {
	settings := map[string]any{}
	setEnabled := func(flag, group string, value bool) {
		if cmd.Flags().Changed(flag) {
			settings[group] = map[string]any{"enabled": value}
		}
	}
	setEnabled("tls-decrypt", "tls_decrypt", f.tlsDecrypt)
	setEnabled("activity-log", "activity_log", f.activityLog)
	setEnabled("block-page", "block_page", f.blockPage)
	setEnabled("extended-email-matching", "extended_email_matching", f.extendedEmailMatching)
	setEnabled("host-selector", "host_selector", f.hostSelector)
	setEnabled("protocol-detection", "protocol_detection", f.protocolDetection)
	setEnabled("sandbox", "sandbox", f.sandbox)
	if cmd.Flags().Changed("fips-tls") {
		settings["fips"] = map[string]any{"tls": f.fipsTLS}
	}
	if cmd.Flags().Changed("browser-isolation") {
		settings["browser_isolation"] = map[string]any{"url_browser_isolation_enabled": f.browserIsolation}
	}
	antivirus := map[string]any{}
	if cmd.Flags().Changed("antivirus-download") {
		antivirus["enabled_download_phase"] = f.antivirusDownload
	}
	if cmd.Flags().Changed("antivirus-upload") {
		antivirus["enabled_upload_phase"] = f.antivirusUpload
	}
	if cmd.Flags().Changed("antivirus-fail-closed") {
		antivirus["fail_closed"] = f.antivirusFailClosed
	}
	if len(antivirus) > 0 {
		settings["antivirus"] = antivirus
	}
	if cmd.Flags().Changed("sandbox-fallback-action") {
		action := strings.ToLower(strings.TrimSpace(f.sandboxFallbackAction))
		if action != "allow" && action != "block" {
			return nil, fmt.Errorf("--sandbox-fallback-action must be allow or block (got %q)", f.sandboxFallbackAction)
		}
		gatewayConfigSetNested(settings, "sandbox", "fallback_action", action)
	}
	if cmd.Flags().Changed("inspection-mode") {
		mode := strings.ToLower(strings.TrimSpace(f.inspectionMode))
		if mode != "static" && mode != "dynamic" {
			return nil, fmt.Errorf("--inspection-mode must be static or dynamic (got %q)", f.inspectionMode)
		}
		settings["inspection"] = map[string]any{"mode": mode}
	}
	if cmd.Flags().Changed("body-scanning-mode") {
		mode := strings.ToLower(strings.TrimSpace(f.bodyScanningMode))
		if mode != "deep" && mode != "shallow" {
			return nil, fmt.Errorf("--body-scanning-mode must be deep or shallow (got %q)", f.bodyScanningMode)
		}
		settings["body_scanning"] = map[string]any{"inspection_mode": mode}
	}
	if cmd.Flags().Changed("max-ttl-secs") && f.clearMaxTTL {
		return nil, errors.New("--max-ttl-secs and --clear-max-ttl cannot be used together")
	}
	if f.clearMaxTTL {
		settings["max_ttl_secs"] = nil
	} else if cmd.Flags().Changed("max-ttl-secs") {
		if f.maxTTLSecs < 60 || f.maxTTLSecs > 36000 {
			return nil, errors.New("--max-ttl-secs must be between 60 and 36000")
		}
		settings["max_ttl_secs"] = f.maxTTLSecs
	}
	if len(settings) == 0 {
		return nil, errors.New("provide at least one setting to change")
	}
	return settings, nil
}

func gatewayConfigSetNested(settings map[string]any, group, key string, value any) {
	child, _ := settings[group].(map[string]any)
	if child == nil {
		child = map[string]any{}
		settings[group] = child
	}
	child[key] = value
}

func gatewayConfigSettingsUpdateBody(cmd *cobra.Command, client *api.Client, accountID string, patch map[string]any) ([]byte, error) {
	current, err := gatewayConfigFetchObject(cmd.Context(), client, gatewayConfigSettingsPath(accountID), "Gateway account settings")
	if err != nil {
		return nil, err
	}
	settings, _ := current["settings"].(map[string]any)
	if settings == nil {
		return nil, errors.New("get Gateway account settings: response did not contain settings")
	}
	gatewayConfigStripSettingsReadOnly(settings)
	for group, patchValue := range patch {
		patchMap, isMap := patchValue.(map[string]any)
		if !isMap {
			settings[group] = patchValue
			continue
		}
		currentMap, _ := settings[group].(map[string]any)
		if currentMap == nil {
			currentMap = map[string]any{}
		}
		for key, value := range patchMap {
			currentMap[key] = value
		}
		settings[group] = currentMap
	}
	gatewayConfigStripSettingsReadOnly(settings)
	return json.Marshal(map[string]any{"settings": settings})
}

func gatewayConfigStripSettingsReadOnly(settings map[string]any) {
	for _, group := range []string{"block_page", "extended_email_matching"} {
		if value, ok := settings[group].(map[string]any); ok {
			for _, key := range []string{"read_only", "source_account", "version"} {
				delete(value, key)
			}
		}
	}
	if value, ok := settings["custom_certificate"].(map[string]any); ok {
		delete(value, "binding_status")
		delete(value, "updated_at")
	}
}

func newGatewayConfigCertificatesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "certificates", Short: "Manage Gateway TLS-interception certificates"}
	cmd.AddCommand(newGatewayConfigCertificatesListCmd(g), newGatewayConfigCertificatesActivateCmd(g))
	return cmd
}

func newGatewayConfigCertificatesListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Gateway TLS-interception certificates",
		Long:  "List Gateway TLS-interception certificates.\n\nExample:\n\n  cf gateway config certificates list --account-id $ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: gatewayConfigCertificatesPath(accountID)}
			if g.DryRun {
				return gatewayConfigRunRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			return gatewayConfigRenderCertificates(cmd, env.Result)
		},
	}
}

func newGatewayConfigCertificatesActivateCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "activate <certificate-id>",
		Short: "Activate a Gateway TLS-interception certificate",
		Long:  "Bind one Gateway TLS-interception certificate to the edge.\n\nExample:\n\n  cf gateway config certificates activate CERTIFICATE_ID --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gatewayConfigIdentifier("certificate-id", args[0], 36); err != nil {
				return err
			}
			client, accountID, err := gatewayConfigClient(g)
			if err != nil {
				return err
			}
			return gatewayConfigRunRequest(cmd, g, client, api.Request{Method: "POST", Path: gatewayConfigCertificateActivatePath(accountID, args[0]), Body: []byte("{}")})
		},
	}
}

func gatewayConfigIdentifier(label, value string, maxLength int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be empty", label)
	}
	if maxLength > 0 && len(value) > maxLength {
		return fmt.Errorf("%s must be at most %d characters", label, maxLength)
	}
	return nil
}

func gatewayConfigRenderLocations(cmd *cobra.Command, raw []byte) error {
	var locations []struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		ClientDefault bool   `json:"client_default"`
		ECSSupport    bool   `json:"ecs_support"`
		MaxTTL        struct {
			Mode string `json:"mode"`
		} `json:"max_ttl"`
	}
	if err := json.Unmarshal(raw, &locations); err != nil {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
	}
	rows := make([][]string, 0, len(locations))
	for _, location := range locations {
		rows = append(rows, []string{location.ID, output.Cell(location.Name), strconv.FormatBool(location.ClientDefault), strconv.FormatBool(location.ECSSupport), location.MaxTTL.Mode})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "DEFAULT", "ECS", "MAX TTL"}, rows)
}

func gatewayConfigRenderCertificates(cmd *cobra.Command, raw []byte) error {
	var certificates []struct {
		ID            string `json:"id"`
		Type          string `json:"type"`
		BindingStatus string `json:"binding_status"`
		InUse         bool   `json:"in_use"`
		ExpiresOn     string `json:"expires_on"`
	}
	if err := json.Unmarshal(raw, &certificates); err != nil {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
	}
	rows := make([][]string, 0, len(certificates))
	for _, certificate := range certificates {
		rows = append(rows, []string{certificate.ID, certificate.Type, certificate.BindingStatus, strconv.FormatBool(certificate.InUse), certificate.ExpiresOn})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "TYPE", "STATUS", "IN USE", "EXPIRES"}, rows)
}
