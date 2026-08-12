package cli

// Device posture porcelain: posture rule and integration CRUD.
// See docs/STYLE.md; rule updates read-merge-write because the API requires a
// full PUT body. Consequently, --dry-run for rule updates performs a GET but
// never sends the PUT.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

const postureIDMaxLength = 36

var (
	postureRuleTypes = []string{
		"file", "application", "tanium", "gateway", "warp", "disk_encryption",
		"serial_number", "sentinelone", "carbonblack", "firewall", "os_version",
		"domain_joined", "client_certificate", "client_certificate_v2", "antivirus",
		"unique_client_id", "kolide", "tanium_s2s", "crowdstrike_s2s", "intune",
		"workspace_one", "sentinelone_s2s", "custom_s2s",
	}
	postureIntegrationTypes = []string{
		"workspace_one", "crowdstrike_s2s", "uptycs", "intune", "kolide",
		"tanium_s2s", "sentinelone_s2s", "custom_s2s",
	}
	postureRuleReadOnly = []string{"id", "enabled"}
	postureDurationRE   = regexp.MustCompile(`^(?:[0-9]+h)?(?:[0-9]+m)?$`)
)

type postureRuleRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Schedule    string `json:"schedule"`
}

type postureIntegrationRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Interval string `json:"interval"`
}

type postureRuleFlags struct {
	description string
	expiration  string
	input       string
	match       string
	name        string
	schedule    string
	typeName    string
}

type postureIntegrationFlags struct {
	config   string
	interval string
	name     string
	typeName string
}

func newDevicesPostureCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "posture",
		Short: "Manage device posture rules and integrations",
	}
	cmd.AddCommand(newPostureRulesCmd(g), newPostureIntegrationsCmd(g))
	return cmd
}

func newPostureRulesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "rules", Short: "Manage device posture rules"}
	cmd.AddCommand(
		newPostureRulesListCmd(g),
		newPostureRulesGetCmd(g),
		newPostureRulesCreateCmd(g),
		newPostureRulesUpdateCmd(g),
		newPostureRulesDeleteCmd(g),
	)
	return cmd
}

func newPostureIntegrationsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "integrations", Short: "Manage device posture integrations"}
	cmd.AddCommand(
		newPostureIntegrationsListCmd(g),
		newPostureIntegrationsGetCmd(g),
		newPostureIntegrationsCreateCmd(g),
		newPostureIntegrationsUpdateCmd(g),
		newPostureIntegrationsDeleteCmd(g),
	)
	return cmd
}

func postureRulesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/devices/posture"
}

func postureRulePath(accountID, ruleID string) string {
	return postureRulesPath(accountID) + "/" + url.PathEscape(ruleID)
}

func postureIntegrationsPath(accountID string) string {
	return postureRulesPath(accountID) + "/integration"
}

func postureIntegrationPath(accountID, integrationID string) string {
	return postureIntegrationsPath(accountID) + "/" + url.PathEscape(integrationID)
}

// postureClientAccount is called only after a command has validated all local
// flags and arguments. Posture endpoints are account-scoped.
func postureClientAccount(g *globalOpts) (*api.Client, string, error) {
	client, cfg, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	if cfg.AccountID == "" {
		return nil, "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return client, cfg.AccountID, nil
}

func runPostureRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func postureValidateID(kind, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s id must not be empty", kind)
	}
	if len(id) > postureIDMaxLength {
		return fmt.Errorf("%s id must be at most %d characters", kind, postureIDMaxLength)
	}
	return nil
}

func postureValidateEnum(flag, value string, allowed []string) error {
	for _, item := range allowed {
		if value == item {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

func postureReadJSONArg(cmd *cobra.Command, flag, value string) ([]byte, error) {
	switch {
	case value == "":
		return nil, fmt.Errorf("--%s must not be empty", flag)
	case value == "@-":
		raw, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return nil, fmt.Errorf("read --%s from stdin: %w", flag, err)
		}
		return raw, nil
	case strings.HasPrefix(value, "@"):
		path := strings.TrimPrefix(value, "@")
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read --%s from %s: %w", flag, path, err)
		}
		return raw, nil
	default:
		return []byte(value), nil
	}
}

func postureJSONObject(cmd *cobra.Command, flag, value string) (map[string]any, error) {
	raw, err := postureReadJSONArg(cmd, flag, value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON object: %w", flag, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("--%s must contain one JSON object", flag)
	}
	object, ok := parsed.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("--%s must be a JSON object", flag)
	}
	return object, nil
}

