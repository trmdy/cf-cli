package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

const sslCertsMaxHosts = 50

type sslCertsPack struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	Hosts        []string `json:"hosts"`
	ValidityDays int      `json:"validity_days"`
}

type sslCertsOriginCA struct {
	ID                string   `json:"id"`
	RequestType       string   `json:"request_type"`
	Hostnames         []string `json:"hostnames"`
	RequestedValidity int      `json:"requested_validity"`
	ExpiresOn         string   `json:"expires_on"`
}

type sslCertsMTLSCertificate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	CA        bool   `json:"ca"`
	ExpiresOn string `json:"expires_on"`
}

func newSSLCertsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssl-certs",
		Short: "Manage certificate packs, Origin CA, and mTLS certificates",
	}
	cmd.AddCommand(newSSLCertsPacksCmd(g), newSSLCertsOriginCACmd(g), newSSLCertsMTLSCmd(g))
	return cmd
}

func newSSLCertsPacksCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "packs", Short: "Manage zone certificate packs"}
	cmd.AddCommand(newSSLCertsPacksListCmd(g), newSSLCertsPacksOrderCmd(g))
	return cmd
}

func newSSLCertsOriginCACmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "origin-ca", Short: "Manage Origin CA certificates"}
	cmd.AddCommand(
		newSSLCertsOriginCAListCmd(g),
		newSSLCertsOriginCAGetCmd(g),
		newSSLCertsOriginCACreateCmd(g),
		newSSLCertsOriginCARevokeCmd(g),
	)
	return cmd
}

func newSSLCertsMTLSCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "mtls", Short: "Manage account mTLS certificates"}
	cmd.AddCommand(newSSLCertsMTLSListCmd(g), newSSLCertsMTLSUploadCmd(g))
	return cmd
}

func sslCertsPacksPath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/ssl/certificate_packs"
}

func sslCertsMTLSPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/mtls_certificates"
}

