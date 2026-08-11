package cli

// Waiting Room porcelain: room CRUD, nested events/rules, status, and preview.
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
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

// Schema bounds from the Waiting Room OpenAPI components.
const (
	waitingRoomMinUsers            = 200
	waitingRoomMaxUsers            = 2147483647
	waitingRoomMinSessionDuration  = 1
	waitingRoomMaxSessionDuration  = 30
	waitingRoomListPerPage         = 100 // multiple of 5; within API 5..1000
	waitingRoomEventMinLeadMinutes = 1
	waitingRoomPrequeueLeadMinutes = 5
)

var (
	waitingRoomNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

	waitingRoomQueueingMethods  = []string{"fifo", "random", "passthrough", "reject"}
	waitingRoomTurnstileModes   = []string{"off", "invisible", "visible_non_interactive", "visible_managed"}
	waitingRoomTurnstileActs    = []string{"log", "infinite_queue"}
	waitingRoomCookieSamesite   = []string{"auto", "lax", "none", "strict"}
	waitingRoomCookieSecure     = []string{"auto", "always", "never"}
	waitingRoomQueueStatusCodes = []int{200, 202, 429}
	waitingRoomRuleActions      = []string{"bypass_waiting_room"}
	waitingRoomOriginCommands   = []string{"revoke"}
	waitingRoomTemplateLangs    = []string{
		"en-US", "es-ES", "de-DE", "fr-FR", "it-IT", "ja-JP", "ko-KR", "pt-BR",
		"zh-CN", "zh-TW", "nl-NL", "pl-PL", "id-ID", "tr-TR", "ar-EG", "ru-RU",
		"fa-IR", "bg-BG", "hr-HR", "cs-CZ", "da-DK", "fi-FI", "lt-LT", "lv-LV",
		"ms-MY", "nb-NO", "ro-RO", "el-GR", "he-IL", "hi-IN", "hu-HU", "sr-BA",
		"sk-SK", "sl-SI", "sv-SE", "tl-PH", "th-TH", "uk-UA", "vi-VN",
	}

	// Read-only response fields stripped before write-back so the API does not
	// reject unknown/immutable properties on the merged body.
	waitingRoomRoomReadOnly = []string{
		"id", "created_on", "modified_on",
		"next_event_prequeue_start_time", "next_event_start_time",
	}
	waitingRoomEventReadOnly = []string{"id", "created_on", "modified_on"}
	waitingRoomRuleReadOnly  = []string{"id", "last_updated", "version"}
)

type waitingRoom struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	Host              string `json:"host,omitempty"`
	Path              string `json:"path,omitempty"`
	NewUsersPerMinute int    `json:"new_users_per_minute,omitempty"`
	TotalActiveUsers  int    `json:"total_active_users,omitempty"`
	QueueingMethod    string `json:"queueing_method,omitempty"`
	SessionDuration   int    `json:"session_duration,omitempty"`
	Suspended         *bool  `json:"suspended,omitempty"`
	QueueAll          *bool  `json:"queue_all,omitempty"`
	Description       string `json:"description,omitempty"`
	CreatedOn         string `json:"created_on,omitempty"`
	ModifiedOn        string `json:"modified_on,omitempty"`
}

type waitingRoomEvent struct {
	ID                string `json:"id,omitempty"`
	Name              string `json:"name,omitempty"`
	EventStartTime    string `json:"event_start_time,omitempty"`
	EventEndTime      string `json:"event_end_time,omitempty"`
	PrequeueStartTime string `json:"prequeue_start_time,omitempty"`
	QueueingMethod    string `json:"queueing_method,omitempty"`
	Description       string `json:"description,omitempty"`
	Suspended         *bool  `json:"suspended,omitempty"`
	CreatedOn         string `json:"created_on,omitempty"`
}