func postureJSONArray(cmd *cobra.Command, flag, value string) ([]any, error) {
	raw, err := postureReadJSONArg(cmd, flag, value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("--%s must be a JSON array: %w", flag, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("--%s must contain one JSON array", flag)
	}
	array, ok := parsed.([]any)
	if !ok || array == nil {
		return nil, fmt.Errorf("--%s must be a JSON array", flag)
	}
	return array, nil
}

func postureValidateSchedule(value string) error {
	if !postureDurationRE.MatchString(value) || value == "" || value == "m" || value == "h" {
		return errors.New("--schedule must use hours and/or minutes and be at least 1m")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < time.Minute {
		return errors.New("--schedule must use hours and/or minutes and be at least 1m")
	}
	return nil
}

func postureValidateInterval(value string) error {
	if !postureDurationRE.MatchString(value) || value == "" || value == "m" || value == "h" {
		return errors.New("--interval must be a duration such as 5m or 1h")
	}
	if _, err := time.ParseDuration(value); err != nil {
		return errors.New("--interval must be a duration such as 5m or 1h")
	}
	return nil
}

func postureString(obj map[string]any, field string, required bool) (string, error) {
	value, exists := obj[field]
	if !exists {
		if required {
			return "", fmt.Errorf("%s is required", field)
		}
		return "", nil
	}
	stringValue, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", field)
	}
	return stringValue, nil
}

func postureValidateRuleBody(body map[string]any) error {
	name, err := postureString(body, "name", true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("name must not be empty")
	}
	typeName, err := postureString(body, "type", true)
	if err != nil {
		return err
	}
	if err := postureValidateEnum("type", typeName, postureRuleTypes); err != nil {
		return err
	}
	if input, exists := body["input"]; exists {
		if _, ok := input.(map[string]any); !ok || input == nil {
			return errors.New("input must be a JSON object")
		}
	}
	if match, exists := body["match"]; exists {
		if _, ok := match.([]any); !ok || match == nil {
			return errors.New("match must be a JSON array")
		}
	}
	if schedule, exists := body["schedule"]; exists {
		value, ok := schedule.(string)
		if !ok {
			return errors.New("schedule must be a string")
		}
		if err := postureValidateSchedule(value); err != nil {
			return err
		}
	}
	for _, field := range []string{"description", "expiration"} {
		if _, err := postureString(body, field, false); err != nil {
			return err
		}
	}
	return nil
}

// postureValidateRulePatch checks only supplied fields. The complete schema is
// checked after a PUT update has merged this patch onto the current rule.
func postureValidateRulePatch(body map[string]any) error {
	if name, exists := body["name"]; exists {
		value, ok := name.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return errors.New("name must be a non-empty string")
		}
	}
	if typeName, exists := body["type"]; exists {
		value, ok := typeName.(string)
		if !ok {
			return errors.New("type must be a string")
		}
		if err := postureValidateEnum("type", value, postureRuleTypes); err != nil {
			return err
		}
	}
	if input, exists := body["input"]; exists {
		if _, ok := input.(map[string]any); !ok || input == nil {
			return errors.New("input must be a JSON object")
		}
	}
	if match, exists := body["match"]; exists {
		if _, ok := match.([]any); !ok || match == nil {
			return errors.New("match must be a JSON array")
		}
	}
	if schedule, exists := body["schedule"]; exists {
		value, ok := schedule.(string)
		if !ok {
			return errors.New("schedule must be a string")
		}
		if err := postureValidateSchedule(value); err != nil {
			return err
		}
	}
	for _, field := range []string{"description", "expiration"} {
		if _, err := postureString(body, field, false); err != nil {
			return err
		}
	}
	return nil
}