func sslCertsAccountID(accountID string) (string, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

func newSSLCertsPacksListCmd(g *globalOpts) *cobra.Command {
	var zone, deploy string
	var page, perPage int
	var allStatuses bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List certificate packs in a zone",
		Long:  "List certificate packs in a zone.\n\nExample:\n\n  cf ssl-certs packs list --zone example.com --deploy production",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := buildSSLCertsPacksListQuery(deploy, page, perPage, allStatuses, cmd.Flags().Changed("page"), cmd.Flags().Changed("per-page"))
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
			req := api.Request{Method: "GET", Path: sslCertsPacksPath(zoneID), Query: q}
			if g.DryRun {
				return runSSLCertsRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderSSLCertsPacks(cmd, g, env.Result)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&deploy, "deploy", "", "deployment environment: staging or production")
	cmd.Flags().BoolVar(&allStatuses, "all-statuses", false, "include certificate packs of all statuses")
	cmd.Flags().IntVar(&page, "page", 0, "page number (minimum 1)")
	cmd.Flags().IntVar(&perPage, "per-page", 0, "certificate packs per page (5-50)")
	return cmd
}

func buildSSLCertsPacksListQuery(deploy string, page, perPage int, allStatuses, pageSet, perPageSet bool) (url.Values, error) {
	q := url.Values{}
	if deploy != "" {
		canonical, err := sslCertsEnum("deploy", deploy, []string{"staging", "production"})
		if err != nil {
			return nil, err
		}
		q.Set("deploy", canonical)
	}
	if pageSet {
		if page < 1 {
			return nil, fmt.Errorf("--page must be at least 1, got %d", page)
		}
		q.Set("page", strconv.Itoa(page))
	}
	if perPageSet {
		if perPage < 5 || perPage > 50 {
			return nil, fmt.Errorf("--per-page must be between 5 and 50, got %d", perPage)
		}
		q.Set("per_page", strconv.Itoa(perPage))
	}
	if allStatuses {
		q.Set("status", "all")
	}
	return q, nil
}

func newSSLCertsPacksOrderCmd(g *globalOpts) *cobra.Command {
	var zone, authority, validationMethod, hostsJSON string
	var hosts []string
	var validityDays int
	var branding bool
	cmd := &cobra.Command{
		Use:   "order",
		Short: "Order an advanced certificate pack",
		Long: `Order an advanced certificate pack. Pass each host with --host, or use --hosts with a JSON array. --dry-run reads the resolved zone to validate that the order includes its apex.

Examples:

  cf ssl-certs packs order --zone example.com --host example.com --host '*.example.com' --certificate-authority lets_encrypt --validation-method txt --validity-days 14
  cf ssl-certs packs order --zone example.com --hosts '["example.com","*.example.com"]' --certificate-authority google --validation-method email --validity-days 365`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildSSLCertsPacksOrderBody(authority, hosts, hostsJSON, validationMethod, validityDays, branding, cmd.Flags().Changed("cloudflare-branding"))
			if err != nil {
				return err
			}
			orderHosts, err := sslCertsPacksOrderHosts(hosts, hostsJSON)
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
			zoneApex, err := sslCertsZoneApex(cmd.Context(), client, zoneID)
			if err != nil {
				return err
			}
			if err := sslCertsRequireZoneApex(orderHosts, zoneApex); err != nil {
				return err
			}
			return runSSLCertsRequest(cmd, g, client, api.Request{Method: "POST", Path: sslCertsPacksPath(zoneID) + "/order", Body: body})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringArrayVar(&hosts, "host", nil, "hostname to include (repeatable)")
	cmd.Flags().StringVar(&hostsJSON, "hosts", "", "hostnames as a JSON array of strings")
	cmd.Flags().StringVar(&authority, "certificate-authority", "", "certificate authority: google, lets_encrypt, or ssl_com")
	cmd.Flags().StringVar(&validationMethod, "validation-method", "", "domain validation method: txt, http, or email")
	cmd.Flags().IntVar(&validityDays, "validity-days", 0, "certificate validity: 14, 30, 90, or 365 days")
	cmd.Flags().BoolVar(&branding, "cloudflare-branding", false, "add Cloudflare branding to the order")
	_ = cmd.MarkFlagRequired("certificate-authority")
	_ = cmd.MarkFlagRequired("validation-method")
	_ = cmd.MarkFlagRequired("validity-days")
	return cmd
}

func buildSSLCertsPacksOrderBody(authority string, repeatedHosts []string, hostsJSON, validationMethod string, validityDays int, branding, brandingSet bool) ([]byte, error) {
	hosts, err := sslCertsPacksOrderHosts(repeatedHosts, hostsJSON)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 || len(hosts) > sslCertsMaxHosts {
		return nil, fmt.Errorf("--host/--hosts must contain between 1 and %d hostnames, got %d", sslCertsMaxHosts, len(hosts))
	}
	canonicalAuthority, err := sslCertsEnum("certificate-authority", authority, []string{"google", "lets_encrypt", "ssl_com"})
	if err != nil {
		return nil, err
	}
	canonicalValidation, err := sslCertsEnum("validation-method", validationMethod, []string{"txt", "http", "email"})
	if err != nil {
		return nil, err
	}
	if !sslCertsIntIn(validityDays, []int{14, 30, 90, 365}) {
		return nil, errors.New("--validity-days must be one of: 14, 30, 90, 365")
	}
	body := map[string]any{
		"certificate_authority": canonicalAuthority,
		"hosts":                 hosts,
		"type":                  "advanced",
		"validation_method":     canonicalValidation,
		"validity_days":         validityDays,
	}
	if brandingSet {
		body["cloudflare_branding"] = branding
	}
	return json.Marshal(body)
}

func sslCertsPacksOrderHosts(repeatedHosts []string, hostsJSON string) ([]string, error) {
	return sslCertsHosts(repeatedHosts, hostsJSON, "host", "hosts")
}

func sslCertsZoneApex(ctx context.Context, client *api.Client, zoneID string) (string, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: "/zones/" + url.PathEscape(zoneID)})
	if err != nil {
		return "", fmt.Errorf("get zone apex: %w", err)
	}
	var zone struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &zone); err != nil || strings.TrimSpace(zone.Name) == "" {
		return "", errors.New("get zone apex: unexpected response")
	}
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone.Name)), "."), nil
}

