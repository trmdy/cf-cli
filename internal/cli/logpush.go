package cli

// Logpush porcelain manages account- and zone-scoped log delivery jobs.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

type logpushScope struct {
	scope string
	zone  string
}

type logpushJob struct {
	ID              int64  `json:"id,omitempty"`
	Dataset         string `json:"dataset,omitempty"`
	DestinationConf string `json:"destination_conf,omitempty"`
	Enabled         *bool  `json:"enabled,omitempty"`
	Name            string `json:"name,omitempty"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

func newLogpushCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logpush",
		Short: "Manage Logpush jobs and destinations",
	}
	cmd.AddCommand(
		newLogpushJobsCmd(g),
		newLogpushDatasetsCmd(g),
		newLogpushOwnershipCmd(g),
	)
	return cmd
}

func newLogpushJobsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "jobs", Short: "Manage Logpush jobs"}
	cmd.AddCommand(
		newLogpushJobsListCmd(g),
		newLogpushJobsGetCmd(g),
		newLogpushJobsCreateCmd(g),
		newLogpushJobsUpdateCmd(g),
		newLogpushJobsDeleteCmd(g),
	)
	return cmd
}

func newLogpushDatasetsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "datasets", Short: "Inspect Logpush datasets"}
	cmd.AddCommand(newLogpushDatasetFieldsCmd(g))
	return cmd
}

func newLogpushOwnershipCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{Use: "ownership", Short: "Verify Logpush destination ownership"}
	cmd.AddCommand(
		newLogpushOwnershipChallengeCmd(g),
		newLogpushOwnershipValidateCmd(g),
	)
	return cmd
}

func addLogpushScopeFlags(cmd *cobra.Command, scope *logpushScope) {
	cmd.Flags().StringVar(&scope.scope, "scope", "account", "resource scope: account or zone")
	cmd.Flags().StringVar(&scope.zone, "zone", "", "zone name or ID (required with --scope zone; default: configured zone)")
}

// resolveLogpushPath turns the explicit porcelain scope into the API prefix.
// A zone name is resolved in the same way as other zone-scoped porcelain.
func resolveLogpushPath(cmd *cobra.Command, g *globalOpts, scope logpushScope) (*api.Client, string, error) {
	client, cfg, err := g.client(true)
	if err != nil {
		return nil, "", err
	}
	switch scope.scope {
	case "account":
		if scope.zone != "" {
			return nil, "", errors.New("--zone requires --scope zone")
		}
		if cfg.AccountID == "" {
			return nil, "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
		}
		return client, "/accounts/" + url.PathEscape(cfg.AccountID) + "/logpush", nil
	case "zone":
		zoneID, err := resolveZoneID(cmd.Context(), client, cfg.ZoneID, scope.zone)
		if err != nil {
			return nil, "", err
		}
		return client, "/zones/" + url.PathEscape(zoneID) + "/logpush", nil
	default:
		return nil, "", fmt.Errorf("--scope must be account or zone (got %q)", scope.scope)
	}
}

func logpushJobPath(prefix, jobID string) (string, error) {
	id, err := strconv.ParseInt(jobID, 10, 64)
	if err != nil || id < 1 {
		return "", fmt.Errorf("job ID must be a positive integer (got %q)", jobID)
	}
	return prefix + "/jobs/" + strconv.FormatInt(id, 10), nil
}

func newLogpushJobsListCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Logpush jobs",
		Long:  "List Logpush jobs.\n\nExamples:\n\n  cf logpush jobs list --account-id $ACCOUNT_ID\n  cf logpush jobs list --scope zone --zone example.com",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runLogpushJobsListRequest(cmd, g, client, api.Request{Method: "GET", Path: prefix + "/jobs"})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	return cmd
}

func newLogpushJobsGetCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	cmd := &cobra.Command{
		Use:   "get <job-id>",
		Short: "Show one Logpush job",
		Long:  "Show one Logpush job.\n\nExample:\n\n  cf logpush jobs get 12345 --account-id $ACCOUNT_ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			path, err := logpushJobPath(prefix, args[0])
			if err != nil {
				return err
			}
			return runLogpushRequest(cmd, g, client, api.Request{Method: "GET", Path: path})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	return cmd
}

type logpushJobOptions struct {
	dataset, destination, name, filter, ownershipChallenge string
	fields                                                 []string
	outputType, timestampFormat, recordTemplate            string
	enabled                                                bool
	maxUploadBytes, maxUploadInterval, maxUploadRecords    int
	sampleRate                                             float64
}

func addLogpushJobFlags(cmd *cobra.Command, options *logpushJobOptions, create bool) {
	flags := cmd.Flags()
	flags.StringVar(&options.dataset, "dataset", "", "Logpush dataset (for example, http_requests)")
	flags.StringVar(&options.destination, "destination", "", "destination configuration URI")
	flags.StringVar(&options.name, "name", "", "human-readable job name")
	flags.StringVar(&options.filter, "filter", "", "Logpush filter expression")
	flags.BoolVar(&options.enabled, "enabled", false, "enable or disable the job")
	flags.StringVar(&options.ownershipChallenge, "ownership-challenge", "", "destination ownership challenge token")
	flags.StringArrayVar(&options.fields, "field", nil, "output field name (repeatable)")
	flags.StringVar(&options.outputType, "output-type", "", "output type: ndjson or csv")
	flags.StringVar(&options.timestampFormat, "timestamp-format", "", "timestamp format (unixnano, unix, rfc3339, rfc3339ms, rfc3339ns)")
	flags.StringVar(&options.recordTemplate, "record-template", "", "template for each output record")
	flags.Float64Var(&options.sampleRate, "sample-rate", 0, "fraction of records to export (0 through 1)")
	flags.IntVar(&options.maxUploadBytes, "max-upload-bytes", 0, "maximum uncompressed bytes per batch (0 disables)")
	flags.IntVar(&options.maxUploadInterval, "max-upload-interval", 0, "maximum seconds between batches (0 disables)")
	flags.IntVar(&options.maxUploadRecords, "max-upload-records", 0, "maximum records per batch (0 disables)")
	if create {
		_ = cmd.MarkFlagRequired("dataset")
		_ = cmd.MarkFlagRequired("destination")
	}
}

// buildLogpushJobBody includes only changed fields for update, while create
// always includes its required dataset and destination.
func buildLogpushJobBody(cmd *cobra.Command, options logpushJobOptions, create bool) ([]byte, error) {
	if create && strings.TrimSpace(options.dataset) == "" {
		return nil, errors.New("--dataset must not be empty")
	}
	if create && strings.TrimSpace(options.destination) == "" {
		return nil, errors.New("--destination must not be empty")
	}
	body := map[string]any{}
	if create || cmd.Flags().Changed("dataset") {
		body["dataset"] = options.dataset
	}
	if create || cmd.Flags().Changed("destination") {
		body["destination_conf"] = options.destination
	}
	for _, field := range []struct {
		flag, key, value string
	}{
		{"name", "name", options.name},
		{"filter", "filter", options.filter},
		{"ownership-challenge", "ownership_challenge", options.ownershipChallenge},
	} {
		if cmd.Flags().Changed(field.flag) {
			body[field.key] = field.value
		}
	}
	if cmd.Flags().Changed("enabled") {
		body["enabled"] = options.enabled
	}
	if cmd.Flags().Changed("max-upload-bytes") {
		body["max_upload_bytes"] = options.maxUploadBytes
	}
	if cmd.Flags().Changed("max-upload-interval") {
		body["max_upload_interval_seconds"] = options.maxUploadInterval
	}
	if cmd.Flags().Changed("max-upload-records") {
		body["max_upload_records"] = options.maxUploadRecords
	}

	outputOptions := map[string]any{}
	if cmd.Flags().Changed("field") {
		if err := validateNonEmptyStrings("field", options.fields); err != nil {
			return nil, err
		}
		outputOptions["field_names"] = options.fields
	}
	if cmd.Flags().Changed("output-type") {
		if options.outputType != "ndjson" && options.outputType != "csv" {
			return nil, errors.New("--output-type must be ndjson or csv")
		}
		outputOptions["output_type"] = options.outputType
	}
	if cmd.Flags().Changed("timestamp-format") {
		switch options.timestampFormat {
		case "unixnano", "unix", "rfc3339", "rfc3339ms", "rfc3339ns":
			outputOptions["timestamp_format"] = options.timestampFormat
		default:
			return nil, errors.New("--timestamp-format must be unixnano, unix, rfc3339, rfc3339ms, or rfc3339ns")
		}
	}
	if cmd.Flags().Changed("record-template") {
		outputOptions["record_template"] = options.recordTemplate
	}
	if cmd.Flags().Changed("sample-rate") {
		if options.sampleRate < 0 || options.sampleRate > 1 {
			return nil, errors.New("--sample-rate must be between 0 and 1")
		}
		outputOptions["sample_rate"] = options.sampleRate
	}
	if len(outputOptions) > 0 {
		body["output_options"] = outputOptions
	}
	if len(body) == 0 {
		return nil, errors.New("nothing to update: pass at least one job option")
	}
	return json.Marshal(body)
}

func newLogpushJobsCreateCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	var options logpushJobOptions
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a Logpush job",
		Long:  "Create a Logpush job.\n\nExamples:\n\n  cf logpush jobs create --account-id $ACCOUNT_ID --dataset gateway_dns --destination 's3://logs-bucket/gateway?region=eu-west-1' --field ClientIP --field Datetime\n  cf logpush jobs create --scope zone --zone example.com --dataset http_requests --destination 's3://logs-bucket/http?region=eu-west-1'",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildLogpushJobBody(cmd, options, true)
			if err != nil {
				return err
			}
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runLogpushRequest(cmd, g, client, api.Request{Method: "POST", Path: prefix + "/jobs", Body: body})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	addLogpushJobFlags(cmd, &options, true)
	return cmd
}

func newLogpushJobsUpdateCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	var options logpushJobOptions
	cmd := &cobra.Command{
		Use:   "update <job-id>",
		Short: "Update a Logpush job",
		Long:  "Update fields of a Logpush job. Supplying any output option replaces that nested object, so include every output option you want to keep.\n\nExample:\n\n  cf logpush jobs update 12345 --account-id $ACCOUNT_ID --enabled=false\n  cf logpush jobs update 12345 --scope zone --zone example.com --field RayID --field ClientIP --timestamp-format rfc3339",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildLogpushJobBody(cmd, options, false)
			if err != nil {
				return err
			}
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			path, err := logpushJobPath(prefix, args[0])
			if err != nil {
				return err
			}
			return runLogpushRequest(cmd, g, client, api.Request{Method: "PUT", Path: path, Body: body})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	addLogpushJobFlags(cmd, &options, false)
	return cmd
}

func newLogpushJobsDeleteCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <job-id>",
		Short: "Delete a Logpush job",
		Long:  "Delete a Logpush job.\n\nExample:\n\n  cf logpush jobs delete 12345 --account-id $ACCOUNT_ID --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			path, err := logpushJobPath(prefix, args[0])
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Logpush job %s?", args[0])) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runLogpushRequest(cmd, g, client, api.Request{Method: "DELETE", Path: path})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newLogpushDatasetFieldsCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	cmd := &cobra.Command{
		Use:   "fields <dataset>",
		Short: "List fields available in a Logpush dataset",
		Long:  "List fields available in a Logpush dataset.\n\nExamples:\n\n  cf logpush datasets fields gateway_dns --account-id $ACCOUNT_ID\n  cf logpush datasets fields http_requests --scope zone --zone example.com",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: prefix + "/datasets/" + url.PathEscape(args[0]) + "/fields"}
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
			var fields map[string]string
			if err := json.Unmarshal(env.Result, &fields); err != nil {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			names := make([]string, 0, len(fields))
			for name := range fields {
				names = append(names, name)
			}
			slices.Sort(names)
			rows := make([][]string, 0, len(names))
			for _, name := range names {
				rows = append(rows, []string{name, output.Cell(fields[name])})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"FIELD", "DESCRIPTION"}, rows)
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	return cmd
}

func newLogpushOwnershipChallengeCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	var destination string
	cmd := &cobra.Command{
		Use:   "challenge",
		Short: "Request a destination ownership challenge",
		Long:  "Request a Logpush destination ownership challenge.\n\nExample:\n\n  cf logpush ownership challenge --account-id $ACCOUNT_ID --destination 's3://logs-bucket/gateway?region=eu-west-1'",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildLogpushOwnershipBody(destination, "", false)
			if err != nil {
				return err
			}
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runLogpushRequest(cmd, g, client, api.Request{Method: "POST", Path: prefix + "/ownership", Body: body})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	cmd.Flags().StringVar(&destination, "destination", "", "destination configuration URI")
	_ = cmd.MarkFlagRequired("destination")
	return cmd
}

func newLogpushOwnershipValidateCmd(g *globalOpts) *cobra.Command {
	var scope logpushScope
	var destination, challenge string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a destination ownership challenge",
		Long:  "Validate a Logpush destination ownership challenge.\n\nExample:\n\n  cf logpush ownership validate --account-id $ACCOUNT_ID --destination 's3://logs-bucket/gateway?region=eu-west-1' --challenge TOKEN",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildLogpushOwnershipBody(destination, challenge, true)
			if err != nil {
				return err
			}
			client, prefix, err := resolveLogpushPath(cmd, g, scope)
			if err != nil {
				return err
			}
			return runLogpushRequest(cmd, g, client, api.Request{Method: "POST", Path: prefix + "/ownership/validate", Body: body})
		},
	}
	addLogpushScopeFlags(cmd, &scope)
	cmd.Flags().StringVar(&destination, "destination", "", "destination configuration URI")
	cmd.Flags().StringVar(&challenge, "challenge", "", "ownership challenge token")
	_ = cmd.MarkFlagRequired("destination")
	_ = cmd.MarkFlagRequired("challenge")
	return cmd
}

func buildLogpushOwnershipBody(destination, challenge string, validate bool) ([]byte, error) {
	if strings.TrimSpace(destination) == "" {
		return nil, errors.New("--destination must not be empty")
	}
	body := map[string]string{"destination_conf": destination}
	if validate {
		if strings.TrimSpace(challenge) == "" {
			return nil, errors.New("--challenge must not be empty")
		}
		body["ownership_challenge"] = challenge
	}
	return json.Marshal(body)
}

func runLogpushRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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

func runLogpushJobsListRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
	var jobs []logpushJob
	if err := json.Unmarshal(env.Result, &jobs); err != nil {
		return g.renderResult(cmd, env.Result, output.JSON)
	}
	rows := make([][]string, 0, len(jobs))
	for _, job := range jobs {
		enabled := ""
		if job.Enabled != nil {
			enabled = strconv.FormatBool(*job.Enabled)
		}
		rows = append(rows, []string{
			strconv.FormatInt(job.ID, 10),
			job.Dataset,
			job.Name,
			output.Cell(job.DestinationConf),
			enabled,
			output.Cell(job.ErrorMessage),
		})
	}
	return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "DATASET", "NAME", "DESTINATION", "ENABLED", "ERROR"}, rows)
}
