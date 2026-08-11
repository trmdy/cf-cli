package cli

import (
	"errors"
	"fmt"
	"io"
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
	var setDefault bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Save an API token to a profile",
		Long:  "Saves a Cloudflare API token (and optional default account/zone) to the\nconfig file. Pass the token with --token or pipe it on stdin:\n\n  echo $MY_TOKEN | cf auth login\n  cf auth login --token <token> --profile work",
		RunE: func(cmd *cobra.Command, args []string) error {
			token := g.Token
			if token == "" {
				st, _ := os.Stdin.Stat()
				if st != nil && st.Mode()&os.ModeCharDevice == 0 {
					data, err := io.ReadAll(cmd.InOrStdin())
					if err != nil {
						return err
					}
					token = strings.TrimSpace(string(data))
				}
			}
			if token == "" {
				return errors.New("no token provided: pass --token or pipe it on stdin")
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
			f.Profiles[name] = p
			if setDefault || f.DefaultProfile == "" {
				f.DefaultProfile = name
			}
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved token to profile %q in %s\n", name, config.Path())
			return nil
		},
	}
	cmd.Flags().StringVar(&accountID, "set-account-id", "", "also store a default account ID in the profile")
	cmd.Flags().StringVar(&zoneID, "set-zone-id", "", "also store a default zone ID in the profile")
	cmd.Flags().BoolVar(&setDefault, "default", false, "make this profile the default")
	return cmd
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