func sslCertsRequireZoneApex(hosts []string, apex string) error {
	for _, host := range hosts {
		if strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".") == apex {
			return nil
		}
	}
	return fmt.Errorf("--host/--hosts must include the zone apex %q", apex)
}

func newSSLCertsOriginCAListCmd(g *globalOpts) *cobra.Command {
	var zone string
	var page, perPage int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Origin CA certificates for a zone",
		Long:  "List Origin CA certificates for a zone.\n\nExample:\n\n  cf ssl-certs origin-ca list --zone example.com --per-page 50",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q, err := buildSSLCertsOriginCAListQuery(page, perPage, cmd.Flags().Changed("page"), cmd.Flags().Changed("per-page"))
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
			q.Set("zone_id", zoneID)
			req := api.Request{Method: "GET", Path: "/certificates", Query: q}
			if g.DryRun {
				return runSSLCertsRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderSSLCertsOriginCA(cmd, g, env.Result)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().IntVar(&page, "page", 0, "page number (minimum 1)")
	cmd.Flags().IntVar(&perPage, "per-page", 0, "certificates per page (5-50)")
	return cmd
}

func buildSSLCertsOriginCAListQuery(page, perPage int, pageSet, perPageSet bool) (url.Values, error) {
	return buildSSLCertsPacksListQuery("", page, perPage, false, pageSet, perPageSet)
}

func newSSLCertsOriginCAGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <certificate-id>",
		Short: "Show an Origin CA certificate",
		Long:  "Show an Origin CA certificate.\n\nExample:\n\n  cf ssl-certs origin-ca get 023e105f4ecef8ad9ca31a8372d0c353",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sslCertsResourceID("certificate-id", args[0]); err != nil {
				return err
			}
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			return runSSLCertsRequest(cmd, g, client, api.Request{Method: "GET", Path: "/certificates/" + url.PathEscape(args[0])})
		},
	}
	return cmd
}

func newSSLCertsOriginCACreateCmd(g *globalOpts) *cobra.Command {
	var csr, requestType, hostnamesJSON string
	var hostnames []string
	var requestedValidity int
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create an Origin CA certificate",
		Long: `Create an Origin CA certificate. Pass each hostname with --hostname, or use --hostnames with a JSON array. --csr accepts an inline CSR, @file, or @- for stdin.

Example:

  cf ssl-certs origin-ca create --csr @origin.csr --hostname example.com --hostname '*.example.com' --request-type origin-rsa --requested-validity 365`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildSSLCertsOriginCACreateBody(csr, cmd.InOrStdin(), hostnames, hostnamesJSON, requestType, requestedValidity, cmd.Flags().Changed("requested-validity"))
			if err != nil {
				return err
			}
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			return runSSLCertsRequest(cmd, g, client, api.Request{Method: "POST", Path: "/certificates", Body: body})
		},
	}
	cmd.Flags().StringVar(&csr, "csr", "", "certificate signing request (inline, @file, or @- for stdin)")
	cmd.Flags().StringArrayVar(&hostnames, "hostname", nil, "hostname to include (repeatable)")
	cmd.Flags().StringVar(&hostnamesJSON, "hostnames", "", "hostnames as a JSON array of strings")
	cmd.Flags().StringVar(&requestType, "request-type", "", "signature type: origin-rsa, origin-ecc, or keyless-certificate")
	cmd.Flags().IntVar(&requestedValidity, "requested-validity", 0, "certificate validity: 7, 30, 90, 365, 730, 1095, or 5475 days")
	_ = cmd.MarkFlagRequired("csr")
	_ = cmd.MarkFlagRequired("request-type")
	return cmd
}

