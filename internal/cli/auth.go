package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

func newAuthCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage API credentials",
	}
	cmd.AddCommand(newAuthLoginCmd(g), newAuthLogoutCmd(g), newAuthStatusCmd(g))
	return cmd
}

func newAuthLoginCmd(g *globalOpts) *cobra.Command {
	var accountID, zoneID string
	var setDefault, noVerify bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in with an API token (interactive on a terminal)",
		Long: `Log in to Cloudflare with an API token and save it to a profile.

On a terminal with no token supplied, login is interactive: it prompts for
the token (input hidden), verifies it against the API, and offers to pick a
default account and zone. Non-interactive forms:

  echo $MY_TOKEN | cf auth login
  cf auth login --token <token> --profile work
  cf auth login --token <token> --set-account-id <id> --set-zone-id <id>

Create tokens at https://dash.cloudflare.com/profile/api-tokens`,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := g.Token
			interactive := false
			if token == "" {
				st, _ := os.Stdin.Stat()
				switch {
				case st != nil && st.Mode()&os.ModeCharDevice == 0:
					data, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					token = strings.TrimSpace(string(data))
				case stdinIsTTY():
					interactive = true
					p := newPrompter()
					fmt.Fprintln(os.Stderr, "Create an API token at https://dash.cloudflare.com/profile/api-tokens")
					var err error
					token, err = p.askSecret("Paste your API token (input hidden): ")
					if err != nil {
						return err
					}
				}
			}
			if token == "" {
				return errors.New("no token provided: pass --token, pipe it on stdin, or run on a terminal for interactive login")
			}

			client := api.New(g.BaseURL, token, Version)
			if !noVerify {
				if _, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: "/user/tokens/verify"}); err != nil {
					return fmt.Errorf("token verification failed: %w (pass --no-verify to save anyway)", err)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "Token verified.")
			}

			f, err := config.Load()
			if err != nil {
				return err
			}
			name := g.Profile
			if name == "" {
				name = os.Getenv("CF_PROFILE")
			}
			if name == "" {
				name = "default"
			}
			p := f.Profiles[name]
			p.APIToken = token
			if accountID != "" {
				p.AccountID = accountID
			}
			if zoneID != "" {
				p.ZoneID = zoneID
			}

			if interactive {
				pr := newPrompter()
				if p.AccountID == "" {
					if id, label, err := pickAccount(cmd, client, pr); err == nil && id != "" {
						p.AccountID = id
						fmt.Fprintf(cmd.ErrOrStderr(), "Default account: %s\n", label)
					}
				}
				if p.ZoneID == "" {
					if yes, _ := pr.confirmYN("Set a default zone?"); yes {
						if id, label, err := pickZone(cmd, client, pr, p.AccountID); err == nil && id != "" {
							p.ZoneID = id
							fmt.Fprintf(cmd.ErrOrStderr(), "Default zone: %s\n", label)
						}
					}
				}
			}

			f.Profiles[name] = p
			if setDefault || f.DefaultProfile == "" {
				f.DefaultProfile = name
			}
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved profile %q in %s\n", name, config.Path())
			return nil
		},
	}
	cmd.Flags().StringVar(&accountID, "set-account-id", "", "also store a default account ID in the profile")
	cmd.Flags().StringVar(&zoneID, "set-zone-id", "", "also store a default zone ID in the profile")
	cmd.Flags().BoolVar(&setDefault, "default", false, "make this profile the default")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "skip token verification against the API")
	return cmd
}

type idName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// pickAccount lists the token's accounts and returns the chosen (or only)
// account ID. Empty on skip or when listing is not permitted by the token.
func pickAccount(cmd *cobra.Command, client *api.Client, pr *prompter) (string, string, error) {
	q := url.Values{}
	q.Set("per_page", "50")
	env, err := client.DoAutoPaginate(cmd.Context(), api.Request{Method: "GET", Path: "/accounts", Query: q})
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "note: could not list accounts with this token; set one later with `cf profile set`")
		return "", "", err
	}
	var accounts []idName
	if err := json.Unmarshal(env.Result, &accounts); err != nil || len(accounts) == 0 {
		return "", "", errors.New("no accounts visible to this token")
	}
	if len(accounts) == 1 {
		return accounts[0].ID, fmt.Sprintf("%s (%s)", accounts[0].Name, accounts[0].ID), nil
	}
	opts := make([]string, len(accounts))
	for i, a := range accounts {
		opts[i] = fmt.Sprintf("%s (%s)", a.Name, a.ID)
	}
	idx, err := pr.selectOption("Which account should be the default?", opts, true)
	if err != nil || idx < 0 {
		return "", "", err
	}
	return accounts[idx].ID, opts[idx], nil
}

// pickZone lists zones (scoped to accountID when set) and returns the chosen
// zone ID. Empty on skip.
func pickZone(cmd *cobra.Command, client *api.Client, pr *prompter, accountID string) (string, string, error) {
	q := url.Values{}
	q.Set("per_page", "50")
	if accountID != "" {
		q.Set("account.id", accountID)
	}
	env, err := client.DoAutoPaginate(cmd.Context(), api.Request{Method: "GET", Path: "/zones", Query: q})
	if err != nil {
		return "", "", err
	}
	var zones []idName
	if err := json.Unmarshal(env.Result, &zones); err != nil || len(zones) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "note: no zones visible; set one later with `cf profile set`")
		return "", "", errors.New("no zones")
	}
	if len(zones) == 1 {
		return zones[0].ID, fmt.Sprintf("%s (%s)", zones[0].Name, zones[0].ID), nil
	}
	opts := make([]string, len(zones))
	for i, z := range zones {
		opts[i] = fmt.Sprintf("%s (%s)", z.Name, z.ID)
	}
	idx, err := pr.selectOption("Which zone should be the default?", opts, true)
	if err != nil || idx < 0 {
		return "", "", err
	}
	return zones[idx].ID, opts[idx], nil
}

func newAuthLogoutCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored API token from a profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			cfg := config.Resolve(f, config.Overrides{Profile: g.Profile})
			p, ok := f.Profiles[cfg.Profile]
			if !ok || p.APIToken == "" {
				return fmt.Errorf("profile %q has no stored token", cfg.Profile)
			}
			p.APIToken = ""
			f.Profiles[cfg.Profile] = p
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed token from profile %q\n", cfg.Profile)
			return nil
		},
	}
}

func newAuthStatusCmd(g *globalOpts) *cobra.Command {
	var verify bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which credentials cf would use",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := g.resolve()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			status := map[string]any{
				"config_file":       config.Path(),
				"profile":           cfg.Profile,
				"token":             maskToken(cfg.Token),
				"token_source":      cfg.TokenSource,
				"account_id":        cfg.AccountID,
				"account_id_source": cfg.AccountIDSource,
				"zone_id":           cfg.ZoneID,
				"zone_id_source":    cfg.ZoneIDSource,
			}
			if verify {
				if cfg.Token == "" {
					return errors.New("no API token found; nothing to verify")
				}
				client := api.New(g.BaseURL, cfg.Token, Version)
				env, err := client.Do(cmd.Context(), api.Request{Method: "GET", Path: "/user/tokens/verify"})
				if err != nil {
					return fmt.Errorf("token verification failed: %w", err)
				}
				status["verify"] = string(env.Result)
			}
			return output.Render(out, g.format(output.JSON), status)
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "verify the token against the Cloudflare API")
	return cmd
}

func maskToken(t string) string {
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "********"
	}
	return "****" + t[len(t)-4:]
}
