package cli

// Workers platform porcelain covers schedules, custom domains, deployments,
// and observability usage. See docs/STYLE.md; internal/cli/dns.go is the
// shape exemplar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

const workersPlatformUsageMaxRangeMS int64 = 90 * 24 * 60 * 60 * 1000

type workersPlatformSchedule struct {
	Cron       string `json:"cron"`
	CreatedOn  string `json:"created_on,omitempty"`
	ModifiedOn string `json:"modified_on,omitempty"`
}

type workersPlatformSchedulesResult struct {
	Schedules []workersPlatformSchedule `json:"schedules"`
}

type workersPlatformDomain struct {
	ID       string `json:"id"`
	CertID   string `json:"cert_id"`
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
	ZoneID   string `json:"zone_id"`
	ZoneName string `json:"zone_name"`
}

type workersPlatformDeployment struct {
	ID        string `json:"id"`
	CreatedOn string `json:"created_on"`
	Source    string `json:"source"`
	Strategy  string `json:"strategy"`
	Versions  []struct {
		Percentage float64 `json:"percentage"`
		VersionID  string  `json:"version_id"`
	} `json:"versions"`
}

type workersPlatformDeploymentsResult struct {
	Deployments []workersPlatformDeployment `json:"deployments"`
}

type workersPlatformUsageResult struct {
	Events    float64 `json:"events"`
	Breakdown []struct {
		Bin     string  `json:"bin"`
		Dataset string  `json:"dataset"`
		Service string  `json:"service"`
		Count   float64 `json:"count"`
	} `json:"breakdown"`
}

func newWorkersPlatformCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "platform",
		Short: "Manage Worker schedules, domains, deployments, and usage",
	}
	cmd.AddCommand(
		newWorkersPlatformCronCmd(g),
		newWorkersPlatformDomainCmd(g),
		newWorkersPlatformDeploymentCmd(g),
		newWorkersPlatformUsageCmd(g),
	)
	return cmd
}

func workersPlatformScriptsPath(accountID, script string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/workers/scripts/" + url.PathEscape(script)
}

func workersPlatformDomainsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/workers/domains"
}

func workersPlatformRequireAccountID(accountID string) error {
	if strings.TrimSpace(accountID) == "" {
		return errors.New("missing account ID: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return nil
}

func workersPlatformRequiredArg(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return value, nil
}

func newWorkersPlatformCronCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cron",
		Short: "Manage Worker Cron Triggers",
	}
	cmd.AddCommand(newWorkersPlatformCronGetCmd(g), newWorkersPlatformCronSetCmd(g))
	return cmd
}

func newWorkersPlatformCronGetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <script-name>",
		Short: "Show Cron Triggers for a Worker",
		Long:  "Show Cron Triggers for a Worker.\n\nExample:\n\n  cf workers platform cron get daily-report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersPlatformRequiredArg("script name", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersPlatformScriptsPath(cfg.AccountID, script) + "/schedules"}
			if g.DryRun {
				return runWorkersPlatformRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var result workersPlatformSchedulesResult
			if err := json.Unmarshal(env.Result, &result); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(result.Schedules))
			for _, schedule := range result.Schedules {
				rows = append(rows, []string{schedule.Cron, schedule.CreatedOn, schedule.ModifiedOn})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"CRON", "CREATED", "MODIFIED"}, rows)
		},
	}
}

