# Review: product/turnstile — porcelain: turnstile

- **Implementer:** claude opus (wave 2)
- **Coordinator approval:** CO.9dc4 (gpt-5.6-sol) at d810930, after 1
  rework round (filter validation before network, region immutability,
  name/domain bounds)
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as f1c9c96, 2026-08-11

## What was checked

- **Spec conformance:** `/accounts/{account_id}/challenges/widgets` CRUD +
  `/rotate_secret` (the taxonomy mapping renames `challenges` -> turnstile)
  match the pinned spec.
- **PUT-merge semantics:** the API has no PATCH on widgets; update GETs
  the current widget and merges changed flags into the full required PUT
  body, keeping create-only region out — correct handling of an awkward
  API shape, with merged-body rejection tests.
- **Gate:** full make check green at d810930; build/fmt/tests re-verified
  on main post-merge.
- **Scope:** turnstile.go + turnstile_test.go + root.go wiring only.
