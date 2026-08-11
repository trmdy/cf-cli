# Review: PR #1 — porcelain: cache

- **Implementer:** grok bee GR.0f91 (wave 1)
- **Coordinator approval:** CO.8a97 (gpt-5.6-sol)
- **Final review:** maintainer (Claude Fable)
- **Verdict:** merged (squash), 2026-08-11

## What was checked

- **Spec conformance:** `POST /zones/{zone_id}/purge_cache` body fields
  (`purge_everything`, `files`, `tags`, `hosts`, `prefixes`) verified
  against the pinned OpenAPI schema (`cache-purge_*` component schemas).
  `PATCH/GET /zones/{zone_id}/cache/tiered_cache_smart_topology_enable`
  verified present with those methods.
- **Gate:** vet, style lint, tests, build all green on the branch.
- **Scope:** touches only `internal/cli/cache.go`, `cache_test.go`, and the
  one-line root.go wiring. No kernel files.
- **Behavior:** mutually-exclusive purge-mode validation with actionable
  errors; `--force`/TTY confirm on purge (destructive); dry-run works;
  results funnel through `renderResult` so `--query`/`--output` apply.
- **Tests:** unit tests for body builders + end-to-end command tests
  through the real cobra tree against `httptest` via `--base-url`
  (`runCacheCLI` helper). Good coverage of validation branches.

## Notes for the consistency pass (non-blocking)

- `cf cache smart-tiered set --value on` — a positional `on|off` arg would
  read better; revisit when more toggle-style commands exist and pick one
  convention.
- The `runCacheCLI` end-to-end helper is a pattern other products will
  duplicate; hoist a shared `runCLIForTest` helper once 2-3 more products
  land.
- `--everything` purge could warn louder (it is the "drop the whole
  cache" button); confirm prompt covers interactive use.

## Precedent set

The end-to-end test pattern (drive the real command tree with `--base-url`
pointing at httptest) is better than what the DNS exemplar had. Future
shards should copy *this* pattern; noted for the exemplar update.
