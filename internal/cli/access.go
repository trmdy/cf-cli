package cli

// Access group scaffold (kernel-owned). Wave-3 sub-shards each add one
// constructor line to the AddCommand list below — the same one-line-conflict
// convention as root.go; the maintainer resolves collisions at merge.

import "github.com/spf13/cobra"

func newAccessCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Manage Cloudflare Access (Zero Trust)",
	}
	cmd.AddCommand(
		newAccessAppsCmd(g),
	// sub-shard constructors register here, one line each:
	// newAccessAppsCmd(g),
	// newAccessIdentityCmd(g),
	)
	return cmd
}
