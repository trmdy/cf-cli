package cli

// Alerting porcelain: notification policies CRUD, available-alerts catalog,
// webhook and PagerDuty destination CRUD, and policy test. See docs/STYLE.md
// and internal/cli/dns.go. Account-scoped only; uses read-merge-write for
// full-schema PUT updates (dry-run reads documented in update help).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

func newAlertingCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alerting",
		Short: "Manage notification policies and alert destinations",
	}
	cmd.AddCommand(
		newAlertingPolicyCmd(g),
		newAlertingDestinationCmd(g),
		newAlertingAvailableAlertsCmd(g),
	)
	return cmd
}

// --- paths -----------------------------------------------------------------

func alertingPoliciesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/alerting/v3/policies"
}

func alertingPolicyPath(accountID, policyID string) string {
	return alertingPoliciesPath(accountID) + "/" + url.PathEscape(policyID)
}

func alertingWebhooksPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/alerting/v3/destinations/webhooks"
}

func alertingWebhookPath(accountID, webhookID string) string {
	return alertingWebhooksPath(accountID) + "/" + url.PathEscape(webhookID)
}

func alertingPagerdutyPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/alerting/v3/destinations/pagerduty"
}

func alertingPagerdutyConnectPath(accountID string) string {
	return alertingPagerdutyPath(accountID) + "/connect"
}

func alertingPagerdutyConnectTokenPath(accountID, tokenID string) string {
	return alertingPagerdutyConnectPath(accountID) + "/" + url.PathEscape(tokenID)
}

func alertingAvailableAlertsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/alerting/v3/available_alerts"
}

// --- client + validation (early, pre-network) ------------------------------

func alertingAccountID(cfg config.Resolved) (string, error) {
	accountID := strings.TrimSpace(cfg.AccountID)
	if accountID == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return accountID, nil
}

// alertingClient validates the full account contract before constructing a
// client or performing name resolution or any network work.
func alertingClient(g *globalOpts) (*api.Client, string, error) {
	cfg, err := g.resolve()
	if err != nil {
		return nil, "", err
	}
	accountID, err := alertingAccountID(cfg)
	if err != nil {
		return nil, "", err
	}
	if !g.DryRun && cfg.Token == "" {
		return nil, "", errors.New("no API token found; run `cf auth login`, set CLOUDFLARE_API_TOKEN, or pass --token")
	}
	return api.New(g.BaseURL, cfg.Token, Version), accountID, nil
}

func validateAlertingIDOrName(kind, v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%s id or name cannot be empty", kind)
	}
	return v, nil
}

func validateAlertingIntegrationTokenID(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", errors.New("token id cannot be empty")
	}
	if utf8.RuneCountInString(v) > 32 {
		return "", errors.New("token id must be at most 32 Unicode code points")
	}
	return v, nil
}

