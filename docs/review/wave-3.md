# Wave 3 porcelain review ledger

Coordinator: `cf-wave3-coordinator` (`CO.3f1fb`). This local ledger records
adversarial review findings and maintainer communication for the ten Wave 3
shards. The authoritative handoff is a local product branch; no GitHub PR or
branch push is used.

## Spectrum — `product/spectrum`

Initial verdict: changes required at `781d80f`, rework round 1.

- Blocking: list sends `per_page=100` without `page`, although the Spectrum
  endpoint documents `page` as required to use pagination parameters and to
  receive `result_info`. The shared paginator cannot simply be seeded with
  `page=1` because it treats an explicit page as caller-pinned. Implement and
  test a product-local transparent page loop, including exact two-page query
  propagation.
- Blocking: create and update do not enforce documented cross-field schema
  constraints: dynamic edge IPs require CNAME, static edge IPs require ADDRESS,
  Argo Smart Routing requires direct TCP/UDP, IP Firewall requires TCP, and
  `origin_port` requires a complete `origin_dns` configuration. Validate both
  fresh creates and post-merge update transitions before writes.
- Blocking: protocol and origin-port ranges do not enforce the API's equal-width
  rule, and `virtual_network_id` is accepted as any non-empty string despite its
  UUID schema. Add validation and invalid/boundary tests.
- Correctness claim: update says it preserves unknown API fields, but decoding
  into `spectrumApp` drops unknown keys before the body is rebuilt. Preserve the
  raw object safely or correct the claim and prove that every modeled mutable
  field survives replacement.

The initial branch is correctly limited to `internal/cli/root.go`,
`internal/cli/spectrum.go`, and `internal/cli/spectrum_test.go`. Both the
coordinator full gate and uncached Spectrum tests are green at `781d80f`, but
green tests do not cover the contract gaps above. Findings were sent by buz
`019ff108-f928-71c9-a1ef-57c44b35112c` and direct notification.

Main subsequently advanced to `21648e3`, making `resolveZoneInteractive`
mandatory for zone-scoped porcelain. Spectrum's round-one response at its
next commit must also rebase onto that main and adopt the helper; this remains
part of the same round.

Final verdict: approved at `13b28f5`. The rework implements and tests the
pagination loop, all requested post-merge constraints, UUID/range validation,
unknown-field preservation, and interactive zone resolution. Targeted
uncached tests and `make check` pass. Approval buz:
`019ff114-ae25-700d-9394-54f048189b34`.

## Workers Platform — `product/workers-platform`

Initial verdict: changes required at `146fe76`, rework round 1.

- Blocking boundary violation: `internal/cli/workers.go` deletes the platform
  scaffold placeholder as well as adding `newWorkersPlatformCmd(g)`. The brief
  requires the parent file to match `origin/main` byte-for-byte except for the
  single constructor addition. Restore every comment unchanged.
- The shard's account-scoped paths, request methods, cron/domain/deployment
  workflows, and `/workers/observability/usage` request match the pinned
  `4e2f140437b8` OpenAPI contract. The full gate was green before rework.

Final verdict: approved at `316f2e9`. The parent-file diff is now exactly one
added constructor line; targeted uncached tests and `make check` pass. Approval
buz: `019ff114-c2b6-7336-b34a-5860e8cbfe27`.

## Workers Dispatch — `product/workers-dispatch`

Initial verdict: changes required at `a10e9fc`, rework round 1.

- Blocking boundary violation: `internal/cli/workers.go` must contain exactly
  one added constructor line; the current diff also deletes/reindents scaffold
  comments.
- Blocking dry-run mismatch: upload declares a newly generated multipart
  boundary but dumps only canonical metadata JSON as its body. That output is
  not a valid representation of the request that would be sent. Build a
  deterministic multipart fixture for dry-run and parse/assert its metadata
  and module parts in tests.
- Blocking wire mismatch: a `main_module` denotes module syntax, but its part
  is sent as `application/javascript`. Use an allowed module MIME such as
  `application/javascript+module` and assert the exact part header.
