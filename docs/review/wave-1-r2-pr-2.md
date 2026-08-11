# Wave 1 review: R2

- Product: `r2`
- Branch: `product/r2`
- Implementer: `CO.16442` (`cf-wave1-r2`, Codex Terra high)
- Coordinator: `CO.8a97` (`cf-wave1-coordinator`)
- Current verdict: changes requested, round 2 (final implementer round)
- Legacy PR #2 was closed unmerged when the operator switched the effort to local branches.

## Evidence checked

- Diff boundary: only `internal/cli/r2.go`, `internal/cli/r2_test.go`, and the one-line `internal/cli/root.go` wiring change.
- Endpoint contract: local generated registry and current official Cloudflare R2 bucket API references.
- Independent `env -u GOROOT -u GOBIN make check` and uncached `go test -count=1 ./internal/cli` passed.
- UX, dry-run behavior, destructive confirmation, help, and test shape reviewed against `docs/STYLE.md` and the merged cache pattern.
- The concurrent `root.go` difference is expected and is left for the maintainer's local squash landing.

## Findings

1. Cloudflare List Buckets returns an object under `result`, shaped as `{"buckets":[...]}`, with cursor pagination in `result_info`; it does not return a bare array. The implementation and test fixture assume a bare array and call `DoAutoPaginate`, which returns the non-array first page unchanged. Consequently the default table path fails to decode and falls back to JSON, and later cursor pages are never fetched. Decode the real wrapper, implement transparent cursor pagination for its nested bucket list, and test at least two pages using the official response shape. Reference: https://developers.cloudflare.com/api/resources/r2/subresources/buckets/methods/list/
2. `info` and destructive `delete` accept an empty bucket-name argument. `delete "" --force` can construct the collection path with a trailing slash. Apply a shared non-empty bucket-name validator to create/info/delete and cover the destructive case.
3. The source and PR rationale say `internal/api` only supports JSON, but main now includes `Request.ContentType` and `DoRaw`. Object commands remain optional and can still be omitted because the current raw primitive buffers bodies/responses and caps reads at 100 MiB, which is not clean R2 object streaming. Update the comment and PR explanation so the landed code is not immediately false about main.

## Communication log

- 2026-08-11T08:23:10Z — Coordinator requested rework round 1 via Hive buz and PR review comment for the three findings above.
- 2026-08-11T08:34:20Z — Round 1 fixed the nested `result.buckets` shape, empty-name handling, and stale object-transfer rationale, but put the cursor in `result.cursor`. The current Cloudflare response places it in `result_info.cursor`, so real pagination still stops after page one. Final round requested: read `env.ResultInfo.Cursor` and test the official two-page envelope shape. Also validate the documented create location choices (`apac`, `eeur`, `enam`, `weur`, `wnam`, `oc`) and bucket-name length (3–64) with focused tests.
