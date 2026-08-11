# Review: product/zones — porcelain: zones

- **Implementer:** claude opus (wave 1)
- **Coordinator approval:** CO.8a97 (gpt-5.6-sol) at 84e9e7f
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as f4c25c2, 2026-08-11 (local branch
  flow, no PR)

## What was checked

- **Spec conformance:** `GET /zones` name-filter operators (`contains:` is
  real spec syntax), `POST /zones` body (`name` + `account` required,
  `type` optional), `GET|PATCH /zones/{zone_id}/settings/{setting_id}`
  with `{"value": ...}` bodies — all verified against the pinned spec.
- **Gate:** vet, style lint, all tests, build green at the approved commit
  (detached-worktree run), and again post-conflict-resolution on main.
- **Scope:** zones.go + zones_test.go + one root.go wiring line only. The
  expected root.go AddCommand conflict with cache resolved at merge.
- **Design notes (good):** `zoneSettingDefs` table is a single source of
  truth for setting flags/validation — a reusable pattern for other
  settings-style porcelain; multi-request commands render `--dry-run` as a
  JSON array of dumps; per-setting API errors are wrapped with the setting
  name.

## Notes for the consistency pass (non-blocking)

- `zones settings set --ssl full` (flag-per-setting) vs cache's
  `smart-tiered set --value on` — two different toggle conventions now
  exist; converge on one (the zones table-driven flags read better).
- `zones pause`/`resume` confirm nothing despite being high-impact; fine
  since reversible, but revisit whether pause deserves a prompt.