func isAlertingID(s string) bool {
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

// isValidAlertType validates against the exact pinned enum from the API spec
// (see components/schemas/aaa_alert_type). Must be called before client
// construction or name resolution.
var validAlertTypes = func() map[string]bool {
	m := map[string]bool{}
	for _, t := range []string{
		"abuse_report_alert",
		"access_custom_certificate_expiration_type",
		"advanced_ddos_attack_l4_alert",
		"advanced_ddos_attack_l7_alert",
		"advanced_http_alert_error",
		"bgp_hijack_notification",
		"billing_usage_alert",
		"block_notification_block_removed",
		"block_notification_new_block",
		"block_notification_review_rejected",
		"bot_traffic_basic_alert",
		"brand_protection_alert",
		"brand_protection_digest",
		"clickhouse_alert_fw_anomaly",
		"clickhouse_alert_fw_ent_anomaly",
		"cloudforce_one_request_notification",
		"cni_maintenance_notification",
		"custom_analytics",
		"custom_bot_detection_alert",
		"custom_ssl_certificate_event_type",
		"dedicated_ssl_certificate_event_type",
		"device_connectivity_anomaly_alert",
		"dos_attack_l4",
		"dos_attack_l7",
		"expiring_service_token_alert",
		"failing_logpush_job_disabled_alert",
		"fbm_auto_advertisement",
		"fbm_dosd_attack",
		"fbm_volumetric_attack",
		"health_check_status_notification",
		"hostname_aop_custom_certificate_expiration_type",
		"http_alert_edge_error",
		"http_alert_origin_error",
		"image_notification",
		"image_resizing_notification",
		"incident_alert",
		"load_balancing_health_alert",
		"load_balancing_pool_enablement_alert",
		"logo_match_alert",
		"magic_tunnel_health_check_event",
		"magic_wan_tunnel_health",
		"maintenance_event_notification",
		"mtls_certificate_store_certificate_expiration_type",
		"pages_event_alert",
		"radar_notification",
		"real_origin_monitoring",
		"scriptmonitor_alert_new_code_change_detections",
		"scriptmonitor_alert_new_hosts",
		"scriptmonitor_alert_new_malicious_hosts",
		"scriptmonitor_alert_new_malicious_scripts",
		"scriptmonitor_alert_new_malicious_url",
		"scriptmonitor_alert_new_max_length_resource_url",
		"scriptmonitor_alert_new_resources",
		"secondary_dns_all_primaries_failing",
		"secondary_dns_primaries_failing",
		"secondary_dns_warning",
		"secondary_dns_zone_successfully_updated",
		"secondary_dns_zone_validation_warning",
		"security_insights_alert",
		"sentinel_alert",
		"stream_live_notifications",
		"synthetic_test_latency_alert",
		"synthetic_test_low_availability_alert",
		"traffic_anomalies_alert",
		"tunnel_health_event",
		"tunnel_update_event",
		"universal_ssl_event_type",
		"web_analytics_metrics_update",
		"zone_aop_custom_certificate_expiration_type",
	} {
		m[t] = true
	}
	return m
}()

func isValidAlertType(s string) bool {
	return validAlertTypes[strings.TrimSpace(s)]
}

// isValidAlertingMechanismID enforces non-blank and at most 32 Unicode code
// points only (per pinned aaa_uuid: type string + maxLength 32; no minLength,
// pattern, or format required by spec).
func isValidAlertingMechanismID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if utf8.RuneCountInString(s) > 32 {
		return false
	}
	return true
}

// resolvePolicyID accepts a policy ID or name. When given a name it lists
// policies (including under --dry-run) so the emitted request always uses the
// canonical ID required by the API.
func resolvePolicyID(ctx context.Context, client *api.Client, accountID, policy string) (string, error) {
	if isAlertingID(policy) {
		return policy, nil
	}
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: alertingPoliciesPath(accountID)})
	if err != nil {
		return "", fmt.Errorf("look up policy %q: %w", policy, err)
	}
	var policies []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &policies); err != nil {
		return "", fmt.Errorf("look up policy %q: unexpected response", policy)
	}
	for _, p := range policies {
		if p.Name == policy || p.ID == policy {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("policy %q not found in this account; run `cf alerting policy list` to see available policies", policy)
}

// resolveWebhookID accepts a webhook ID or name. Lookup performed on dry-run.
func resolveWebhookID(ctx context.Context, client *api.Client, accountID, webhook string) (string, error) {
	if isAlertingID(webhook) {
		return webhook, nil
	}
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: alertingWebhooksPath(accountID)})
	if err != nil {
		return "", fmt.Errorf("look up webhook %q: %w", webhook, err)
	}
	var hooks []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &hooks); err != nil {
		return "", fmt.Errorf("look up webhook %q: unexpected response", webhook)
	}
	for _, h := range hooks {
		if h.Name == webhook || h.ID == webhook {
			return h.ID, nil
		}
	}
	return "", fmt.Errorf("webhook %q not found in this account; run `cf alerting destination webhook list` to see available webhooks", webhook)
}

// --- helpers ---------------------------------------------------------------

func runAlertingRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func buildAlertingMechanisms(emails, webhooks, pagerduties []string) (map[string]any, error) {
	m := map[string]any{}
	addEmail := func(ids []string) error {
		if len(ids) == 0 {
			return nil
		}
		arr := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				return errors.New("blank email destination not allowed")
			}
			// emails are free-form strings per spec (not UUID)
			arr = append(arr, map[string]string{"id": id})
		}
		m["email"] = arr
		return nil
	}
	addUUIDFamily := func(key string, ids []string) error {
		if len(ids) == 0 {
			return nil
		}
		arr := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				return fmt.Errorf("blank %s destination ID not allowed", key)
			}
			if !isValidAlertingMechanismID(id) {
				return fmt.Errorf("invalid %s destination ID %q (must be non-blank and <=32 Unicode code points per pinned aaa_uuid maxLength:32)", key, id)
			}
			arr = append(arr, map[string]string{"id": id})
		}
		m[key] = arr
		return nil
	}
	if err := addEmail(emails); err != nil {
		return nil, err
	}
	if err := addUUIDFamily("webhooks", webhooks); err != nil {
		return nil, err
	}
	if err := addUUIDFamily("pagerduty", pagerduties); err != nil {
		return nil, err
	}
	return m, nil
}