func postureValidateIntegrationBody(body map[string]any, create bool) error {
	name, err := postureString(body, "name", create)
	if err != nil {
		return err
	}
	if name != "" && strings.TrimSpace(name) == "" {
		return errors.New("name must not be empty")
	}
	typeName, err := postureString(body, "type", create)
	if err != nil {
		return err
	}
	if typeName != "" {
		if err := postureValidateEnum("type", typeName, postureIntegrationTypes); err != nil {
			return err
		}
	}
	interval, err := postureString(body, "interval", create)
	if err != nil {
		return err
	}
	if interval != "" {
		if err := postureValidateInterval(interval); err != nil {
			return err
		}
	}
	if config, exists := body["config"]; exists {
		if _, ok := config.(map[string]any); !ok || config == nil {
			return errors.New("config must be a JSON object")
		}
	} else if create {
		return errors.New("config is required")
	}
	return nil
}

func postureRuleBodyFromFlags(cmd *cobra.Command, f postureRuleFlags, create bool) ([]byte, error) {
	body := map[string]any{}
	if create || cmd.Flags().Changed("name") {
		body["name"] = f.name
	}
	if create || cmd.Flags().Changed("type") {
		body["type"] = f.typeName
	}
	if cmd.Flags().Changed("description") {
		body["description"] = f.description
	}
	if cmd.Flags().Changed("expiration") {
		body["expiration"] = f.expiration
	}
	if cmd.Flags().Changed("schedule") {
		body["schedule"] = f.schedule
	}
	if cmd.Flags().Changed("input") {
		input, err := postureJSONObject(cmd, "input", f.input)
		if err != nil {
			return nil, err
		}
		body["input"] = input
	}
	if cmd.Flags().Changed("match") {
		match, err := postureJSONArray(cmd, "match", f.match)
		if err != nil {
			return nil, err
		}
		body["match"] = match
	}
	var err error
	if create {
		err = postureValidateRuleBody(body)
	} else {
		err = postureValidateRulePatch(body)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func postureIntegrationBodyFromFlags(cmd *cobra.Command, f postureIntegrationFlags, create bool) ([]byte, error) {
	body := map[string]any{}
	if create || cmd.Flags().Changed("name") {
		body["name"] = f.name
	}
	if create || cmd.Flags().Changed("type") {
		body["type"] = f.typeName
	}
	if create || cmd.Flags().Changed("interval") {
		body["interval"] = f.interval
	}
	if create || cmd.Flags().Changed("config") {
		config, err := postureJSONObject(cmd, "config", f.config)
		if err != nil {
			return nil, err
		}
		body["config"] = config
	}
	if err := postureValidateIntegrationBody(body, create); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func postureFetchObject(cmd *cobra.Command, client *api.Client, path, label string) (map[string]any, error) {
	env, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: path})
	if err != nil {
		return nil, fmt.Errorf("read %s before update: %w", label, err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(env.Result)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, fmt.Errorf("read %s before update: unexpected response", label)
	}
	return object, nil
}

func postureRuleUpdateBody(cmd *cobra.Command, client *api.Client, accountID, ruleID string, patch []byte) ([]byte, error) {
	var changes map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(patch)))
	decoder.UseNumber()
	if err := decoder.Decode(&changes); err != nil {
		return nil, err
	}
	current, err := postureFetchObject(cmd, client, postureRulePath(accountID, ruleID), "posture rule "+ruleID)
	if err != nil {
		return nil, err
	}
	for _, field := range postureRuleReadOnly {
		delete(current, field)
	}
	for field, value := range changes {
		current[field] = value
	}
	for _, field := range postureRuleReadOnly {
		delete(current, field)
	}
	if err := postureValidateRuleBody(current); err != nil {
		return nil, fmt.Errorf("posture rule %s cannot be updated: %w", ruleID, err)
	}
	return json.Marshal(current)
}

