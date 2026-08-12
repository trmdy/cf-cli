package cli

// AI is the Workers AI porcelain: run models (cf ai run <model>) with -f fields
// or --data for the input body, with readable rendering of common responses;
// and model list/search with table output.
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

func newAICmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ai",
		Short: "Workers AI",
	}
	cmd.AddCommand(
		newAIRunCmd(g),
		newAIModelsCmd(g),
	)
	return cmd
}

func newAIModelsCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List and search available AI models",
	}
	cmd.AddCommand(newAIModelsListCmd(g))
	return cmd
}

func aiAccountID(configured string) (string, error) {
	if configured == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return configured, nil
}

func aiModelsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/ai/models/search"
}

func aiRunPath(accountID, model string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/ai/run/" + url.PathEscape(model)
}

func newAIModelsListCmd(g *globalOpts) *cobra.Command {
	var search, author, task, format string
	var hideExperimental, includeDeprecated bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List and search AI models",
		Long: `List and search models available for Workers AI on the account.

Filters: --search, --author, --task, --format (openrouter), --hide-experimental, --include-deprecated.

Examples:

  cf ai models list
  cf ai models list --search llama --task "Text Generation"
  cf ai models list --author Meta`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if search != "" {
				q.Set("search", search)
			}
			if author != "" {
				q.Set("author", author)
			}
			if task != "" {
				q.Set("task", task)
			}
			if cmd.Flags().Changed("hide-experimental") {
				q.Set("hide_experimental", fmt.Sprintf("%t", hideExperimental))
			}
			if cmd.Flags().Changed("include-deprecated") {
				q.Set("include_deprecated", fmt.Sprintf("%t", includeDeprecated))
			}
			if format != "" {
				if format != "openrouter" {
					return fmt.Errorf("unknown --format %q (expected one of: openrouter)", format)
				}
				q.Set("format", format)
			}
			q.Set("per_page", "100")

			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := aiAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: aiModelsPath(accountID), Query: q}
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
			var models []aiModel
			if err := json.Unmarshal(env.Result, &models); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(models))
			for _, m := range models {
				taskName := m.Task.ID
				if m.Task.Name != "" {
					taskName = m.Task.Name
				}
				rows = append(rows, []string{m.ID, taskName, output.Cell(m.Description)})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "TASK", "DESCRIPTION"}, rows)
		},
	}
	cmd.Flags().StringVar(&search, "search", "", "search term across model metadata")
	cmd.Flags().StringVar(&author, "author", "", "filter by author")
	cmd.Flags().StringVar(&task, "task", "", "filter by task name or identifier")
	cmd.Flags().StringVar(&format, "format", "", "return in marketplace format (openrouter)")
	cmd.Flags().BoolVar(&hideExperimental, "hide-experimental", false, "hide experimental models")
	cmd.Flags().BoolVar(&includeDeprecated, "include-deprecated", false, "include deprecated models (within grace window)")
	return cmd
}

type aiModel struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Task        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"task"`
}

func newAIRunCmd(g *globalOpts) *cobra.Command {
	var data string
	var fields []string
	cmd := &cobra.Command{
		Use:   "run <model>",
		Short: "Run a Workers AI model",
		Long: `Run inference against a Workers AI model.

Supply the model input body via --data (JSON inline, @file, or @- for stdin) or repeated -f/--field key=value pairs (values are JSON-parsed when they look like JSON; dots create nesting).

The primary  workflow is cf ai run <model> with input, producing readable output for text responses.

Examples:

  cf ai run @cf/meta/llama-3-8b-instruct -f prompt="Summarize edge computing."
  cf ai run @cf/baai/bge-base-en-v1.5 --data '{"text":"search vector"}'
  cf ai run @cf/... --data @input.json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			model := strings.TrimSpace(args[0])
			if model == "" {
				return errors.New("model name cannot be empty")
			}
			// Validate the full local input contract (data/fields) BEFORE client
			// construction, account resolution, or any network work.
			body, err := buildBody(data, fields, cmd.InOrStdin())
			if err != nil {
				return err
			}
			if body == nil {
				return errors.New("no input provided: pass --data or -f/--field to supply the model input")
			}
			// --data must decode to a non-null JSON object before client construction.
			// (Repeated --field already produces an object.)
			var obj map[string]any
			if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
				return errors.New("--data must be a non-null JSON object")
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := aiAccountID(cfg.AccountID)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: aiRunPath(accountID, model), Body: body}
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
			return renderAIResponse(cmd, g, env.Result)
		},
	}
	cmd.Flags().StringVar(&data, "data", "", "model input JSON: inline string, @file, or @- for stdin")
	cmd.Flags().StringArrayVarP(&fields, "field", "f", nil, "set an input field as key=value (value JSON-parsed if possible; repeatable)")
	return cmd
}

func renderAIResponse(cmd *cobra.Command, g *globalOpts, raw json.RawMessage) error {
	if g.Query != "" || g.Output != "" {
		return g.renderResult(cmd, raw, output.JSON)
	}
	// Readable rendering for the common text-generation response shape.
	// Other result shapes (e.g. embeddings arrays) fall back to structured JSON.
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err == nil {
		if resp, ok := obj["response"].(string); ok && resp != "" {
			fmt.Fprint(cmd.OutOrStdout(), resp)
			if !strings.HasSuffix(resp, "\n") {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return nil
		}
	}
	return g.renderResult(cmd, raw, output.JSON)
}