func buildSSLCertsOriginCACreateBody(csr string, stdin io.Reader, repeatedHostnames []string, hostnamesJSON, requestType string, requestedValidity int, requestedValiditySet bool) ([]byte, error) {
	csrValue, err := sslCertsReadText("csr", csr, stdin)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(csrValue) == "" {
		return nil, errors.New("--csr must not be empty")
	}
	hostnames, err := sslCertsHosts(repeatedHostnames, hostnamesJSON, "hostname", "hostnames")
	if err != nil {
		return nil, err
	}
	if len(hostnames) == 0 {
		return nil, errors.New("pass at least one --hostname or a non-empty --hostnames JSON array")
	}
	if err := sslCertsValidateOriginCAHostnames(hostnames); err != nil {
		return nil, err
	}
	canonicalType, err := sslCertsEnum("request-type", requestType, []string{"origin-rsa", "origin-ecc", "keyless-certificate"})
	if err != nil {
		return nil, err
	}
	body := map[string]any{"csr": csrValue, "hostnames": hostnames, "request_type": canonicalType}
	if requestedValiditySet {
		if !sslCertsIntIn(requestedValidity, []int{7, 30, 90, 365, 730, 1095, 5475}) {
			return nil, errors.New("--requested-validity must be one of: 7, 30, 90, 365, 730, 1095, 5475")
		}
		body["requested_validity"] = requestedValidity
	}
	return json.Marshal(body)
}

func newSSLCertsOriginCARevokeCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "revoke <certificate-id>",
		Short: "Revoke an Origin CA certificate",
		Long:  "Revoke an Origin CA certificate.\n\nExample:\n\n  cf ssl-certs origin-ca revoke 023e105f4ecef8ad9ca31a8372d0c353 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := sslCertsResourceID("certificate-id", args[0]); err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Revoke Origin CA certificate %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			return runSSLCertsRequest(cmd, g, client, api.Request{Method: "DELETE", Path: "/certificates/" + url.PathEscape(args[0])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newSSLCertsMTLSListCmd(g *globalOpts) *cobra.Command {
	var types []string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List mTLS certificates",
		Long:  "List mTLS certificates for the configured account.\n\nExample:\n\n  cf ssl-certs mtls list --type custom --type access_managed",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.resolve()
			if err != nil {
				return err
			}
			accountID, err := sslCertsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			q, err := buildSSLCertsMTLSListQuery(types)
			if err != nil {
				return err
			}
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: sslCertsMTLSPath(accountID), Query: q}
			if g.DryRun {
				return runSSLCertsRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			return renderSSLCertsMTLS(cmd, g, env.Result)
		},
	}
	cmd.Flags().StringArrayVar(&types, "type", nil, "certificate type: custom, gateway_managed, or access_managed (repeatable)")
	return cmd
}

func buildSSLCertsMTLSListQuery(types []string) (url.Values, error) {
	q := url.Values{}
	if len(types) == 0 {
		return q, nil
	}
	canonical := make([]string, 0, len(types))
	for _, typeValue := range types {
		for _, part := range strings.Split(typeValue, ",") {
			value, err := sslCertsEnum("type", part, []string{"custom", "gateway_managed", "access_managed"})
			if err != nil {
				return nil, err
			}
			canonical = append(canonical, value)
		}
	}
	q.Set("type", strings.Join(canonical, ","))
	return q, nil
}