type waitingRoomRule struct {
	ID          string `json:"id,omitempty"`
	Action      string `json:"action,omitempty"`
	Expression  string `json:"expression,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
	Version     string `json:"version,omitempty"`
}

func newWaitingRoomCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "waiting-room",
		Short: "Manage Waiting Rooms, events, and rules",
	}
	cmd.AddCommand(
		newWaitingRoomListCmd(g),
		newWaitingRoomGetCmd(g),
		newWaitingRoomCreateCmd(g),
		newWaitingRoomUpdateCmd(g),
		newWaitingRoomDeleteCmd(g),
		newWaitingRoomStatusCmd(g),
		newWaitingRoomPreviewCmd(g),
		newWaitingRoomEventsCmd(g),
		newWaitingRoomRulesCmd(g),
	)
	return cmd
}

// --- paths -----------------------------------------------------------------

func waitingRoomZonePath(zoneID string) string {
	return "/zones/" + url.PathEscape(zoneID) + "/waiting_rooms"
}

func waitingRoomAccountPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/waiting_rooms"
}

func waitingRoomItemPath(zoneID, roomID string) string {
	return waitingRoomZonePath(zoneID) + "/" + url.PathEscape(roomID)
}

func waitingRoomEventsPath(zoneID, roomID string) string {
	return waitingRoomItemPath(zoneID, roomID) + "/events"
}

func waitingRoomEventPath(zoneID, roomID, eventID string) string {
	return waitingRoomEventsPath(zoneID, roomID) + "/" + url.PathEscape(eventID)
}

func waitingRoomRulesPath(zoneID, roomID string) string {
	return waitingRoomItemPath(zoneID, roomID) + "/rules"
}

func waitingRoomRulePath(zoneID, roomID, ruleID string) string {
	return waitingRoomRulesPath(zoneID, roomID) + "/" + url.PathEscape(ruleID)
}

// resolveWaitingRoomZone builds a client then resolves the zone through
// resolveZoneInteractive (--zone > configured zone > TTY picker). Local
// input validation must already have passed before this is called.
func resolveWaitingRoomZone(cmd *cobra.Command, g *globalOpts, zone string) (*api.Client, string, error) {
	client, cfg, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
	if err != nil {
		return nil, "", err
	}
	return client, zoneID, nil
}

// --- shared request runner -------------------------------------------------

func runWaitingRoomRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

// --- validation helpers ----------------------------------------------------

func waitingRoomEnum(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

func waitingRoomValidateName(flag, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s must not be empty", flag)
	}
	if !waitingRoomNameRe.MatchString(name) {
		return fmt.Errorf("%s %q is invalid: only alphanumeric characters, hyphens, and underscores are allowed", flag, name)
	}
	return nil
}

// waitingRoomValidateHost enforces the documented bare-hostname contract: no
// scheme, no wildcards, no path separators.
func waitingRoomValidateHost(flag, host string) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s must not be empty", flag)
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/*?") {
		return fmt.Errorf("%s must be a bare hostname without scheme or wildcards", flag)
	}
	return nil
}

// waitingRoomValidatePath rejects wildcards and query strings; empty means
// "API default /" and is allowed when the field is optional.
func waitingRoomValidatePath(flag, path string) error {
	if path == "" {
		return nil
	}
	if strings.ContainsAny(path, "*?") {
		return fmt.Errorf("%s must not contain wildcards or query parameters", flag)
	}
	return nil
}

func waitingRoomStripReadOnly(obj map[string]any, keys []string) {
	for _, k := range keys {
		delete(obj, k)
	}
}

func waitingRoomMergeObject(base, patch map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		out[k] = v
	}
	return out
}

func waitingRoomFetchObject(ctx context.Context, client *api.Client, path, label string) (map[string]any, error) {
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

func waitingRoomFetchRule(ctx context.Context, client *api.Client, zoneID, roomID, ruleID string) (map[string]any, error) {
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: waitingRoomRulesPath(zoneID, roomID)})
	if err != nil {
		return nil, fmt.Errorf("read rule %s before update: %w", ruleID, err)
	}
	var value any
	if err := json.Unmarshal(env.Result, &value); err != nil {
		return nil, fmt.Errorf("read rule %s before update: unexpected response", ruleID)
	}
	arr, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("read rule %s before update: unexpected response", ruleID)
	}
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := obj["id"].(string)
		if id == ruleID {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("rule %s not found on waiting room %s", ruleID, roomID)
}

// waitingRoomAsInt coerces a JSON number into a whole integer.
func waitingRoomAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

func waitingRoomValidateUsers(flag string, n int) error {
	if n < waitingRoomMinUsers || n > waitingRoomMaxUsers {
		return fmt.Errorf("--%s must be between %d and %d", flag, waitingRoomMinUsers, waitingRoomMaxUsers)
	}
	return nil
}

func waitingRoomValidateSessionDuration(n int) error {
	if n < waitingRoomMinSessionDuration || n > waitingRoomMaxSessionDuration {
		return fmt.Errorf("--session-duration must be between %d and %d minutes", waitingRoomMinSessionDuration, waitingRoomMaxSessionDuration)
	}
	return nil
}

func waitingRoomValidateQueueStatusCode(code int) error {
	for _, c := range waitingRoomQueueStatusCodes {
		if code == c {
			return nil
		}
	}
	return fmt.Errorf("--queueing-status-code must be one of: 200, 202, 429")
}

func waitingRoomValidateUserPair(newUsers, totalUsers int) error {
	if err := waitingRoomValidateUsers("new-users-per-minute", newUsers); err != nil {
		return err
	}
	if err := waitingRoomValidateUsers("total-active-users", totalUsers); err != nil {
		return err
	}
	if newUsers > totalUsers {
		return errors.New("--new-users-per-minute must be less than or equal to --total-active-users")
	}
	return nil
}

// parseWaitingRoomJSONArray decodes a non-null JSON array. null, objects, and
// scalars are rejected so they cannot slip through as empty slices.
func parseWaitingRoomJSONArray(flag, raw string) ([]any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("--%s must be valid JSON: %w", flag, err)
	}
	arr, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("--%s must be a JSON array", flag)
	}
	return arr, nil
}

// parseWaitingRoomJSONObject decodes a non-null JSON object.
func parseWaitingRoomJSONObject(flag, raw string) (map[string]any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("--%s must be valid JSON: %w", flag, err)
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("--%s must be a JSON object", flag)
	}
	return obj, nil
}

func parseWaitingRoomAdditionalRoutes(raw string) ([]any, error) {
	arr, err := parseWaitingRoomJSONArray("additional-routes", raw)
	if err != nil {
		return nil, err
	}
	if err := waitingRoomValidateAdditionalRoutes(arr); err != nil {
		return nil, err
	}
	return arr, nil
}

func waitingRoomValidateAdditionalRoutes(arr []any) error {
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("additional_routes[%d] must be a JSON object with host/path", i)
		}
		host, _ := obj["host"].(string)
		if err := waitingRoomValidateHost(fmt.Sprintf("additional_routes[%d].host", i), host); err != nil {
			return err
		}
		if path, ok := obj["path"]; ok {
			s, ok := path.(string)
			if !ok {
				return fmt.Errorf("additional_routes[%d].path must be a string", i)
			}
			if err := waitingRoomValidatePath(fmt.Sprintf("additional_routes[%d].path", i), s); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseWaitingRoomCookieAttributes(raw string) (map[string]any, error) {
	obj, err := parseWaitingRoomJSONObject("cookie-attributes", raw)
	if err != nil {
		return nil, err
	}
	if v, ok := obj["samesite"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("--cookie-attributes.samesite must be a string")
		}
		if err := waitingRoomEnum("cookie-attributes.samesite", s, waitingRoomCookieSamesite); err != nil {
			return nil, err
		}
	}
	if v, ok := obj["secure"]; ok {
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("--cookie-attributes.secure must be a string")
		}
		if err := waitingRoomEnum("cookie-attributes.secure", s, waitingRoomCookieSecure); err != nil {
			return nil, err
		}
	}
	if samesite, _ := obj["samesite"].(string); samesite == "none" {
		if secure, _ := obj["secure"].(string); secure == "never" {
			return nil, errors.New("--cookie-attributes: samesite=none cannot be combined with secure=never")
		}
	}
	return obj, nil
}

func parseWaitingRoomRulesJSON(raw string) ([]any, error) {
	arr, err := parseWaitingRoomJSONArray("rules", raw)
	if err != nil {
		return nil, err
	}
	for i, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("--rules[%d] must be a JSON object", i)
		}
		action, _ := obj["action"].(string)
		if err := waitingRoomEnum("rules.action", action, waitingRoomRuleActions); err != nil {
			return nil, fmt.Errorf("--rules[%d]: %w", i, err)
		}
		expr, _ := obj["expression"].(string)
		if strings.TrimSpace(expr) == "" {
			return nil, fmt.Errorf("--rules[%d] requires a non-empty expression", i)
		}
	}
	return arr, nil
}

func parseWaitingRoomPosition(raw string) (map[string]any, error) {
	obj, err := parseWaitingRoomJSONObject("position", raw)
	if err != nil {
		return nil, err
	}
	keys := 0
	if v, ok := obj["index"]; ok {
		keys++
		n, ok := waitingRoomAsInt(v)
		if !ok || n < 1 {
			return nil, errors.New("--position.index must be an integer starting at 1")
		}
		obj["index"] = n
	}
	if _, ok := obj["before"]; ok {
		keys++
		if _, ok := obj["before"].(string); !ok {
			return nil, errors.New("--position.before must be a string rule ID")
		}
	}
	if _, ok := obj["after"]; ok {
		keys++
		if _, ok := obj["after"].(string); !ok {
			return nil, errors.New("--position.after must be a string rule ID")
		}
	}
	if keys != 1 {
		return nil, errors.New(`--position must be exactly one of {"index":N}, {"before":"id"}, or {"after":"id"}`)
	}
	return obj, nil
}

func parseWaitingRoomTime(flag, raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, fmt.Errorf("--%s must not be empty", flag)
	}
	// Accept RFC3339 and the slightly looser RFC3339Nano used in CF examples.
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--%s must be an ISO 8601 timestamp (RFC3339), got %q", flag, raw)
}

func waitingRoomValidateOriginCommands(cmds []string) error {
	if len(cmds) == 0 {
		return errors.New("--enabled-origin-command requires at least one value when set")
	}
	for _, c := range cmds {
		if err := waitingRoomEnum("enabled-origin-command", c, waitingRoomOriginCommands); err != nil {
			return err
		}
	}
	return nil
}

// --- room flags / body builders --------------------------------------------

type waitingRoomFlags struct {
	name, host, path, description, cookieSuffix             string
	customPageHTML, defaultTemplateLanguage, queueingMethod string
	turnstileMode, turnstileAction                          string
	additionalRoutesJSON, cookieAttributesJSON              string
	newUsersPerMinute, totalActiveUsers, sessionDuration    int
	queueingStatusCode                                      int
	queueAll, suspended, disableSessionRenewal              bool
	jsonResponseEnabled                                     bool
	enabledOriginCommands                                   []string
}

func bindWaitingRoomBodyFlags(cmd *cobra.Command, f *waitingRoomFlags, create bool) {
	flags := cmd.Flags()
	if create {
		flags.StringVar(&f.name, "name", "", "unique waiting room name (alphanumeric, hyphen, underscore)")
		flags.StringVar(&f.host, "host", "", "hostname the waiting room applies to (no scheme or wildcards)")
		flags.IntVar(&f.newUsersPerMinute, "new-users-per-minute", 0, "baseline new users admitted per minute (min 200)")
		flags.IntVar(&f.totalActiveUsers, "total-active-users", 0, "baseline concurrent active users on the route (min 200)")
		_ = cmd.MarkFlagRequired("name")
		_ = cmd.MarkFlagRequired("host")
		_ = cmd.MarkFlagRequired("new-users-per-minute")
		_ = cmd.MarkFlagRequired("total-active-users")
	} else {
		flags.StringVar(&f.name, "name", "", "unique waiting room name")
		flags.StringVar(&f.host, "host", "", "hostname the waiting room applies to")
		flags.IntVar(&f.newUsersPerMinute, "new-users-per-minute", 0, "baseline new users admitted per minute (min 200)")
		flags.IntVar(&f.totalActiveUsers, "total-active-users", 0, "baseline concurrent active users on the route (min 200)")
	}
	flags.StringVar(&f.path, "path", "", "path within the host (default / on create)")
	flags.StringVar(&f.description, "description", "", "human-readable description")
	flags.StringVar(&f.cookieSuffix, "cookie-suffix", "", "custom cookie name suffix (required with --additional-routes)")
	flags.StringVar(&f.customPageHTML, "custom-page-html", "", "mustache HTML template for the waiting room page")
	flags.StringVar(&f.defaultTemplateLanguage, "default-template-language", "", "language for the default template (for example en-US)")
	flags.StringVar(&f.queueingMethod, "queueing-method", "", "queueing method: fifo, random, passthrough, or reject")
	flags.StringVar(&f.turnstileMode, "turnstile-mode", "", "Turnstile mode: off, invisible, visible_non_interactive, or visible_managed")
	flags.StringVar(&f.turnstileAction, "turnstile-action", "", "Turnstile fail action: log or infinite_queue")
	flags.StringVar(&f.additionalRoutesJSON, "additional-routes", "", `JSON array of additional {"host","path"} routes`)
	flags.StringVar(&f.cookieAttributesJSON, "cookie-attributes", "", `JSON object of cookie attributes, e.g. {"samesite":"auto","secure":"auto"}`)
	flags.IntVar(&f.sessionDuration, "session-duration", 0, "session cookie lifetime in minutes (1-30)")
	flags.IntVar(&f.queueingStatusCode, "queueing-status-code", 0, "HTTP status while queueing: 200, 202, or 429")
	flags.BoolVar(&f.queueAll, "queue-all", false, "queue all traffic and block origin access")
	flags.BoolVar(&f.suspended, "suspended", false, "suspend the waiting room")
	flags.BoolVar(&f.disableSessionRenewal, "disable-session-renewal", false, "do not renew session cookies on each request")
	flags.BoolVar(&f.jsonResponseEnabled, "json-response-enabled", false, "return JSON status for Accept: application/json")
	flags.StringArrayVar(&f.enabledOriginCommands, "enabled-origin-command", nil, "enabled origin command (repeatable; only revoke)")
}

// waitingRoomFieldsFromFlags builds the flag-driven object fragment. create
// includes every required create field; update only includes changed flags.
// Cross-field constraints that depend on the full object (user pair after
// merge, cookie_suffix with additional_routes after merge) are checked by
// validateWaitingRoomObject, not here — except create, where the fragment is
// the complete body.
func waitingRoomFieldsFromFlags(cmd *cobra.Command, f waitingRoomFlags, create bool) (map[string]any, error) {
	body := map[string]any{}

	if create || cmd.Flags().Changed("name") {
		if err := waitingRoomValidateName("--name", f.name); err != nil {
			return nil, err
		}
		body["name"] = f.name
	}
	if create || cmd.Flags().Changed("host") {
		if err := waitingRoomValidateHost("--host", f.host); err != nil {
			return nil, err
		}
		body["host"] = f.host
	}

	newSet := create || cmd.Flags().Changed("new-users-per-minute")
	totalSet := create || cmd.Flags().Changed("total-active-users")
	if newSet {
		if err := waitingRoomValidateUsers("new-users-per-minute", f.newUsersPerMinute); err != nil {
			return nil, err
		}
		body["new_users_per_minute"] = f.newUsersPerMinute
	}
	if totalSet {
		if err := waitingRoomValidateUsers("total-active-users", f.totalActiveUsers); err != nil {
			return nil, err
		}
		body["total_active_users"] = f.totalActiveUsers
	}
	// When both limits appear in the same flag fragment, enforce the pair
	// before any network I/O; post-merge validation still catches cross-GET cases.
	if newSet && totalSet {
		if err := waitingRoomValidateUserPair(f.newUsersPerMinute, f.totalActiveUsers); err != nil {
			return nil, err
		}
	}

	if cmd.Flags().Changed("path") {
		if err := waitingRoomValidatePath("--path", f.path); err != nil {
			return nil, err
		}
		body["path"] = f.path
	}
	if cmd.Flags().Changed("description") {
		body["description"] = f.description
	}
	if cmd.Flags().Changed("cookie-suffix") {
		body["cookie_suffix"] = f.cookieSuffix
	}
	if cmd.Flags().Changed("custom-page-html") {
		body["custom_page_html"] = f.customPageHTML
	}
	if cmd.Flags().Changed("default-template-language") {
		if err := waitingRoomEnum("default-template-language", f.defaultTemplateLanguage, waitingRoomTemplateLangs); err != nil {
			return nil, err
		}
		body["default_template_language"] = f.defaultTemplateLanguage
	}
	if cmd.Flags().Changed("queueing-method") {
		if err := waitingRoomEnum("queueing-method", f.queueingMethod, waitingRoomQueueingMethods); err != nil {
			return nil, err
		}
		body["queueing_method"] = f.queueingMethod
	}
	if cmd.Flags().Changed("turnstile-mode") {
		if err := waitingRoomEnum("turnstile-mode", f.turnstileMode, waitingRoomTurnstileModes); err != nil {
			return nil, err
		}
		body["turnstile_mode"] = f.turnstileMode
	}
	if cmd.Flags().Changed("turnstile-action") {
		if err := waitingRoomEnum("turnstile-action", f.turnstileAction, waitingRoomTurnstileActs); err != nil {
			return nil, err
		}
		body["turnstile_action"] = f.turnstileAction
	}
	if cmd.Flags().Changed("session-duration") {
		if err := waitingRoomValidateSessionDuration(f.sessionDuration); err != nil {
			return nil, err
		}
		body["session_duration"] = f.sessionDuration
	}
	if cmd.Flags().Changed("queueing-status-code") {
		if err := waitingRoomValidateQueueStatusCode(f.queueingStatusCode); err != nil {
			return nil, err
		}
		body["queueing_status_code"] = f.queueingStatusCode
	}
	if cmd.Flags().Changed("queue-all") {
		body["queue_all"] = f.queueAll
	}
	if cmd.Flags().Changed("suspended") {
		body["suspended"] = f.suspended
	}
	if cmd.Flags().Changed("disable-session-renewal") {
		body["disable_session_renewal"] = f.disableSessionRenewal
	}
	if cmd.Flags().Changed("json-response-enabled") {
		body["json_response_enabled"] = f.jsonResponseEnabled
	}
	if cmd.Flags().Changed("enabled-origin-command") {
		if err := waitingRoomValidateOriginCommands(f.enabledOriginCommands); err != nil {
			return nil, err
		}
		body["enabled_origin_commands"] = f.enabledOriginCommands
	}
	if cmd.Flags().Changed("additional-routes") {
		routes, err := parseWaitingRoomAdditionalRoutes(f.additionalRoutesJSON)
		if err != nil {
			return nil, err
		}
		body["additional_routes"] = routes
	}
	if cmd.Flags().Changed("cookie-attributes") {
		attrs, err := parseWaitingRoomCookieAttributes(f.cookieAttributesJSON)
		if err != nil {
			return nil, err
		}
		body["cookie_attributes"] = attrs
	}

	if !create && len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one waiting room field")
	}
	return body, nil
}

// validateWaitingRoomObject checks the complete write body (create or
// post-merge update) against the pinned OpenAPI required fields and bounds.
func validateWaitingRoomObject(body map[string]any) error {
	name, _ := body["name"].(string)
	if err := waitingRoomValidateName("name", name); err != nil {
		return err
	}
	host, _ := body["host"].(string)
	if err := waitingRoomValidateHost("host", host); err != nil {
		return err
	}
	newUsers, okNew := waitingRoomAsInt(body["new_users_per_minute"])
	if !okNew {
		return errors.New("new_users_per_minute is required and must be an integer")
	}
	totalUsers, okTotal := waitingRoomAsInt(body["total_active_users"])
	if !okTotal {
		return errors.New("total_active_users is required and must be an integer")
	}
	if err := waitingRoomValidateUserPair(newUsers, totalUsers); err != nil {
		return err
	}
	if path, ok := body["path"]; ok {
		s, ok := path.(string)
		if !ok {
			return errors.New("path must be a string")
		}
		if err := waitingRoomValidatePath("path", s); err != nil {
			return err
		}
	}
	if v, ok := body["session_duration"]; ok && v != nil {
		n, ok := waitingRoomAsInt(v)
		if !ok {
			return errors.New("session_duration must be an integer")
		}
		if err := waitingRoomValidateSessionDuration(n); err != nil {
			return err
		}
	}
	if v, ok := body["queueing_status_code"]; ok && v != nil {
		n, ok := waitingRoomAsInt(v)
		if !ok {
			return errors.New("queueing_status_code must be an integer")
		}
		if err := waitingRoomValidateQueueStatusCode(n); err != nil {
			return err
		}
	}
	if v, ok := body["queueing_method"].(string); ok && v != "" {
		if err := waitingRoomEnum("queueing-method", v, waitingRoomQueueingMethods); err != nil {
			return err
		}
	}
	if v, ok := body["turnstile_mode"].(string); ok && v != "" {
		if err := waitingRoomEnum("turnstile-mode", v, waitingRoomTurnstileModes); err != nil {
			return err
		}
	}
	if v, ok := body["turnstile_action"].(string); ok && v != "" {
		if err := waitingRoomEnum("turnstile-action", v, waitingRoomTurnstileActs); err != nil {
			return err
		}
	}
	if v, ok := body["default_template_language"].(string); ok && v != "" {
		if err := waitingRoomEnum("default-template-language", v, waitingRoomTemplateLangs); err != nil {
			return err
		}
	}
	if raw, ok := body["additional_routes"]; ok && raw != nil {
		arr, ok := raw.([]any)
		if !ok {
			return errors.New("additional_routes must be a JSON array")
		}
		if err := waitingRoomValidateAdditionalRoutes(arr); err != nil {
			return err
		}
		if len(arr) > 0 {
			suffix, _ := body["cookie_suffix"].(string)
			if strings.TrimSpace(suffix) == "" {
				return errors.New("cookie_suffix is required when additional_routes is set")
			}
		}
	}
	if raw, ok := body["enabled_origin_commands"]; ok && raw != nil {
		cmds, err := waitingRoomStringSlice(raw)
		if err != nil {
			return fmt.Errorf("enabled_origin_commands: %w", err)
		}
		if len(cmds) > 0 {
			if err := waitingRoomValidateOriginCommands(cmds); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitingRoomStringSlice(raw any) ([]string, error) {
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, 0, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("[%d] must be a string", i)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, errors.New("must be an array of strings")
	}
}

func buildWaitingRoomCreateBody(cmd *cobra.Command, f waitingRoomFlags) ([]byte, error) {
	body, err := waitingRoomFieldsFromFlags(cmd, f, true)
	if err != nil {
		return nil, err
	}
	if err := validateWaitingRoomObject(body); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

// buildWaitingRoomUpdateBody GETs the current room as a raw object, merges the
// flag patch, strips read-only fields, validates the complete post-merge
// object, and returns the write body. The GET runs even under --dry-run.
func buildWaitingRoomUpdateBody(cmd *cobra.Command, client *api.Client, zoneID, roomID string, f waitingRoomFlags) ([]byte, error) {
	patch, err := waitingRoomFieldsFromFlags(cmd, f, false)
	if err != nil {
		return nil, err
	}
	cur, err := waitingRoomFetchObject(cmd.Context(), client, waitingRoomItemPath(zoneID, roomID), "waiting room "+roomID)
	if err != nil {
		return nil, err
	}
	waitingRoomStripReadOnly(cur, waitingRoomRoomReadOnly)
	merged := waitingRoomMergeObject(cur, patch)
	waitingRoomStripReadOnly(merged, waitingRoomRoomReadOnly)
	if err := validateWaitingRoomObject(merged); err != nil {
		return nil, fmt.Errorf("waiting room %s cannot be updated: %w", roomID, err)
	}
	return json.Marshal(merged)
}

// --- room commands ---------------------------------------------------------

func newWaitingRoomListCmd(g *globalOpts) *cobra.Command {
	var zone, scope string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List waiting rooms",
		Long: `List waiting rooms for a zone (default) or the whole account.

Examples:

  cf waiting-room list --zone example.com
  cf waiting-room list --scope account --account-id $ACCOUNT_ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch scope {
			case "zone", "account":
			default:
				return fmt.Errorf("--scope must be zone or account (got %q)", scope)
			}
			if scope == "account" && zone != "" {
				return errors.New("--zone requires --scope zone")
			}

			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			var path string
			if scope == "account" {
				if cfg.AccountID == "" {
					return errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
				}
				path = waitingRoomAccountPath(cfg.AccountID)
			} else {
				zoneID, err := resolveZoneInteractive(cmd, g, client, cfg, zone)
				if err != nil {
					return err
				}
				path = waitingRoomZonePath(zoneID)
			}

			q := url.Values{"per_page": {strconv.Itoa(waitingRoomListPerPage)}}
			req := api.Request{Method: "GET", Path: path, Query: q}
			if g.DryRun {
				return runWaitingRoomRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var rooms []waitingRoom
			if err := json.Unmarshal(env.Result, &rooms); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(rooms))
			for _, r := range rooms {
				rows = append(rows, []string{
					r.ID,
					output.Cell(r.Name),
					output.Cell(r.Host + r.Path),
					strconv.Itoa(r.NewUsersPerMinute),
					strconv.Itoa(r.TotalActiveUsers),
					r.QueueingMethod,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "NAME", "HOST/PATH", "NEW/MIN", "ACTIVE", "METHOD"}, rows)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "zone", "resource scope: zone or account")
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone; zone scope only)")
	return cmd
}

func newWaitingRoomGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get <waiting-room-id>",
		Short: "Show one waiting room",
		Long:  "Show one waiting room.\n\nExample:\n\n  cf waiting-room get 699d98642c564d2e855e9661899b7252 --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: waitingRoomItemPath(zoneID, args[0])}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newWaitingRoomCreateCmd(g *globalOpts) *cobra.Command {
	var zone string
	var f waitingRoomFlags
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a waiting room",
		Long: `Create a waiting room on a zone.

Examples:

  cf waiting-room create --zone example.com --name checkout --host shop.example.com \
    --new-users-per-minute 200 --total-active-users 300 --path /checkout
  cf waiting-room create --zone example.com --name sale --host shop.example.com \
    --new-users-per-minute 500 --total-active-users 1000 --queueing-method fifo --session-duration 10`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildWaitingRoomCreateBody(cmd, f)
			if err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: waitingRoomZonePath(zoneID), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindWaitingRoomBodyFlags(cmd, &f, true)
	return cmd
}

func newWaitingRoomUpdateCmd(g *globalOpts) *cobra.Command {
	var zone string
	var f waitingRoomFlags
	cmd := &cobra.Command{
		Use:   "update <waiting-room-id>",
		Short: "Update fields of a waiting room",
		Long: `Update selected fields of a waiting room.

The API requires a full waiting room object on write, so this command first
reads the room, merges your flags onto the raw object (preserving unknown
fields), strips read-only properties, and validates the complete result —
including new_users_per_minute <= total_active_users and cookie_suffix when
additional_routes is present. --dry-run performs that read but never sends
the write.

Examples:

  cf waiting-room update 699d98642c564d2e855e9661899b7252 --zone example.com --total-active-users 500
  cf waiting-room update 699d98642c564d2e855e9661899b7252 --zone example.com --suspended=true`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			// Validate the flag fragment before any network I/O.
			if _, err := waitingRoomFieldsFromFlags(cmd, f, false); err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			body, err := buildWaitingRoomUpdateBody(cmd, client, zoneID, args[0], f)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: waitingRoomItemPath(zoneID, args[0]), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindWaitingRoomBodyFlags(cmd, &f, false)
	return cmd
}

func newWaitingRoomDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <waiting-room-id>",
		Short: "Delete a waiting room",
		Long:  "Delete a waiting room.\n\nExample:\n\n  cf waiting-room delete 699d98642c564d2e855e9661899b7252 --zone example.com --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete waiting room %s from zone %s?", args[0], zoneID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: waitingRoomItemPath(zoneID, args[0])}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newWaitingRoomStatusCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "status <waiting-room-id>",
		Short: "Get waiting room queue status",
		Long:  "Get live queue status for a waiting room.\n\nExample:\n\n  cf waiting-room status 699d98642c564d2e855e9661899b7252 --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: waitingRoomItemPath(zoneID, args[0]) + "/status"}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newWaitingRoomPreviewCmd(g *globalOpts) *cobra.Command {
	var zone, customHTML string
	cmd := &cobra.Command{
		Use:   "preview",
		Short: "Preview a custom waiting room page",
		Long: `Upload custom waiting room HTML and receive a temporary preview URL.

Example:

  cf waiting-room preview --zone example.com --custom-html '{{#waitTimeKnown}}{{waitTime}} mins{{/waitTimeKnown}}'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(customHTML) == "" {
				return errors.New("--custom-html must not be empty")
			}
			body, err := json.Marshal(map[string]any{"custom_html": customHTML})
			if err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: waitingRoomZonePath(zoneID) + "/preview", Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&customHTML, "custom-html", "", "mustache HTML template to preview")
	_ = cmd.MarkFlagRequired("custom-html")
	return cmd
}

// --- events ----------------------------------------------------------------

func newWaitingRoomEventsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Manage waiting room events",
	}
	cmd.AddCommand(
		newWaitingRoomEventsListCmd(g),
		newWaitingRoomEventsGetCmd(g),
		newWaitingRoomEventsCreateCmd(g),
		newWaitingRoomEventsUpdateCmd(g),
		newWaitingRoomEventsDeleteCmd(g),
		newWaitingRoomEventsDetailsCmd(g),
	)
	return cmd
}

type waitingRoomEventFlags struct {
	name, description, customPageHTML                     string
	eventStartTime, eventEndTime, prequeueStartTime       string
	queueingMethod, turnstileMode, turnstileAction        string
	newUsersPerMinute, totalActiveUsers, sessionDuration  int
	shuffleAtEventStart, suspended, disableSessionRenewal bool
}

func bindWaitingRoomEventFlags(cmd *cobra.Command, f *waitingRoomEventFlags, create bool) {
	flags := cmd.Flags()
	if create {
		flags.StringVar(&f.name, "name", "", "unique event name (alphanumeric, hyphen, underscore)")
		flags.StringVar(&f.eventStartTime, "event-start-time", "", "event start (RFC3339)")
		flags.StringVar(&f.eventEndTime, "event-end-time", "", "event end (RFC3339)")
		_ = cmd.MarkFlagRequired("name")
		_ = cmd.MarkFlagRequired("event-start-time")
		_ = cmd.MarkFlagRequired("event-end-time")
	} else {
		flags.StringVar(&f.name, "name", "", "unique event name")
		flags.StringVar(&f.eventStartTime, "event-start-time", "", "event start (RFC3339)")
		flags.StringVar(&f.eventEndTime, "event-end-time", "", "event end (RFC3339)")
	}
	flags.StringVar(&f.prequeueStartTime, "prequeue-start-time", "", "prequeue start (RFC3339; at least 5 minutes before start)")
	flags.StringVar(&f.description, "description", "", "human-readable description")
	flags.StringVar(&f.customPageHTML, "custom-page-html", "", "override custom page HTML while the event is active")
	flags.StringVar(&f.queueingMethod, "queueing-method", "", "override queueing method while the event is active")
	flags.StringVar(&f.turnstileMode, "turnstile-mode", "", "override Turnstile mode while the event is active")
	flags.StringVar(&f.turnstileAction, "turnstile-action", "", "override Turnstile action while the event is active")
	flags.IntVar(&f.newUsersPerMinute, "new-users-per-minute", 0, "override new users per minute (requires --total-active-users)")
	flags.IntVar(&f.totalActiveUsers, "total-active-users", 0, "override total active users (requires --new-users-per-minute)")
	flags.IntVar(&f.sessionDuration, "session-duration", 0, "override session duration in minutes (1-30)")
	flags.BoolVar(&f.shuffleAtEventStart, "shuffle-at-event-start", false, "shuffle prequeued users when the event starts")
	flags.BoolVar(&f.suspended, "suspended", false, "suspend the event")
	flags.BoolVar(&f.disableSessionRenewal, "disable-session-renewal", false, "override session renewal while the event is active")
}

func waitingRoomEventFieldsFromFlags(cmd *cobra.Command, f waitingRoomEventFlags, create bool) (map[string]any, error) {
	body := map[string]any{}

	if create || cmd.Flags().Changed("name") {
		if err := waitingRoomValidateName("--name", f.name); err != nil {
			return nil, err
		}
		body["name"] = f.name
	}
	if create || cmd.Flags().Changed("event-start-time") {
		if _, err := parseWaitingRoomTime("event-start-time", f.eventStartTime); err != nil {
			return nil, err
		}
		body["event_start_time"] = f.eventStartTime
	}
	if create || cmd.Flags().Changed("event-end-time") {
		if _, err := parseWaitingRoomTime("event-end-time", f.eventEndTime); err != nil {
			return nil, err
		}
		body["event_end_time"] = f.eventEndTime
	}
	if cmd.Flags().Changed("prequeue-start-time") {
		if _, err := parseWaitingRoomTime("prequeue-start-time", f.prequeueStartTime); err != nil {
			return nil, err
		}
		body["prequeue_start_time"] = f.prequeueStartTime
	}
	if cmd.Flags().Changed("description") {
		body["description"] = f.description
	}
	if cmd.Flags().Changed("custom-page-html") {
		body["custom_page_html"] = f.customPageHTML
	}
	if cmd.Flags().Changed("queueing-method") {
		if err := waitingRoomEnum("queueing-method", f.queueingMethod, waitingRoomQueueingMethods); err != nil {
			return nil, err
		}
		body["queueing_method"] = f.queueingMethod
	}
	if cmd.Flags().Changed("turnstile-mode") {
		if err := waitingRoomEnum("turnstile-mode", f.turnstileMode, waitingRoomTurnstileModes); err != nil {
			return nil, err
		}
		body["turnstile_mode"] = f.turnstileMode
	}
	if cmd.Flags().Changed("turnstile-action") {
		if err := waitingRoomEnum("turnstile-action", f.turnstileAction, waitingRoomTurnstileActs); err != nil {
			return nil, err
		}
		body["turnstile_action"] = f.turnstileAction
	}

	newSet := cmd.Flags().Changed("new-users-per-minute")
	totalSet := cmd.Flags().Changed("total-active-users")
	if newSet != totalSet {
		return nil, errors.New("--new-users-per-minute and --total-active-users must be set together for events")
	}
	if newSet && totalSet {
		if err := waitingRoomValidateUserPair(f.newUsersPerMinute, f.totalActiveUsers); err != nil {
			return nil, err
		}
		body["new_users_per_minute"] = f.newUsersPerMinute
		body["total_active_users"] = f.totalActiveUsers
	}
	if cmd.Flags().Changed("session-duration") {
		if err := waitingRoomValidateSessionDuration(f.sessionDuration); err != nil {
			return nil, err
		}
		body["session_duration"] = f.sessionDuration
	}
	if cmd.Flags().Changed("shuffle-at-event-start") {
		body["shuffle_at_event_start"] = f.shuffleAtEventStart
	}
	if cmd.Flags().Changed("suspended") {
		body["suspended"] = f.suspended
	}
	if cmd.Flags().Changed("disable-session-renewal") {
		body["disable_session_renewal"] = f.disableSessionRenewal
	}

	if !create && len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one event field")
	}
	return body, nil
}

func validateWaitingRoomEventObject(body map[string]any) error {
	name, _ := body["name"].(string)
	if err := waitingRoomValidateName("name", name); err != nil {
		return err
	}
	startRaw, _ := body["event_start_time"].(string)
	start, err := parseWaitingRoomTime("event-start-time", startRaw)
	if err != nil {
		return err
	}
	endRaw, _ := body["event_end_time"].(string)
	end, err := parseWaitingRoomTime("event-end-time", endRaw)
	if err != nil {
		return err
	}
	if end.Sub(start) < waitingRoomEventMinLeadMinutes*time.Minute {
		return errors.New("event_start_time must be at least one minute before event_end_time")
	}
	if raw, ok := body["prequeue_start_time"]; ok && raw != nil {
		preRaw, ok := raw.(string)
		if !ok {
			return errors.New("prequeue_start_time must be a string")
		}
		// Empty string means clear/unset inherit in some APIs; treat as unset.
		if strings.TrimSpace(preRaw) != "" {
			prequeue, err := parseWaitingRoomTime("prequeue-start-time", preRaw)
			if err != nil {
				return err
			}
			if start.Sub(prequeue) < waitingRoomPrequeueLeadMinutes*time.Minute {
				return errors.New("prequeue_start_time must be at least five minutes before event_start_time")
			}
		}
	}
	_, hasNew := body["new_users_per_minute"]
	_, hasTotal := body["total_active_users"]
	// null means inherit; treat null as unset.
	if hasNew && body["new_users_per_minute"] == nil {
		hasNew = false
	}
	if hasTotal && body["total_active_users"] == nil {
		hasTotal = false
	}
	if hasNew != hasTotal {
		return errors.New("new_users_per_minute and total_active_users must both be set or both omitted on events")
	}
	if hasNew && hasTotal {
		newUsers, okNew := waitingRoomAsInt(body["new_users_per_minute"])
		totalUsers, okTotal := waitingRoomAsInt(body["total_active_users"])
		if !okNew || !okTotal {
			return errors.New("event user limits must be integers")
		}
		if err := waitingRoomValidateUserPair(newUsers, totalUsers); err != nil {
			return err
		}
	}
	if v, ok := body["session_duration"]; ok && v != nil {
		n, ok := waitingRoomAsInt(v)
		if !ok {
			return errors.New("session_duration must be an integer")
		}
		if err := waitingRoomValidateSessionDuration(n); err != nil {
			return err
		}
	}
	if v, ok := body["queueing_method"].(string); ok && v != "" {
		if err := waitingRoomEnum("queueing-method", v, waitingRoomQueueingMethods); err != nil {
			return err
		}
	}
	if v, ok := body["turnstile_mode"].(string); ok && v != "" {
		if err := waitingRoomEnum("turnstile-mode", v, waitingRoomTurnstileModes); err != nil {
			return err
		}
	}
	if v, ok := body["turnstile_action"].(string); ok && v != "" {
		if err := waitingRoomEnum("turnstile-action", v, waitingRoomTurnstileActs); err != nil {
			return err
		}
	}
	if shuffle, ok := body["shuffle_at_event_start"].(bool); ok && shuffle {
		pre, _ := body["prequeue_start_time"].(string)
		if strings.TrimSpace(pre) == "" {
			return errors.New("shuffle_at_event_start requires prequeue_start_time")
		}
	}
	return nil
}

func buildWaitingRoomEventCreateBody(cmd *cobra.Command, f waitingRoomEventFlags) ([]byte, error) {
	body, err := waitingRoomEventFieldsFromFlags(cmd, f, true)
	if err != nil {
		return nil, err
	}
	if err := validateWaitingRoomEventObject(body); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func buildWaitingRoomEventUpdateBody(cmd *cobra.Command, client *api.Client, zoneID, roomID, eventID string, f waitingRoomEventFlags) ([]byte, error) {
	patch, err := waitingRoomEventFieldsFromFlags(cmd, f, false)
	if err != nil {
		return nil, err
	}
	cur, err := waitingRoomFetchObject(cmd.Context(), client, waitingRoomEventPath(zoneID, roomID, eventID), "event "+eventID)
	if err != nil {
		return nil, err
	}
	waitingRoomStripReadOnly(cur, waitingRoomEventReadOnly)
	merged := waitingRoomMergeObject(cur, patch)
	waitingRoomStripReadOnly(merged, waitingRoomEventReadOnly)
	if err := validateWaitingRoomEventObject(merged); err != nil {
		return nil, fmt.Errorf("event %s cannot be updated: %w", eventID, err)
	}
	return json.Marshal(merged)
}

func newWaitingRoomEventsListCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "list <waiting-room-id>",
		Short: "List events for a waiting room",
		Long:  "List events for a waiting room.\n\nExample:\n\n  cf waiting-room events list 699d98642c564d2e855e9661899b7252 --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			q := url.Values{"per_page": {strconv.Itoa(waitingRoomListPerPage)}}
			req := api.Request{Method: "GET", Path: waitingRoomEventsPath(zoneID, args[0]), Query: q}
			if g.DryRun {
				return runWaitingRoomRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var events []waitingRoomEvent
			if err := json.Unmarshal(env.Result, &events); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(events))
			for _, e := range events {
				rows = append(rows, []string{
					e.ID,
					output.Cell(e.Name),
					e.EventStartTime,
					e.EventEndTime,
					e.QueueingMethod,
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "NAME", "START", "END", "METHOD"}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newWaitingRoomEventsGetCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "get <waiting-room-id> <event-id>",
		Short: "Show one waiting room event",
		Long:  "Show one waiting room event.\n\nExample:\n\n  cf waiting-room events get 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 --zone example.com",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return errors.New("waiting room ID and event ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: waitingRoomEventPath(zoneID, args[0], args[1])}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newWaitingRoomEventsCreateCmd(g *globalOpts) *cobra.Command {
	var zone string
	var f waitingRoomEventFlags
	cmd := &cobra.Command{
		Use:   "create <waiting-room-id>",
		Short: "Create a waiting room event",
		Long: `Create an event that temporarily changes waiting room behavior.

Example:

  cf waiting-room events create 699d98642c564d2e855e9661899b7252 --zone example.com \
    --name sale_open --event-start-time 2021-09-28T15:30:00.000Z \
    --event-end-time 2021-09-28T17:00:00.000Z --queueing-method random`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			body, err := buildWaitingRoomEventCreateBody(cmd, f)
			if err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: waitingRoomEventsPath(zoneID, args[0]), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindWaitingRoomEventFlags(cmd, &f, true)
	return cmd
}

func newWaitingRoomEventsUpdateCmd(g *globalOpts) *cobra.Command {
	var zone string
	var f waitingRoomEventFlags
	cmd := &cobra.Command{
		Use:   "update <waiting-room-id> <event-id>",
		Short: "Update fields of a waiting room event",
		Long: `Update selected fields of a waiting room event.

The API requires name, event_start_time, and event_end_time on write, so this
command first reads the event, merges your flags onto the raw object
(preserving unknown fields), strips read-only properties, and validates the
complete result. --dry-run performs that read but never sends the write.

Example:

  cf waiting-room events update 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 \
    --zone example.com --suspended=true`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return errors.New("waiting room ID and event ID must not be empty")
			}
			if _, err := waitingRoomEventFieldsFromFlags(cmd, f, false); err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			body, err := buildWaitingRoomEventUpdateBody(cmd, client, zoneID, args[0], args[1], f)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: waitingRoomEventPath(zoneID, args[0], args[1]), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	bindWaitingRoomEventFlags(cmd, &f, false)
	return cmd
}

func newWaitingRoomEventsDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <waiting-room-id> <event-id>",
		Short: "Delete a waiting room event",
		Long:  "Delete a waiting room event.\n\nExample:\n\n  cf waiting-room events delete 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 --zone example.com --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return errors.New("waiting room ID and event ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete event %s from waiting room %s?", args[1], args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: waitingRoomEventPath(zoneID, args[0], args[1])}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newWaitingRoomEventsDetailsCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "details <waiting-room-id> <event-id>",
		Short: "Preview active event details",
		Long:  "Preview the effective configuration of an active event.\n\nExample:\n\n  cf waiting-room events details 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 --zone example.com",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return errors.New("waiting room ID and event ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: waitingRoomEventPath(zoneID, args[0], args[1]) + "/details"}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

// --- rules -----------------------------------------------------------------

func newWaitingRoomRulesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rules",
		Short: "Manage waiting room rules",
	}
	cmd.AddCommand(
		newWaitingRoomRulesListCmd(g),
		newWaitingRoomRulesCreateCmd(g),
		newWaitingRoomRulesUpdateCmd(g),
		newWaitingRoomRulesReplaceCmd(g),
		newWaitingRoomRulesDeleteCmd(g),
	)
	return cmd
}

func newWaitingRoomRulesListCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "list <waiting-room-id>",
		Short: "List waiting room rules",
		Long:  "List rules for a waiting room.\n\nExample:\n\n  cf waiting-room rules list 699d98642c564d2e855e9661899b7252 --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: waitingRoomRulesPath(zoneID, args[0])}
			if g.DryRun {
				return runWaitingRoomRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var rules []waitingRoomRule
			if err := json.Unmarshal(env.Result, &rules); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(rules))
			for _, r := range rules {
				enabled := ""
				if r.Enabled != nil {
					enabled = strconv.FormatBool(*r.Enabled)
				}
				rows = append(rows, []string{
					r.ID,
					r.Action,
					output.Cell(r.Expression),
					enabled,
					output.Cell(r.Description),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(),
				[]string{"ID", "ACTION", "EXPRESSION", "ENABLED", "DESCRIPTION"}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newWaitingRoomRulesCreateCmd(g *globalOpts) *cobra.Command {
	var zone, action, expression, description string
	var enabled bool
	cmd := &cobra.Command{
		Use:   "create <waiting-room-id>",
		Short: "Create a waiting room rule",
		Long: `Create a bypass rule for a waiting room.

Example:

  cf waiting-room rules create 699d98642c564d2e855e9661899b7252 --zone example.com \
    --action bypass_waiting_room --expression 'ip.src in {10.20.30.40}' --enabled`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			body, err := buildWaitingRoomRuleBody(cmd, action, expression, description, enabled, "", true)
			if err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: waitingRoomRulesPath(zoneID, args[0]), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&action, "action", "bypass_waiting_room", "rule action (only bypass_waiting_room)")
	cmd.Flags().StringVar(&expression, "expression", "", "match expression")
	cmd.Flags().StringVar(&description, "description", "", "rule description")
	cmd.Flags().BoolVar(&enabled, "enabled", true, "whether the rule is enabled")
	_ = cmd.MarkFlagRequired("expression")
	return cmd
}

func newWaitingRoomRulesUpdateCmd(g *globalOpts) *cobra.Command {
	var zone, action, expression, description, positionJSON string
	var enabled bool
	cmd := &cobra.Command{
		Use:   "update <waiting-room-id> <rule-id>",
		Short: "Update a waiting room rule",
		Long: `Update selected fields of a waiting room rule.

The API requires action and expression on write, so this command first lists
rules to load the current rule as a raw object, merges your flags (preserving
unknown fields), strips read-only properties, and validates the complete
result. --dry-run performs that read but never sends the write.

Examples:

  cf waiting-room rules update 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 \
    --zone example.com --enabled=false
  cf waiting-room rules update 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 \
    --zone example.com --position '{"index":1}'`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return errors.New("waiting room ID and rule ID must not be empty")
			}
			if _, err := waitingRoomRuleFieldsFromFlags(cmd, action, expression, description, enabled, positionJSON, false); err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			body, err := buildWaitingRoomRuleUpdateBody(cmd, client, zoneID, args[0], args[1], action, expression, description, enabled, positionJSON)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PATCH", Path: waitingRoomRulePath(zoneID, args[0], args[1]), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&action, "action", "", "rule action (only bypass_waiting_room)")
	cmd.Flags().StringVar(&expression, "expression", "", "match expression")
	cmd.Flags().StringVar(&description, "description", "", "rule description")
	cmd.Flags().BoolVar(&enabled, "enabled", false, "whether the rule is enabled")
	cmd.Flags().StringVar(&positionJSON, "position", "", `JSON position object: {"index":N}, {"before":"id"}, or {"after":"id"}`)
	return cmd
}

func newWaitingRoomRulesReplaceCmd(g *globalOpts) *cobra.Command {
	var zone, rulesJSON string
	cmd := &cobra.Command{
		Use:   "replace <waiting-room-id>",
		Short: "Replace all waiting room rules",
		Long: `Replace the full rule list for a waiting room.

Example:

  cf waiting-room rules replace 699d98642c564d2e855e9661899b7252 --zone example.com \
    --rules '[{"action":"bypass_waiting_room","expression":"ip.src in {10.20.30.40}","enabled":true}]'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("waiting room ID must not be empty")
			}
			rules, err := parseWaitingRoomRulesJSON(rulesJSON)
			if err != nil {
				return err
			}
			body, err := json.Marshal(rules)
			if err != nil {
				return err
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			req := api.Request{Method: "PUT", Path: waitingRoomRulesPath(zoneID, args[0]), Body: body}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().StringVar(&rulesJSON, "rules", "", "JSON array of rule objects")
	_ = cmd.MarkFlagRequired("rules")
	return cmd
}

