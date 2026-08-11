package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/trmdy/cf-cli/internal/api"
	"github.com/trmdy/cf-cli/internal/config"
)

// resolveZoneInteractive is the standard zone resolution for zone-scoped
// porcelain: explicit --zone (name or ID) > configured zone (flag/env/
// profile) > an inline picker when running on a terminal. A picked zone can
// be saved back to the active profile so the question is asked once.
func resolveZoneInteractive(cmd *cobra.Command, g *globalOpts, client *api.Client, cfg config.Resolved, flagZone string) (string, error) {
	if flagZone != "" || cfg.ZoneID != "" {
		return resolveZoneID(cmd.Context(), client, cfg.ZoneID, flagZone)
	}
	if !stdinIsTTY() || g.DryRun {
		return "", errors.New("no zone specified: pass --zone <name|id>, set CLOUDFLARE_ZONE_ID, run `cf profile set " + cfg.Profile + " zone-id <id>`, or run interactively to pick one")
	}
	pr := newPrompter()
	id, label, err := pickZone(cmd, client, pr, cfg.AccountID)
	if err != nil {
		return "", fmt.Errorf("no zone specified and none could be listed: %w", err)
	}
	if id == "" {
		return "", errors.New("no zone selected: pass --zone <name|id> or set a default with `cf profile set`")
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "Using zone %s\n", label)
	if yes, _ := pr.confirmYN(fmt.Sprintf("Save as default zone for profile %q?", cfg.Profile)); yes {
		if err := saveProfileZone(cfg.Profile, id); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: could not save default zone: %v\n", err)
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "Saved. Change it later with `cf profile set %s zone-id <id>`.\n", cfg.Profile)
		}
	}
	return id, nil
}

func saveProfileZone(profile, zoneID string) error {
	f, err := config.Load()
	if err != nil {
		return err
	}
	p := f.Profiles[profile]
	p.ZoneID = zoneID
	f.Profiles[profile] = p
	if f.DefaultProfile == "" {
		f.DefaultProfile = profile
	}
	return config.Save(f)
}
