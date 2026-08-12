package cli

// Custom Hostnames porcelain: zone-scoped CRUD, SSL status view, and
// fallback-origin get/set for Cloudflare for SaaS.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.
//
// Scope is the human workflows (list/get/create/update/delete, ssl status,
// fallback-origin get/set). Certificate-pack mutations and quota remain on
// `cf api custom-hostnames`.
//
// Edit is a true partial PATCH (all body fields optional), so updates send only
// the changed fields — no read-merge-write. Fallback-origin set is PUT with the
// single required `origin` field. Dry-run performs no read for either path.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// Enumerations mirror the custom-hostnames OpenAPI / registry schemas.
var (
	customHostnameSSLMethods = []string{"http", "txt", "email"}
	customHostnameSSLTypes   = []string{"dv"}
	customHostnameBundleMeth = []string{"ubiquitous", "optimal", "force"}
	// Create/edit body CertificateCA includes digicert; list filter CA does not.
	customHostnameCreateCAs = []string{"digicert", "google", "lets_encrypt", "ssl_com"}
	customHostnameListCAs   = []string{"google", "lets_encrypt", "ssl_com"}
	customHostnameOnOff     = []string{"on", "off"}
	customHostnameMinTLS    = []string{"1.0", "1.1", "1.2", "1.3"}
	customHostnameOrders    = []string{"ssl", "ssl_status"}
	customHostnameDirs      = []string{"asc", "desc"}
	customHostnameStatuses  = []string{
		"active", "pending", "active_redeploying", "moved", "pending_deletion",
		"deleted", "pending_blocked", "pending_migration", "pending_provisioned",
		"test_pending", "test_active", "test_active_apex", "test_blocked",
		"test_failed", "provisioned", "blocked",
	}
	customHostnameSSLStatuses = []string{
		"initializing", "pending_validation", "deleted", "pending_issuance",
		"pending_deployment", "pending_deletion", "pending_expiration", "expired",
		"active", "initializing_timed_out", "validation_timed_out",
		"issuance_timed_out", "deployment_timed_out", "deletion_timed_out",
		"pending_cleanup", "staging_deployment", "staging_active", "deactivating",
		"inactive", "backup_issued", "holding_deployment",
	}
)

// Page size for transparent list pagination. The list example uses 20; the
// registry does not document a hard max, so we stay near the example without
// inventing a bound check.
const customHostnameListPerPage = "50"

// Bounds from the pinned OpenAPI (JSON Schema maxLength/minLength count
// Unicode code points, not Go bytes).
const (
	customHostnameMaxLen    = 255 // hostname_post, origin, list hostname
	customHostnameListIDLen = 36  // list query id minLength=maxLength=36
)

// customHostname is the subset used for table list rows and SSL status view.
type customHostname struct {
	ID                 string             `json:"id,omitempty"`
	Hostname           string             `json:"hostname,omitempty"`
	Status             string             `json:"status,omitempty"`
	CustomOriginServer string             `json:"custom_origin_server,omitempty"`
	CustomOriginSNI    string             `json:"custom_origin_sni,omitempty"`
	CreatedAt          string             `json:"created_at,omitempty"`
	SSL                *customHostnameSSL `json:"ssl,omitempty"`
	VerificationErrors []string           `json:"verification_errors,omitempty"`
	CustomMetadata     map[string]string  `json:"custom_metadata,omitempty"`
	OwnershipVerify    *json.RawMessage   `json:"ownership_verification,omitempty"`
	OwnershipHTTP      *json.RawMessage   `json:"ownership_verification_http,omitempty"`
}

type customHostnameSSL struct {
	ID                   string                       `json:"id,omitempty"`
	Status               string                       `json:"status,omitempty"`
	Method               string                       `json:"method,omitempty"`
	Type                 string                       `json:"type,omitempty"`
	Wildcard             *bool                        `json:"wildcard,omitempty"`
	BundleMethod         string                       `json:"bundle_method,omitempty"`
	CertificateAuthority string                       `json:"certificate_authority,omitempty"`
	ExpiresOn            string                       `json:"expires_on,omitempty"`
	Hosts                []string                     `json:"hosts,omitempty"`
	Issuer               string                       `json:"issuer,omitempty"`
	Settings             *customHostnameSSLSettings   `json:"settings,omitempty"`
	ValidationErrors     []customHostnameValidationEr `json:"validation_errors,omitempty"`
	ValidationRecords    json.RawMessage              `json:"validation_records,omitempty"`
}