func bindPostureRuleFlags(cmd *cobra.Command, f *postureRuleFlags, create bool) {
	cmd.Flags().StringVar(&f.name, "name", "", "rule name")
	cmd.Flags().StringVar(&f.typeName, "type", "", "rule type: "+strings.Join(postureRuleTypes, ", "))
	cmd.Flags().StringVar(&f.description, "description", "", "rule description")
	cmd.Flags().StringVar(&f.expiration, "expiration", "", "result expiration duration (empty clears it)")
	cmd.Flags().StringVar(&f.schedule, "schedule", "", "WARP polling duration (minimum 1m)")
	cmd.Flags().StringVar(&f.input, "input", "", "JSON input object, @file, or @- for stdin")
	cmd.Flags().StringVar(&f.match, "match", "", "JSON platform-match array, @file, or @- for stdin")
	if create {
		_ = cmd.MarkFlagRequired("name")
		_ = cmd.MarkFlagRequired("type")
	}
}

func bindPostureIntegrationFlags(cmd *cobra.Command, f *postureIntegrationFlags, create bool) {
	cmd.Flags().StringVar(&f.name, "name", "", "integration name")
	cmd.Flags().StringVar(&f.typeName, "type", "", "integration type: "+strings.Join(postureIntegrationTypes, ", "))
	cmd.Flags().StringVar(&f.interval, "interval", "", "polling interval, such as 5m or 1h")
	cmd.Flags().StringVar(&f.config, "config", "", "JSON configuration object, @file, or @- for stdin")
	if create {
		_ = cmd.MarkFlagRequired("name")
		_ = cmd.MarkFlagRequired("type")
		_ = cmd.MarkFlagRequired("interval")
		_ = cmd.MarkFlagRequired("config")
	}
}

func newPostureRulesListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List device posture rules",
		Long:  "List device posture rules.\n\nExample:\n\n  cf devices posture rules list --account-id $ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: postureRulesPath(accountID)}
			if g.DryRun {
				return runPostureRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var rules []postureRuleRow
			if err := json.Unmarshal(env.Result, &rules); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(rules))
			for _, rule := range rules {
				rows = append(rows, []string{rule.ID, rule.Name, rule.Type, output.Cell(rule.Description), rule.Schedule})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "TYPE", "DESCRIPTION", "SCHEDULE"}, rows)
		},
	}
}

func newPostureRulesGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <rule-id>",
		Short: "Show a device posture rule",
		Long:  "Show a device posture rule.\n\nExample:\n\n  cf devices posture rules get f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postureValidateID("rule", args[0]); err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "GET", Path: postureRulePath(accountID, args[0])})
		},
	}
}

