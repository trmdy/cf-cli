package cli

// Hyperdrive porcelain manages account-level database connection configs.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type hyperdriveConfig struct {
	ID                    string          `json:"id"`
	Name                  string          `json:"name"`
	Origin                json.RawMessage `json:"origin"`
	OriginConnectionLimit int             `json:"origin_connection_limit"`
	Caching               struct {
		Disabled bool `json:"disabled"`
	} `json:"caching"`
}

type hyperdriveOriginFlags struct {
	host, database, user, password, scheme string
	port                                   int
	accessClientID, accessClientSecret     string
	serviceID                              string
}

type hyperdriveSettingsFlags struct {
	cachingDisabled                    bool
	cacheMaxAge, staleWhileRevalidate  int
	caCertificateID, mtlsCertificateID string
	sslmode                            string
	originConnectionLimit              int
}

func newHyperdriveCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hyperdrive",
		Short: "Manage Hyperdrive database connection configs",
	}
	cmd.AddCommand(
		newHyperdriveListCmd(g),
		newHyperdriveGetCmd(g),
		newHyperdriveCreateCmd(g),
		newHyperdriveUpdateCmd(g),
		newHyperdriveDeleteCmd(g),
	)
	return cmd
}

func hyperdrivePath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/hyperdrive/configs"
}

func requireHyperdriveAccountID(accountID string) error {
	if accountID == "" {
		return errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return nil
}

func newHyperdriveListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Hyperdrive configs",
		Long:  "List Hyperdrive configs for the configured account.\n\nExamples:\n\n  cf hyperdrive list --account-id 023e105f4ecef8ad9ca31a8372d0c353",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireHyperdriveAccountID(cfg.AccountID); err != nil {
				return err
			}
			q := url.Values{"per_page": []string{"100"}}
			req := api.Request{Method: "GET", Path: hyperdrivePath(cfg.AccountID), Query: q}
			if g.DryRun {
				return runHyperdriveRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var configs []hyperdriveConfig
			if err := json.Unmarshal(env.Result, &configs); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(configs))
			for _, config := range configs {
				rows = append(rows, []string{
					config.ID,
					config.Name,
					output.Cell(hyperdriveOriginLabel(config.Origin)),
					fmt.Sprintf("%t", config.Caching.Disabled),
					fmt.Sprintf("%d", config.OriginConnectionLimit),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "ORIGIN", "CACHE DISABLED", "CONNECTION LIMIT"}, rows)
		},
	}
	return cmd
}

func hyperdriveOriginLabel(raw json.RawMessage) string {
	var origin struct {
		Host      string `json:"host"`
		ServiceID string `json:"service_id"`
		Database  string `json:"database"`
	}
	if err := json.Unmarshal(raw, &origin); err != nil {
		return string(raw)
	}
	endpoint := origin.Host
	if endpoint == "" {
		endpoint = origin.ServiceID
	}
	if endpoint == "" {
		return origin.Database
	}
	if origin.Database == "" {
		return endpoint
	}
	return endpoint + "/" + origin.Database
}

func newHyperdriveGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <config-id>",
		Short: "Show a Hyperdrive config",
		Long:  "Show a Hyperdrive config.\n\nExamples:\n\n  cf hyperdrive get 023e105f4ecef8ad9ca31a8372d0c353",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireHyperdriveAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: hyperdrivePath(cfg.AccountID) + "/" + url.PathEscape(args[0])}
			return runHyperdriveRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newHyperdriveCreateCmd(g *globalOpts) *cobra.Command {
	var origin hyperdriveOriginFlags
	var settings hyperdriveSettingsFlags
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Hyperdrive config",
		Long: `Create a Hyperdrive config for a database origin.

Examples:

  cf hyperdrive create app-db --host db.example.com --database app --user app --password "$DB_PASSWORD" --scheme postgres
  cf hyperdrive create app-db --service-id 023e105f4ecef8ad9ca31a8372d0c353 --database app --user app --password "$DB_PASSWORD" --scheme postgres`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildHyperdriveCreateBody(cmd, args[0], origin, settings)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireHyperdriveAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: hyperdrivePath(cfg.AccountID), Body: body}
			return runHyperdriveRequest(cmd, g, client, req)
		},
	}
	bindHyperdriveOriginFlags(cmd, &origin)
	bindHyperdriveSettingsFlags(cmd, &settings)
	return cmd
}

func newHyperdriveUpdateCmd(g *globalOpts) *cobra.Command {
	var name string
	var origin hyperdriveOriginFlags
	var settings hyperdriveSettingsFlags
	cmd := &cobra.Command{
		Use:   "update <config-id>",
		Short: "Update fields of a Hyperdrive config",
		Long: `Update selected fields of a Hyperdrive config.

Examples:

  cf hyperdrive update 023e105f4ecef8ad9ca31a8372d0c353 --cache-max-age 120 --stale-while-revalidate 30
  cf hyperdrive update 023e105f4ecef8ad9ca31a8372d0c353 --connection-limit 80`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildHyperdriveUpdateBody(cmd, name, origin, settings)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireHyperdriveAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: hyperdrivePath(cfg.AccountID) + "/" + url.PathEscape(args[0]), Body: body}
			return runHyperdriveRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "config name")
	bindHyperdriveOriginFlags(cmd, &origin)
	bindHyperdriveSettingsFlags(cmd, &settings)
	return cmd
}

func newHyperdriveDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <config-id>",
		Short: "Delete a Hyperdrive config",
		Long:  "Delete a Hyperdrive config.\n\nExamples:\n\n  cf hyperdrive delete 023e105f4ecef8ad9ca31a8372d0c353 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := requireHyperdriveAccountID(cfg.AccountID); err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Hyperdrive config %s from account %s?", args[0], cfg.AccountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: hyperdrivePath(cfg.AccountID) + "/" + url.PathEscape(args[0])}
			return runHyperdriveRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func bindHyperdriveOriginFlags(cmd *cobra.Command, origin *hyperdriveOriginFlags) {
	flags := cmd.Flags()
	flags.StringVar(&origin.host, "host", "", "database hostname or IP address")
	flags.StringVar(&origin.database, "database", "", "database name")
	flags.StringVar(&origin.user, "user", "", "database user")
	flags.StringVar(&origin.password, "password", "", "database password")
	flags.StringVar(&origin.scheme, "scheme", "", "database scheme: postgres, postgresql, or mysql")
	flags.IntVar(&origin.port, "port", 0, "database port")
	flags.StringVar(&origin.accessClientID, "access-client-id", "", "Cloudflare Access client ID for the origin")
	flags.StringVar(&origin.accessClientSecret, "access-client-secret", "", "Cloudflare Access client secret for the origin")
	flags.StringVar(&origin.serviceID, "service-id", "", "Workers VPC Service ID for the origin")
}

func bindHyperdriveSettingsFlags(cmd *cobra.Command, settings *hyperdriveSettingsFlags) {
	flags := cmd.Flags()
	flags.BoolVar(&settings.cachingDisabled, "caching-disabled", false, "disable SQL response caching")
	flags.IntVar(&settings.cacheMaxAge, "cache-max-age", 0, "cache max age in seconds")
	flags.IntVar(&settings.staleWhileRevalidate, "stale-while-revalidate", 0, "serve stale cached responses for this many seconds")
	flags.StringVar(&settings.caCertificateID, "ca-certificate-id", "", "CA certificate ID for mTLS")
	flags.StringVar(&settings.mtlsCertificateID, "mtls-certificate-id", "", "client certificate ID for mTLS")
	flags.StringVar(&settings.sslmode, "sslmode", "", "TLS mode: require, verify-ca, or verify-full")
	flags.IntVar(&settings.originConnectionLimit, "connection-limit", 0, "maximum connections to the origin (minimum 5)")
}

func buildHyperdriveCreateBody(cmd *cobra.Command, name string, origin hyperdriveOriginFlags, settings hyperdriveSettingsFlags) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("config name must not be empty")
	}
	originBody, err := buildHyperdriveCreateOrigin(origin)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"name": name, "origin": originBody}
	if err := addHyperdriveSettings(cmd, body, settings, origin.serviceID != ""); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func buildHyperdriveCreateOrigin(origin hyperdriveOriginFlags) (map[string]any, error) {
	scheme, err := normalizeHyperdriveScheme(origin.scheme)
	if err != nil {
		return nil, err
	}
	if origin.serviceID != "" {
		if origin.host != "" || origin.port != 0 || origin.accessClientID != "" || origin.accessClientSecret != "" {
			return nil, errors.New("--service-id cannot be combined with --host, --port, or Access credentials")
		}
		if err := requireHyperdriveFields("--database, --user, --password, and --scheme are required for a VPC Service origin", origin.database, origin.user, origin.password); err != nil {
			return nil, err
		}
		return map[string]any{"service_id": origin.serviceID, "database": origin.database, "user": origin.user, "password": origin.password, "scheme": scheme}, nil
	}
	if err := requireHyperdriveFields("--host, --database, --user, --password, and --scheme are required", origin.host, origin.database, origin.user, origin.password); err != nil {
		return nil, err
	}
	if (origin.accessClientID == "") != (origin.accessClientSecret == "") {
		return nil, errors.New("--access-client-id and --access-client-secret must be provided together")
	}
	body := map[string]any{"host": origin.host, "database": origin.database, "user": origin.user, "password": origin.password, "scheme": scheme}
	if origin.port != 0 {
		if origin.port < 1 || origin.port > 65535 {
			return nil, errors.New("--port must be between 1 and 65535")
		}
		body["port"] = origin.port
	}
	if origin.accessClientID != "" {
		body["access_client_id"] = origin.accessClientID
		body["access_client_secret"] = origin.accessClientSecret
	}
	return body, nil
}

func requireHyperdriveFields(message string, fields ...string) error {
	for _, field := range fields {
		if strings.TrimSpace(field) == "" {
			return errors.New(message)
		}
	}
	return nil
}

func normalizeHyperdriveScheme(scheme string) (string, error) {
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	switch scheme {
	case "postgres", "postgresql", "mysql":
		return scheme, nil
	default:
		return "", errors.New("--scheme must be postgres, postgresql, or mysql")
	}
}

