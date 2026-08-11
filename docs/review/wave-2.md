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

Maintainer handoff: approval buz `019feff4-4063-769b-b057-bd43639d6a48`;
squash-merged as `7e1855c`. The clean Stream worktree/local branch was removed
and sealed implementer `GR.e41a` retired after the merge notification.

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

Re-review verdict: approved at `e4ac222`.

- A product-scoped resolver bypasses lookup for 32-hex IDs and auto-paginates
  the account queue list for names; queue get/update/delete, consumer add/remove,
  and message send/pull/ack all use the resolved ID.
- Name hit/miss, direct ID, nested consumer/message requests, and dry-run lookup
  behavior are covered by tests.
- JSON message bodies decode to `any`, preserving objects, arrays, strings,
  numbers, booleans, and `null`, while malformed JSON is rejected.
- `git diff main...product/queues` changes only `internal/cli/root.go`,
  `internal/cli/queues.go`, and `internal/cli/queues_test.go`; no prohibited
  kernel paths are changed.
- Coordinator gate: `env -u GOROOT -u GOBIN make check` green at `e4ac222`.

Remaining low-risk tradeoff: a queue name that itself looks exactly like a
32-character hexadecimal resource ID is treated as an ID and skips lookup.

Gate addendum: after main added `fmt-check`, the branch passed
`env -u GOROOT -u GOBIN make -f <current-main>/Makefile check`; `gofmt -l`
reported no Queue files. Supplemental approval buz:
`019feff8-f9e8-73fb-bf2b-2183a33002a2`.

Maintainer handoff: squash-merged as `78a67e4`. The clean Queues
worktree/local branch was removed and sealed implementer `CO.6517` retired.

## Hyperdrive — `product/hyperdrive`

Initial verdict: changes required, rework round 1.

- Blocking: `--sslmode` validation is case-insensitive and whitespace-tolerant,
  but the original value is serialized. Values such as `VERIFY-FULL` therefore
  pass locally and send an invalid API enum. Normalize the value to its canonical
  lowercase form for create and update, and test the serialized body.
- Blocking: `--connection-limit` enforced the documented minimum of 5 but not
  the API's absolute maximum of 100. Reject larger values for both create and
  update and add boundary coverage.

The initial branch scope was clean and the coordinator full gate was green at
`ca7fc5a`; the verdict remains changes required until the request-validation
rework is sealed. Findings were sent to `CO.d2ed` by buz subject
`review.rework.round1` and direct notification.

Re-review verdict: approved at `a6d5764`.

- SSL mode is trimmed, lowercased, validated, and the canonical enum is placed
  in both create and update bodies.
- Connection limits outside 5 through 100 are rejected for create and update;
  upper and lower errors plus the maximum boundary are covered.
- `git diff main...product/hyperdrive` remains limited to
  `internal/cli/root.go`, `internal/cli/hyperdrive.go`, and
  `internal/cli/hyperdrive_test.go`; no prohibited kernel paths.
- Coordinator gate: current-main `Makefile check`, including `fmt-check`, green
  at `a6d5764` with GOROOT/GOBIN unset.

## D1 — `product/d1`

Initial verdict: changes required, rework round 1.

- Blocking: `--jurisdiction` and `--primary-location` advertise closed enums in
  help but are serialized without validation or normalization. Validate the API
  values (`eu|fedramp|us` and `wnam|enam|weur|eeur|apac|oc`) consistently with
  the existing read-replication normalization and add body/boundary tests.
- Blocking: supplying jurisdiction and primary-location together is silently
  accepted even though the API ignores the location hint when jurisdiction is
  present. Reject this contradictory request locally.
- Blocking: whitespace-only inline `--command` SQL is sent, while equivalent
  whitespace-only `@file` input is correctly rejected. Make the two paths
  consistent and add coverage.

The initial diff changes only `internal/cli/d1.go`, `internal/cli/d1_test.go`,
and the expected root registration; no kernel paths. The old gate was green at
`027568a`. Findings and the repaired current-main formatting-gate requirement
were sent to `CO.0350` by buz subject `review.rework.round1` and direct message.

Re-review verdict: approved at `d334b36`.

- Jurisdiction and primary-location values are trimmed, lowercased, and checked
  against their pinned API enums; valid canonical body output and invalid values
  are covered.
- The mutually ineffective jurisdiction plus primary-location combination is
  rejected before client/network work.
- Inline whitespace-only SQL now follows the same empty-input rule as `@file`.
- Branch scope remains the two D1 CLI files plus root registration; no prohibited
  kernel paths.
- Coordinator gate: current-main `Makefile check`, including `fmt-check`, green
  at `d334b36` with GOROOT/GOBIN unset.

## Pages — `product/pages`

Initial verdict: changes required, rework round 1.

- Blocking: `production_branch` is required by project create, but an explicitly
  empty or whitespace `--production-branch` is trimmed and omitted through
  `omitempty`. Reject it locally and cover body/CLI validation.
- Blocking: create help says the command creates projects for Git builds, but
  the request has no repository `source` object and therefore creates a Direct
  Upload project. Correct the help without expanding scope into repository auth.
- Blocking: rollback help says the deployment becomes live for “its
  environment,” while the endpoint only rolls back to successful production
  deployments. Correct Long/help/confirmation text and assert it.

The initial diff changes only `internal/cli/pages.go`,
`internal/cli/pages_test.go`, and the root registration; no kernel paths. It
passed the current-main gate including `fmt-check` at `3b343dd`. Findings were
sent to `CL.91a` by buz subject `review.rework.round1` and direct message.

## Images — `product/images`

Initial verdict: changes required, rework round 1.

- Blocking: list help documents `--creator ""` as the filter for images with no
  creator, but request construction drops an empty value. Preserve whether the
  flag was explicitly changed so dry-run and every page send `creator=` only
  when requested; add request and pagination coverage.
- Blocking: upload metadata claims to require a JSON object, but JSON `null`
  unmarshals into a nil map without error and is accepted. Reject null alongside
  arrays, scalars, and malformed JSON.
- Integration: rename generic package helper `requireAccountID` to
  `requireImagesAccountID`; a sibling collision with the same name was already
  found during Stream review, and product-scoped symbols prevent recurrence.

The initial diff changes only `internal/cli/images.go`,
`internal/cli/images_test.go`, and the root registration; multipart upload uses
the shared `api.Request.ContentType` without a kernel edit. It passed the
current-main gate including `fmt-check` at `8f1dd15`. Findings were sent to
`GR.bb2d` by buz subject `review.rework.round1` and direct message.