type customHostnameSSLSettings struct {
	Ciphers       []string `json:"ciphers,omitempty"`
	EarlyHints    string   `json:"early_hints,omitempty"`
	HTTP2         string   `json:"http2,omitempty"`
	MinTLSVersion string   `json:"min_tls_version,omitempty"`
	TLS13         string   `json:"tls_1_3,omitempty"`
}

type customHostnameValidationEr struct {
	Message string `json:"message,omitempty"`
}

type customHostnameFallback struct {
	Origin    string   `json:"origin,omitempty"`
	Status    string   `json:"status,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	Errors    []string `json:"errors,omitempty"`
}

func newCustomHostnamesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "custom-hostnames",
		Short: "Manage custom hostnames (Cloudflare for SaaS)",
	}
	cmd.AddCommand(
		newCustomHostnameListCmd(g),
		newCustomHostnameGetCmd(g),
		newCustomHostnameCreateCmd(g),
		newCustomHostnameUpdateCmd(g),
		newCustomHostnameDeleteCmd(g),
		newCustomHostnameSSLCmd(g),
		newCustomHostnameFallbackOriginCmd(g),
	)
	return cmd
}

func customHostnamesPath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/custom_hostnames"
}

func customHostnamePath(zoneID, id string) string {
	return customHostnamesPath(zoneID) + "/" + url.PathEscape(id)
}

func customHostnameFallbackPath(zoneID string) string {
	return customHostnamesPath(zoneID) + "/fallback_origin"
}

func runCustomHostnameRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func customHostnameEnum(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

// parseCustomHostnameMetadata decodes --custom-metadata as a JSON object of
// string values. null and non-objects are rejected.
func parseCustomHostnameMetadata(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("--custom-metadata must be a JSON object of string values")
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, errors.New("--custom-metadata must be a JSON object of string values")
	}
	if v == nil {
		return nil, errors.New("--custom-metadata must be a JSON object of string values, not null")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("--custom-metadata must be a JSON object of string values")
	}
	out := make(map[string]string, len(obj))
	for k, item := range obj {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("--custom-metadata key %q must be a string value", k)
		}
		out[k] = s
	}
	return out, nil
}

func validateCustomHostnameName(hostname string) error {
	if strings.TrimSpace(hostname) == "" {
		return errors.New("hostname must not be empty")
	}
	if n := utf8.RuneCountInString(hostname); n > customHostnameMaxLen {
		return fmt.Errorf("hostname is %d characters; the API allows at most %d", n, customHostnameMaxLen)
	}
	return nil
}

// validateCustomHostnameOrigin enforces the fallback-origin body maxLength.
func validateCustomHostnameOrigin(origin string) error {
	if strings.TrimSpace(origin) == "" {
		return errors.New("origin must not be empty")
	}
	if n := utf8.RuneCountInString(origin); n > customHostnameMaxLen {
		return fmt.Errorf("origin is %d characters; the API allows at most %d", n, customHostnameMaxLen)
	}
	return nil
}

// validateCustomHostnameListID enforces list query id minLength=maxLength=36.
func validateCustomHostnameListID(id string) error {
	if n := utf8.RuneCountInString(id); n != customHostnameListIDLen {
		return fmt.Errorf("--id is %d characters; the API requires exactly %d", n, customHostnameListIDLen)
	}
	return nil
}

// validateCustomHostnameListHostname enforces list query hostname maxLength.
func validateCustomHostnameListHostname(hostname string) error {
	if n := utf8.RuneCountInString(hostname); n > customHostnameMaxLen {
		return fmt.Errorf("--hostname is %d characters; the API allows at most %d", n, customHostnameMaxLen)
	}
	return nil
}

// --- create/update body builders -------------------------------------------

type customHostnameWriteOpts struct {
	Hostname           string // create only
	CustomOriginServer *string
	CustomOriginSNI    *string
	CustomMetadata     map[string]string
	// SSL fields; ssl set when any SSL flag is present (or defaults on create).
	SSLMethod string
	SSLType   string
	SSLBundle string
	SSLCA     string
	SSLMinTLS string
	SSLHTTP2  string
	SSLTLS13  string
	SSLEarly  string
	SSLWild   *bool
	SSLBrand  *bool
	// includeSSL forces an ssl object (create defaults to method=http, type=dv).
	IncludeSSL bool
}

func buildCustomHostnameSSLObject(o customHostnameWriteOpts) (map[string]any, error) {
	if o.SSLMethod != "" {
		if err := customHostnameEnum("ssl-method", o.SSLMethod, customHostnameSSLMethods); err != nil {
			return nil, err
		}
	}
	if o.SSLType != "" {
		if err := customHostnameEnum("ssl-type", o.SSLType, customHostnameSSLTypes); err != nil {
			return nil, err
		}
	}
	if o.SSLBundle != "" {
		if err := customHostnameEnum("bundle-method", o.SSLBundle, customHostnameBundleMeth); err != nil {
			return nil, err
		}
	}
	if o.SSLCA != "" {
		if err := customHostnameEnum("certificate-authority", o.SSLCA, customHostnameCreateCAs); err != nil {
			return nil, err
		}
	}
	if o.SSLMinTLS != "" {
		if err := customHostnameEnum("min-tls-version", o.SSLMinTLS, customHostnameMinTLS); err != nil {
			return nil, err
		}
	}
	if o.SSLHTTP2 != "" {
		if err := customHostnameEnum("http2", o.SSLHTTP2, customHostnameOnOff); err != nil {
			return nil, err
		}
	}
	if o.SSLTLS13 != "" {
		if err := customHostnameEnum("tls-1-3", o.SSLTLS13, customHostnameOnOff); err != nil {
			return nil, err
		}
	}
	if o.SSLEarly != "" {
		if err := customHostnameEnum("early-hints", o.SSLEarly, customHostnameOnOff); err != nil {
			return nil, err
		}
	}

	ssl := map[string]any{}
	if o.SSLMethod != "" {
		ssl["method"] = o.SSLMethod
	}
	if o.SSLType != "" {
		ssl["type"] = o.SSLType
	}
	if o.SSLBundle != "" {
		ssl["bundle_method"] = o.SSLBundle
	}
	if o.SSLCA != "" {
		ssl["certificate_authority"] = o.SSLCA
	}
	if o.SSLWild != nil {
		ssl["wildcard"] = *o.SSLWild
	}
	if o.SSLBrand != nil {
		ssl["cloudflare_branding"] = *o.SSLBrand
	}
	settings := map[string]any{}
	if o.SSLMinTLS != "" {
		settings["min_tls_version"] = o.SSLMinTLS
	}
	if o.SSLHTTP2 != "" {
		settings["http2"] = o.SSLHTTP2
	}
	if o.SSLTLS13 != "" {
		settings["tls_1_3"] = o.SSLTLS13
	}
	if o.SSLEarly != "" {
		settings["early_hints"] = o.SSLEarly
	}
	if len(settings) > 0 {
		ssl["settings"] = settings
	}
	return ssl, nil
}

// buildCustomHostnameCreateBody validates the create contract and returns the
// POST body. SSL defaults to method=http, type=dv (common SaaS path) unless
// the caller cleared IncludeSSL.
func buildCustomHostnameCreateBody(o customHostnameWriteOpts) ([]byte, error) {
	if err := validateCustomHostnameName(o.Hostname); err != nil {
		return nil, err
	}
	body := map[string]any{"hostname": o.Hostname}
	if o.CustomOriginServer != nil {
		if strings.TrimSpace(*o.CustomOriginServer) == "" {
			return nil, errors.New("--custom-origin-server must not be empty")
		}
		body["custom_origin_server"] = *o.CustomOriginServer
	}
	if o.CustomOriginSNI != nil {
		if strings.TrimSpace(*o.CustomOriginSNI) == "" {
			return nil, errors.New("--custom-origin-sni must not be empty")
		}
		body["custom_origin_sni"] = *o.CustomOriginSNI
	}
	if o.CustomMetadata != nil {
		body["custom_metadata"] = o.CustomMetadata
	}
	if o.IncludeSSL {
		// Apply create defaults when method/type were not set explicitly.
		if o.SSLMethod == "" {
			o.SSLMethod = "http"
		}
		if o.SSLType == "" {
			o.SSLType = "dv"
		}
		ssl, err := buildCustomHostnameSSLObject(o)
		if err != nil {
			return nil, err
		}
		body["ssl"] = ssl
	}
	return json.Marshal(body)
}

// buildCustomHostnameUpdateBody builds a partial PATCH body. Empty patch is
// rejected. No read-merge: the Edit endpoint accepts optional fields only.
func buildCustomHostnameUpdateBody(o customHostnameWriteOpts) ([]byte, error) {
	body := map[string]any{}
	if o.CustomOriginServer != nil {
		// Empty string is allowed on update to clear a custom origin (API
		// accepts optional string; clearing is a common SaaS workflow).
		body["custom_origin_server"] = *o.CustomOriginServer
	}
	if o.CustomOriginSNI != nil {
		body["custom_origin_sni"] = *o.CustomOriginSNI
	}
	if o.CustomMetadata != nil {
		body["custom_metadata"] = o.CustomMetadata
	}
	if o.IncludeSSL {
		ssl, err := buildCustomHostnameSSLObject(o)
		if err != nil {
			return nil, err
		}
		if len(ssl) == 0 {
			return nil, errors.New("ssl update requires at least one of --ssl-method, --ssl-type, --bundle-method, --certificate-authority, --wildcard, --cloudflare-branding, --min-tls-version, --http2, --tls-1-3, --early-hints")
		}
		body["ssl"] = ssl
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one of --custom-origin-server, --custom-origin-sni, --custom-metadata, or an SSL flag")
	}
	return json.Marshal(body)
}

func buildCustomHostnameFallbackBody(origin string) ([]byte, error) {
	if err := validateCustomHostnameOrigin(origin); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"origin": origin})
}

// collectCustomHostnameWriteFlags reads create/update flags into opts.
// validate/body build still run before any client work.
func collectCustomHostnameWriteFlags(cmd *cobra.Command, o *customHostnameWriteOpts, forCreate bool) error {
	if cmd.Flags().Changed("custom-origin-server") {
		v, _ := cmd.Flags().GetString("custom-origin-server")
		o.CustomOriginServer = &v
	}
	if cmd.Flags().Changed("custom-origin-sni") {
		v, _ := cmd.Flags().GetString("custom-origin-sni")
		o.CustomOriginSNI = &v
	}
	if cmd.Flags().Changed("custom-metadata") {
		raw, _ := cmd.Flags().GetString("custom-metadata")
		meta, err := parseCustomHostnameMetadata(raw)
		if err != nil {
			return err
		}
		o.CustomMetadata = meta
	}

	sslTouched := false
	if cmd.Flags().Changed("ssl-method") {
		o.SSLMethod, _ = cmd.Flags().GetString("ssl-method")
		sslTouched = true
	}
	if cmd.Flags().Changed("ssl-type") {
		o.SSLType, _ = cmd.Flags().GetString("ssl-type")
		sslTouched = true
	}
	if cmd.Flags().Changed("bundle-method") {
		o.SSLBundle, _ = cmd.Flags().GetString("bundle-method")
		sslTouched = true
	}
	if cmd.Flags().Changed("certificate-authority") {
		o.SSLCA, _ = cmd.Flags().GetString("certificate-authority")
		sslTouched = true
	}
	if cmd.Flags().Changed("min-tls-version") {
		o.SSLMinTLS, _ = cmd.Flags().GetString("min-tls-version")
		sslTouched = true
	}
	if cmd.Flags().Changed("http2") {
		o.SSLHTTP2, _ = cmd.Flags().GetString("http2")
		sslTouched = true
	}
	if cmd.Flags().Changed("tls-1-3") {
		o.SSLTLS13, _ = cmd.Flags().GetString("tls-1-3")
		sslTouched = true
	}
	if cmd.Flags().Changed("early-hints") {
		o.SSLEarly, _ = cmd.Flags().GetString("early-hints")
		sslTouched = true
	}
	if cmd.Flags().Changed("wildcard") {
		v, _ := cmd.Flags().GetBool("wildcard")
		o.SSLWild = &v
		sslTouched = true
	}
	if cmd.Flags().Changed("cloudflare-branding") {
		v, _ := cmd.Flags().GetBool("cloudflare-branding")
		o.SSLBrand = &v
		sslTouched = true
	}
	if forCreate {
		// Create always requests an SSL certificate unless --no-ssl.
		noSSL, _ := cmd.Flags().GetBool("no-ssl")
		o.IncludeSSL = !noSSL
		if noSSL && sslTouched {
			return errors.New("--no-ssl cannot be combined with SSL flags")
		}
	} else {
		o.IncludeSSL = sslTouched
	}
	return nil
}

func bindCustomHostnameWriteFlags(cmd *cobra.Command, forCreate bool) {
	f := cmd.Flags()
	f.String("custom-origin-server", "", "origin hostname in this zone (A/AAAA/CNAME) for this custom hostname")
	f.String("custom-origin-sni", "", "SNI hostname sent to the custom origin (or :request_host_header:)")
	f.String("custom-metadata", "", `JSON object of string metadata, e.g. '{"customer_id":"abc"}'`)
	f.String("ssl-method", "", "DCV method: http, txt, or email")
	f.String("ssl-type", "", "domain validation type (only dv)")
	f.String("bundle-method", "", "certificate bundle method: ubiquitous, optimal, or force")
	f.String("certificate-authority", "", "issuing CA: digicert, google, lets_encrypt, or ssl_com")
	f.String("min-tls-version", "", "minimum TLS version: 1.0, 1.1, 1.2, or 1.3")
	f.String("http2", "", "enable HTTP/2 on the certificate: on or off")
	f.String("tls-1-3", "", "enable TLS 1.3 on the certificate: on or off")
	f.String("early-hints", "", "enable Early Hints: on or off")
	f.Bool("wildcard", false, "request a wildcard certificate covering the hostname")
	f.Bool("cloudflare-branding", false, "add Cloudflare branding (sni.cloudflaressl.com CN) for long hostnames")
	if forCreate {
		f.Bool("no-ssl", false, "create the hostname without requesting an SSL certificate")
	}
}

// --- list ------------------------------------------------------------------

func newCustomHostnameListCmd(g *globalOpts) *cobra.Command {
	var zone, id, hostname, hostnameStatus, sslStatus, order, direction, ca string
	var wildcard bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List custom hostnames in a zone",
		Long: `List custom hostnames in a zone.

Examples:

  cf custom-hostnames list --zone example.com
  cf custom-hostnames list --zone example.com --hostname app.customer.com
  cf custom-hostnames list --zone example.com --status pending --ssl-status pending_validation`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Pinned list descriptions: id cannot be used with hostname (or
			// hostname.exact/contain/startsWith). Porcelain exposes --hostname only.
			if id != "" && hostname != "" {
				return errors.New("--id cannot be combined with --hostname")
			}
			if id != "" {
				if err := validateCustomHostnameListID(id); err != nil {
					return err
				}
			}
			if hostname != "" {
				if err := validateCustomHostnameListHostname(hostname); err != nil {
					return err
				}
			}
			if hostnameStatus != "" {
				if err := customHostnameEnum("status", hostnameStatus, customHostnameStatuses); err != nil {
					return err
				}
			}
			if sslStatus != "" {
				if err := customHostnameEnum("ssl-status", sslStatus, customHostnameSSLStatuses); err != nil {
					return err
				}
			}
			if order != "" {
				if err := customHostnameEnum("order", order, customHostnameOrders); err != nil {
					return err
				}
			}
			if direction != "" {
				if err := customHostnameEnum("direction", direction, customHostnameDirs); err != nil {
					return err
				}
			}
			if ca != "" {
				if err := customHostnameEnum("certificate-authority", ca, customHostnameListCAs); err != nil {
					return err
				}
			}

			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}

			q := url.Values{}
			q.Set("per_page", customHostnameListPerPage)
			if id != "" {
				q.Set("id", id)
			}
			if hostname != "" {
				q.Set("hostname", hostname)
			}
			if hostnameStatus != "" {
				q.Set("hostname_status", hostnameStatus)
			}
			if sslStatus != "" {
				q.Set("ssl_status", sslStatus)
			}
			if order != "" {
				q.Set("order", order)
			}
			if direction != "" {
				q.Set("direction", direction)
			}
			if ca != "" {
				q.Set("certificate_authority", ca)
			}
			if cmd.Flags().Changed("wildcard") {
				if wildcard {
					q.Set("wildcard", "true")
				} else {
					q.Set("wildcard", "false")
				}
			}

			req := api.Request{Method: "GET", Path: customHostnamesPath(zoneID), Query: q}
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
			var rows []customHostname
			if err := json.Unmarshal(env.Result, &rows); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			table := make([][]string, 0, len(rows))
			for _, h := range rows {
				sslSt := ""
				if h.SSL != nil {
					sslSt = h.SSL.Status
				}
				table = append(table, []string{
					h.ID,
					output.Cell(h.Hostname),
					h.Status,
					sslSt,
					output.Cell(h.CustomOriginServer),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "HOSTNAME", "STATUS", "SSL", "ORIGIN"}, table)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&id, "id", "", "filter by custom hostname ID (36 characters; mutually exclusive with --hostname)")
	cmd.Flags().StringVar(&hostname, "hostname", "", "filter by exact hostname (mutually exclusive with --id)")
	cmd.Flags().StringVar(&hostnameStatus, "status", "", "filter by hostname activation status")
	cmd.Flags().StringVar(&sslStatus, "ssl-status", "", "filter by SSL certificate status")
	cmd.Flags().StringVar(&order, "order", "", "order by: ssl or ssl_status")
	cmd.Flags().StringVar(&direction, "direction", "", "sort direction: asc or desc")
	cmd.Flags().StringVar(&ca, "certificate-authority", "", "filter by CA: google, lets_encrypt, or ssl_com")
	cmd.Flags().BoolVar(&wildcard, "wildcard", false, "filter by wildcard certificate flag")
	return cmd
}

// --- get -------------------------------------------------------------------

func newCustomHostnameGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get <custom-hostname-id>",
		Short: "Show one custom hostname",
		Long: `Show full details for a custom hostname, including SSL and ownership verification.

Examples:

  cf custom-hostnames get 023e105f4ecef8ad9ca31a8372d0c353 --zone example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("custom-hostname-id must not be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: customHostnamePath(zoneID, args[0])}
			return runCustomHostnameRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

// --- create ----------------------------------------------------------------

func newCustomHostnameCreateCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "create <hostname>",
		Short: "Create a custom hostname and request SSL",
		Long: `Create a custom hostname and request an SSL certificate (method=http, type=dv by default).

Examples:

  cf custom-hostnames create app.customer.com --zone example.com
  cf custom-hostnames create app.customer.com --zone example.com --ssl-method txt --custom-origin-server origin.example.com
  cf custom-hostnames create long-hostname.customer.com --zone example.com --cloudflare-branding`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var o customHostnameWriteOpts
			o.Hostname = args[0]
			if err := collectCustomHostnameWriteFlags(cmd, &o, true); err != nil {
				return err
			}
			body, err := buildCustomHostnameCreateBody(o)
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
			req := api.Request{Method: "POST", Path: customHostnamesPath(zoneID), Body: body}
			return runCustomHostnameRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindCustomHostnameWriteFlags(cmd, true)
	return cmd
}

// --- update ----------------------------------------------------------------

func newCustomHostnameUpdateCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "update <custom-hostname-id>",
		Short: "Update fields of a custom hostname",
		Long: `Patch selected fields of a custom hostname (true partial PATCH; no read-merge).

Sending SSL method/type matching the existing configuration re-triggers DCV
when the hostname is pending validation.

Examples:

  cf custom-hostnames update 023e105f4ecef8ad9ca31a8372d0c353 --zone example.com --custom-origin-server origin2.example.com
  cf custom-hostnames update 023e105f4ecef8ad9ca31a8372d0c353 --zone example.com --ssl-method http --ssl-type dv
  cf custom-hostnames update 023e105f4ecef8ad9ca31a8372d0c353 --zone example.com --min-tls-version 1.2 --http2 on`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("custom-hostname-id must not be empty")
			}
			var o customHostnameWriteOpts
			if err := collectCustomHostnameWriteFlags(cmd, &o, false); err != nil {
				return err
			}
			body, err := buildCustomHostnameUpdateBody(o)
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
			req := api.Request{Method: "PATCH", Path: customHostnamePath(zoneID, args[0]), Body: body}
			return runCustomHostnameRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindCustomHostnameWriteFlags(cmd, false)
	return cmd
}