func newSSLCertsMTLSUploadCmd(g *globalOpts) *cobra.Command {
	var certificates, privateKey, name string
	var ca bool
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload an mTLS certificate",
		Long: `Upload an mTLS certificate. --certificates and --private-key accept inline values, @file, or @- for stdin.

Example:

  cf ssl-certs mtls upload --certificates @root-ca.pem --ca --name internal-root`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.resolve()
			if err != nil {
				return err
			}
			accountID, err := sslCertsAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			body, err := buildSSLCertsMTLSUploadBody(certificates, privateKey, name, ca, cmd.Flags().Changed("ca"), cmd.InOrStdin())
			if err != nil {
				return err
			}
			client, _, err := g.client(true)
			if err != nil {
				return err
			}
			return runSSLCertsRequest(cmd, g, client, api.Request{Method: "POST", Path: sslCertsMTLSPath(accountID), Body: body})
		},
	}
	cmd.Flags().StringVar(&certificates, "certificates", "", "certificate chain (inline, @file, or @- for stdin)")
	cmd.Flags().StringVar(&privateKey, "private-key", "", "private key (inline, @file, or @- for stdin)")
	cmd.Flags().StringVar(&name, "name", "", "optional human-readable certificate name")
	cmd.Flags().BoolVar(&ca, "ca", false, "whether this is a CA certificate")
	_ = cmd.MarkFlagRequired("certificates")
	_ = cmd.MarkFlagRequired("ca")
	return cmd
}

func buildSSLCertsMTLSUploadBody(certificates, privateKey, name string, ca, caSet bool, stdin io.Reader) ([]byte, error) {
	if !caSet {
		return nil, errors.New("--ca must be specified as true or false")
	}
	if certificates == "@-" && privateKey == "@-" {
		return nil, errors.New("--certificates and --private-key cannot both read stdin (@-)")
	}
	certificateValue, err := sslCertsReadText("certificates", certificates, stdin)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(certificateValue) == "" {
		return nil, errors.New("--certificates must not be empty")
	}
	body := map[string]any{"ca": ca, "certificates": certificateValue}
	if name != "" {
		body["name"] = name
	}
	if privateKey != "" {
		privateKeyValue, err := sslCertsReadText("private-key", privateKey, stdin)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(privateKeyValue) == "" {
			return nil, errors.New("--private-key must not be empty")
		}
		body["private_key"] = privateKeyValue
	}
	return json.Marshal(body)
}

func sslCertsHosts(repeated []string, rawJSON, singularFlag, pluralFlag string) ([]string, error) {
	if len(repeated) > 0 && rawJSON != "" {
		return nil, fmt.Errorf("--%s and --%s cannot be used together", singularFlag, pluralFlag)
	}
	var values []string
	if rawJSON != "" {
		parsed, err := sslCertsJSONArrayOfStrings(pluralFlag, rawJSON)
		if err != nil {
			return nil, err
		}
		values = parsed
	} else {
		values = repeated
	}
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
		if values[i] == "" {
			return nil, fmt.Errorf("--%s/--%s entries must not be empty", singularFlag, pluralFlag)
		}
	}
	return values, nil
}

// sslCertsValidateOriginCAHostnames enforces the Origin CA hostname contract:
// each name must be an FQDN, and a wildcard may appear only as one leading
// "*." on a multi-label domain.
func sslCertsValidateOriginCAHostnames(hostnames []string) error {
	for _, hostname := range hostnames {
		if err := sslCertsValidateOriginCAHostname(hostname); err != nil {
			return err
		}
	}
	return nil
}

func sslCertsValidateOriginCAHostname(hostname string) error {
	original := hostname
	hostname = strings.TrimSuffix(strings.TrimSpace(hostname), ".")
	if hostname == "" {
		return errors.New("--hostname entries must be fully qualified domain names")
	}
	if strings.Contains(hostname, "*") {
		if !strings.HasPrefix(hostname, "*.") || strings.Count(hostname, "*") != 1 {
			return fmt.Errorf("--hostname %q has an invalid wildcard: use only a single leading *.", original)
		}
		hostname = strings.TrimPrefix(hostname, "*.")
	}
	labels := strings.Split(hostname, ".")
	if len(labels) < 2 {
		return fmt.Errorf("--hostname %q must be a fully qualified domain name with at least two labels", original)
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("--hostname %q is not a valid fully qualified domain name", original)
		}
		for _, r := range label {
			if r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
				continue
			}
			return fmt.Errorf("--hostname %q is not a valid fully qualified domain name", original)
		}
	}
	return nil
}

