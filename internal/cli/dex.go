package cli

// DEX porcelain: synthetic tests list/details, fleet status (devices/live),
// and traceroute results. Read-only analytics. Invests in table output for
// the common human workflows.
//
// See docs/STYLE.md and internal/cli/dns.go as exemplar.
// All validation of local input contract happens before client construction.
// Account-scoped only (no zones). Uses endpoint-local pagination for
// fleet-status/devices (page and per_page are required by the pinned API spec).
// Dry-run is deterministic (representative page-1 request for paged lists)
// and performs no reads.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

const (
	dexFleetPerPage            = 50
	dexMaxPages                = 1000
	dexLiveDefaultSinceMinutes = 60
)

func newDexCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dex",
		Short: "Digital Experience Monitoring (DEX) read-only analytics",
	}
	cmd.AddCommand(
		newDexTestsCmd(g),
		newDexFleetStatusCmd(g),
		newDexTracerouteCmd(g),
	)
	return cmd
}

func newDexTestsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tests",
		Short: "Synthetic test list and details",
	}
	cmd.AddCommand(
		newDexTestsListCmd(g),
		newDexTestsGetCmd(g),
	)
	return cmd
}

func newDexFleetStatusCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fleet-status",
		Short: "Fleet status (devices and live aggregates)",
	}
	cmd.AddCommand(
		newDexFleetStatusDevicesCmd(g),
		newDexFleetStatusLiveCmd(g),
	)
	return cmd
}

func newDexTracerouteCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "traceroute",
		Short: "Traceroute results",
	}
	cmd.AddCommand(newDexTracerouteResultsCmd(g))
	return cmd
}

// --- account helper (must be present for all dex commands) ---

func dexAccountID(cfg config.Resolved) (string, error) {
	if cfg.AccountID == "" {
		return "", errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return cfg.AccountID, nil
}

func dexPath(accountID, suffix string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/dex" + suffix
}

func dexTestsPath(accountID string) string {
	return dexPath(accountID, "/devices/dex_tests")
}

func dexTestPath(accountID, testID string) string {
	return dexTestsPath(accountID) + "/" + url.PathEscape(testID)
}

func dexFleetStatusDevicesPath(accountID string) string {
	return dexPath(accountID, "/fleet-status/devices")
}

func dexFleetStatusLivePath(accountID string) string {
	return dexPath(accountID, "/fleet-status/live")
}

func dexTracerouteResultNetworkPath(accountID, resultID string) string {
	return dexPath(accountID, "/traceroute-test-results/") + url.PathEscape(resultID) + "/network-path"
}

// --- validation (local contract, pre-client) ---

func validateDexKind(kind string) error {
	for _, k := range []string{"http", "traceroute"} {
		if kind == k {
			return nil
		}
	}
	return fmt.Errorf("--kind must be one of: http, traceroute")
}

func validateDexEnum(flag, value string, allowed []string) error {
	for _, a := range allowed {
		if value == a {
			return nil
		}
	}
	return fmt.Errorf("--%s must be one of: %s", flag, strings.Join(allowed, ", "))
}

func validateDexTimestamp(flag, v string) error {
	if v == "" {
		return nil
	}
	// millis since epoch
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return nil
	}
	// RFC3339 and common ISO variants accepted by the API (incl. pinned example with space and +00)
	layouts := []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05-07:00", "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05-0700"}
	for _, l := range layouts {
		if _, err := time.Parse(l, v); err == nil {
			return nil
		}
	}
	// accept pinned "2023-10-11 00:00:00+00" (normalize +00 -> +0000)
	if strings.HasSuffix(v, "+00") {
		v2 := strings.Replace(v, "+00", "+0000", 1)
		if _, err := time.Parse("2006-01-02 15:04:05-0700", v2); err == nil {
			return nil
		}
	}
	return fmt.Errorf("--%s must be RFC3339/ISO8601 or milliseconds since epoch", flag)
}

func validateDexSinceMinutes(m int) error {
	if m < 1 || m > 60 {
		return fmt.Errorf("--since-minutes must be between 1 and 60 (inclusive)")
	}
	return nil
}

func validateDexTestID(id string) error {
	if id == "" {
		return errors.New("dex-test-id must not be empty")
	}
	if n := utf8.RuneCountInString(id); n > 32 {
		return fmt.Errorf("dex-test-id is %d characters; the API allows at most 32", n)
	}
	return nil
}

func validateDexTracerouteResultID(id string) error {
	if id == "" {
		return errors.New("test-result-id must not be empty")
	}
	if n := utf8.RuneCountInString(id); n > 36 {
		return fmt.Errorf("test-result-id is %d characters; the API allows at most 36", n)
	}
	return nil
}

