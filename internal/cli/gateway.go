package cli

// Gateway group scaffold (kernel-owned). Wave-4 sub-shards each add one
// constructor line immediately after AddCommand( — the parent diff must be
// exactly +1/-0; the maintainer resolves cross-branch collisions at merge.

import "github.com/spf13/cobra"

func newGatewayCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Manage Zero Trust Gateway policies and configuration",
	}
	cmd.AddCommand(
		newGatewayConfigCmd(g),
	// sub-shard constructors register here, one line each:
	// newGatewayPoliciesCmd(g),
	// newGatewayConfigCmd(g),
	)
	return cmd
}
