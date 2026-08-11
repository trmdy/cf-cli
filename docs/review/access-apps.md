# Review: product/access-apps — porcelain: cf access (apps sub-shard)

- **Implementer:** claude opus (wave 3)
- **Coordinator approval:** CO.3f1fb (gpt-5.6-sol) at b8b9b3c, after 1
  rework round
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 23e21c1, 2026-08-11

## What was checked

- **Sub-shard boundary:** access.go delta exactly one AddCommand line;
  root.go and siblings untouched.
- **Spec conformance:** `/access/apps` CRUD (account scope, zone variant)
  and `/access/apps/{app_id}/policies` CRUD match the pinned spec.
- **Validation ordering:** invalid scope combinations and account-only
  filters rejected before any client construction or zone resolution —
  zero-network error paths.
- **Update semantics:** PUT read-merge replacement per the established
  convention.
- **Gate:** full make check green at b8b9b3c; build/fmt/tests re-verified
  on main post-merge.

## Not in scope (available via cf api access)

App CA endpoints, reusable (account-level) policies, short-lived cert
settings.