func newPostureRulesCreateCmd(g *globalOpts) *cobra.Command {
	var f postureRuleFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a device posture rule",
		Long: `Create a device posture rule. Use --input for the type-specific API object.

Examples:

  cf devices posture rules create --name "Signed binary" --type file --input '{"operating_system":"mac","path":"/usr/local/bin/tool"}' --schedule 5m --account-id $ACCOUNT_ID
  cf devices posture rules create --name "Windows firewall" --type firewall --input '{"operating_system":"windows","enabled":true}' --account-id $ACCOUNT_ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := postureRuleBodyFromFlags(cmd, f, true)
			if err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "POST", Path: postureRulesPath(accountID), Body: body})
		},
	}
	bindPostureRuleFlags(cmd, &f, true)
	return cmd
}

func newPostureRulesUpdateCmd(g *globalOpts) *cobra.Command {
	var f postureRuleFlags
	cmd := &cobra.Command{
		Use:   "update <rule-id>",
		Short: "Update a device posture rule",
		Long: `Update a device posture rule. The API requires a full PUT body, so this
command first reads the rule, applies your changed flags while preserving
unknown writable fields, strips read-only fields, and writes the merged body.
--dry-run performs the read but never sends the PUT.

Example:

  cf devices posture rules update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --schedule 10m --account-id $ACCOUNT_ID`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postureValidateID("rule", args[0]); err != nil {
				return err
			}
			changed := false
			for _, name := range []string{"name", "type", "description", "expiration", "schedule", "input", "match"} {
				changed = changed || cmd.Flags().Changed(name)
			}
			if !changed {
				return errors.New("nothing to update: pass at least one rule flag")
			}
			// Validate and parse every supplied flag before constructing a client
			// or reading the rule needed for the full-schema PUT.
			patch, err := postureRuleBodyFromFlags(cmd, f, false)
			if err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			body, err := postureRuleUpdateBody(cmd, client, accountID, args[0], patch)
			if err != nil {
				return err
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "PUT", Path: postureRulePath(accountID, args[0]), Body: body})
		},
	}
	bindPostureRuleFlags(cmd, &f, false)
	return cmd
}

func newPostureRulesDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <rule-id>",
		Short: "Delete a device posture rule",
		Long:  "Delete a device posture rule.\n\nExample:\n\n  cf devices posture rules delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postureValidateID("rule", args[0]); err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete device posture rule %s?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "DELETE", Path: postureRulePath(accountID, args[0]), Body: []byte("{}")})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newPostureIntegrationsListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List device posture integrations",
		Long:  "List device posture integrations.\n\nExample:\n\n  cf devices posture integrations list --account-id $ACCOUNT_ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: postureIntegrationsPath(accountID)}
			if g.DryRun {
				return runPostureRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			if g.Query != "" || g.format(output.Table) != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var integrations []postureIntegrationRow
			if err := json.Unmarshal(env.Result, &integrations); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(integrations))
			for _, integration := range integrations {
				rows = append(rows, []string{integration.ID, integration.Name, integration.Type, integration.Interval})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "TYPE", "INTERVAL"}, rows)
		},
	}
}

func newPostureIntegrationsGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <integration-id>",
		Short: "Show a device posture integration",
		Long:  "Show a device posture integration.\n\nExample:\n\n  cf devices posture integrations get f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postureValidateID("integration", args[0]); err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "GET", Path: postureIntegrationPath(accountID, args[0])})
		},
	}
}

func newPostureIntegrationsCreateCmd(g *globalOpts) *cobra.Command {
	var f postureIntegrationFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a device posture integration",
		Long: `Create a device posture integration. The type-specific configuration is a
JSON object; use @file or @- to keep credentials out of shell history.

Example:

  cf devices posture integrations create --name "CrowdStrike" --type crowdstrike_s2s --interval 5m --config @crowdstrike.json --account-id $ACCOUNT_ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := postureIntegrationBodyFromFlags(cmd, f, true)
			if err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "POST", Path: postureIntegrationsPath(accountID), Body: body})
		},
	}
	bindPostureIntegrationFlags(cmd, &f, true)
	return cmd
}

func newPostureIntegrationsUpdateCmd(g *globalOpts) *cobra.Command {
	var f postureIntegrationFlags
	cmd := &cobra.Command{
		Use:   "update <integration-id>",
		Short: "Update a device posture integration",
		Long: `Update fields of a device posture integration.

Example:

  cf devices posture integrations update f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --interval 15m --account-id $ACCOUNT_ID`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postureValidateID("integration", args[0]); err != nil {
				return err
			}
			changed := false
			for _, name := range []string{"name", "type", "interval", "config"} {
				changed = changed || cmd.Flags().Changed(name)
			}
			if !changed {
				return errors.New("nothing to update: pass at least one integration flag")
			}
			body, err := postureIntegrationBodyFromFlags(cmd, f, false)
			if err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "PATCH", Path: postureIntegrationPath(accountID, args[0]), Body: body})
		},
	}
	bindPostureIntegrationFlags(cmd, &f, false)
	return cmd
}

func newPostureIntegrationsDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <integration-id>",
		Short: "Delete a device posture integration",
		Long:  "Delete a device posture integration.\n\nExample:\n\n  cf devices posture integrations delete f174e90a-fafe-4643-bbbc-4a0ed4fc8415 --force --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := postureValidateID("integration", args[0]); err != nil {
				return err
			}
			client, accountID, err := postureClientAccount(g)
			if err != nil {
				return err
			}
			if !force && !g.DryRun && !confirm(fmt.Sprintf("Delete device posture integration %s?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			return runPostureRequest(cmd, g, client, api.Request{Method: "DELETE", Path: postureIntegrationPath(accountID, args[0]), Body: []byte("{}")})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}