- The namespace/script endpoints and `bindings_inherit=strict` query match the
  pinned API. The live upload correctly uses `Client.DoStream`.

Final verdict: approved at `df3176b`. The upload now uses one shared multipart
writer for deterministic dry-run output and streamed live requests, with the
module MIME fixed to `application/javascript+module`. The parent-file diff is
exactly one constructor line inside the existing `AddCommand` list. Targeted
uncached tests and `make check` pass. Approval buz:
`019ff129-6de7-766d-8d4c-b42f4c9ac7e1`.

Landed on main as `fac68f8`; its clean worktree, local product branch, and Hive
session were retired.

## Workers Scripts — `product/workers-scripts`

Initial verdict: changes required at `87eaf43`, rework round 1.

- Blocking boundary violation: `internal/cli/workers.go` currently replaces a
  scaffold placeholder. It must instead add one constructor line immediately
  after the existing `AddCommand(` while leaving every comment untouched.
- Escape the account path component; it is currently interpolated raw unlike
  every other account-scoped path in the product.
- Multipart module names are inserted into a hand-built Content-Disposition
  header after quote/backslash escaping only. Use a standards-aware media-type
  formatter or reject control characters so CR/LF cannot corrupt a MIME
  header; retain nested module names and test edge cases.
- The exact multipart dry-run fixture is strong: it parses the declared
  boundary and asserts metadata, multiple module bodies, names, and all module
  MIME types. Live upload uses `DoStream`, content download streams raw bytes,
  and the remaining endpoints match the pinned API.

Final verdict: approved at `3512e2e`. The parent-file diff is exactly one
in-list constructor addition, the account component is path-escaped, and
multipart module names reject header controls and invalid UTF-8 while retaining
nested names. `Client.DoStream` and `Request.ContentType` cover the large and
non-JSON transfers as required. Targeted uncached tests and `make check` pass.
Approval buz: `019ff129-6de9-762b-81cb-aab1a97b546a`.

## SSL Certificates — `product/ssl-certs`

Initial verdict: changes required at `b12cba9`, rework round 1.

- Rebase onto main `21648e3` and replace the custom `resolveZoneID` wrapper and
  pre-resolution configured-zone error with mandatory
  `resolveZoneInteractive` calls for pack list/order and Origin CA list.
- Certificate-pack order validates 1..50 hosts but omits the API requirement
  that the requested hostnames contain the resolved zone apex.
- Origin CA create accepts any non-empty hostname. Enforce the documented FQDN
  and wildcard shape locally, including rejection of double/interior
  wildcards and invalid single-label wildcard names.

Round 2 fixed the remaining dry-run inconsistency: pack order now reads the
resolved zone and enforces the apex requirement in both live and dry-run modes,
while dry-run still never sends the POST.

Final verdict: approved at `aa73351`. All zone-scoped paths use
`resolveZoneInteractive`, the apex and Origin CA hostname contracts are covered,
and the standalone diff is the two product files plus one root registration.
Targeted uncached tests and `make check` pass. Approval buz:
`019ff129-6ddd-7329-815e-117fdc9b4559`.

Landed on main as `78d8889`; its clean worktree, local product branch, and Hive
session were retired.

## Rulesets — `product/rulesets`

Initial verdict: changes required at `fb4b919`, rework round 1.

- Main `21648e3` makes `resolveZoneInteractive` mandatory for the zone half of
  this dual-scope shard. Rebase and adopt it without changing account scope.
- The ruleset version semantics are represented honestly: rule and phase
  entrypoint mutations are documented as creating new versions. The endpoint
  set and enum validation match the pinned API in the reviewed implementation.

Final verdict: approved at `25db6e7`. The branch is rebased on `21648e3`, uses
the interactive helper for zone scope, retains the correct version semantics,
and passes targeted uncached tests plus `make check`. Approval buz:
`019ff114-b7df-73f2-be7d-4f546efd8588`.

## Waiting Room — `product/waiting-room`

Initial verdict: changes required at `0c1cd5c`, rework round 1.

