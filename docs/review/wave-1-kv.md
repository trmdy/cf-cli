# Wave 1 review: KV

- Branch: `product/kv`
- Reviewed commit: `a9bc3a890d328dd26b0fe8e453591dcc8c0a18ec`
- Verdict: changes requested (round 1)

## Scope and boundary

The committed branch diff is limited to `internal/cli/kv.go`, `internal/cli/kv_test.go`, and one root wiring line. The merge from main was explicitly authorized for the maintainer-provided raw API primitive.

## Round 1 findings

1. `kv key list --limit` accepts every positive integer, but Cloudflare's documented range is 10 through 1000. Values such as 1 and 1001 are currently sent even though the API rejects them. Validate both bounds and add boundary tests.
2. `kv key get --query ...` currently enables JSON rendering without an explicit `--output json`. This conflicts with the maintainer's raw-value contract: get must emit raw bytes unless JSON output is explicitly selected. Reject `--query` unless `--output json` was explicitly provided, retain exact raw output otherwise, and cover both the rejection and the explicit JSON/query path.

The raw `DoRaw` path, `text/plain` single-key write, bulk metadata guidance, endpoint methods/paths, destructive confirmations, namespace resolution, and branch boundary otherwise look sound.
