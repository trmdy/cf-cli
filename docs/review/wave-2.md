# Wave 2 porcelain review ledger

Coordinator: `cf-wave2-coordinator` (`CO.9dc4`). This local ledger records
review findings and maintainer communication for the ten Wave 2 shards. The
authoritative handoff is a local product branch; no GitHub PR is used.

## Stream — `product/stream`

Verdict: approved at `2eef19b`, after rework round 1.

- Blocking: `requireAccountID` in `stream.go` collides with the same
  package-level symbol in Images PR #7, preventing the two PRs from compiling
  together. Rename it with a product prefix.
- Blocking: `--exp` validates only positivity, despite the command help and
  Cloudflare API limiting expiration to the next 24 hours. Validate future and
  upper-bound behavior with deterministic clock tests.
- Blocking: direct upload accepts `--max-duration-seconds` values greater than
  the API maximum of 36,000 seconds. Add the bound and tests while preserving
  the documented `-1` unknown-duration behavior.
- Design review: `DoAutoPaginate` cannot follow Stream's top-level
  `range`/`total` response fields, so the current list path silently stops at
  the API's 1,000-video request cap. Implement a safe Stream continuation path
  or make the limitation/range controls explicit and report why kernel-free
  transparent pagination is not safe.

Communication: findings sent to `GR.e41a` by buz subject
`review.rework.round1`; direct notification also delivered. Acceptance requires
an updated branch, green `env -u GOROOT -u GOBIN make check`, and a new seal/buz
payload with `rework_round: 1`.

Re-review result:

- `requireStreamAccountID` is product-scoped and no longer collides with Images.
- Signed-token expiration is validated against an injected clock: strictly in
  the future and no more than 24 hours away, with boundary coverage.
- Direct-upload maximum duration accepts `-1` or 1 through 36,000 seconds.
- Video listing is an explicit single request capped at 1,000 results, with
  validated `--after`/`--before` range controls and clear help text for manual
  windowing; it no longer implies automatic pagination the API cannot support.
- `git diff main...product/stream` changes only `internal/cli/root.go`,
  `internal/cli/stream.go`, and `internal/cli/stream_test.go`; no prohibited
  kernel paths are changed.
- Coordinator gate: `env -u GOROOT -u GOBIN make check` green at `2eef19b`.

Remaining documented tradeoff: libraries over 1,000 videos require manual time
windowing because the Stream endpoint is single-page and exposes no standard
`result_info` cursor for the shared paginator.

## Queues — `product/queues`

Initial verdict: changes required, rework round 1.

- Blocking: queue-specific endpoints were documented as accepting a queue name
  or ID, but passed the raw argument into `{queue_id}`. Add a product-scoped
  name-or-ID resolver and use the resolved ID for queue CRUD, consumers, and
  messages. Cover direct ID, name hit, name miss, and representative nested
  operations. Document that resolving a name performs a read during dry-run.
- Blocking: `message send` decoded only JSON objects even though the API accepts
  any valid JSON message body. Decode to `any` so arrays, scalars, and `null`
  remain valid while malformed JSON is rejected.

Communication: findings sent to `CO.6517` by buz subject
`review.rework.round1`; direct notification also delivered. Acceptance requires
the local-only rework branch, a green full gate, and a new seal/buz payload with
`rework_round: 1`.