// --- delete ----------------------------------------------------------------

func newCustomHostnameDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <custom-hostname-id>",
		Short: "Delete a custom hostname and its SSL certificates",
		Long: `Delete a custom hostname and any issued SSL certificates.

Examples:

  cf custom-hostnames delete 023e105f4ecef8ad9ca31a8372d0c353 --zone example.com --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("custom-hostname-id must not be empty")
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
				if !confirm(fmt.Sprintf("Delete custom hostname %s (and issued SSL certificates) from zone %s?", args[0], zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: customHostnamePath(zoneID, args[0])}
			return runCustomHostnameRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- ssl status ------------------------------------------------------------

func newCustomHostnameSSLCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "ssl <custom-hostname-id>",
		Short: "Show SSL status for a custom hostname",
		Long: `Show the SSL certificate status and validation details for a custom hostname.

Table output summarizes status, method, CA, and expiry; use --output json for
the full ssl object (validation records, errors, settings).

Examples:

  cf custom-hostnames ssl 023e105f4ecef8ad9ca31a8372d0c353 --zone example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("custom-hostname-id must not be empty")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: customHostnamePath(zoneID, args[0])}
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
			var host customHostname
			if err := json.Unmarshal(env.Result, &host); err != nil {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			if host.SSL == nil {
				return errors.New("custom hostname has no ssl object in the API response")
			}
			sslRaw, err := json.Marshal(host.SSL)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, sslRaw, output.JSON)
			}
			ssl := host.SSL
			ca := ssl.CertificateAuthority
			method := ssl.Method
			wild := ""
			if ssl.Wildcard != nil {
				if *ssl.Wildcard {
					wild = "true"
				} else {
					wild = "false"
				}
			}
			valErr := ""
			if len(ssl.ValidationErrors) > 0 {
				valErr = ssl.ValidationErrors[0].Message
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"HOSTNAME", "SSL_STATUS", "METHOD", "TYPE", "CA", "WILDCARD", "EXPIRES", "VALIDATION_ERROR"},
				[][]string{{
					output.Cell(host.Hostname),
					ssl.Status,
					method,
					ssl.Type,
					ca,
					wild,
					ssl.ExpiresOn,
					output.Cell(valErr),
				}})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

