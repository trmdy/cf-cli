# Wave 1 review: load balancers

- Product: `load-balancers`
- Branch: `product/load-balancers`
- Implementer: `CL.dadce` (`cf-wave1-load-balancers`, Claude Opus)
- Coordinator: `CO.8a97` (`cf-wave1-coordinator`)
- Current verdict: approved after round 1
- Approved commit: `329e4c807f48485b4cb7cad9dcb286d7a832fd79`
- Legacy PR #13 was closed unmerged after the operator switched the effort to local branches.

## Evidence checked

- Diff boundary: only `internal/cli/load_balancers.go`, `internal/cli/load_balancers_test.go`, and the one-line `internal/cli/root.go` wiring change.
- Endpoint contracts: local generated registry plus the current official Cloudflare load-balancer, pool, monitor, and pool-health API references.
- Coordinator independently verified `gofmt -l .` produced no output, the current-main `make check` gate passed, and `go test -count=1 ./internal/cli` passed.
- All sixteen leaf commands were reviewed for request construction, dry-run behavior, output paths, destructive confirmation, validation, and help text.
- The concurrent `root.go` difference is expected and is left for the maintainer's local squash landing.

## Round 1 findings

1. `pool health` models `pop_health` as a map keyed by PoP and its tests mock that invented shape. The current API returns one `pop_health` object containing `healthy` and `origins`, so a real response cannot populate the current table and falls back to raw JSON. Model and test the official response envelope, remove the per-PoP claims/columns from help and table output, and keep origin rows stable by sorting their map keys. Reference: https://developers.cloudflare.com/api/resources/load_balancers/subresources/pools/subresources/health/methods/get/
2. Monitor create sends HTTP-only flags (`--path`, `--expected-codes`, `--expected-body`, `--header`, `--follow-redirects`, `--allow-insecure`) for TCP/UDP-ICMP/ICMP/SMTP when explicitly passed, even though Cloudflare documents them as valid only for HTTP/HTTPS. Reject those combinations with an actionable error. TCP, UDP-ICMP, and SMTP create also require an explicit valid port; enforce that and test the protocol matrix. For update, enforce the same incompatibility when a non-HTTP `--type` is explicitly supplied; when type is omitted, the server remains authoritative because the current type is unknown.
3. `--check-region` accepts arbitrary strings and forwards lowercase values. Validate and normalize the documented choices `WNAM`, `ENAM`, `WEU`, `EEU`, `NSAM`, `SSAM`, `OC`, `ME`, `NAF`, `SAF`, `SAS`, `SEAS`, `NEAS`, and `ALL_REGIONS` for pool create/update, with focused accepted/rejected tests.
4. `--session-affinity header` is offered but this porcelain has no way to send the required `session_affinity_attributes.headers`, so it constructs an incomplete request. Either add the required header-attribute workflow and tests or remove/reject `header` until that workflow exists. `none`, `cookie`, and `ip_cookie` remain usable.
5. Exact-argument validation permits blank resource IDs/names, including destructive calls that become collection paths with a trailing slash. Reject blank IDs for load-balancer get/update/delete, pool get/update/delete/health, and monitor get/update/delete. Reject blank load-balancer create/update names and fallback-pool values. Validate pool create/update names against the documented alphanumeric/hyphen/underscore contract. Add focused tests, especially for each destructive family.
6. Structured origin parsing accepts arbitrary float precision even though origin weight has a documented `multipleOf 0.01` constraint. Reject values that are not hundredths and cover accepted boundaries plus a value such as `0.555`.

## Communication log

- 2026-08-11T08:44:00Z — Coordinator requested rework round 1 with the six consolidated API/validation findings above. Local branch only; no push, PR, merge, or sibling rebase. Final verification must use `gofmt -l .` and the current-main Makefile gate.
- 2026-08-11T08:58:08Z — Approved commit `329e4c807f48485b4cb7cad9dcb286d7a832fd79`. The official single-object `pop_health` envelope, stable origin rows, stdout-only health table, protocol-specific monitor validation, documented check regions, per-verb session-affinity guidance, blank-ID/name guards, pool-name validation, and hundredth-step origin weights are implemented and tested. The worktree is clean, `git diff --check` passes, the file boundary is intact, and all current-main gates pass.
