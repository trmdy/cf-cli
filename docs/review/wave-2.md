# Wave 2 porcelain review ledger

Coordinator: `cf-wave2-coordinator` (`CO.9dc4`). This local ledger records
review findings and PR communication for the ten Wave 2 shards.

## Stream — PR #3

Initial verdict: changes required, rework round 1.

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
