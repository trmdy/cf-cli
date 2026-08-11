# Wave 1 review: KV

- Branch: `product/kv`
- Approved commit: `9bf1ea7576ccaa494e7c285647c12df78084ecd3`
- Verdict: approved after round 1

## Scope and boundary

The committed branch diff is limited to `internal/cli/kv.go`, `internal/cli/kv_test.go`, and one root wiring line. The merge from main was explicitly authorized for the maintainer-provided raw API primitive.

## Round 1 findings

1. `kv key list --limit` accepts every positive integer, but Cloudflare's documented range is 10 through 1000. Values such as 1 and 1001 are currently sent even though the API rejects them. Validate both bounds and add boundary tests.
2. `kv key get --query ...` currently enables JSON rendering without an explicit `--output json`. This conflicts with the maintainer's raw-value contract: get must emit raw bytes unless JSON output is explicitly selected. Reject `--query` unless `--output json` was explicitly provided, retain exact raw output otherwise, and cover both the rejection and the explicit JSON/query path.

The raw `DoRaw` path, `text/plain` single-key write, bulk metadata guidance, endpoint methods/paths, destructive confirmations, namespace resolution, and branch boundary otherwise look sound.

## Round 1 resolution and verification

- `--limit` now enforces 10–1000 and tests both rejected and accepted boundaries.
- Raw get now rejects `--query` unless `--output json` is explicit; both the rejection and explicit JSON/query path are tested.
- `git diff --check main...product/kv`: passed.
- `env -u GOROOT -u GOBIN make check`: passed.
- `env -u GOROOT -u GOBIN go test -count=1 ./internal/cli`: passed.

No remaining blocking findings.
