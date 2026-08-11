package cli

// D1 porcelain: database lifecycle, SQL queries, and SQL exports.
// See docs/STYLE.md; internal/cli/dns.go is the shape exemplar.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/output"
)

var d1ExportPollInterval = time.Second

type d1Database struct {
	UUID         string `json:"uuid,omitempty"`
	Name         string `json:"name,omitempty"`
	Jurisdiction string `json:"jurisdiction,omitempty"`
	Version      string `json:"version,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
}

func newD1Cmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "d1",
		Short: "Manage D1 databases",
	}
	cmd.AddCommand(
		newD1ListCmd(g),
		newD1GetCmd(g),
		newD1CreateCmd(g),
		newD1UpdateCmd(g),
		newD1DeleteCmd(g),
		newD1QueryCmd(g),
		newD1ExportCmd(g),
	)
	return cmd
}

func d1AccountID(accountID string) (string, error) {
	if accountID == "" {
		return "", errors.New("no account ID specified: set CLOUDFLARE_ACCOUNT_ID, configure a profile, or pass --account-id")
	}
	return accountID, nil
}

func d1DatabasesPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/d1/database"
}

func d1DatabasePath(accountID, databaseID string) string {
	return d1DatabasesPath(accountID) + "/" + url.PathEscape(databaseID)
}

func newD1ListCmd(g *globalOpts) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List D1 databases",
		Long:  "List D1 databases.\n\nExamples:\n\n  cf d1 list\n  cf d1 list --name app-data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			q := url.Values{"per_page": {"100"}}
			if name != "" {
				q.Set("name", name)
			}
			req := api.Request{Method: "GET", Path: d1DatabasesPath(accountID), Query: q}
			if g.DryRun {
				return runD1Request(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var databases []d1Database
			if err := json.Unmarshal(env.Result, &databases); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(databases))
			for _, database := range databases {
				rows = append(rows, []string{database.UUID, database.Name, database.Jurisdiction, database.Version, database.CreatedAt})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "NAME", "JURISDICTION", "VERSION", "CREATED"}, rows)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "filter by database name")
	return cmd
}

func newD1GetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "get <database-id>",
		Short: "Show one D1 database",
		Long:  "Show one D1 database.\n\nExample:\n\n  cf d1 get 7f0f5e3d-7d2e-4ef2-9db6-100000000001",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			return runD1Request(cmd, g, client, api.Request{Method: "GET", Path: d1DatabasePath(accountID, args[0])})
		},
	}
}

func newD1CreateCmd(g *globalOpts) *cobra.Command {
	var jurisdiction, primaryLocation, readReplication string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a D1 database",
		Long:  "Create a D1 database.\n\nExamples:\n\n  cf d1 create app-data\n  cf d1 create eu-data --jurisdiction eu --read-replication auto\n  cf d1 create regional-data --primary-location weur",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildD1CreateBody(args[0], jurisdiction, primaryLocation, readReplication)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			return runD1Request(cmd, g, client, api.Request{Method: "POST", Path: d1DatabasesPath(accountID), Body: body})
		},
	}
	cmd.Flags().StringVar(&jurisdiction, "jurisdiction", "", "data jurisdiction (eu, fedramp, or us)")
	cmd.Flags().StringVar(&primaryLocation, "primary-location", "", "preferred primary region (wnam, enam, weur, eeur, apac, or oc)")
	cmd.Flags().StringVar(&readReplication, "read-replication", "", "read replication mode (auto or disabled)")
	return cmd
}

func buildD1CreateBody(name, jurisdiction, primaryLocation, readReplication string) ([]byte, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("database name must not be empty")
	}
	jurisdiction, err := d1Jurisdiction(jurisdiction)
	if err != nil {
		return nil, err
	}
	primaryLocation, err = d1PrimaryLocation(primaryLocation)
	if err != nil {
		return nil, err
	}
	if jurisdiction != "" && primaryLocation != "" {
		return nil, errors.New("--jurisdiction and --primary-location cannot be used together")
	}
	body := map[string]any{"name": name}
	if jurisdiction != "" {
		body["jurisdiction"] = jurisdiction
	}
	if primaryLocation != "" {
		body["primary_location_hint"] = primaryLocation
	}
	if readReplication != "" {
		mode, err := d1ReadReplicationMode(readReplication)
		if err != nil {
			return nil, err
		}
		body["read_replication"] = map[string]string{"mode": mode}
	}
	return json.Marshal(body)
}

func d1Jurisdiction(value string) (string, error) {
	jurisdiction := strings.ToLower(strings.TrimSpace(value))
	if jurisdiction == "" || jurisdiction == "eu" || jurisdiction == "fedramp" || jurisdiction == "us" {
		return jurisdiction, nil
	}
	return "", errors.New("--jurisdiction must be eu, fedramp, or us")
}

func d1PrimaryLocation(value string) (string, error) {
	location := strings.ToLower(strings.TrimSpace(value))
	switch location {
	case "", "wnam", "enam", "weur", "eeur", "apac", "oc":
		return location, nil
	default:
		return "", errors.New("--primary-location must be wnam, enam, weur, eeur, apac, or oc")
	}
}

func newD1UpdateCmd(g *globalOpts) *cobra.Command {
	var readReplication string
	cmd := &cobra.Command{
		Use:   "update <database-id>",
		Short: "Update D1 database settings",
		Long:  "Update D1 database settings.\n\nExample:\n\n  cf d1 update 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --read-replication auto",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("read-replication") {
				return errors.New("nothing to update: pass --read-replication auto or --read-replication disabled")
			}
			body, err := buildD1UpdateBody(readReplication)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			return runD1Request(cmd, g, client, api.Request{Method: "PATCH", Path: d1DatabasePath(accountID, args[0]), Body: body})
		},
	}
	cmd.Flags().StringVar(&readReplication, "read-replication", "", "read replication mode (auto or disabled)")
	return cmd
}

func buildD1UpdateBody(readReplication string) ([]byte, error) {
	mode, err := d1ReadReplicationMode(readReplication)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"read_replication": map[string]string{"mode": mode}})
}

func d1ReadReplicationMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode != "auto" && mode != "disabled" {
		return "", errors.New("--read-replication must be auto or disabled")
	}
	return mode, nil
}

func newD1DeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <database-id>",
		Short: "Delete a D1 database",
		Long:  "Delete a D1 database.\n\nExample:\n\n  cf d1 delete 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete D1 database %s from account %s?", args[0], accountID)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			return runD1Request(cmd, g, client, api.Request{Method: "DELETE", Path: d1DatabasePath(accountID, args[0])})
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newD1QueryCmd(g *globalOpts) *cobra.Command {
	var command string
	var params []string
	cmd := &cobra.Command{
		Use:   "query <database-id>",
		Short: "Execute SQL against a D1 database",
		Long:  "Execute SQL against a D1 database. Pass SQL directly or use @file to read it from a file.\n\nExamples:\n\n  cf d1 query 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --command 'SELECT * FROM users'\n  cf d1 query 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --command @schema.sql\n  cf d1 query 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --command 'SELECT * FROM users WHERE id = ?' --param 42",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildD1QueryBody(command, params)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			return runD1Request(cmd, g, client, api.Request{Method: "POST", Path: d1DatabasePath(accountID, args[0]) + "/query", Body: body})
		},
	}
	cmd.Flags().StringVar(&command, "command", "", "SQL command, or @file containing SQL")
	cmd.Flags().StringArrayVar(&params, "param", nil, "SQL parameter value (repeatable)")
	_ = cmd.MarkFlagRequired("command")
	return cmd
}

func buildD1QueryBody(command string, params []string) ([]byte, error) {
	sql, err := d1SQLCommand(command)
	if err != nil {
		return nil, err
	}
	body := map[string]any{"sql": sql}
	if len(params) > 0 {
		body["params"] = params
	}
	return json.Marshal(body)
}

func d1SQLCommand(command string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", errors.New("--command must contain SQL; pass SQL directly or @file")
	}
	if !strings.HasPrefix(command, "@") {
		return command, nil
	}
	path := strings.TrimPrefix(command, "@")
	if path == "" {
		return "", errors.New("--command @file requires a file path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read SQL file %q: %w", path, err)
	}
	sql := string(data)
	if strings.TrimSpace(sql) == "" {
		return "", fmt.Errorf("SQL file %q is empty", path)
	}
	return sql, nil
}

func newD1ExportCmd(g *globalOpts) *cobra.Command {
	var noData, noSchema bool
	var tables []string
	cmd := &cobra.Command{
		Use:   "export <database-id>",
		Short: "Export a D1 database as SQL",
		Long:  "Export a D1 database as SQL and print its temporary download URL.\n\nExamples:\n\n  cf d1 export 7f0f5e3d-7d2e-4ef2-9db6-100000000001\n  cf d1 export 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --no-data\n  cf d1 export 7f0f5e3d-7d2e-4ef2-9db6-100000000001 --table users --table sessions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildD1ExportBody(noData, noSchema, tables, "")
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := d1AccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			path := d1DatabasePath(accountID, args[0]) + "/export"
			if g.DryRun {
				return runD1Request(cmd, g, client, api.Request{Method: "POST", Path: path, Body: body})
			}
			return runD1Export(cmd.Context(), cmd, g, client, path, body)
		},
	}
	cmd.Flags().BoolVar(&noData, "no-data", false, "export table definitions without their contents")
	cmd.Flags().BoolVar(&noSchema, "no-schema", false, "export table contents without their definitions")
	cmd.Flags().StringArrayVar(&tables, "table", nil, "limit the export to a table (repeatable)")
	return cmd
}

func buildD1ExportBody(noData, noSchema bool, tables []string, currentBookmark string) ([]byte, error) {
	if noData && noSchema {
		return nil, errors.New("--no-data and --no-schema cannot be used together")
	}
	body := map[string]any{"output_format": "polling"}
	if currentBookmark != "" {
		body["current_bookmark"] = currentBookmark
		return json.Marshal(body)
	}
	dumpOptions := map[string]any{}
	if noData {
		dumpOptions["no_data"] = true
	}
	if noSchema {
		dumpOptions["no_schema"] = true
	}
	if len(tables) > 0 {
		if err := validateNonEmptyStrings("table", tables); err != nil {
			return nil, err
		}
		dumpOptions["tables"] = tables
	}
	if len(dumpOptions) > 0 {
		body["dump_options"] = dumpOptions
	}
	return json.Marshal(body)
}

func runD1Export(ctx context.Context, cmd *cobra.Command, g *globalOpts, client *api.Client, path string, body []byte) error {
	for {
		env, err := client.Do(ctx, api.Request{Method: "POST", Path: path, Body: body})
		if err != nil {
			return err
		}
		var result struct {
			AtBookmark string `json:"at_bookmark"`
			Error      string `json:"error"`
			Status     string `json:"status"`
		}
		if err := json.Unmarshal(env.Result, &result); err != nil {
			return fmt.Errorf("parse D1 export response: %w", err)
		}
		switch result.Status {
		case "complete":
			return g.renderResult(cmd, env.Result, output.JSON)
		case "error":
			if result.Error != "" {
				return fmt.Errorf("D1 export failed: %s", result.Error)
			}
			return errors.New("D1 export failed")
		}
		if result.AtBookmark == "" {
			return errors.New("D1 export is in progress but did not return a polling bookmark")
		}
		body, err = buildD1ExportBody(false, false, nil, result.AtBookmark)
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d1ExportPollInterval):
		}
	}
}

func runD1Request(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