func newWaitingRoomRulesDeleteCmd(g *globalOpts) *cobra.Command {
	var zone string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <waiting-room-id> <rule-id>",
		Short: "Delete a waiting room rule",
		Long:  "Delete a waiting room rule.\n\nExample:\n\n  cf waiting-room rules delete 699d98642c564d2e855e9661899b7252 25756b2dfe6e378a06b033b670413757 --zone example.com --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[1]) == "" {
				return errors.New("waiting room ID and rule ID must not be empty")
			}
			client, zoneID, err := resolveWaitingRoomZone(cmd, g, zone)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete rule %s from waiting room %s?", args[1], args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: waitingRoomRulePath(zoneID, args[0], args[1])}
			return runWaitingRoomRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func waitingRoomRuleFieldsFromFlags(cmd *cobra.Command, action, expression, description string, enabled bool, positionJSON string, create bool) (map[string]any, error) {
	body := map[string]any{}
	if create || cmd.Flags().Changed("action") {
		if err := waitingRoomEnum("action", action, waitingRoomRuleActions); err != nil {
			return nil, err
		}
		body["action"] = action
	}
	if create || cmd.Flags().Changed("expression") {
		if strings.TrimSpace(expression) == "" {
			return nil, errors.New("--expression must not be empty")
		}
		body["expression"] = expression
	}
	if cmd.Flags().Changed("description") {
		body["description"] = description
	}
	if create || cmd.Flags().Changed("enabled") {
		body["enabled"] = enabled
	}
	if cmd.Flags().Changed("position") {
		pos, err := parseWaitingRoomPosition(positionJSON)
		if err != nil {
			return nil, err
		}
		body["position"] = pos
	}
	if !create && len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one of --action, --expression, --description, --enabled, --position")
	}
	return body, nil
}

