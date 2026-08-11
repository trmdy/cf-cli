# Wave 1 cache review — PR #1

- Product: `cache`
- Branch: `product/cache`
- Implementer: `GR.0f91` (`cf-wave1-cache`, Grok)
- Coordinator: `CO.8a97` (`cf-wave1-coordinator`)
- Current verdict: approved and handed to maintainer

## Evidence checked

- Diff boundary: only `internal/cli/cache.go`, `internal/cli/cache_test.go`, and the one-line `internal/cli/root.go` wiring change.
- Endpoint contract: local generated registry plus the official Cloudflare purge and Smart Tiered Cache API references.
- Request behavior: independent dry-run smoke checks for purge and Smart Tiered Cache get/set.
- Quality gates: independent `env -u GOROOT -u GOBIN make check` passed; GitHub CI `test` check passed.
- UX/help and test coverage reviewed against `docs/STYLE.md` and `internal/cli/dns.go`.

## Findings

1. `cache purge`, `cache smart-tiered get`, and `cache smart-tiered set` do not declare an argument validator. Cobra therefore accepts and silently ignores unexpected positional arguments, as demonstrated by `cf cache purge unexpected --zone <id> --everything --dry-run` succeeding. Add `Args: cobra.NoArgs` to all three leaf commands and cover rejection in tests.
2. `internal/cli/cache_test.go` declares the generic package-level identifiers `testZoneID` and `assertJSONEqual`. Wave 1 deliberately merges independently authored tests into the same `cli` package, so these names are avoidable integration-collision hazards. Prefix them for this shard, for example `cacheTestZoneID` and `assertCacheJSONEqual`.

## Communication log

- 2026-08-11T08:09:01Z — Coordinator requested rework round 1 via Hive buz and PR comment for the two findings above.
- 2026-08-11T08:10:52Z — Implementer delivered commit `dafbf805367b2f59101d9862121a082d4d1fdffa`; both findings are resolved. Independent `make check` and stray-argument smoke test passed. Updated GitHub CI is pending.
- 2026-08-11T08:12:15Z — Updated CI passed. Uncached `go test -count=1 ./internal/cli` passed. Coordinator posted the approval review, sealed the verdict, and handed PR #1 to the maintainer; coordinator did not merge.