func alertingStripReadOnly(obj map[string]any, keys []string) {
	for _, k := range keys {
		delete(obj, k)
	}
}

func alertingMergeObject(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		out[k] = v
	}
	return out
}

// --- policy ----------------------------------------------------------------

func newAlertingPolicyCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Manage notification policies",
	}
	cmd.AddCommand(
		newAlertingPolicyListCmd(g),
		newAlertingPolicyGetCmd(g),
		newAlertingPolicyCreateCmd(g),
		newAlertingPolicyUpdateCmd(g),
		newAlertingPolicyDeleteCmd(g),
		newAlertingPolicyTestCmd(g),
	)
	return cmd
}

func newAlertingPolicyListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List notification policies",
		Long:  "List notification policies in the account.\n\nExample:\n\n  cf alerting policy list --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingPoliciesPath(accountID)}
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
			var policies []struct {
				ID            string `json:"id"`
				Name          string `json:"name"`
				Enabled       bool   `json:"enabled"`
				AlertType     string `json:"alert_type"`
				AlertInterval string `json:"alert_interval,omitempty"`
			}
			if err := json.Unmarshal(env.Result, &policies); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(policies))
			for _, p := range policies {
				rows = append(rows, []string{p.ID, p.Name, strconv.FormatBool(p.Enabled), p.AlertType, p.AlertInterval})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "ENABLED", "ALERT_TYPE", "INTERVAL"}, rows)
		},
	}
}

func newAlertingPolicyGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <policy>",
		Short: "Show one notification policy (by ID or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pol, err := validateAlertingIDOrName("policy", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			policyID, err := resolvePolicyID(cmd.Context(), client, accountID, pol)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingPolicyPath(accountID, policyID)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newAlertingPolicyCreateCmd(g *globalOpts) *cobra.Command {
	var alertType, description, alertInterval string
	var enabled bool
	var emails, webhooks, pagerduties []string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a notification policy",
		Long: `Create a notification policy.

Examples:

  cf alerting policy create "Origin errors" --alert-type http_alert_origin_error --enabled --email ops@example.com
  cf alerting policy create "LB health" --alert-type load_balancing_health_alert --enabled --pagerduty <pd-id>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.TrimSpace(args[0])
			if name == "" {
				return errors.New("policy name cannot be empty")
			}
			at := strings.TrimSpace(alertType)
			if at == "" {
				return errors.New("missing --alert-type (run `cf alerting available-alerts list` to discover values)")
			}
			if !isValidAlertType(at) {
				return fmt.Errorf("invalid --alert-type %q (must match pinned enum; run `cf alerting available-alerts list`)", at)
			}
			mechs, err := buildAlertingMechanisms(emails, webhooks, pagerduties)
			if err != nil {
				return err
			}
			if len(mechs) == 0 {
				return errors.New("at least one destination mechanism is required: --email, --webhook or --pagerduty")
			}
			bodyMap := map[string]any{
				"name":       name,
				"alert_type": at,
				"enabled":    enabled,
				"mechanisms": mechs,
			}
			if description != "" {
				bodyMap["description"] = description
			}
			if alertInterval != "" {
				bodyMap["alert_interval"] = alertInterval
			}
			body, err := json.Marshal(bodyMap)
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: alertingPoliciesPath(accountID), Body: body}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&alertType, "alert-type", "", "alert type from the catalog (required)")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "whether policy is active")
	cmd.Flags().StringVar(&description, "description", "", "optional description")
	cmd.Flags().StringVar(&alertInterval, "alert-interval", "", "re-alert interval (e.g. 30m); not supported on all types")
	cmd.Flags().StringArrayVar(&emails, "email", nil, "email address destination (repeatable)")
	cmd.Flags().StringArrayVar(&webhooks, "webhook", nil, "webhook destination ID (repeatable)")
	cmd.Flags().StringArrayVar(&pagerduties, "pagerduty", nil, "PagerDuty destination ID (repeatable)")
	_ = cmd.MarkFlagRequired("alert-type")
	return cmd
}

func newAlertingPolicyUpdateCmd(g *globalOpts) *cobra.Command {
	var name, alertType, description, alertInterval string
	var enabled bool
	var emails, webhooks, pagerduties []string
	cmd := &cobra.Command{
		Use:   "update <policy>",
		Short: "Update fields of a notification policy",
		Long: `Update selected fields of a notification policy.