func sslCertsJSONArrayOfStrings(flagName, raw string) ([]string, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array of strings: %w", flagName, err)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("--%s must be a JSON array of strings", flagName)
	}
	values := make([]string, len(items))
	for i, item := range items {
		stringValue, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("--%s item %d must be a string", flagName, i+1)
		}
		values[i] = stringValue
	}
	return values, nil
}

func sslCertsReadText(flagName, value string, stdin io.Reader) (string, error) {
	if value == "" {
		return "", fmt.Errorf("missing --%s", flagName)
	}
	if value == "@-" {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read --%s from stdin: %w", flagName, err)
		}
		return string(raw), nil
	}
	if strings.HasPrefix(value, "@") {
		path := strings.TrimPrefix(value, "@")
		if path == "" {
			return "", fmt.Errorf("--%s @file requires a file path", flagName)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --%s: %w", flagName, err)
		}
		return string(raw), nil
	}
	return value, nil
}

func sslCertsEnum(flagName, value string, allowed []string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if canonical == candidate {
			return canonical, nil
		}
	}
	return "", fmt.Errorf("--%s must be one of: %s", flagName, strings.Join(allowed, ", "))
}

func sslCertsIntIn(value int, allowed []int) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func sslCertsResourceID(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

func runSSLCertsRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func renderSSLCertsPacks(cmd *cobra.Command, g *globalOpts, raw []byte) error {
	if g.Query != "" || g.format(output.Table) != output.Table {
		return g.renderResult(cmd, raw, output.JSON)
	}
	var packs []sslCertsPack
	if err := json.Unmarshal(raw, &packs); err != nil {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
	}
	rows := make([][]string, 0, len(packs))
	for _, pack := range packs {
		rows = append(rows, []string{pack.ID, pack.Type, pack.Status, output.Cell(strings.Join(pack.Hosts, ", ")), strconv.Itoa(pack.ValidityDays)})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "TYPE", "STATUS", "HOSTS", "VALIDITY DAYS"}, rows)
}

func renderSSLCertsOriginCA(cmd *cobra.Command, g *globalOpts, raw []byte) error {
	if g.Query != "" || g.format(output.Table) != output.Table {
		return g.renderResult(cmd, raw, output.JSON)
	}
	var certificates []sslCertsOriginCA
	if err := json.Unmarshal(raw, &certificates); err != nil {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
	}
	rows := make([][]string, 0, len(certificates))
	for _, certificate := range certificates {
		rows = append(rows, []string{certificate.ID, certificate.RequestType, output.Cell(strings.Join(certificate.Hostnames, ", ")), strconv.Itoa(certificate.RequestedValidity), certificate.ExpiresOn})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "TYPE", "HOSTNAMES", "VALIDITY DAYS", "EXPIRES"}, rows)
}

func renderSSLCertsMTLS(cmd *cobra.Command, g *globalOpts, raw []byte) error {
	if g.Query != "" || g.format(output.Table) != output.Table {
		return g.renderResult(cmd, raw, output.JSON)
	}
	var certificates []sslCertsMTLSCertificate
	if err := json.Unmarshal(raw, &certificates); err != nil {
		return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
	}
	rows := make([][]string, 0, len(certificates))
	for _, certificate := range certificates {
		rows = append(rows, []string{certificate.ID, certificate.Name, certificate.Type, strconv.FormatBool(certificate.CA), certificate.ExpiresOn})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "TYPE", "CA", "EXPIRES"}, rows)
}
