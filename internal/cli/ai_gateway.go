package cli

// AI Gateway porcelain: gateway CRUD, log inspection, and datasets.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// Bounds and enums are from the pinned Cloudflare OpenAPI schema
// (aig-config-{create,update}-gateway and aig-config-{create,update}-dataset).
const (
	aiGatewayIDMaxLength      = 64
	aiGatewayLogManagementMin = 10000
	aiGatewayLogManagementMax = 10000000
	aiGatewayLogpushPublicMin = 16
	aiGatewayLogpushPublicMax = 1024
	aiGatewayRetryDelayMax    = 5000
	aiGatewayRetryAttemptsMin = 1
	aiGatewayRetryAttemptsMax = 5
	aiGatewayLogsPerPage      = 50
	aiGatewayLogsMaxPages     = 1000
)

var (
	aiGatewayIDPattern      = regexp.MustCompile(`^[a-z0-9_]+(?:-[a-z0-9_]+)*$`)
	aiGatewayLogOrders      = []string{"created_at", "provider", "model", "model_type", "success", "cached"}
	aiGatewayDirections     = []string{"asc", "desc"}
	aiGatewayRateTechniques = []string{"fixed", "sliding"}
	aiGatewayRetryBackoffs  = []string{"constant", "linear", "exponential"}
	aiGatewayLogStrategies  = []string{"STOP_INSERTING", "DELETE_OLDEST"}
	aiGatewayBillingModes   = []string{"postpaid", "unified"}
	aiGatewayLogFilterKeys  = []string{
		"id", "created_at", "request_content_type", "response_content_type", "request_type", "success", "cached",
		"provider", "model", "model_type", "cost", "tokens", "tokens_in", "tokens_out", "duration", "feedback",
		"event_id", "metadata.key", "metadata.value", "authentication", "wholesale", "compatibilityMode", "dlp_action", "user_agent",
	}
	aiGatewayDatasetFilterKeys = []string{
		"created_at", "request_content_type", "response_content_type", "success", "cached", "provider", "model",
		"cost", "tokens", "tokens_in", "tokens_out", "duration", "feedback",
	}
	aiGatewayLogFilterOperators     = []string{"eq", "neq", "contains", "lt", "gt"}
	aiGatewayDatasetFilterOperators = []string{"eq", "contains", "lt", "gt"}
)