func buildHyperdriveUpdateBody(cmd *cobra.Command, name string, origin hyperdriveOriginFlags, settings hyperdriveSettingsFlags) ([]byte, error) {
	body := map[string]any{}
	if cmd.Flags().Changed("name") {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("--name must not be empty")
		}
		body["name"] = name
	}
	originBody, err := buildHyperdriveOriginPatch(cmd, origin)
	if err != nil {
		return nil, err
	}
	if len(originBody) > 0 {
		body["origin"] = originBody
	}
	if err := addHyperdriveSettings(cmd, body, settings, cmd.Flags().Changed("service-id")); err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one config, origin, caching, mTLS, or connection-limit flag")
	}
	return json.Marshal(body)
}

func buildHyperdriveOriginPatch(cmd *cobra.Command, origin hyperdriveOriginFlags) (map[string]any, error) {
	if cmd.Flags().Changed("service-id") && (cmd.Flags().Changed("host") || cmd.Flags().Changed("port") || cmd.Flags().Changed("access-client-id") || cmd.Flags().Changed("access-client-secret")) {
		return nil, errors.New("--service-id cannot be updated with --host, --port, or Access credentials")
	}
	if cmd.Flags().Changed("port") && (origin.port < 1 || origin.port > 65535) {
		return nil, errors.New("--port must be between 1 and 65535")
	}
	if cmd.Flags().Changed("scheme") {
		scheme, err := normalizeHyperdriveScheme(origin.scheme)
		if err != nil {
			return nil, err
		}
		origin.scheme = scheme
	}
	patch := map[string]any{}
	for flag, value := range map[string]any{
		"host":                 origin.host,
		"database":             origin.database,
		"user":                 origin.user,
		"password":             origin.password,
		"scheme":               origin.scheme,
		"port":                 origin.port,
		"access-client-id":     origin.accessClientID,
		"access-client-secret": origin.accessClientSecret,
		"service-id":           origin.serviceID,
	} {
		if cmd.Flags().Changed(flag) {
			patch[strings.ReplaceAll(flag, "-", "_")] = value
		}
	}
	return patch, nil
}

func addHyperdriveSettings(cmd *cobra.Command, body map[string]any, settings hyperdriveSettingsFlags, vpcOrigin bool) error {
	if cmd.Flags().Changed("connection-limit") {
		if settings.originConnectionLimit < 5 || settings.originConnectionLimit > 100 {
			return errors.New("--connection-limit must be between 5 and 100")
		}
		body["origin_connection_limit"] = settings.originConnectionLimit
	}
	if cmd.Flags().Changed("cache-max-age") && settings.cacheMaxAge < 0 {
		return errors.New("--cache-max-age must not be negative")
	}
	if cmd.Flags().Changed("stale-while-revalidate") && settings.staleWhileRevalidate < 0 {
		return errors.New("--stale-while-revalidate must not be negative")
	}
	if cmd.Flags().Changed("caching-disabled") || cmd.Flags().Changed("cache-max-age") || cmd.Flags().Changed("stale-while-revalidate") {
		if cmd.Flags().Changed("caching-disabled") && settings.cachingDisabled && (cmd.Flags().Changed("cache-max-age") || cmd.Flags().Changed("stale-while-revalidate")) {
			return errors.New("cache durations cannot be set when --caching-disabled=true")
		}
		caching := map[string]any{}
		if cmd.Flags().Changed("caching-disabled") {
			caching["disabled"] = settings.cachingDisabled
		}
		if cmd.Flags().Changed("cache-max-age") {
			caching["max_age"] = settings.cacheMaxAge
		}
		if cmd.Flags().Changed("stale-while-revalidate") {
			caching["stale_while_revalidate"] = settings.staleWhileRevalidate
		}
		body["caching"] = caching
	}
	if cmd.Flags().Changed("ca-certificate-id") || cmd.Flags().Changed("mtls-certificate-id") || cmd.Flags().Changed("sslmode") {
		if vpcOrigin {
			return errors.New("mTLS settings cannot be used with a Workers VPC Service origin")
		}
		if cmd.Flags().Changed("sslmode") {
			sslmode, err := normalizeHyperdriveSSLMode(settings.sslmode)
			if err != nil {
				return err
			}
			settings.sslmode = sslmode
		}
		mtls := map[string]any{}
		if cmd.Flags().Changed("ca-certificate-id") {
			mtls["ca_certificate_id"] = settings.caCertificateID
		}
		if cmd.Flags().Changed("mtls-certificate-id") {
			mtls["mtls_certificate_id"] = settings.mtlsCertificateID
		}
		if cmd.Flags().Changed("sslmode") {
			mtls["sslmode"] = settings.sslmode
		}
		body["mtls"] = mtls
	}
	return nil
}

func normalizeHyperdriveSSLMode(mode string) (string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "require", "verify-ca", "verify-full":
		return mode, nil
	default:
		return "", errors.New("--sslmode must be require, verify-ca, or verify-full")
	}
}

func runHyperdriveRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