func validateWaitingRoomRuleObject(body map[string]any) error {
	action, _ := body["action"].(string)
	if err := waitingRoomEnum("action", action, waitingRoomRuleActions); err != nil {
		return err
	}
	expr, _ := body["expression"].(string)
	if strings.TrimSpace(expr) == "" {
		return errors.New("expression must not be empty")
	}
	if raw, ok := body["position"]; ok && raw != nil {
		pos, ok := raw.(map[string]any)
		if !ok {
			return errors.New("position must be an object")
		}
		// Re-check via marshal/parse so index bounds apply when preserved.
		b, err := json.Marshal(pos)
		if err != nil {
			return err
		}
		if _, err := parseWaitingRoomPosition(string(b)); err != nil {
			return err
		}
	}
	return nil
}

func buildWaitingRoomRuleBody(cmd *cobra.Command, action, expression, description string, enabled bool, positionJSON string, create bool) ([]byte, error) {
	body, err := waitingRoomRuleFieldsFromFlags(cmd, action, expression, description, enabled, positionJSON, create)
	if err != nil {
		return nil, err
	}
	if create {
		if err := validateWaitingRoomRuleObject(body); err != nil {
			return nil, err
		}
	}
	return json.Marshal(body)
}

func buildWaitingRoomRuleUpdateBody(cmd *cobra.Command, client *api.Client, zoneID, roomID, ruleID, action, expression, description string, enabled bool, positionJSON string) ([]byte, error) {
	patch, err := waitingRoomRuleFieldsFromFlags(cmd, action, expression, description, enabled, positionJSON, false)
	if err != nil {
		return nil, err
	}
	cur, err := waitingRoomFetchRule(cmd.Context(), client, zoneID, roomID, ruleID)
	if err != nil {
		return nil, err
	}
	waitingRoomStripReadOnly(cur, waitingRoomRuleReadOnly)
	merged := waitingRoomMergeObject(cur, patch)
	waitingRoomStripReadOnly(merged, waitingRoomRuleReadOnly)
	if err := validateWaitingRoomRuleObject(merged); err != nil {
		return nil, fmt.Errorf("rule %s cannot be updated: %w", ruleID, err)
	}
	return json.Marshal(merged)
}
