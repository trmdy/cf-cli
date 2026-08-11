// Package cli wires up the cf command tree.
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/TormodHaugland/cf-cli/internal/api"
	"github.com/TormodHaugland/cf-cli/internal/config"
	"github.com/TormodHaugland/cf-cli/internal/output"
)

// Version is stamped via -ldflags at build time.
var Version = "dev"

type globalOpts struct {
	Output    string
	DryRun    bool
	Profile   string
	Token     string
	AccountID string
	ZoneID    string
	BaseURL   string
}

func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	g := &globalOpts{}
	cmd := &cobra.Command{
		Use:           "cf",
		Short:         "An open-source Cloudflare CLI",
		Long:          "cf is an open-source CLI for the entire Cloudflare API.\n\nUse the porcelain commands (cf dns ...) for common workflows, or\n`cf api <product> <operation>` for low-level access to every endpoint.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if g.Output != "" {
				if _, err := output.ParseFormat(g.Output); err != nil {
					return err
				}
			}
			return nil
		},
	}
	pf := cmd.PersistentFlags()
	pf.StringVarP(&g.Output, "output", "o", "", "output format: json, yaml, or table")
	pf.BoolVar(&g.DryRun, "dry-run", false, "print the HTTP request instead of sending it")
	pf.StringVar(&g.Profile, "profile", "", "config profile to use (default: $CF_PROFILE or 'default')")
	pf.StringVar(&g.Token, "token", "", "Cloudflare API token (default: $CLOUDFLARE_API_TOKEN or profile)")
	pf.StringVar(&g.AccountID, "account-id", "", "Cloudflare account ID (default: $CLOUDFLARE_ACCOUNT_ID or profile)")
	pf.StringVar(&g.ZoneID, "zone-id", "", "Cloudflare zone ID (default: $CLOUDFLARE_ZONE_ID or profile)")
	pf.StringVar(&g.BaseURL, "base-url", os.Getenv("CF_BASE_URL"), "API base URL override")
	_ = pf.MarkHidden("base-url")

	cmd.AddCommand(
		newVersionCmd(),
		newAuthCmd(g),
		newAPICmd(g),
		newDNSCmd(g),
	)
	return cmd
}

// resolve loads the config file and applies flag/env precedence.
func (g *globalOpts) resolve() (config.Resolved, error) {
	f, err := config.Load()
	if err != nil {
		return config.Resolved{}, err
	}
	return config.Resolve(f, config.Overrides{
		Profile:   g.Profile,
		Token:     g.Token,
		AccountID: g.AccountID,
		ZoneID:    g.ZoneID,
	}), nil
}

// client resolves config and builds an API client. When requireToken is set
// and no token is available (and this is not a dry run), it errors with a
// pointer to auth setup.
func (g *globalOpts) client(requireToken bool) (*api.Client, config.Resolved, error) {
	cfg, err := g.resolve()
	if err != nil {
		return nil, cfg, err
	}
	if requireToken && !g.DryRun && cfg.Token == "" {
		return nil, cfg, fmt.Errorf("no API token found; run `cf auth login`, set CLOUDFLARE_API_TOKEN, or pass --token")
	}
	return api.New(g.BaseURL, cfg.Token, Version), cfg, nil
}

// format returns the effective output format, falling back to the command's
// preferred default when --output was not given.
func (g *globalOpts) format(def output.Format) output.Format {
	if g.Output == "" {
		return def
	}
	f, err := output.ParseFormat(g.Output)
	if err != nil {
		return def
	}
	return f
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cf version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "cf %s\n", Version)
		},
	}
}