// --- fallback origin -------------------------------------------------------

func newCustomHostnameFallbackOriginCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fallback-origin",
		Short: "Get or set the zone fallback origin for custom hostnames",
	}
	cmd.AddCommand(
		newCustomHostnameFallbackGetCmd(g),
		newCustomHostnameFallbackSetCmd(g),
	)
	return cmd
}

func newCustomHostnameFallbackGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show the fallback origin for custom hostnames",
		Long: `Show the zone's fallback origin used when a custom hostname has no custom origin.

Examples:

  cf custom-hostnames fallback-origin get --zone example.com`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: customHostnameFallbackPath(zoneID)}
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
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var fo customHostnameFallback
			if err := json.Unmarshal(env.Result, &fo); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			errCell := ""
			if len(fo.Errors) > 0 {
				errCell = fo.Errors[0]
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ORIGIN", "STATUS", "UPDATED", "ERROR"},
				[][]string{{output.Cell(fo.Origin), fo.Status, fo.UpdatedAt, output.Cell(errCell)}})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newCustomHostnameFallbackSetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "set <origin>",
		Short: "Set the fallback origin for custom hostnames",
		Long: `Set the zone fallback origin hostname (must be a proxied A/AAAA/CNAME in the zone).

Examples:

  cf custom-hostnames fallback-origin set fallback.example.com --zone example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildCustomHostnameFallbackBody(args[0])
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
			req := api.Request{Method: "PUT", Path: customHostnameFallbackPath(zoneID), Body: body}
			return runCustomHostnameRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}
