package cli

// Pages porcelain: project CRUD, deployment inspection/rollback, and custom
// domain attachment. See docs/STYLE.md; internal/cli/dns.go is the shape
// exemplar.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

type pagesProject struct {
	ID               string   `json:"id,omitempty"`
	Name             string   `json:"name,omitempty"`
	Subdomain        string   `json:"subdomain,omitempty"`
	Domains          []string `json:"domains,omitempty"`
	ProductionBranch string   `json:"production_branch,omitempty"`
	CreatedOn        string   `json:"created_on,omitempty"`
}

type pagesStage struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

type pagesDeployment struct {
	ID          string     `json:"id,omitempty"`
	Environment string     `json:"environment,omitempty"`
	URL         string     `json:"url,omitempty"`
	CreatedOn   string     `json:"created_on,omitempty"`
	LatestStage pagesStage `json:"latest_stage"`
	Trigger     struct {
		Metadata struct {
			Branch string `json:"branch,omitempty"`
		} `json:"metadata"`
	} `json:"deployment_trigger"`
}

type pagesBuildConfig struct {
	BuildCommand   string `json:"build_command,omitempty"`
	DestinationDir string `json:"destination_dir,omitempty"`
	RootDir        string `json:"root_dir,omitempty"`
}

type pagesProjectCreate struct {
	Name             string            `json:"name"`
	ProductionBranch string            `json:"production_branch,omitempty"`
	BuildConfig      *pagesBuildConfig `json:"build_config,omitempty"`
}

func newPagesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pages",
		Short: "Manage Pages projects, deployments, and custom domains",
	}
	cmd.AddCommand(
		newPagesProjectCmd(g),
		newPagesDeploymentCmd(g),
		newPagesDomainCmd(g),
	)
	return cmd
}

// pagesAccountID resolves the account the Pages endpoints are scoped to. Pages
// is account-scoped only, so it uses the global --account-id/env/profile
// chain rather than a per-command flag.
func pagesAccountID(cfg config.Resolved) (string, error) {
	id := strings.TrimSpace(cfg.AccountID)
	if id == "" {
		return "", errors.New("no account specified: pass --account-id, set CLOUDFLARE_ACCOUNT_ID, or configure a profile")
	}
	return id, nil
}

func pagesProjectsPath(accountID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/pages/projects"
}

func pagesProjectPath(accountID, project string) string {
	return pagesProjectsPath(accountID) + "/" + url.PathEscape(project)
}

func pagesDeploymentsPath(accountID, project string) string {
	return pagesProjectPath(accountID, project) + "/deployments"
}

func pagesDeploymentPath(accountID, project, deployment string) string {
	return pagesDeploymentsPath(accountID, project) + "/" + url.PathEscape(deployment)
}

func pagesDomainsPath(accountID, project string) string {
	return pagesProjectPath(accountID, project) + "/domains"
}

func pagesDomainPath(accountID, project, domain string) string {
	return pagesDomainsPath(accountID, project) + "/" + url.PathEscape(domain)
}

// pagesArg trims a positional argument and rejects an empty one, so
// `cf pages project get ""` fails with a clear message instead of requesting a
// collection URL.
func pagesArg(name, value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

// pagesTime shortens an API timestamp for table display:
// "2021-03-09T00:55:03.923456Z" -> "2021-03-09 00:55:03".
func pagesTime(s string) string {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	// Some Pages timestamps come back without a zone offset
	// ("2021-03-09T00:58:59.045655"); fall back to trimming the prefix.
	if len(s) >= 19 {
		return strings.Replace(s[:19], "T", " ", 1)
	}
	return s
}

func newPagesProjectCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Manage Pages projects",
	}
	cmd.AddCommand(
		newPagesProjectListCmd(g),
		newPagesProjectGetCmd(g),
		newPagesProjectCreateCmd(g),
		newPagesProjectDeleteCmd(g),
	)
	return cmd
}

func newPagesProjectListCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Pages projects in the account",
		Long:  "List Pages projects in the account.\n\nExamples:\n\n  cf pages project list\n  cf pages project list --output json",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			// The Pages list endpoints paginate with page/per_page and report
			// result_info.total_pages; leave per_page at the API default and
			// let DoAutoPaginate walk the pages.
			req := api.Request{Method: "GET", Path: pagesProjectsPath(accountID)}
			if g.DryRun {
				return runPagesRequest(cmd, g, client, req)
			}
			env, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, env.Result, output.JSON)
			}
			var projects []pagesProject
			if err := json.Unmarshal(env.Result, &projects); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, env.Result)
			}
			rows := make([][]string, 0, len(projects))
			for _, p := range projects {
				rows = append(rows, []string{
					p.Name,
					p.ID,
					output.Cell(p.Subdomain),
					p.ProductionBranch,
					output.Cell(strings.Join(p.Domains, ",")),
					pagesTime(p.CreatedOn),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "ID", "SUBDOMAIN", "BRANCH", "DOMAINS", "CREATED"}, rows)
		},
	}
	return cmd
}

func newPagesProjectGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <project>",
		Short: "Show one Pages project",
		Long:  "Show one Pages project, including its build and deployment configuration.\n\nExamples:\n\n  cf pages project get my-site",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := pagesArg("project name", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: pagesProjectPath(accountID, project)}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newPagesProjectCreateCmd(g *globalOpts) *cobra.Command {
	var productionBranch, buildCommand, destinationDir, rootDir string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Pages project",
		Long: `Create a Pages project for direct uploads or Git builds.

Examples:

  cf pages project create my-site
  cf pages project create my-site --production-branch main
  cf pages project create my-site --build-command "npm run build" --destination-dir dist`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := buildPagesProjectBody(args[0], productionBranch, buildCommand, destinationDir, rootDir)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: pagesProjectsPath(accountID), Body: body}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().StringVar(&productionBranch, "production-branch", "main", "branch whose deployments are production")
	cmd.Flags().StringVar(&buildCommand, "build-command", "", "command used to build the project (Git projects)")
	cmd.Flags().StringVar(&destinationDir, "destination-dir", "", "build output directory to deploy")
	cmd.Flags().StringVar(&rootDir, "root-dir", "", "directory to run the build command in")
	return cmd
}

// buildPagesProjectBody assembles the create-project body, omitting the whole
// build_config object when no build flag was given.
func buildPagesProjectBody(name, productionBranch, buildCommand, destinationDir, rootDir string) ([]byte, error) {
	projectName, err := pagesArg("project name", name)
	if err != nil {
		return nil, err
	}
	p := pagesProjectCreate{
		Name:             projectName,
		ProductionBranch: strings.TrimSpace(productionBranch),
	}
	bc := pagesBuildConfig{
		BuildCommand:   strings.TrimSpace(buildCommand),
		DestinationDir: strings.TrimSpace(destinationDir),
		RootDir:        strings.TrimSpace(rootDir),
	}
	if bc != (pagesBuildConfig{}) {
		p.BuildConfig = &bc
	}
	return json.Marshal(p)
}

func newPagesProjectDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a Pages project",
		Long:  "Delete a Pages project and all of its deployments.\n\nExamples:\n\n  cf pages project delete my-site\n  cf pages project delete my-site --force",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := pagesArg("project name", args[0])
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Delete Pages project %s and all of its deployments?", project)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: pagesProjectPath(accountID, project)}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newPagesDeploymentCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deployment",
		Short: "Inspect and roll back Pages deployments",
	}
	cmd.AddCommand(
		newPagesDeploymentListCmd(g),
		newPagesDeploymentGetCmd(g),
		newPagesDeploymentRollbackCmd(g),
	)
	return cmd
}

func newPagesDeploymentListCmd(g *globalOpts) *cobra.Command {
	var env string
	cmd := &cobra.Command{
		Use:   "list <project>",
		Short: "List deployments of a Pages project",
		Long:  "List deployments of a Pages project, optionally filtered by environment.\n\nExamples:\n\n  cf pages deployment list my-site\n  cf pages deployment list my-site --env production",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := pagesArg("project name", args[0])
			if err != nil {
				return err
			}
			q, err := pagesDeploymentListQuery(env)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: pagesDeploymentsPath(accountID, project), Query: q}
			if g.DryRun {
				return runPagesRequest(cmd, g, client, req)
			}
			envelope, err := client.DoAutoPaginate(cmd.Context(), req)
			if err != nil {
				return err
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderResult(cmd, envelope.Result, output.JSON)
			}
			var deployments []pagesDeployment
			if err := json.Unmarshal(envelope.Result, &deployments); err != nil {
				return output.RenderRaw(cmd.OutOrStdout(), output.JSON, envelope.Result)
			}
			rows := make([][]string, 0, len(deployments))
			for _, d := range deployments {
				rows = append(rows, []string{
					d.ID,
					d.Environment,
					d.LatestStage.Status,
					d.LatestStage.Name,
					d.Trigger.Metadata.Branch,
					pagesTime(d.CreatedOn),
					output.Cell(d.URL),
				})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"ID", "ENVIRONMENT", "STATUS", "STAGE", "BRANCH", "CREATED", "URL"}, rows)
		},
	}
	cmd.Flags().StringVar(&env, "env", "", "filter by environment: production or preview")
	return cmd
}