// --- models for table rendering ---

type dexTest struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type dexFleetDevice struct {
	DeviceID  string `json:"device_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Colo      string `json:"colo,omitempty"`
	Platform  string `json:"platform,omitempty"`
	Status    string `json:"status,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Version   string `json:"version,omitempty"`
}

// --- list helpers (endpoint local pagination where required) ---

func cloneURLValues(v url.Values) url.Values {
	if v == nil {
		return url.Values{}
	}
	out := make(url.Values, len(v))
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// listAllDexFleetDevices performs endpoint-local pagination.
// The fleet-status/devices endpoint requires both page and per_page on every
// request per the pinned API spec; DoAutoPaginate cannot be used.
// per_page max is 50 per the pinned spec.
func listAllDexFleetDevices(ctx context.Context, client *api.Client, accountID string, baseQ url.Values) ([]dexFleetDevice, []byte, error) {
	const perPage = dexFleetPerPage
	var allRaw []json.RawMessage
	var devices []dexFleetDevice
	for page := 1; page <= dexMaxPages; page++ {
		q := cloneURLValues(baseQ)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", strconv.Itoa(perPage))
		req := api.Request{
			Method: "GET",
			Path:   dexFleetStatusDevicesPath(accountID),
			Query:  q,
		}
		env, err := client.Do(ctx, req)
		if err != nil {
			return nil, nil, err
		}
		var items []json.RawMessage
		if err := json.Unmarshal(env.Result, &items); err != nil {
			if page == 1 {
				// unexpected shape on first page: fall back to raw for json path
				return nil, env.Result, nil
			}
			return nil, nil, fmt.Errorf("fleet-status devices page %d: result was not an array", page)
		}
		allRaw = append(allRaw, items...)
		for _, raw := range items {
			var d dexFleetDevice
			_ = json.Unmarshal(raw, &d)
			devices = append(devices, d)
		}
		// honor total_pages before short-page fallback (per review)
		if env.ResultInfo != nil && env.ResultInfo.TotalPages > 0 && page >= env.ResultInfo.TotalPages {
			break
		}
		if len(items) < perPage {
			break
		}
	}
	raw, err := json.Marshal(allRaw)
	if err != nil {
		return nil, nil, err
	}
	return devices, raw, nil
}

// --- commands: tests ---

func newDexTestsListCmd(g *globalOpts) *cobra.Command {
	var kind, testName string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List synthetic DEX tests",
		Long: `List Device DEX tests (synthetic monitoring).

Examples:

  cf dex tests list --account-id $ACCOUNT
  cf dex tests list --kind http --test-name login`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate the full local input contract before any client construction.
			if kind != "" {
				if err := validateDexKind(kind); err != nil {
					return err
				}
			}
			q := url.Values{}
			if kind != "" {
				q.Set("kind", kind)
			}
			if testName != "" {
				q.Set("testName", testName)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dexAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dexTestsPath(accountID), Query: q}
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
			var tests []dexTest
			if err := json.Unmarshal(env.Result, &tests); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(tests))
			for _, t := range tests {
				en := ""
				if t.Enabled != nil {
					en = strconv.FormatBool(*t.Enabled)
				}
				rows = append(rows, []string{t.ID, t.Name, t.Kind, en, output.Cell(t.Description)})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "KIND", "ENABLED", "DESCRIPTION"}, rows)
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "filter by test type (http, traceroute)")
	cmd.Flags().StringVar(&testName, "test-name", "", "filter by exact test name")
	return cmd
}

func newDexTestsGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <dex-test-id>",
		Short: "Show one synthetic DEX test",
		Long:  "Show details for a Device DEX test.\n\nExample:\n\n  cf dex tests get <dex-test-id> --account-id $ACCOUNT",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			testID := strings.TrimSpace(args[0])
			if err := validateDexTestID(testID); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dexAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dexTestPath(accountID, testID)}
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
		},
	}
	return cmd
}

// --- commands: fleet-status ---