type aiGateway struct {
	ID          string `json:"id,omitempty"`
	CollectLogs *bool  `json:"collect_logs,omitempty"`
	CacheTTL    *int   `json:"cache_ttl,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type aiGatewayLog struct {
	ID        string  `json:"id,omitempty"`
	CreatedAt string  `json:"created_at,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	Model     string  `json:"model,omitempty"`
	Duration  int     `json:"duration,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
	Success   bool    `json:"success"`
	Cached    bool    `json:"cached"`
}

type aiGatewayDataset struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Enable   bool   `json:"enable"`
	Modified string `json:"modified_at,omitempty"`
}

type aiGatewayGatewayFlags struct {
	authentication, collectLogs, cacheInvalidate, logpush, zdr bool
	cacheTTL, rateLimitInterval, rateLimitLimit                int
	logManagement, retryDelay, retryAttempts                   int
	rateTechnique, logStrategy, logpushPublicKey               string
	retryBackoff, billingMode                                  string
}

func newAIGatewayCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai-gateway",
		Short: "Manage AI Gateways, logs, and datasets",
		Long:  "Manage AI Gateways, their request logs, and saved datasets.\n\nExample:\n\n  cf ai-gateway list",
	}
	cmd.AddCommand(
		newAIGatewayListCmd(g),
		newAIGatewayGetCmd(g),
		newAIGatewayCreateCmd(g),
		newAIGatewayUpdateCmd(g),
		newAIGatewayDeleteCmd(g),
		newAIGatewayLogsCmd(g),
		newAIGatewayDatasetsCmd(g),
	)
	return cmd
}

func aiGatewayPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/ai-gateway/gateways"
}

func aiGatewayItemPath(accountID, gatewayID string) string {
	return aiGatewayPath(accountID) + "/" + url.PathEscape(gatewayID)
}

func aiGatewayLogsPath(accountID, gatewayID string) string {
	return aiGatewayItemPath(accountID, gatewayID) + "/logs"
}

func aiGatewayDatasetsPath(accountID, gatewayID string) string {
	return aiGatewayItemPath(accountID, gatewayID) + "/datasets"
}

func aiGatewayDatasetPath(accountID, gatewayID, datasetID string) string {
	return aiGatewayDatasetsPath(accountID, gatewayID) + "/" + url.PathEscape(datasetID)
}

func aiGatewayClient(g *globalOpts) (*api.Client, string, error) {
	cfg, err := g.resolve()
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(cfg.AccountID) == "" {
		return nil, "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	if !g.DryRun && cfg.Token == "" {
		return nil, "", errors.New("no API token found; run `cf auth login`, set CLOUDFLARE_API_TOKEN, or pass --token")
	}
	return api.New(g.BaseURL, cfg.Token, Version), cfg.AccountID, nil
}

func runAIGatewayRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func aiGatewayValidateID(label, id string) error {
	if len(id) == 0 || len(id) > aiGatewayIDMaxLength || !aiGatewayIDPattern.MatchString(id) {
		return fmt.Errorf("%s must be 1-%d lowercase letters, numbers, underscores, or hyphen-separated segments", label, aiGatewayIDMaxLength)
	}
	return nil
}

func aiGatewayValidateNonEmpty(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", label)
	}
	return nil
}

func aiGatewayValidateEnum(flag, value string, allowed []string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

func aiGatewayValidateGatewayFlags(cmd *cobra.Command, values aiGatewayGatewayFlags, creating bool) error {
	if creating {
		for _, name := range []string{"rate-limit-interval", "rate-limit-limit", "collect-logs", "cache-ttl", "cache-invalidate-on-update"} {
			if !cmd.Flags().Changed(name) {
				return fmt.Errorf("--%s is required", name)
			}
		}
	}
	for _, item := range []struct {
		flag  string
		value int
	}{
		{"rate-limit-interval", values.rateLimitInterval},
		{"rate-limit-limit", values.rateLimitLimit},
		{"cache-ttl", values.cacheTTL},
	} {
		if cmd.Flags().Changed(item.flag) && item.value < 0 {
			return fmt.Errorf("--%s must be at least 0", item.flag)
		}
	}
	if cmd.Flags().Changed("log-management") && (values.logManagement < aiGatewayLogManagementMin || values.logManagement > aiGatewayLogManagementMax) {
		return fmt.Errorf("--log-management must be between %d and %d", aiGatewayLogManagementMin, aiGatewayLogManagementMax)
	}
	if cmd.Flags().Changed("logpush-public-key") && (len(values.logpushPublicKey) < aiGatewayLogpushPublicMin || len(values.logpushPublicKey) > aiGatewayLogpushPublicMax) {
		return fmt.Errorf("--logpush-public-key must be between %d and %d characters", aiGatewayLogpushPublicMin, aiGatewayLogpushPublicMax)
	}
	if cmd.Flags().Changed("retry-delay") && (values.retryDelay < 0 || values.retryDelay > aiGatewayRetryDelayMax) {
		return fmt.Errorf("--retry-delay must be between 0 and %d", aiGatewayRetryDelayMax)
	}
	if cmd.Flags().Changed("retry-max-attempts") && (values.retryAttempts < aiGatewayRetryAttemptsMin || values.retryAttempts > aiGatewayRetryAttemptsMax) {
		return fmt.Errorf("--retry-max-attempts must be between %d and %d", aiGatewayRetryAttemptsMin, aiGatewayRetryAttemptsMax)
	}
	for _, item := range []struct {
		flag, value string
		allowed     []string
	}{
		{"rate-limit-technique", values.rateTechnique, aiGatewayRateTechniques},
		{"log-management-strategy", values.logStrategy, aiGatewayLogStrategies},
		{"retry-backoff", values.retryBackoff, aiGatewayRetryBackoffs},
		{"workers-ai-billing-mode", values.billingMode, aiGatewayBillingModes},
	} {
		if cmd.Flags().Changed(item.flag) {
			if err := aiGatewayValidateEnum(item.flag, item.value, item.allowed); err != nil {
				return err
			}
		}
	}
	return nil
}

func aiGatewayGatewayPatch(cmd *cobra.Command, values aiGatewayGatewayFlags) map[string]any {
	patch := map[string]any{}
	for _, item := range []struct {
		flag, key string
		value     any
	}{
		{"authentication", "authentication", values.authentication},
		{"collect-logs", "collect_logs", values.collectLogs},
		{"cache-invalidate-on-update", "cache_invalidate_on_update", values.cacheInvalidate},
		{"cache-ttl", "cache_ttl", values.cacheTTL},
		{"rate-limit-interval", "rate_limiting_interval", values.rateLimitInterval},
		{"rate-limit-limit", "rate_limiting_limit", values.rateLimitLimit},
		{"rate-limit-technique", "rate_limiting_technique", values.rateTechnique},
		{"log-management", "log_management", values.logManagement},
		{"log-management-strategy", "log_management_strategy", values.logStrategy},
		{"logpush", "logpush", values.logpush},
		{"logpush-public-key", "logpush_public_key", values.logpushPublicKey},
		{"retry-backoff", "retry_backoff", values.retryBackoff},
		{"retry-delay", "retry_delay", values.retryDelay},
		{"retry-max-attempts", "retry_max_attempts", values.retryAttempts},
		{"workers-ai-billing-mode", "workers_ai_billing_mode", values.billingMode},
		{"zdr", "zdr", values.zdr},
	} {
		if cmd.Flags().Changed(item.flag) {
			patch[item.key] = item.value
		}
	}
	return patch
}

func addAIGatewayGatewayFlags(cmd *cobra.Command, values *aiGatewayGatewayFlags) {
	f := cmd.Flags()
	f.BoolVar(&values.authentication, "authentication", false, "require authentication for the gateway")
	f.BoolVar(&values.collectLogs, "collect-logs", false, "collect request logs")
	f.BoolVar(&values.cacheInvalidate, "cache-invalidate-on-update", false, "invalidate cached responses after updates")
	f.IntVar(&values.cacheTTL, "cache-ttl", 0, "cache TTL in seconds (at least 0)")
	f.IntVar(&values.rateLimitInterval, "rate-limit-interval", 0, "rate limit interval in seconds (at least 0)")
	f.IntVar(&values.rateLimitLimit, "rate-limit-limit", 0, "rate limit request limit (at least 0)")
	f.StringVar(&values.rateTechnique, "rate-limit-technique", "", "rate limit technique: fixed or sliding")
	f.IntVar(&values.logManagement, "log-management", 0, "maximum retained logs (10000-10000000)")
	f.StringVar(&values.logStrategy, "log-management-strategy", "", "log retention strategy: STOP_INSERTING or DELETE_OLDEST")
	f.BoolVar(&values.logpush, "logpush", false, "enable Logpush")
	f.StringVar(&values.logpushPublicKey, "logpush-public-key", "", "Logpush public key (16-1024 characters)")
	f.StringVar(&values.retryBackoff, "retry-backoff", "", "retry backoff: constant, linear, or exponential")
	f.IntVar(&values.retryDelay, "retry-delay", 0, "retry delay in milliseconds (0-5000)")
	f.IntVar(&values.retryAttempts, "retry-max-attempts", 0, "maximum retry attempts (1-5)")
	f.StringVar(&values.billingMode, "workers-ai-billing-mode", "", "Workers AI billing mode: postpaid or unified")
	f.BoolVar(&values.zdr, "zdr", false, "enable zero data retention")
}

func newAIGatewayListCmd(g *globalOpts) *cobra.Command {
	var search string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List AI Gateways",
		Long:  "List AI Gateways.\n\nExample:\n\n  cf ai-gateway list --search production",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			q := url.Values{}
			if search != "" {
				q.Set("search", search)
			}
			req := api.Request{Method: "GET", Path: aiGatewayPath(accountID), Query: q}
			if g.DryRun {
				return runAIGatewayRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var gateways []aiGateway
			if err := json.Unmarshal(env.Result, &gateways); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(gateways))
			for _, gateway := range gateways {
				logs, ttl := "", ""
				if gateway.CollectLogs != nil {
					logs = strconv.FormatBool(*gateway.CollectLogs)
				}
				if gateway.CacheTTL != nil {
					ttl = strconv.Itoa(*gateway.CacheTTL)
				}
				rows = append(rows, []string{gateway.ID, logs, ttl, gateway.CreatedAt})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "COLLECT LOGS", "CACHE TTL", "CREATED"}, rows)
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "search by gateway ID")
	return cmd
}

func newAIGatewayGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <gateway-id>",
		Short: "Show one AI Gateway",
		Long:  "Show one AI Gateway.\n\nExample:\n\n  cf ai-gateway get production-gateway",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "GET", Path: aiGatewayItemPath(accountID, args[0])})
		},
	}
	return cmd
}

func newAIGatewayCreateCmd(g *globalOpts) *cobra.Command {
	var values aiGatewayGatewayFlags
	cmd := &cobra.Command{
		Use:   "create <gateway-id>",
		Short: "Create an AI Gateway",
		Long:  "Create an AI Gateway.\n\nExample:\n\n  cf ai-gateway create production-gateway --rate-limit-interval 60 --rate-limit-limit 100 --collect-logs --cache-ttl 300 --cache-invalidate-on-update",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateGatewayFlags(cmd, values, true); err != nil {
				return err
			}
			body := aiGatewayGatewayPatch(cmd, values)
			body["id"] = args[0]
			encoded, err := json.Marshal(body)
			if err != nil {
				return err
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "POST", Path: aiGatewayPath(accountID), Body: encoded})
		},
	}
	addAIGatewayGatewayFlags(cmd, &values)
	return cmd
}

// aiGatewayGatewayReadOnly is intentionally small: these fields are returned
// by GET but absent from the full-schema PUT request. Everything else is
// retained to avoid deleting writable fields the porcelain does not expose.
var aiGatewayGatewayReadOnly = []string{"id", "created_at", "modified_at", "is_default"}

func aiGatewayFetchObject(ctx context.Context, client *api.Client, path, label string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s before update: %w", label, err)
	}
	var value any
	if err := json.Unmarshal(env.Result, &value); err != nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	return obj, nil
}

func aiGatewayMergeWrite(base, patch map[string]any, readOnly []string) map[string]any {
	merged := make(map[string]any, len(base)+len(patch))
	for key, value := range base {
		merged[key] = value
	}
	for _, key := range readOnly {
		delete(merged, key)
	}
	for key, value := range patch {
		merged[key] = value
	}
	return merged
}

func aiGatewayNumberAtLeast(body map[string]any, key string, min float64) error {
	value, ok := body[key]
	if !ok {
		return fmt.Errorf("read gateway before update: missing required %q", key)
	}
	// The gateway schema requires these keys but permits null, so retain a
	// server-returned null during a partial read-merge-write update.
	if value == nil {
		return nil
	}
	var number float64
	switch n := value.(type) {
	case float64:
		number = n
	case int:
		number = float64(n)
	case int64:
		number = float64(n)
	case json.Number:
		parsed, err := n.Float64()
		if err != nil {
			return fmt.Errorf("read gateway before update: invalid %q", key)
		}
		number = parsed
	default:
		return fmt.Errorf("read gateway before update: invalid %q", key)
	}
	if number < min || number != float64(int(number)) {
		return fmt.Errorf("read gateway before update: invalid %q", key)
	}
	return nil
}

func aiGatewayValidateMergedGateway(body map[string]any) error {
	for _, key := range []string{"collect_logs", "cache_invalidate_on_update"} {
		if _, ok := body[key].(bool); !ok {
			return fmt.Errorf("read gateway before update: missing or invalid required %q", key)
		}
	}
	for _, key := range []string{"rate_limiting_interval", "rate_limiting_limit", "cache_ttl"} {
		if err := aiGatewayNumberAtLeast(body, key, 0); err != nil {
			return err
		}
	}
	return nil
}

func newAIGatewayUpdateCmd(g *globalOpts) *cobra.Command {
	var values aiGatewayGatewayFlags
	cmd := &cobra.Command{
		Use:   "update <gateway-id>",
		Short: "Update common AI Gateway settings",
		Long: `Update common AI Gateway settings.

The API uses a full-schema PUT. This command reads the existing gateway,
preserves unknown writable fields, removes known read-only fields, and sends
the merged replacement. Consequently, --dry-run performs that required read
before printing the deterministic PUT request.

Example:

  cf ai-gateway update production-gateway --collect-logs=false --cache-ttl 600`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateGatewayFlags(cmd, values, false); err != nil {
				return err
			}
			patch := aiGatewayGatewayPatch(cmd, values)
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one gateway setting flag")
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			path := aiGatewayItemPath(accountID, args[0])
			base, err := aiGatewayFetchObject(cmd.Context(), client, path, "gateway")
			if err != nil {
				return err
			}
			body := aiGatewayMergeWrite(base, patch, aiGatewayGatewayReadOnly)
			if err := aiGatewayValidateMergedGateway(body); err != nil {
				return err
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: encoded})
		},
	}
	addAIGatewayGatewayFlags(cmd, &values)
	return cmd
}

func newAIGatewayDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <gateway-id>",
		Short: "Delete an AI Gateway",
		Long:  "Delete an AI Gateway and its configuration.\n\nExample:\n\n  cf ai-gateway delete production-gateway --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete AI Gateway %s?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "DELETE", Path: aiGatewayItemPath(accountID, args[0])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newAIGatewayLogsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Inspect AI Gateway request logs",
		Long:  "Inspect AI Gateway request logs.\n\nExample:\n\n  cf ai-gateway logs list production-gateway",
	}
	cmd.AddCommand(newAIGatewayLogsListCmd(g), newAIGatewayLogsGetCmd(g))
	return cmd
}

func aiGatewayParseFilters(rawFilters []string, keys, operators []string, allowNull bool) ([]map[string]any, error) {
	filters := make([]map[string]any, 0, len(rawFilters))
	for _, raw := range rawFilters {
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, fmt.Errorf("--filter must be valid JSON: %w", err)
		}
		filter, ok := value.(map[string]any)
		if !ok || filter == nil {
			return nil, errors.New("--filter must be a JSON object with key, operator, and value")
		}
		if len(filter) != 3 {
			return nil, errors.New("--filter must contain only key, operator, and value")
		}
		key, ok := filter["key"].(string)
		if !ok {
			return nil, errors.New("--filter.key must be a string")
		}
		if err := aiGatewayValidateEnum("filter.key", key, keys); err != nil {
			return nil, err
		}
		operator, ok := filter["operator"].(string)
		if !ok {
			return nil, errors.New("--filter.operator must be a string")
		}
		if err := aiGatewayValidateEnum("filter.operator", operator, operators); err != nil {
			return nil, err
		}
		values, ok := filter["value"].([]any)
		if !ok {
			return nil, errors.New("--filter.value must be a JSON array")
		}
		for _, item := range values {
			switch item.(type) {
			case string, float64, bool:
			case nil:
				if !allowNull {
					return nil, errors.New("--filter.value entries must be strings, numbers, or booleans")
				}
			default:
				return nil, errors.New("--filter.value entries must be strings, numbers, booleans, or null")
			}
		}
		filters = append(filters, filter)
	}
	return filters, nil
}

func aiGatewayLogsQuery(filters []map[string]any, search, order, direction string, metaInfo bool, metaChanged bool, page int) (url.Values, error) {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(aiGatewayLogsPerPage))
	for _, filter := range filters {
		encoded, err := json.Marshal(filter)
		if err != nil {
			return nil, err
		}
		q.Add("filters", string(encoded))
	}
	if search != "" {
		q.Set("search", search)
	}
	if order != "" {
		q.Set("order_by", order)
	}
	if direction != "" {
		q.Set("order_by_direction", direction)
	}
	if metaChanged {
		q.Set("meta_info", strconv.FormatBool(metaInfo))
	}
	return q, nil
}

// aiGatewayListLogs uses the endpoint's page/per_page/total_count metadata.
// It cannot use DoAutoPaginate because this API does not return total_pages.
// The bool is false only when the first result is not an array; callers then
// render that valid-but-unexpected API response as raw JSON instead of an
// empty table.
func aiGatewayListLogs(ctx context.Context, client *api.Client, accountID, gatewayID string, filters []map[string]any, search, order, direction string, metaInfo bool, metaChanged bool) ([]aiGatewayLog, []byte, bool, error) {
	var all []json.RawMessage
	var logs []aiGatewayLog
	for page := 1; page <= aiGatewayLogsMaxPages; page++ {
		q, err := aiGatewayLogsQuery(filters, search, order, direction, metaInfo, metaChanged, page)
		if err != nil {
			return nil, nil, false, err
		}
		env, err := client.Do(ctx, api.Request{Method: "GET", Path: aiGatewayLogsPath(accountID, gatewayID), Query: q})
		if err != nil {
			return nil, nil, false, err
		}
		var pageLogs []json.RawMessage
		if err := json.Unmarshal(env.Result, &pageLogs); err != nil {
			if page == 1 {
				return nil, env.Result, false, nil
			}
			return nil, nil, false, fmt.Errorf("list AI Gateway logs page %d: unexpected response", page)
		}
		all = append(all, pageLogs...)
		for i, raw := range pageLogs {
			var log aiGatewayLog
			if err := json.Unmarshal(raw, &log); err != nil {
				return nil, nil, false, fmt.Errorf("list AI Gateway logs page %d item %d: unexpected response", page, i+1)
			}
			logs = append(logs, log)
		}
		if env.ResultInfo != nil && env.ResultInfo.TotalCount > 0 && len(all) >= env.ResultInfo.TotalCount {
			break
		}
		if env.ResultInfo != nil && env.ResultInfo.TotalCount > 0 {
			if len(pageLogs) == 0 {
				return nil, nil, false, fmt.Errorf("list AI Gateway logs page %d: result_info.total_count exceeds returned items", page)
			}
			continue
		}
		if len(pageLogs) == 0 || len(pageLogs) < aiGatewayLogsPerPage {
			break
		}
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return nil, nil, false, err
	}
	return logs, raw, true, nil
}

func newAIGatewayLogsListCmd(g *globalOpts) *cobra.Command {
	var filters []string
	var search, order, direction string
	var metaInfo bool
	cmd := &cobra.Command{
		Use:   "list <gateway-id>",
		Short: "List AI Gateway request logs",
		Long: `List AI Gateway request logs. Filters are repeatable JSON objects with
key, operator, and value fields; values are arrays.

Examples:

  cf ai-gateway logs list production-gateway --filter '{"key":"provider","operator":"eq","value":["openai"]}'
  cf ai-gateway logs list production-gateway --search timeout --order-by created_at --direction desc`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			parsedFilters, err := aiGatewayParseFilters(filters, aiGatewayLogFilterKeys, aiGatewayLogFilterOperators, true)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("order-by") {
				if err := aiGatewayValidateEnum("order-by", order, aiGatewayLogOrders); err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("direction") {
				if err := aiGatewayValidateEnum("direction", direction, aiGatewayDirections); err != nil {
					return err
				}
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			if g.DryRun {
				q, err := aiGatewayLogsQuery(parsedFilters, search, order, direction, metaInfo, cmd.Flags().Changed("meta-info"), 1)
				if err != nil {
					return err
				}
				return runAIGatewayRequest(cmd, g, client, api.Request{Method: "GET", Path: aiGatewayLogsPath(accountID, args[0]), Query: q})
			}
			logs, raw, tabular, err := aiGatewayListLogs(cmd.Context(), client, accountID, args[0], parsedFilters, search, order, direction, metaInfo, cmd.Flags().Changed("meta-info"))
			if err != nil {
				return err
			}
			if !tabular {
				return g.renderResult(cmd, raw, output.JSON)
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, raw, output.JSON)
			}
			rows := make([][]string, 0, len(logs))
			for _, log := range logs {
				rows = append(rows, []string{log.ID, log.CreatedAt, log.Provider, output.Cell(log.Model), strconv.FormatBool(log.Success), strconv.FormatBool(log.Cached), strconv.Itoa(log.Duration), strconv.FormatFloat(log.Cost, 'f', -1, 64)})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "CREATED", "PROVIDER", "MODEL", "SUCCESS", "CACHED", "DURATION", "COST"}, rows)
		},
	}
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "filter as JSON object with key, operator, and value array (repeatable)")
	cmd.Flags().StringVar(&search, "search", "", "search log fields")
	cmd.Flags().StringVar(&order, "order-by", "", "sort by: created_at, provider, model, model_type, success, or cached")
	cmd.Flags().StringVar(&direction, "direction", "", "sort direction: asc or desc")
	cmd.Flags().BoolVar(&metaInfo, "meta-info", false, "include filter metadata in the response")
	return cmd
}

func newAIGatewayLogsGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <gateway-id> <log-id>",
		Short: "Show one AI Gateway request log",
		Long:  "Show one AI Gateway request log.\n\nExample:\n\n  cf ai-gateway logs get production-gateway 27a1c",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateNonEmpty("log ID", args[1]); err != nil {
				return err
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "GET", Path: aiGatewayLogsPath(accountID, args[0]) + "/" + url.PathEscape(args[1])})
		},
	}
	return cmd
}

func newAIGatewayDatasetsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "dataset",
		Aliases: []string{"datasets"},
		Short:   "Manage AI Gateway datasets",
		Long:    "Manage AI Gateway datasets.\n\nExample:\n\n  cf ai-gateway dataset list production-gateway",
	}
	cmd.AddCommand(
		newAIGatewayDatasetListCmd(g),
		newAIGatewayDatasetGetCmd(g),
		newAIGatewayDatasetCreateCmd(g),
		newAIGatewayDatasetUpdateCmd(g),
		newAIGatewayDatasetDeleteCmd(g),
	)
	return cmd
}

func aiGatewayDatasetBody(name string, enable bool, filters []map[string]any) map[string]any {
	return map[string]any{"name": name, "enable": enable, "filters": filters}
}

func aiGatewayValidateDatasetBody(body map[string]any) error {
	if _, ok := body["name"].(string); !ok {
		return errors.New("read dataset before update: missing or invalid required \"name\"")
	}
	if _, ok := body["enable"].(bool); !ok {
		return errors.New("read dataset before update: missing or invalid required \"enable\"")
	}
	filtersValue, ok := body["filters"]
	if !ok || filtersValue == nil {
		return errors.New("read dataset before update: missing or invalid required \"filters\"")
	}
	filtersRaw, err := json.Marshal(filtersValue)
	if err != nil {
		return errors.New("read dataset before update: missing or invalid required \"filters\"")
	}
	var filters []json.RawMessage
	if err := json.Unmarshal(filtersRaw, &filters); err != nil {
		return errors.New("read dataset before update: missing or invalid required \"filters\"")
	}
	rawFilters := make([]string, 0, len(filters))
	for _, filter := range filters {
		rawFilters = append(rawFilters, string(filter))
	}
	_, err = aiGatewayParseFilters(rawFilters, aiGatewayDatasetFilterKeys, aiGatewayDatasetFilterOperators, false)
	return err
}

func newAIGatewayDatasetListCmd(g *globalOpts) *cobra.Command {
	var name, search string
	var enable bool
	cmd := &cobra.Command{
		Use:   "list <gateway-id>",
		Short: "List datasets for an AI Gateway",
		Long:  "List datasets for an AI Gateway.\n\nExample:\n\n  cf ai-gateway dataset list production-gateway --enabled",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			q := url.Values{}
			if name != "" {
				q.Set("name", name)
			}
			if search != "" {
				q.Set("search", search)
			}
			if cmd.Flags().Changed("enabled") {
				q.Set("enable", strconv.FormatBool(enable))
			}
			req := api.Request{Method: "GET", Path: aiGatewayDatasetsPath(accountID, args[0]), Query: q}
			if g.DryRun {
				return runAIGatewayRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var datasets []aiGatewayDataset
			if err := json.Unmarshal(env.Result, &datasets); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(datasets))
			for _, dataset := range datasets {
				rows = append(rows, []string{dataset.ID, dataset.Name, strconv.FormatBool(dataset.Enable), dataset.Modified})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "ENABLED", "MODIFIED"}, rows)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter by exact dataset name")
	cmd.Flags().StringVar(&search, "search", "", "search dataset ID, name, or filters")
	cmd.Flags().BoolVar(&enable, "enabled", false, "show only enabled or disabled datasets")
	return cmd
}

func newAIGatewayDatasetGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <gateway-id> <dataset-id>",
		Short: "Show one AI Gateway dataset",
		Long:  "Show one AI Gateway dataset.\n\nExample:\n\n  cf ai-gateway dataset get production-gateway ds_123",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateNonEmpty("dataset ID", args[1]); err != nil {
				return err
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "GET", Path: aiGatewayDatasetPath(accountID, args[0], args[1])})
		},
	}
	return cmd
}

func newAIGatewayDatasetCreateCmd(g *globalOpts) *cobra.Command {
	var filters []string
	var enable bool
	cmd := &cobra.Command{
		Use:   "create <gateway-id> <name>",
		Short: "Create an AI Gateway dataset",
		Long: `Create an AI Gateway dataset. Filters are repeatable JSON objects with
key, operator, and value fields; values are arrays.

Example:

  cf ai-gateway dataset create production-gateway costly-openai --filter '{"key":"provider","operator":"eq","value":["openai"]}' --enabled`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateNonEmpty("dataset name", args[1]); err != nil {
				return err
			}
			if !cmd.Flags().Changed("enabled") {
				return errors.New("--enabled is required")
			}
			parsedFilters, err := aiGatewayParseFilters(filters, aiGatewayDatasetFilterKeys, aiGatewayDatasetFilterOperators, false)
			if err != nil {
				return err
			}
			body, err := json.Marshal(aiGatewayDatasetBody(args[1], enable, parsedFilters))
			if err != nil {
				return err
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "POST", Path: aiGatewayDatasetsPath(accountID, args[0]), Body: body})
		},
	}
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "dataset filter as JSON object with key, operator, and value array (repeatable)")
	cmd.Flags().BoolVar(&enable, "enabled", false, "enable the dataset")
	return cmd
}

var aiGatewayDatasetReadOnly = []string{"id", "gateway_id", "created_at", "modified_at"}

func newAIGatewayDatasetUpdateCmd(g *globalOpts) *cobra.Command {
	var name string
	var filters []string
	var enable bool
	cmd := &cobra.Command{
		Use:   "update <gateway-id> <dataset-id>",
		Short: "Update an AI Gateway dataset",
		Long: `Update an AI Gateway dataset.

The API uses a full-schema PUT. This command reads the existing dataset,
preserves unknown writable fields, removes known read-only fields, and sends
the merged replacement. Consequently, --dry-run performs that required read
before printing the deterministic PUT request.

Example:

  cf ai-gateway dataset update production-gateway ds_123 --enabled=false`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateNonEmpty("dataset ID", args[1]); err != nil {
				return err
			}
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				if err := aiGatewayValidateNonEmpty("dataset name", name); err != nil {
					return err
				}
				patch["name"] = name
			}
			if cmd.Flags().Changed("enabled") {
				patch["enable"] = enable
			}
			if cmd.Flags().Changed("filter") {
				parsedFilters, err := aiGatewayParseFilters(filters, aiGatewayDatasetFilterKeys, aiGatewayDatasetFilterOperators, false)
				if err != nil {
					return err
				}
				patch["filters"] = parsedFilters
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass --name, --enabled, or --filter")
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			path := aiGatewayDatasetPath(accountID, args[0], args[1])
			base, err := aiGatewayFetchObject(cmd.Context(), client, path, "dataset")
			if err != nil {
				return err
			}
			body := aiGatewayMergeWrite(base, patch, aiGatewayDatasetReadOnly)
			if err := aiGatewayValidateDatasetBody(body); err != nil {
				return err
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: encoded})
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "new dataset name")
	cmd.Flags().StringArrayVar(&filters, "filter", nil, "replace filters with JSON object with key, operator, and value array (repeatable)")
	cmd.Flags().BoolVar(&enable, "enabled", false, "enable or disable the dataset")
	return cmd
}

func newAIGatewayDatasetDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <gateway-id> <dataset-id>",
		Short: "Delete an AI Gateway dataset",
		Long:  "Delete an AI Gateway dataset.\n\nExample:\n\n  cf ai-gateway dataset delete production-gateway ds_123 --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := aiGatewayValidateID("gateway ID", args[0]); err != nil {
				return err
			}
			if err := aiGatewayValidateNonEmpty("dataset ID", args[1]); err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete AI Gateway dataset %s?", args[1])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			client, accountID, err := aiGatewayClient(g)
			if err != nil {
				return err
			}
			return runAIGatewayRequest(cmd, g, client, api.Request{Method: "DELETE", Path: aiGatewayDatasetPath(accountID, args[0], args[1])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}
