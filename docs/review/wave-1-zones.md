# Wave 1 review: zones

- Branch: `product/zones`
- Commit: `84e9e7fc5ed6ba282422ee1030d482ed46e297da`
- Verdict: approved
- Rework rounds: 0

## Scope and boundary

The branch adds only `internal/cli/zones.go`, `internal/cli/zones_test.go`, and the single `newZonesCmd(g)` root wiring line. It does not modify kernel, build, tooling, or CI files.

## Correctness review

- Zone list/get/create/delete use the documented Cloudflare methods and paths.
- List filters, zone create types, zone status values, pause/resume PATCH payloads, and the three supported setting IDs/values were checked against the current Cloudflare API reference.
- Zone names resolve through the shared resolver; IDs are path escaped.
- Destructive delete requires confirmation or `--force`, while dry-run remains non-interactive.
- Multi-setting reads and writes retain stable ordering, aggregate JSON for output/query handling, and identify the failing setting on API errors.
- All leaf commands validate positional arguments and expose concrete examples.

## Verification

- `git diff --check main...product/zones`: passed.
- `env -u GOROOT -u GOBIN make check`: passed.
- `env -u GOROOT -u GOBIN go test -count=1 ./internal/cli`: passed.
- Tests execute through `NewRootCmd` and `--base-url` against `httptest`, covering request method/path/query/body, name resolution, validation, dry-run, output modes, confirmation behavior, help, and root wiring.

No blocking findings.
