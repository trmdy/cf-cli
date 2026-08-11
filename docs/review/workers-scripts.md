# Review: product/workers-scripts — porcelain: cf workers script

- **Implementer:** claude opus (wave 3)
- **Coordinator approval:** CO.3f1fb (gpt-5.6-sol) at 3512e2e, after 1
  rework round
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as ea83566, 2026-08-11. Completes
  the workers group (script + platform + dispatch).

## What was checked

- **Sub-shard boundary:** workers.go delta exactly one AddCommand line.
- **Spec conformance:** `/workers/scripts` list, `/{script_name}` PUT/
  DELETE, `/content`, `/secrets` (+ per-secret delete), `/subdomain`,
  `/settings` all match the pinned spec.
- **Upload wire format (the hardest command in the project):** multipart
  metadata + module parts held to the dispatch precedent — deterministic
  dry-run body, DoStream live in both directions (upload and content
  download), module-name validation rejecting control chars and invalid
  UTF-8 while allowing nested module paths, Request.ContentType carrying
  the multipart boundary.
- **Gate:** full make check green at 3512e2e; build/fmt/tests re-verified
  on main post-merge. 1,455-line test file.

## Not in scope (available via cf api workers)

content/v2, secrets-bulk, versions/deployments beyond platform sub-shard,
tail sessions.