func newWorkersPlatformCronSetCmd(g *globalOpts) *cobra.Command {
	var schedules string
	cmd := &cobra.Command{
		Use:   "set <script-name>",
		Short: "Replace a Worker's Cron Triggers",
		Long: `Replace all Cron Triggers for a Worker. Pass an empty JSON array to remove every trigger.

Examples:

  cf workers platform cron set daily-report --schedules '[{"cron":"0 6 * * 1-5"}]'
  cf workers platform cron set daily-report --schedules '[]'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersPlatformRequiredArg("script name", args[0])
			if err != nil {
				return err
			}
			body, err := workersPlatformSchedulesBody(schedules)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			return runWorkersPlatformRequest(cmd, g, client, api.Request{Method: "PUT", Path: workersPlatformScriptsPath(cfg.AccountID, script) + "/schedules", Body: body})
		},
	}
	cmd.Flags().StringVar(&schedules, "schedules", "", "JSON array of schedule objects, each with a cron string")
	_ = cmd.MarkFlagRequired("schedules")
	return cmd
}

// workersPlatformSchedulesBody validates the exact JSON array accepted by
// the schedules endpoint. Decoding through any keeps JSON null and malformed
// element shapes from silently becoming nil Go values.
func workersPlatformSchedulesBody(raw string) ([]byte, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("--schedules must be a JSON array of schedule objects: %w", err)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.New("--schedules must be a JSON array of schedule objects")
	}
	schedules := make([]workersPlatformSchedule, 0, len(items))
	for i, item := range items {
		object, ok := item.(map[string]any)
		if !ok || object == nil {
			return nil, fmt.Errorf("--schedules[%d] must be a JSON object", i)
		}
		if len(object) != 1 {
			return nil, fmt.Errorf("--schedules[%d] must contain only cron", i)
		}
		cron, ok := object["cron"].(string)
		if !ok || strings.TrimSpace(cron) == "" {
			return nil, fmt.Errorf("--schedules[%d].cron must be a non-empty string", i)
		}
		schedules = append(schedules, workersPlatformSchedule{Cron: cron})
	}
	return json.Marshal(schedules)
}

func newWorkersPlatformDomainCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage Worker custom domains",
	}
	cmd.AddCommand(
		newWorkersPlatformDomainListCmd(g),
		newWorkersPlatformDomainAddCmd(g),
		newWorkersPlatformDomainRemoveCmd(g),
	)
	return cmd
}

func newWorkersPlatformDomainListCmd(g *globalOpts) *cobra.Command {
	var zone, service, hostname string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Worker custom domains",
		Long:  "List Worker custom domains. A zone name is resolved to its ID, including during --dry-run.\n\nExamples:\n\n  cf workers platform domain list\n  cf workers platform domain list --service app-worker --zone example.com",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := workersPlatformDomainListQuery(cmd, service, hostname)
			if err != nil {
				return err
			}
			zone, err = workersPlatformOptionalArg("--zone", zone)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			if zone != "" {
				if isZoneID(zone) {
					query.Set("zone_id", zone)
				} else {
					zoneID, _, err := resolveWorkersPlatformZone(cmd.Context(), client, zone)
					if err != nil {
						return err
					}
					query.Set("zone_id", zoneID)
				}
			}
			req := api.Request{Method: "GET", Path: workersPlatformDomainsPath(cfg.AccountID), Query: query}
			if g.DryRun {
				return runWorkersPlatformRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var domains []workersPlatformDomain
			if err := json.Unmarshal(env.Result, &domains); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(domains))
			for _, domain := range domains {
				rows = append(rows, []string{domain.ID, domain.Hostname, domain.Service, domain.ZoneName, domain.ZoneID})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "HOSTNAME", "SERVICE", "ZONE", "ZONE ID"}, rows)
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID")
	cmd.Flags().StringVar(&service, "service", "", "filter by Worker script name")
	cmd.Flags().StringVar(&hostname, "hostname", "", "filter by custom domain hostname")
	return cmd
}

func workersPlatformDomainListQuery(cmd *cobra.Command, service, hostname string) (url.Values, error) {
	query := url.Values{}
	for _, flag := range []struct {
		name  string
		value string
		key   string
	}{
		{"service", service, "service"},
		{"hostname", hostname, "hostname"},
	} {
		if cmd.Flags().Changed(flag.name) {
			value, err := workersPlatformRequiredArg("--"+flag.name, flag.value)
			if err != nil {
				return nil, err
			}
			query.Set(flag.key, value)
		}
	}
	return query, nil
}

func newWorkersPlatformDomainAddCmd(g *globalOpts) *cobra.Command {
	var zone string
	cmd := &cobra.Command{
		Use:   "add <hostname> <script-name>",
		Short: "Attach a custom domain to a Worker",
		Long: `Attach a custom domain to a Worker. The zone may be supplied by name or ID.

Example:

  cf workers platform domain add app.example.com app-worker --zone example.com`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname, err := workersPlatformRequiredArg("hostname", args[0])
			if err != nil {
				return err
			}
			service, err := workersPlatformRequiredArg("script name", args[1])
			if err != nil {
				return err
			}
			resolved, err := g.resolve()
			if err != nil {
				return err
			}
			if zone == "" {
				zone = resolved.ZoneID
			}
			zone = strings.TrimSpace(zone)
			if zone == "" {
				return errors.New("no zone specified: pass --zone, set CLOUDFLARE_ZONE_ID, or configure a profile")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			zoneID, zoneName, err := resolveWorkersPlatformZone(cmd.Context(), client, zone)
			if err != nil {
				return err
			}
			body, err := json.Marshal(map[string]string{
				"hostname":  hostname,
				"service":   service,
				"zone_id":   zoneID,
				"zone_name": zoneName,
			})
			if err != nil {
				return err
			}
			return runWorkersPlatformRequest(cmd, g, client, api.Request{Method: "PUT", Path: workersPlatformDomainsPath(cfg.AccountID), Body: body})
		},
	}
	cmd.Flags().StringVar(&zone, "zone", "", "zone name or ID (default: configured zone)")
	return cmd
}

func newWorkersPlatformDomainRemoveCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <domain-id>",
		Short: "Detach a custom domain from a Worker",
		Long:  "Detach a custom domain from its Worker.\n\nExample:\n\n  cf workers platform domain remove dbe10b4bc17c295377eabd600e1787fd --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			domainID, err := workersPlatformRequiredArg("domain ID", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Detach Worker custom domain %s?", domainID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runWorkersPlatformRequest(cmd, g, client, api.Request{Method: "DELETE", Path: workersPlatformDomainsPath(cfg.AccountID) + "/" + url.PathEscape(domainID)})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func workersPlatformOptionalArg(name, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return workersPlatformRequiredArg(name, value)
}

// resolveWorkersPlatformZone accepts a zone name or ID and returns both values
// needed by Workers custom-domain requests. It runs only after every local
// argument has been validated.
func resolveWorkersPlatformZone(ctx context.Context, client *api.Client, zone string) (string, string, error) {
	if isZoneID(zone) {
		env, err := client.Do(ctx, api.Request{Method: "GET", Path: "/zones/" + url.PathEscape(zone)})
		if err != nil {
			return "", "", fmt.Errorf("look up zone %q: %w", zone, err)
		}
		var result struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(env.Result, &result); err != nil || strings.TrimSpace(result.Name) == "" {
			return "", "", fmt.Errorf("look up zone %q: unexpected response", zone)
		}
		return zone, result.Name, nil
	}
	q := url.Values{"name": []string{zone}}
	env, err := client.Do(ctx, api.Request{Method: "GET", Path: "/zones", Query: q})
	if err != nil {
		return "", "", fmt.Errorf("look up zone %q: %w", zone, err)
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &zones); err != nil || len(zones) == 0 || strings.TrimSpace(zones[0].ID) == "" || strings.TrimSpace(zones[0].Name) == "" {
		return "", "", fmt.Errorf("zone %q not found on this account", zone)
	}
	return zones[0].ID, zones[0].Name, nil
}

func newWorkersPlatformDeploymentCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deployment",
		Short: "Inspect Worker deployments",
	}
	cmd.AddCommand(newWorkersPlatformDeploymentListCmd(g))
	return cmd
}

func newWorkersPlatformDeploymentListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list <script-name>",
		Short: "List deployments of a Worker",
		Long:  "List deployments of a Worker, with the active deployment first.\n\nExample:\n\n  cf workers platform deployment list app-worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			script, err := workersPlatformRequiredArg("script name", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: workersPlatformScriptsPath(cfg.AccountID, script) + "/deployments"}
			if g.DryRun {
				return runWorkersPlatformRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var result workersPlatformDeploymentsResult
			if err := json.Unmarshal(env.Result, &result); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(result.Deployments))
			for _, deployment := range result.Deployments {
				versions := make([]string, 0, len(deployment.Versions))
				for _, version := range deployment.Versions {
					versions = append(versions, fmt.Sprintf("%s (%s%%)", version.VersionID, strconv.FormatFloat(version.Percentage, 'f', -1, 64)))
				}
				rows = append(rows, []string{deployment.ID, deployment.CreatedOn, deployment.Source, deployment.Strategy, output.Cell(strings.Join(versions, ", "))})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "CREATED", "SOURCE", "STRATEGY", "VERSIONS"}, rows)
		},
	}
}

func newWorkersPlatformUsageCmd(g *globalOpts) *cobra.Command {
	var from, to string
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Show Worker observability event usage",
		Long: `Show Worker observability event usage by day, dataset, and service.
The queried range must be between 1 millisecond and 90 days.

Example:

  cf workers platform usage --from 1711929600000 --to 1712534400000`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			query, err := workersPlatformUsageQuery(from, to)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			if err := workersPlatformRequireAccountID(cfg.AccountID); err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: "/accounts/" + url.PathEscape(cfg.AccountID) + "/workers/observability/usage", Query: query}
			if g.DryRun {
				return runWorkersPlatformRequest(cmd, g, client, req)
			}
			env, err := client.Do(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var result workersPlatformUsageResult
			if err := json.Unmarshal(env.Result, &result); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(result.Breakdown))
			for _, entry := range result.Breakdown {
				rows = append(rows, []string{entry.Bin, entry.Dataset, entry.Service, strconv.FormatFloat(entry.Count, 'f', -1, 64)})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"DATE", "DATASET", "SERVICE", "EVENTS"}, rows)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "range start as a Unix timestamp in milliseconds")
	cmd.Flags().StringVar(&to, "to", "", "range end as a Unix timestamp in milliseconds")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func workersPlatformUsageQuery(from, to string) (url.Values, error) {
	fromMS, err := workersPlatformTimestamp("--from", from)
	if err != nil {
		return nil, err
	}
	toMS, err := workersPlatformTimestamp("--to", to)
	if err != nil {
		return nil, err
	}
	if toMS <= fromMS {
		return nil, errors.New("--to must be later than --from")
	}
	if toMS-fromMS > workersPlatformUsageMaxRangeMS {
		return nil, errors.New("--from and --to must cover at most 90 days")
	}
	return url.Values{"from": []string{strconv.FormatInt(fromMS, 10)}, "to": []string{strconv.FormatInt(toMS, 10)}}, nil
}

func workersPlatformTimestamp(name, value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	timestamp, err := strconv.ParseInt(value, 10, 64)
	if err != nil || timestamp < 0 {
		return 0, fmt.Errorf("%s must be a non-negative Unix timestamp in milliseconds", name)
	}
	return timestamp, nil
}

func runWorkersPlatformRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