func newDexFleetStatusDevicesCmd(g *globalOpts) *cobra.Command {
	var from, to, colo, deviceID, platform, status, sortBy, source string
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "List fleet device status details",
		Long: `List details of devices using WARP.

--from and --to are required (ISO 8601 or milliseconds since epoch).
Uses endpoint-local pagination (the API declares page and per_page required).
Dry-run emits a deterministic single-page request.

Examples:

  cf dex fleet-status devices --from 2026-08-01T00:00:00Z --to 2026-08-12T00:00:00Z --account-id $ID
  cf dex fleet-status devices --from 1723420800000 --to 1723507200000 --colo SJC --sort-by timestamp`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Full local contract validation before client.
			if from == "" {
				return errors.New("--from is required (ISO 8601 datetime or milliseconds since epoch)")
			}
			if to == "" {
				return errors.New("--to is required (ISO 8601 datetime or milliseconds since epoch)")
			}
			if err := validateDexTimestamp("from", from); err != nil {
				return err
			}
			if err := validateDexTimestamp("to", to); err != nil {
				return err
			}
			if sortBy != "" {
				if err := validateDexEnum("sort-by", sortBy, []string{"colo", "device_id", "mode", "platform", "status", "timestamp", "version"}); err != nil {
					return err
				}
			}
			if source != "" {
				if err := validateDexEnum("source", source, []string{"last_seen", "hourly", "raw"}); err != nil {
					return err
				}
			}
			q := url.Values{}
			q.Set("from", from)
			q.Set("to", to)
			if colo != "" {
				q.Set("colo", colo)
			}
			if deviceID != "" {
				q.Set("device_id", deviceID)
			}
			if platform != "" {
				q.Set("platform", platform)
			}
			if status != "" {
				q.Set("status", status)
			}
			if sortBy != "" {
				q.Set("sort_by", sortBy)
			}
			if source != "" {
				q.Set("source", source)
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dexAccountID(cfg)
			if err != nil {
				return err
			}
			if g.DryRun {
				// Deterministic dry-run body: first page only.
				dq := cloneURLValues(q)
				dq.Set("page", "1")
				dq.Set("per_page", strconv.Itoa(dexFleetPerPage))
				req := api.Request{Method: "GET", Path: dexFleetStatusDevicesPath(accountID), Query: dq}
				dump, err := client.Dump(req)
				if err != nil {
					return err
				}
				return g.renderValue(cmd, dump, output.JSON)
			}
			devs, raw, err := listAllDexFleetDevices(cmd.Context(), client, accountID, q)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, raw, output.JSON)
			}
			if len(devs) == 0 {
				// non-array first page (or empty): render raw rather than empty table
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, raw)
			}
			rows := make([][]string, 0, len(devs))
			for _, d := range devs {
				rows = append(rows, []string{
					d.DeviceID,
					d.Timestamp,
					d.Colo,
					d.Platform,
					d.Status,
					output.Cell(d.Version),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"DEVICE_ID", "TIMESTAMP", "COLO", "PLATFORM", "STATUS", "VERSION"}, rows)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "start of time range (required)")
	cmd.Flags().StringVar(&to, "to", "", "end of time range (required)")
	cmd.Flags().StringVar(&colo, "colo", "", "filter by colo airport code")
	cmd.Flags().StringVar(&deviceID, "device-id", "", "filter by device UUID")
	cmd.Flags().StringVar(&platform, "platform", "", "filter by OS platform")
	cmd.Flags().StringVar(&status, "status", "", "filter by network status")
	cmd.Flags().StringVar(&sortBy, "sort-by", "", "sort dimension: colo,device_id,mode,platform,status,timestamp,version")
	cmd.Flags().StringVar(&source, "source", "", "data source: last_seen,hourly,raw")
	return cmd
}

func newDexFleetStatusLiveCmd(g *globalOpts) *cobra.Command {
	var sinceMinutes int
	cmd := &cobra.Command{
		Use:   "live",
		Short: "Get live aggregate device details by dimension",
		Long: `Live fleet aggregates (by colo, platform, etc).

--since-minutes must be 1..60 (API requires it); defaults to 60.

Example:

  cf dex fleet-status live --since-minutes 5 --account-id $ID`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate explicit --since-minutes 1..60 before client construction.
			if err := validateDexSinceMinutes(sinceMinutes); err != nil {
				return err
			}
			q := url.Values{}
			q.Set("since_minutes", strconv.Itoa(sinceMinutes))
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dexAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dexFleetStatusLivePath(accountID), Query: q}
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
			// Aggregates are typically objects; render via result (supports --query/--output).
			return g.renderResult(cmd, env.Result, output.JSON)
		},
	}
	cmd.Flags().IntVar(&sinceMinutes, "since-minutes", dexLiveDefaultSinceMinutes, "lookback window in minutes (API requires this parameter)")
	return cmd
}

// --- commands: traceroute results ---

func newDexTracerouteResultsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "results <test-result-id>",
		Short: "Get network path details for a traceroute test run",
		Long:  "Fetch the network path for one traceroute test result.\n\nExample:\n\n  cf dex traceroute results <test-result-id> --account-id $ACCOUNT",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rid := strings.TrimSpace(args[0])
			if err := validateDexTracerouteResultID(rid); err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := dexAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: dexTracerouteResultNetworkPath(accountID, rid)}
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
		},
	}
	return cmd
}
