# Review: product/workers-platform — porcelain: cf workers (platform sub-shard)

- **Implementer:** codex gpt-5.6-terra high (wave 3)
- **Coordinator approval:** CO.3f1fb (gpt-5.6-sol) at 316f2e9, after a
  round-1 boundary correction
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 09963ac, 2026-08-11

## What was checked

- **Sub-shard boundary (the new mechanic, first use):** workers.go delta
  is exactly one AddCommand line; root.go and sibling files untouched.
  The rework round was itself a boundary correction — the discipline is
  being enforced.
- **Spec conformance:** `/workers/scripts/{name}/schedules` (cron get/set),
  `/workers/domains` (+ delete by id), `/workers/scripts/{name}/deployments`,
  `/workers/observability/usage` all match the pinned spec.
- **Gate:** full make check green at 316f2e9; build/fmt/tests re-verified
  on main post-merge.

## Notes

Commands surface as `cf workers cron|domain|deployment|usage ...` under the
shared kernel-owned group. Sibling sub-shards (scripts, dispatch) land
independently.