Scalar fields (name, alert-type, description, alert-interval, enabled) are
sent as partial changed fields only. Body construction for scalars is
read-free only when the <policy> positional is a 32-hex ID (including
under --dry-run). A name still triggers the resolution-list GET in
resolvePolicyID (including for dry-run). When any mechanism family is
changed (--email/--webhook/--pagerduty), a targeted GET of the current
policy is performed (even for --dry-run) solely to merge the nested
mechanisms object so that untouched families (email/webhooks/pagerduty)
survive from the server object. The resulting body is validated locally.

Add --name to rename. alert-type must be valid per pinned enum.

Examples:

  cf alerting policy update my-policy --enabled=false
  cf alerting policy update 0da2b59ef118439d8097bdfb215203c9 --name "new-name" --account-id $ID
  cf alerting policy update mypol --email new@ex.com  # merges, preserves other families`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pol, err := validateAlertingIDOrName("policy", args[0])
			if err != nil {
				return err
			}
			// Full local contract validation before any client, name resolution,
			// or network (including conditional mech GET and write).
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				n := strings.TrimSpace(name)
				if n == "" {
					return errors.New("--name cannot be blank")
				}
				patch["name"] = n
			}
			if cmd.Flags().Changed("alert-type") {
				at := strings.TrimSpace(alertType)
				if at == "" {
					return errors.New("--alert-type cannot be blank")
				}
				if !isValidAlertType(at) {
					return fmt.Errorf("invalid --alert-type %q (must match pinned enum; run `cf alerting available-alerts list`)", at)
				}
				patch["alert_type"] = at
			}
			if cmd.Flags().Changed("description") {
				patch["description"] = description
			}
			if cmd.Flags().Changed("alert-interval") {
				patch["alert_interval"] = alertInterval
			}
			if cmd.Flags().Changed("enabled") {
				patch["enabled"] = enabled
			}
			changedMech := cmd.Flags().Changed("email") || cmd.Flags().Changed("webhook") || cmd.Flags().Changed("pagerduty")
			var mechPatch map[string]any
			if changedMech {
				var err error
				mechPatch, err = buildAlertingMechanisms(emails, webhooks, pagerduties)
				if err != nil {
					return err
				}
			}
			if len(patch) == 0 && !changedMech {
				return errors.New("nothing to update: pass at least one of --name, --alert-type, --description, --alert-interval, --enabled, --email, --webhook, --pagerduty")
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			policyID, err := resolvePolicyID(cmd.Context(), client, accountID, pol)
			if err != nil {
				return err
			}
			// Build body: scalars are partial (no read). Only for changed mech
			// families do a *targeted* GET (even dry-run) to merge nested
			// mechanisms and preserve untouched families.
			body := map[string]any{}
			for k, v := range patch {
				body[k] = v
			}
			if changedMech {
				env, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: alertingPolicyPath(accountID, policyID)})
				if err != nil {
					return fmt.Errorf("read policy %s mechanisms before update: %w", policyID, err)
				}
				var cur map[string]any
				if err := json.Unmarshal(env.Result, &cur); err != nil {
					return fmt.Errorf("read policy %s mechanisms before update: unexpected response", policyID)
				}
				curMechs := map[string]any{}
				if rawMechs, exists := cur["mechanisms"]; exists && rawMechs != nil {
					m, ok := rawMechs.(map[string]any)
					if !ok {
						return fmt.Errorf("read policy %s mechanisms before update: unexpected response", policyID)
					}
					curMechs = m
				}
				for fam, val := range mechPatch {
					curMechs[fam] = val
				}
				if len(curMechs) > 0 {
					body["mechanisms"] = curMechs
				}
			}
			bodyBytes, err := json.Marshal(body)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: alertingPolicyPath(accountID, policyID), Body: bodyBytes}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "policy name")
	cmd.Flags().StringVar(&alertType, "alert-type", "", "alert type")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "enable or disable the policy")
	cmd.Flags().StringVar(&description, "description", "", "policy description")
	cmd.Flags().StringVar(&alertInterval, "alert-interval", "", "re-alert interval")
	cmd.Flags().StringArrayVar(&emails, "email", nil, "replace email destinations (repeatable)")
	cmd.Flags().StringArrayVar(&webhooks, "webhook", nil, "replace webhook destinations (repeatable)")
	cmd.Flags().StringArrayVar(&pagerduties, "pagerduty", nil, "replace PagerDuty destinations (repeatable)")
	return cmd
}

func newAlertingPolicyDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <policy>",
		Short: "Delete a notification policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pol, err := validateAlertingIDOrName("policy", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			policyID, err := resolvePolicyID(cmd.Context(), client, accountID, pol)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete notification policy %s?", policyID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: alertingPolicyPath(accountID, policyID)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newAlertingPolicyTestCmd(g *globalOpts) *cobra.Command {
	var severity, stateEvent int
	var source, stateCorr string
	cmd := &cobra.Command{
		Use:   "test <policy>",
		Short: "Test-fire a notification policy (sends a test alert)",
		Long: `Send a test notification for the policy to verify its destinations work.

Optional body fields control the test alert. Defaults to INFO severity when omitted.

Examples:

  cf alerting policy test my-policy
  cf alerting policy test my-policy --severity 2 --source test-script`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pol, err := validateAlertingIDOrName("policy", args[0])
			if err != nil {
				return err
			}
			// Build + validate test body before client construction or name
			// resolution/lookup.
			body := map[string]any{}
			if cmd.Flags().Changed("severity") {
				if severity < 0 || severity > 4 {
					return errors.New("--severity must be an integer 0-4")
				}
				body["severity"] = severity
			}
			if cmd.Flags().Changed("source") {
				// send explicit value even if blank (do not silently omit)
				body["source"] = source
			}
			if cmd.Flags().Changed("state-correlation-id") {
				body["state_correlation_id"] = stateCorr
			}
			if cmd.Flags().Changed("state-event") {
				if stateEvent < 0 || stateEvent > 2 {
					return errors.New("--state-event must be 0, 1 or 2")
				}
				body["state_event"] = stateEvent
			}
			if _, hasEvent := body["state_event"]; hasEvent {
				sc, _ := body["state_correlation_id"].(string)
				if strings.TrimSpace(sc) == "" {
					return errors.New("state_event requires --state-correlation-id")
				}
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			policyID, err := resolvePolicyID(cmd.Context(), client, accountID, pol)
			if err != nil {
				return err
			}
			var reqBody []byte
			if len(body) > 0 {
				reqBody, err = json.Marshal(body)
				if err != nil {
					return err
				}
			}
			req := api.Request{Method: "POST", Path: alertingPolicyPath(accountID, policyID) + "/test", Body: reqBody}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().IntVar(&severity, "severity", 1, "severity level for test (0-4)")
	cmd.Flags().StringVar(&source, "source", "", "source identifier")
	cmd.Flags().StringVar(&stateCorr, "state-correlation-id", "", "correlation id (with --state-event)")
	cmd.Flags().IntVar(&stateEvent, "state-event", 0, "state event type (0-2, requires correlation id)")
	return cmd
}

// --- destination -----------------------------------------------------------

func newAlertingDestinationCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "destination",
		Short: "Manage webhook and PagerDuty destinations for notifications",
	}
	cmd.AddCommand(
		newAlertingDestinationWebhookCmd(g),
		newAlertingDestinationPagerDutyCmd(g),
	)
	return cmd
}

// webhook sub-group

func newAlertingDestinationWebhookCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Manage webhook destinations",
	}
	cmd.AddCommand(
		newAlertingWebhookListCmd(g),
		newAlertingWebhookGetCmd(g),
		newAlertingWebhookCreateCmd(g),
		newAlertingWebhookUpdateCmd(g),
		newAlertingWebhookDeleteCmd(g),
	)
	return cmd
}

func newAlertingWebhookListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List webhook destinations",
		Long:  "List webhook destinations in the account.\n\nExample:\n\n  cf alerting destination webhook list --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingWebhooksPath(accountID)}
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
			var hooks []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				URL         string `json:"url"`
				LastSuccess string `json:"last_success,omitempty"`
				LastFailure string `json:"last_failure,omitempty"`
			}
			if err := json.Unmarshal(env.Result, &hooks); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(hooks))
			for _, h := range hooks {
				rows = append(rows, []string{h.ID, h.Name, output.Cell(h.URL), h.LastSuccess, h.LastFailure})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "URL", "LAST_SUCCESS", "LAST_FAILURE"}, rows)
		},
	}
}

func newAlertingWebhookGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <webhook>",
		Short: "Show one webhook destination (ID or name)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hook, err := validateAlertingIDOrName("webhook", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			webhookID, err := resolveWebhookID(cmd.Context(), client, accountID, hook)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingWebhookPath(accountID, webhookID)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newAlertingWebhookCreateCmd(g *globalOpts) *cobra.Command {
	var urlStr, secret string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a webhook destination",
		Long: `Create a webhook destination.

Example:

  cf alerting destination webhook create "ops" --url https://example.com/hook --secret s3cr3t`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wname := strings.TrimSpace(args[0])
			if wname == "" {
				return errors.New("webhook name cannot be empty")
			}
			u := strings.TrimSpace(urlStr)
			if u == "" {
				return errors.New("missing --url")
			}
			bodyMap := map[string]any{
				"name": wname,
				"url":  u,
			}
			if secret != "" {
				bodyMap["secret"] = secret
			}
			body, err := json.Marshal(bodyMap)
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: alertingWebhooksPath(accountID), Body: body}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&urlStr, "url", "", "webhook target URL (required)")
	cmd.Flags().StringVar(&secret, "secret", "", "optional secret for signature validation")
	_ = cmd.MarkFlagRequired("url")
	return cmd
}

func newAlertingWebhookUpdateCmd(g *globalOpts) *cobra.Command {
	var name, urlStr, secret string
	cmd := &cobra.Command{
		Use:   "update <webhook>",
		Short: "Update a webhook destination (read-merge-write)",
		Long: `Update fields of a webhook.

The API uses full PUT, so update reads the current webhook (even under
--dry-run), merges changes (preserving unknown writable fields), strips
known read-only fields (id, created_at, last_*, type), and writes.

Example:

  cf alerting destination webhook update myhook --url https://new.example.com/hook`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hook, err := validateAlertingIDOrName("webhook", args[0])
			if err != nil {
				return err
			}
			// Reject explicitly blank for required fields (name/url) before
			// client or name resolution.
			patch := map[string]any{}
			if cmd.Flags().Changed("name") {
				n := strings.TrimSpace(name)
				if n == "" {
					return errors.New("--name cannot be blank (PUT requires name)")
				}
				patch["name"] = n
			}
			if cmd.Flags().Changed("url") {
				u := strings.TrimSpace(urlStr)
				if u == "" {
					return errors.New("--url cannot be blank (PUT requires url)")
				}
				patch["url"] = u
			}
			if cmd.Flags().Changed("secret") {
				patch["secret"] = secret
			}
			if len(patch) == 0 {
				return errors.New("nothing to update: pass at least one of --name, --url, --secret")
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			webhookID, err := resolveWebhookID(cmd.Context(), client, accountID, hook)
			if err != nil {
				return err
			}
			body, err := buildAlertingWebhookUpdateBody(cmd.Context(), client, accountID, webhookID, patch)
			if err != nil {
				return err
			}
			// Validate the complete merged object before PUT (name/url required
			// and non-blank per schema).
			var merged map[string]any
			if err := json.Unmarshal(body, &merged); err != nil {
				return err
			}
			if n, _ := merged["name"].(string); strings.TrimSpace(n) == "" {
				return errors.New("merged body missing or blank name (PUT requires it)")
			}
			if u, _ := merged["url"].(string); strings.TrimSpace(u) == "" {
				return errors.New("merged body missing or blank url (PUT requires it)")
			}
			req := api.Request{Method: "PUT", Path: alertingWebhookPath(accountID, webhookID), Body: body}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "webhook name")
	cmd.Flags().StringVar(&urlStr, "url", "", "target URL")
	cmd.Flags().StringVar(&secret, "secret", "", "secret (set to empty string to clear if supported)")
	return cmd
}

func buildAlertingWebhookUpdateBody(ctx context.Context, client *api.Client, accountID, webhookID string, patch map[string]any) ([]byte, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: alertingWebhookPath(accountID, webhookID)})
	if err != nil {
		return nil, fmt.Errorf("read webhook %s before update: %w", webhookID, err)
	}
	var cur any
	if err := json.Unmarshal(env.Result, &cur); err != nil {
		return nil, fmt.Errorf("read webhook %s before update: unexpected response", webhookID)
	}
	curObj, ok := cur.(map[string]any)
	if !ok || curObj == nil {
		return nil, fmt.Errorf("read webhook %s before update: unexpected response", webhookID)
	}
	alertingStripReadOnly(curObj, []string{"id", "created_at", "last_failure", "last_success", "type"})
	merged := alertingMergeObject(curObj, patch)
	alertingStripReadOnly(merged, []string{"id", "created_at", "last_failure", "last_success", "type"})
	return json.Marshal(merged)
}

func newAlertingWebhookDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <webhook>",
		Short: "Delete a webhook destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hook, err := validateAlertingIDOrName("webhook", args[0])
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			webhookID, err := resolveWebhookID(cmd.Context(), client, accountID, hook)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete webhook destination %s?", webhookID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: alertingWebhookPath(accountID, webhookID)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// pagerduty sub-group

func newAlertingDestinationPagerDutyCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pagerduty",
		Short: "Manage PagerDuty destinations",
	}
	cmd.AddCommand(
		newAlertingPagerDutyListCmd(g),
		newAlertingPagerDutyConnectCmd(g),
		newAlertingPagerDutyLinkCmd(g),
		newAlertingPagerDutyDeleteCmd(g),
	)
	return cmd
}

func newAlertingPagerDutyListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List PagerDuty destinations",
		Long:  "List connected PagerDuty services.\n\nExample:\n\n  cf alerting destination pagerduty list --account-id $CLOUDFLARE_ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingPagerdutyPath(accountID)}
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
			var pds []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(env.Result, &pds); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(pds))
			for _, p := range pds {
				rows = append(rows, []string{p.ID, p.Name})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME"}, rows)
		},
	}
}

func newAlertingPagerDutyConnectCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Create a PagerDuty integration token",
		Long: `Create a new PagerDuty integration token for the account.

Use the returned token when setting up the integration in PagerDuty, then
call link with the token ID if the service requires explicit linking.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: alertingPagerdutyConnectPath(accountID)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newAlertingPagerDutyLinkCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link <token-id>",
		Short: "Link/connect PagerDuty using a token ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := validateAlertingIntegrationTokenID(args[0])
			if err != nil {
				return err
			}
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingPagerdutyConnectTokenPath(accountID, token)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newAlertingPagerDutyDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete ALL PagerDuty services for the account",
		Long: `Deletes every PagerDuty service connected to the account.

This is destructive. Use --force to skip interactive confirmation.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete ALL PagerDuty services for account %s?", accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: alertingPagerdutyPath(accountID)}
			return runAlertingRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

// --- available alerts ------------------------------------------------------

func newAlertingAvailableAlertsCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "available-alerts",
		Short: "List available alert types (the catalog for policies)",
		Long: `List alert types that can be used when creating notification policies.

The TYPE value is what you pass to --alert-type on policy create.

Example:

  cf alerting available-alerts --account-id $ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := alertingClient(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: alertingAvailableAlertsPath(accountID)}
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
			// result is { "Category": [ {type, display_name, description, ...}, ... ] }
			var catalog map[string][]struct {
				Type        string `json:"type"`
				DisplayName string `json:"display_name"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(env.Result, &catalog); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			// Sort category keys for deterministic output (per review).
			cats := make([]string, 0, len(catalog))
			for cat := range catalog {
				cats = append(cats, cat)
			}
			sort.Strings(cats)
			rows := make([][]string, 0)
			for _, cat := range cats {
				for _, a := range catalog[cat] {
					rows = append(rows, []string{cat, a.Type, a.DisplayName, output.Cell(a.Description)})
				}
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"CATEGORY", "TYPE", "DISPLAY_NAME", "DESCRIPTION"}, rows)
		},
	}
}
