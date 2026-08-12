package cli

// Devices group scaffold (kernel-owned). Wave-4 sub-shards each add one
// constructor line immediately after AddCommand( — the parent diff must be
// exactly +1/-0; the maintainer resolves cross-branch collisions at merge.

import "github.com/spf13/cobra"

func newDevicesCmd(g *globalOpts) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "devices",
		Short: "Manage Zero Trust devices, WARP profiles, and posture",
	}
	cmd.AddCommand(
	// sub-shard constructors register here, one line each:
	// newDevicesFleetCmd(g),
	// newDevicesPostureCmd(g),
	)
	return cmd
}
