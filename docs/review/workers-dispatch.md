# Review: product/workers-dispatch — porcelain: cf workers dispatch

- **Implementer:** codex gpt-5.6-terra high (wave 3)
- **Coordinator approval:** CO.3f1fb (gpt-5.6-sol) at df3176b, after 1
  rework round + registration addendum
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as fac68f8, 2026-08-11

## What was checked

- **Sub-shard boundary:** workers.go delta exactly one AddCommand line;
  root.go and siblings untouched. Both workers sub-shards now compose
  (`cf workers platform|dispatch ...`) — group-file conflict resolved
  mechanically at merge.
- **Spec conformance:** `/workers/dispatch/namespaces` CRUD and
  namespaced `/scripts` list/get/delete/upload match the pinned spec;
  `bindings_inherit=strict` per spec.
- **Upload wire format (first DoStream consumer):** deterministic
  multipart with metadata + ES-module parts, module MIME
  `application/javascript+module`; dry-run emits the full multipart body
  for inspection, live path streams via Client.DoStream.
- **Gate:** full make check green at df3176b; build/fmt/tests re-verified
  on main post-merge.

## Not in scope (available via cf api workers-for-platforms)

Asset upload sessions, per-script bindings/secrets/settings/tags within
namespaces.