// pagesDeploymentListQuery validates the --env filter against the values the
// API accepts.
func pagesDeploymentListQuery(env string) (url.Values, error) {
	q := url.Values{}
	switch e := strings.ToLower(strings.TrimSpace(env)); e {
	case "":
	case "production", "preview":
		q.Set("env", e)
	default:
		return nil, fmt.Errorf("--env must be production or preview (got %q)", env)
	}
	return q, nil
}

func newPagesDeploymentGetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <project> <deployment-id>",
		Short: "Show one Pages deployment",
		Long:  "Show one Pages deployment, including its build stages.\n\nExamples:\n\n  cf pages deployment get my-site f64788e9-fccd-4d4a-a28a-cb84f88f6c8b",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, deployment, err := pagesDeploymentArgs(args)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "GET", Path: pagesDeploymentPath(accountID, project, deployment)}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func newPagesDeploymentRollbackCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rollback <project> <deployment-id>",
		Short: "Roll a Pages project back to an earlier deployment",
		Long:  "Redeploy an earlier deployment, making it the live one for its environment.\n\nExamples:\n\n  cf pages deployment rollback my-site f64788e9-fccd-4d4a-a28a-cb84f88f6c8b\n  cf pages deployment rollback my-site f64788e9-fccd-4d4a-a28a-cb84f88f6c8b --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, deployment, err := pagesDeploymentArgs(args)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			// Rollback changes what visitors see, so it confirms like the
			// destructive commands do.
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Roll back Pages project %s to deployment %s?", project, deployment)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "POST", Path: pagesDeploymentPath(accountID, project, deployment) + "/rollback"}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func pagesDeploymentArgs(args []string) (project, deployment string, err error) {
	if project, err = pagesArg("project name", args[0]); err != nil {
		return "", "", err
	}
	if deployment, err = pagesArg("deployment ID", args[1]); err != nil {
		return "", "", err
	}
	return project, deployment, nil
}

func newPagesDomainCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage custom domains on a Pages project",
	}
	cmd.AddCommand(
		newPagesDomainAddCmd(g),
		newPagesDomainRemoveCmd(g),
	)
	return cmd
}

func newPagesDomainAddCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <project> <domain>",
		Short: "Attach a custom domain to a Pages project",
		Long:  "Attach a custom domain to a Pages project.\n\nExamples:\n\n  cf pages domain add my-site www.example.com",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, domain, err := pagesDomainArgs(args)
			if err != nil {
				return err
			}
			body, err := buildPagesDomainBody(domain)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			req := api.Request{Method: "POST", Path: pagesDomainsPath(accountID, project), Body: body}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	return cmd
}

func buildPagesDomainBody(domain string) ([]byte, error) {
	name, err := pagesArg("domain", domain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"name": name})
}

func newPagesDomainRemoveCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <project> <domain>",
		Short: "Detach a custom domain from a Pages project",
		Long:  "Detach a custom domain from a Pages project.\n\nExamples:\n\n  cf pages domain remove my-site www.example.com\n  cf pages domain remove my-site www.example.com --force",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			project, domain, err := pagesDomainArgs(args)
			if err != nil {
				return err
			}
			client, cfg, err := g.client(true)
			if err != nil {
				return err
			}
			accountID, err := pagesAccountID(cfg)
			if err != nil {
				return err
			}
			if !force && !g.DryRun {
				if !confirm(fmt.Sprintf("Remove domain %s from Pages project %s?", domain, project)) {
					return errors.New("aborted (pass --force to skip confirmation)")
				}
			}
			req := api.Request{Method: "DELETE", Path: pagesDomainPath(accountID, project, domain)}
			return runPagesRequest(cmd, g, client, req)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func pagesDomainArgs(args []string) (project, domain string, err error) {
	if project, err = pagesArg("project name", args[0]); err != nil {
		return "", "", err
	}
	if domain, err = pagesArg("domain", args[1]); err != nil {
		return "", "", err
	}
	return project, domain, nil
}

func runPagesRequest(cmd *cobra.Command, g *globalOpts, client *api.Client, req api.Request) error {
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
