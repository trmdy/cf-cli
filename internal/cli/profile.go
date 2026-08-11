package cli

// Profile management: list/show/use/set/unset/delete/rename over the
// config file. Values print with tokens masked in every output format.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/config"
	"github.com/trmdy/cf-cli/internal/output"
)

// profileKeys maps the keys accepted by `cf profile set/unset` onto profile
// fields. Underscore spellings are normalized to these.
var profileKeys = []string{"api-token", "account-id", "zone-id"}

func newProfileCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage configuration profiles",
		Long: `Manage named credential profiles in ` + config.Path() + `.

Profiles hold an API token and default account/zone. Select one per
invocation with --profile or $CF_PROFILE, or persistently with
` + "`cf profile use`" + `.`,
	}
	cmd.AddCommand(
		newProfileListCmd(g),
		newProfileShowCmd(g),
		newProfileUseCmd(g),
		newProfileSetCmd(g),
		newProfileUnsetCmd(g),
		newProfileDeleteCmd(g),
		newProfileRenameCmd(g),
	)
	return cmd
}

type profileView struct {
	Name      string `json:"name"`
	Default   bool   `json:"default"`
	APIToken  string `json:"api_token,omitempty"` // always masked
	AccountID string `json:"account_id,omitempty"`
	ZoneID    string `json:"zone_id,omitempty"`
}

func defaultProfileName(f config.File) string {
	if f.DefaultProfile != "" {
		return f.DefaultProfile
	}
	return "default"
}

func newProfileListCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			names := make([]string, 0, len(f.Profiles))
			for n := range f.Profiles {
				names = append(names, n)
			}
			sort.Strings(names)
			def := defaultProfileName(f)
			views := make([]profileView, 0, len(names))
			for _, n := range names {
				p := f.Profiles[n]
				views = append(views, profileView{
					Name: n, Default: n == def, APIToken: maskToken(p.APIToken),
					AccountID: p.AccountID, ZoneID: p.ZoneID,
				})
			}
			format := g.format(output.Table)
			if g.Query != "" || format != output.Table {
				return g.renderValue(cmd, views, output.JSON)
			}
			rows := make([][]string, 0, len(views))
			for _, v := range views {
				d := ""
				if v.Default {
					d = "*"
				}
				rows = append(rows, []string{v.Name, d, v.APIToken, v.AccountID, v.ZoneID})
			}
			return output.RenderTable(cmd.OutOrStdout(), []string{"NAME", "DEFAULT", "TOKEN", "ACCOUNT_ID", "ZONE_ID"}, rows)
		},
	}
}

func newProfileShowCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "show [name]",
		Short: "Show one profile (token masked)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			name := defaultProfileName(f)
			if g.Profile != "" {
				name = g.Profile
			}
			if len(args) == 1 {
				name = args[0]
			}
			p, ok := f.Profiles[name]
			if !ok {
				return fmt.Errorf("profile %q does not exist (see `cf profile list`)", name)
			}
			return g.renderValue(cmd, profileView{
				Name: name, Default: name == defaultProfileName(f),
				APIToken: maskToken(p.APIToken), AccountID: p.AccountID, ZoneID: p.ZoneID,
			}, output.JSON)
		},
	}
}

func newProfileUseCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Make a profile the default",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := f.Profiles[args[0]]; !ok {
				return fmt.Errorf("profile %q does not exist (see `cf profile list`)", args[0])
			}
			f.DefaultProfile = args[0]
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Default profile is now %q\n", args[0])
			return nil
		},
	}
}

func normalizeProfileKey(k string) (string, error) {
	k = strings.ReplaceAll(strings.ToLower(k), "_", "-")
	for _, valid := range profileKeys {
		if k == valid {
			return k, nil
		}
	}
	return "", fmt.Errorf("unknown key %q: expected one of %s", k, strings.Join(profileKeys, ", "))
}

func applyProfileKey(p *config.Profile, key, value string) {
	switch key {
	case "api-token":
		p.APIToken = value
	case "account-id":
		p.AccountID = value
	case "zone-id":
		p.ZoneID = value
	}
}

func newProfileSetCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <name> <key> <value>",
		Short: "Set a profile value (creates the profile if missing)",
		Long: `Set a value on a profile. Keys: api-token, account-id, zone-id.

Examples:

  cf profile set work account-id 023e105f4ecef8ad9ca31a8372d0c353
  cf profile set work zone-id 0123456789abcdef0123456789abcdef
  cf profile set personal api-token $MY_TOKEN`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := normalizeProfileKey(args[1])
			if err != nil {
				return err
			}
			if args[2] == "" {
				return errors.New("empty value: use `cf profile unset` to clear a key")
			}
			f, err := config.Load()
			if err != nil {
				return err
			}
			p := f.Profiles[args[0]]
			applyProfileKey(&p, key, args[2])
			f.Profiles[args[0]] = p
			if f.DefaultProfile == "" {
				f.DefaultProfile = args[0]
			}
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Set %s on profile %q\n", key, args[0])
			return nil
		},
	}
	return cmd
}

func newProfileUnsetCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "unset <name> <key>",
		Short: "Clear a profile value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, err := normalizeProfileKey(args[1])
			if err != nil {
				return err
			}
			f, err := config.Load()
			if err != nil {
				return err
			}
			p, ok := f.Profiles[args[0]]
			if !ok {
				return fmt.Errorf("profile %q does not exist", args[0])
			}
			applyProfileKey(&p, key, "")
			f.Profiles[args[0]] = p
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unset %s on profile %q\n", key, args[0])
			return nil
		},
	}
}

func newProfileDeleteCmd(g *globalOpts) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			if _, ok := f.Profiles[args[0]]; !ok {
				return fmt.Errorf("profile %q does not exist", args[0])
			}
			if !force && !confirm(fmt.Sprintf("Delete profile %q and its stored token?", args[0])) {
				return errors.New("aborted (pass --force to skip confirmation)")
			}
			delete(f.Profiles, args[0])
			if f.DefaultProfile == args[0] {
				f.DefaultProfile = ""
			}
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deleted profile %q\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	return cmd
}

func newProfileRenameCmd(g *globalOpts) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a profile",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := config.Load()
			if err != nil {
				return err
			}
			p, ok := f.Profiles[args[0]]
			if !ok {
				return fmt.Errorf("profile %q does not exist", args[0])
			}
			if _, exists := f.Profiles[args[1]]; exists {
				return fmt.Errorf("profile %q already exists", args[1])
			}
			f.Profiles[args[1]] = p
			delete(f.Profiles, args[0])
			if f.DefaultProfile == args[0] {
				f.DefaultProfile = args[1]
			}
			if err := config.Save(f); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Renamed profile %q to %q\n", args[0], args[1])
			return nil
		},
	}
}
