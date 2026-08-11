# Review: product/tunnel — porcelain: tunnel

- **Implementer:** claude opus (wave 2)
- **Coordinator approval:** CO.9dc4 (gpt-5.6-sol) at 427b361, after 2
  rework rounds (invented secret max removed, route comment/identifier
  bounds, deprecated CONNS column dropped, validation ordered before
  network lookup with zero-request regression tests)
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as a7d163b, 2026-08-11. Completes
  wave 2.

## What was checked

- **Spec conformance:** `cfd_tunnel` CRUD, `/token`, `/configurations`
  (GET/PUT) and `teamnet/routes` list/add/remove all match the pinned
  spec; the taxonomy mapping folds cfd_tunnel -> tunnel and
  teamnet -> tunnel-routes as designed.
- **Review quality note:** the coordinator caught an *invented* API bound
  (a 64-byte secret maximum not in Cloudflare's docs) — exactly the
  hallucination class the layered review exists to stop.
- **Gate:** full make check green at 427b361; build/fmt/tests re-verified
  on main post-merge.
- **Scope:** tunnel.go + tunnel_test.go + root.go wiring only.

## Residual (documented) behaviors

- Name resolution performs a read request even under --dry-run for
  name inputs (matches dns exemplar).
- Token prints as a JSON string; help documents `-q -r`-style extraction.