- Rebase onto main `21648e3` and use `resolveZoneInteractive` for all
  zone-scoped waiting-room commands.
- The first resolver rework passed, but room/event/rule PATCH bodies omit
  fields marked required by the pinned schemas. Preserve partial CLI updates
  through GET + raw merge (or an equally safe full replacement), validate the
  complete post-merge body, and test exact GET-then-write behavior plus unknown
  field preservation.
- Rule position `index` currently accepts fractional, zero, and negative JSON
  numbers; the API defines a one-based integer.
- Validate the documented no-wildcard/no-query route paths and apply the same
  no-scheme/no-wildcard host rule to additional routes.

Final verdict: approved at `4ca5554`. Room, event, and rule updates now perform
the documented read-merge-write flow, preserve unknown fields, strip read-only
properties, and validate the complete merged request; dry-run performs the read
without writing. Position and route constraints and interactive zone resolution
are covered. Targeted uncached tests and `make check` pass. Approval buz:
`019ff129-6deb-730a-8c33-16c90ebab895`.

Landed on main as `22f4bd9`; its clean worktree, local product branch, and Hive
session were retired.

## Access Apps — `product/access-apps`

Initial verdict: changes required at `b6bc057`, rework round 1.

- Blocking boundary violation: `internal/cli/access.go` must match main except
  for exactly one added `newAccessAppsCmd(g)` line. The current diff also
  reindents scaffold comments.
- Its zone scope must rebase onto main `21648e3` and use
  `resolveZoneInteractive`; account scope remains unchanged. Remaining API
  review continues in this round.
- Pinned-spec comparison shows that name/domain/aud/exact/search are supported
  only by the account list endpoint. Reject these filters under `--scope zone`
  before client or zone resolution, and validate scope combinations before
  constructing a client.

Final verdict: approved at `b8b9b3c`. The access parent diff is exactly one
in-list constructor line, zone resolution is interactive, and invalid scope or
zone-filter combinations fail before client construction or resolution. The
CRUD, read-merge PUT, policy-rule, dry-run, pagination, and output contracts
match the pinned API. Targeted uncached tests and `make check` pass. Approval
buz: `019ff129-6dee-71dc-80a2-c760a87d6132`.

Landed on main as `23e21c1`; its clean worktree, local product branch, and Hive
session were retired.

## Access Identity — `product/access-identity`

Initial verdict: changes required at `8c73a4d`, rework round 1.

- Blocking boundary violation: `internal/cli/access.go` reindents/deletes
  scaffold comments. Restore the parent file exactly and add only
  `newAccessIdentityCmd(g)`.
- The corrected commit `7853001` now has the required in-list +1/-0 group diff.
- Blocking wire error remains: `/access/organizations/revoke_user` defines
  `devices` in the JSON body beside `email`, but `revoke-sessions` sends it as
  a query parameter. Move it into the body and test unset, true, and explicit
  false.

Final verdict: approved at `681db36`. The access parent diff is exactly one
in-list constructor line. Revoke sessions now sends `devices` in the JSON body,
preserves explicit false, omits the field when unset, and sends no query string.
The remaining provider, group, token, and user contracts match the pinned API.
Targeted uncached tests and `make check` pass. Approval buz:
`019ff129-6def-73ec-b98b-6e96b6770d97`.

## Web Analytics — `product/web-analytics`

Final verdict: approved at `32ec8df` with no rework.

- Site CRUD, rule CRUD, and bulk rule application paths/methods and request
  schemas match pinned OpenAPI `4e2f140437b8`.
- Reviewed enum, host/zone, JSON-array, boolean, confirmation, pagination,
  table/raw output, dry-run, and API-error behavior. Scope is the two product
  files plus exactly one root registration.
- Targeted uncached tests and branch `make check` pass; integrated main
  `4f3131c` also passes. Approval buz:
  `019ff121-8c9f-749c-9f61-2bc97a19d6fe`.

Landed on main as `8f02c3d`; its clean worktree, local product branch, and Hive
session were retired.
