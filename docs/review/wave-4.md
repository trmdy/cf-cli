# Wave 4 porcelain review ledger

Coordinator: `cf-wave4-coordinator` (`CO.6c02`). This local ledger records
adversarial review findings and maintainer communication for the thirteen Wave
4 shards. The authoritative handoff is a local product branch; no GitHub PR or
branch push is used.

## AI — `product/ai`

Initial verdict: changes required at `75015e3`, rework round 1.

- Blocking input contract: `cf ai run <model>` currently accepts neither
  `--data` nor `--field` and emits a bodyless POST. The scoped workflow requires
  one of those input forms; reject the missing-input case before client
  construction and prove it sends no request.
- Blocking schema mismatch: `--data null`, arrays, and scalar JSON are accepted,
  but the pinned run endpoint declares an object request body. Decode through
  `any`, require a non-null JSON object, and test null/array/scalar rejection
  before client construction while keeping model-specific contents flexible.

The standalone scope is correct: `ai.go`, `ai_test.go`, and exactly one added
root constructor line. Account/model path escaping, model-search parameters,
pagination behavior, output/query routing, and the common text response match
the pinned API. Coordinator `make check` and uncached `internal/cli` tests pass
at `75015e3`; the green gates do not cover the two request-contract gaps above.

Round 2 verdict at `5759dd6`: changes still required, final rework round.

The missing-input check is fixed before client work, but the original schema
finding remains unchanged: `--data null`, arrays, strings, numbers, and booleans
are still passed through by the shared body builder. Validate the resulting JSON
as a non-null object before client construction and cover each rejected JSON
kind plus a valid object. Repeated `--field` input already produces an object.

Final verdict: approved at `3019d26` after rework round 2.

Both original input-contract blockers are closed: a body is required and
`--data` must decode to a non-null JSON object before client construction.
Coordinator uncached `internal/cli` tests and `make check` pass serially; the
standalone diff remains the two product files plus exactly one root constructor.

## Custom Hostnames — `product/custom-hostnames`

Initial verdict: changes required at `cfbf579`, rework round 1.

- Blocking pinned bounds: create accepts hostnames longer than the documented
  255-character maximum, and fallback-origin set accepts origins beyond the
  same documented maximum. Enforce both exact bounds before client/zone work
  and test 255/256 boundaries.
- Superseded coordinator finding: the initial review incorrectly said list
  `--id` and `--hostname` could be combined. The pinned parameter descriptions
  explicitly make them mutually exclusive. Round 2 below corrects this.
- Leaf command grammar: `list` and `fallback-origin get` silently ignore extra
  positional arguments. Add `cobra.NoArgs` and focused rejection coverage, in
  line with current porcelain conventions.

The standalone scope is otherwise correct: two product files and one added
root constructor line. CRUD/fallback paths and methods, partial PATCH semantics,
SSL enums and output, interactive zone resolution, confirmation, pagination,
metadata shape validation, and output/query routing match the pinned API.
Coordinator `make check` passes at `cfbf579`; the uncached package gate remains
in progress under concurrent Wave 4 test load.

Round 2 verdict at `a788729`: changes still required, final rework round.

- Restore the `--id`/`--hostname` mutual exclusion removed in round 1 and test
  it as local validation. Validate list `--id` at its exact pinned length of 36
  and list `--hostname` at its pinned maximum of 255.
- The new hostname and fallback-origin maximum checks use byte length while
  JSON Schema `maxLength` counts Unicode code points. Use a rune/code-point
  count for these and the list hostname filter; cover multibyte 255/256 cases.

The create/fallback ASCII boundary, `cobra.NoArgs` leaves, and other round-1
changes are sound. The filter correction above is a coordinator review error,
not implementer-caused scope churn.

Final verdict: approved at `f9fbdf0` after rework round 2.

The pinned list-filter exclusion is restored; list ID/hostname bounds and
Unicode code-point limits are validated locally, while the earlier ASCII
boundary and `NoArgs` fixes remain intact. Coordinator uncached `internal/cli`
tests and `make check` pass serially. Standalone scope remains exact.

## Gateway Config — `product/gateway-config`

Initial verdict: changes required at `e6bd8dc`, rework round 1.

- Blocking wire mismatch: `--browser-isolation` writes
  `settings.browser_isolation.enabled`, but the pinned schema has no `enabled`
  field there. The common Clientless Browser Isolation toggle is
  `url_browser_isolation_enabled`; serialize that exact field and test both
  explicit true and explicit false in the merged PUT.
- Incomplete nullable setting: the shard exposes `max_ttl_secs`, whose pinned
  schema explicitly uses JSON null to remove the account cap, but it has no way
  to clear a configured cap. Add a clear form (for example
  `--clear-max-ttl`) that is mutually exclusive with `--max-ttl-secs`, merges
  `null`, validates before client work, and has exact request tests.

All three sub-shard invariants pass mechanically at handoff: `gateway.go` is
exactly `1 0`, `root.go` is unchanged, and `newGatewayConfigCmd(g)` is the first
entry immediately after `cmd.AddCommand(` with scaffold comments untouched.
Location CRUD/full PUT merge, settings read-merge-write and read-only stripping,
remaining settings fields, certificate paths, request methods, confirmations,
and pinned bounds/enums are otherwise sound. Coordinator `make check` passes at
`e6bd8dc`; the uncached package run was killed by host pressure from simultaneous
Wave 4 gates and will be rerun serially after rework.

Round 2 verdict at `3cd4ec6`: changes still required, final rework round.

- Blocking destructive boolean behavior: `--clear-max-ttl=false` still serializes
  `max_ttl_secs: null` because the patch builder checks only whether the flag was
  changed and ignores its boolean value. Only an explicit true value may clear
  the cap. Add focused explicit-false regression coverage; it may be a no-op that
  reaches the existing nothing-to-change validation or an explicit local error.

The requested Browser Isolation field correction and nullable clear workflow are
otherwise present. All three sub-shard boundary checks remain green at this
handoff.

Final verdict: approved at `6c03cc8` after rework round 2.

Only a true `--clear-max-ttl` now serializes JSON null; explicit false reaches
local nothing-to-change validation with zero requests. Coordinator uncached
`internal/cli` tests and `make check` pass serially. The final boundary checks
are again `gateway.go` `1 0`, clean `root.go`, and first-entry placement with
comments untouched.

## Devices Posture — `product/devices-posture`

Initial verdict: changes required at `7d29596`, rework round 1.

- Blocking definition-of-done gap: request-construction coverage exists for
  rule create/update and integration create/update, but not for rule
  list/get/delete or integration list/get/delete. `STYLE.md` requires request
  tests for every command. Add exact method/path coverage for all six, including
  table-vs-structured list rendering and destructive `--force`/dry-run behavior
  without confirmation.

All three sub-shard invariants pass mechanically at handoff: `devices.go` is
exactly `1 0`, `root.go` is unchanged, and `newDevicesPostureCmd(g)` is the
first entry immediately after `cmd.AddCommand(` with comments untouched. The
rule PUT read-merge-write preserves unknown writable fields and strips `id` and
computed `enabled`; integration PATCH, enums, UUID max length, duration/JSON
shape validation, confirmation, output routing, and endpoint paths/methods match
the pinned API. Coordinator `make check` passes at `7d29596`; its uncached
package run was killed by the same host-pressure incident and will be rerun
serially after rework.

Final verdict: approved at `91c2a02` after rework round 1.

The six missing list/get/delete paths now have exact request and output or
destructive dry-run coverage. Coordinator uncached `internal/cli` tests and
`make check` pass serially. Final sub-shard checks remain `devices.go` `1 0`,
clean `root.go`, and first-entry constructor placement with comments untouched.

## Secondary DNS — `product/secondary-dns`

Initial verdict: changes required at `fb7b2b3`, rework round 1.

- Blocking local contract: peer `--ip` accepts any non-empty string even though
  the pinned field is explicitly an IPv4/IPv6 nameserver address. Parse with
  `netip.ParseAddr` before client work and validate the complete merged peer on
  PUT; cover IPv4, IPv6, and invalid input.
- Blocking definition-of-done gap: the 22-leaf command surface has request
  coverage for only a small subset. Add a compact table-driven request matrix
  that proves every leaf's method/path and required body/read-before-write
  behavior, including peers and TSIG list/get/create/update/delete; incoming and
  outgoing get/create/update/delete; force-AXFR; outgoing status/enable/disable/
  notify; and destructive dry-run/`--force` behavior. `STYLE.md` requires
  request-construction tests for every command.

The standalone diff is correctly limited to two product files plus exactly one
root constructor. Account/zone scoping, interactive zone resolution, TSIG and
configuration full-object read-merge-write, unknown-field preservation,
read-only stripping, auto-refresh minimum, endpoint-local pagination, action
methods, output routing, and current tests match the pinned API. Initial
coordinator gates were killed by the recorded host-pressure incident and must
be rerun serially after rework.

Final verdict: approved at `0dfacc6` after rework round 1.

Peer IPs are parsed as IPv4/IPv6 both from flags and in the complete merged PUT
object. A compact real-command-tree matrix now covers every Secondary DNS leaf,
including GET-before-PUT and destructive force/dry-run paths. Coordinator
uncached `internal/cli` tests and `make check` pass serially; standalone scope
remains the two product files plus exactly one root constructor.

## DNS Config — `product/dns-config`

Initial verdict: changes required at `7e26261`, rework round 1.

- Blocking pinned bounds: DNS Firewall `get`, `update`, and `delete` accept a
  cluster ID longer than the schema's 32-character maximum and can reach client
  work. Validate the shared path argument before client construction and cover
  the exact 32/33 boundary with zero-request failure behavior.
- Blocking character-count mismatch: the DNS Firewall cluster-name maximum is
  measured with byte length, but JSON Schema `maxLength: 160` counts Unicode
  characters. Use a rune/code-point count and cover a multibyte 160-character
  valid name plus a 161-character rejection before client work.
- Definition-of-done gap: DNS Firewall `get` is the only leaf without an exact
  method/path request-construction test. Add it per `docs/STYLE.md`.

All three sub-shard invariants pass mechanically: `dns.go` is exactly `1 0`,
`root.go` is unchanged, and `newDNSConfigCmd(g)` is the first entry immediately
after `cmd.AddCommand(` with scaffold comments untouched. Zone-settings nested
read-merge-write, DNSSEC state changes and consequence wording, DNS Firewall
partial updates, endpoint-local pagination, enums, numeric bounds, IP parsing,
dry-run behavior, scoping, and output routing otherwise match the pinned API.
The recovered implementer reports both serial gates green at `7e26261`.

Final verdict: approved at `3255f2b` after rework round 1.

The cluster-ID validation now runs before client work on all three item leaves,
cluster-name length uses Unicode code points, and Firewall `get` has exact
request coverage. Coordinator `go test -count=1 ./internal/cli` and `make check`
both pass serially. The three `dns.go` boundary checks pass again at final
handoff.

## DEX — `product/dex`

Initial verdict: changes required at `90ac3a6`, rework round 1.

- Blocking pagination wire contract: fleet devices sends `per_page=100`, while
  the pinned `digital-experience-monitoring_per_page` maximum is 50. Use 50 and
  ensure the loop honors `result_info.total_pages` when present before applying
  a short-page fallback, so a server cap cannot silently truncate results.
- Blocking pinned bound: live accepts values above the documented
  `since_minutes` maximum 60 and silently turns explicit zero/negative values
  into 60. Validate the inclusive 1–60 range before client construction; keep
  any chosen in-range default only for an omitted flag. Cover 0, 1, 60, and 61.
- Blocking timestamp compatibility: the local validator rejects the pinned
  example `2023-10-11 00:00:00+00`. Accept that documented ISO form (and retain
  RFC3339/milliseconds), or loosen validation without inventing a constraint;
  add the pinned example to tests.
- Blocking pinned ID bounds: test details accepts more than the documented 32
  characters and traceroute results accepts more than 36. Validate both before
  client work and cover exact/over-limit behavior with zero requests.
- Silent output loss: when the first fleet-devices result is not an array, the
  helper returns raw JSON but the default table path prints an empty table.
  Render the raw result as the established table-fallback behavior and test it.

The standalone boundary is correct: two product files plus exactly one root
constructor. The five scoped workflows, remaining paths/queries, list table and
structured routing, account scoping, dry-run dumps, filters, and leaf request
coverage otherwise match the pinned API.

Round 1 at `08c65dd`: pagination, since-minutes, timestamp compatibility, raw
fallback, and ASCII ID bounds are corrected, but one blocking spec mismatch
remains for final rework round 2. `digital-experience-monitoring_schemas-test-id`
and `digital-experience-monitoring_uuid` use JSON Schema `maxLength`, so their
32/36 limits count Unicode code points; both new validators use byte `len`.
Switch them to `utf8.RuneCountInString` and add multibyte exact/over-bound
coverage that also proves over-limit values fail before any request.

Final verdict: approved at `d590b70` after rework round 2.

Both identifier validators now count Unicode code points and the real command
tree covers multibyte exact/over-limit values. All earlier round-one corrections
remain intact. Coordinator uncached `internal/cli` tests and `make check` pass
serially; standalone scope remains two product files plus one root constructor.

## Addressing — `product/addressing`

Initial verdict: changes required at `d64fd54`, rework round 1.

- Blocking update semantics: Address Map update is a true partial PATCH whose
  pinned request schema has three optional fields (`default_sni`, `description`,
  `enabled`). It currently GETs and merges the response, then sends unknown
  response fields outside that closed request surface. Build a changed-fields-
  only PATCH with no read; dry-run must perform zero network I/O.
- Blocking create flag bug: create binds `--enabled` but never sets
  `EnabledSet`, so explicit true and false are both silently omitted. Set it
  from `Flags().Changed`, test both wire values, and keep omitted distinct.
- Blocking invented ID constraints: the pinned identifier schemas specify only
  `maxLength: 32`; the shared helper additionally rejects `/`, `?`, and `#` even
  though path escaping is already used, and counts bytes rather than JSON
  Schema code points. Remove the undocumented character ban, count code points,
  and cover encoded special characters plus multibyte 32/33 boundaries.
- Blocking file contract: upload accepts any file renamed `.pdf`, although the
  pinned schema says PDF is the supported file type. Validate the `%PDF-`
  signature before client work, preserving deterministic multipart dry-run and
  live `DoStream`; add a renamed-non-PDF zero-request regression.
- Definition-of-done gap: request coverage is missing for several of the 17
  leaves (including prefix/advertisement get, map list/get/create/delete, and
  half the membership mutations). Add a compact method/path/body matrix for
  every leaf, retaining force/dry-run coverage for destructive removals.

The standalone boundary is otherwise exact. Prefix and map pagination, zone
resolution, membership paths, confirmations, deterministic multipart, streamed
live upload/download, raw output restrictions, and existing tests are sound.

Final verdict: approved at `2ea9b30` after rework round 1.

Map update now emits a read-free, changed-fields-only PATCH; create preserves
all three `--enabled` states; identifiers use code-point length and safe path
escaping; and LOA upload rejects renamed non-PDF content before client work.
The compact dry-run matrix covers all 17 leaves while deterministic multipart
and live `DoStream` behavior remain pinned. Coordinator uncached `internal/cli`
tests and `make check` pass serially; standalone scope remains exact.

## AI Gateway — `product/ai-gateway`

Initial verdict: changes required at `4884967`, rework round 1.

- Blocking definition-of-done gap: the 12-leaf surface has exact request tests
  for only gateway create/update, logs list, and dataset create/update. Add a
  compact real-command-tree method/path/query/body matrix for gateway
  list/get/create/update/delete, logs list/get, and dataset
  list/get/create/update/delete, including force/dry-run behavior for deletes.
- Silent log-list output loss: if the first log result is not an array, the
  helper returns raw JSON but the default table path renders an empty table.
  Render the raw result through the established table fallback and test it;
  likewise do not silently omit a malformed individual list item.
- Pagination robustness: the log loop checks `len(page) < requested per_page`
  before the authoritative `result_info.total_count`. Honor `total_count` first
  when supplied so an endpoint-side cap or short intermediate page does not
  truncate; cover a short first page with a larger total count.

The standalone diff is exact. Gateway full-schema PUT read-merge-write and
read-only stripping, dataset replacement update, required create fields,
filters, pinned gateway bounds/enums, boolean tri-state flag handling,
account scope, confirmations, and table/structured output otherwise match the
pinned API.

Final verdict: approved at `74c08cb` after rework round 1.

All 12 leaves now have real-command-tree request coverage, including forced and
dry-run destructive paths. Log listing renders a malformed first result raw,
rejects malformed individual items, and honors authoritative `total_count`
before the short-page fallback. Coordinator uncached `internal/cli` tests and
`make check` pass serially; standalone scope remains exact.

## Devices Fleet — `product/devices-fleet`

Initial verdict: changes required at `4da9193`, rework round 1.

- Blocking invented constraint: `--proxy-port` is rejected unless
  `--service-mode` is also passed, but the pinned custom/default PATCH schemas
  expose both `service_mode_v2.port` and `.mode` as independently optional and
  declare neither required. Remove the pairing rule and serialize whichever
  nested fields changed; cover port-only and mode-only requests.
- Blocking character-count mismatch: profile name/match/description and profile
  ID maxima use byte length. JSON Schema `maxLength` counts Unicode code points.
  Use rune counts and add multibyte exact/over-bound tests for 100, 500, and 36.
- Leaf grammar: device `list` and profile `list` omit `cobra.NoArgs` and accept
  stray positional input. Add it with focused rejection coverage.

All three sub-shard invariants pass at initial handoff: `devices.go` is exactly
`1 0`, `root.go` is unchanged, and `newDevicesFleetCmd(g)` is first immediately
after `cmd.AddCommand(` with scaffold comments untouched. Device cursor
pagination, list filters, get/revoke consequence wording and confirmation,
profile endpoints and partial PATCH behavior, default/custom separation,
remaining spec-derived constraints, dry-run zero-network behavior, and the
existing every-leaf request matrix are otherwise sound.

Final verdict: approved at `7a8700a` after rework round 1.

Mode and port are now independently optional in the nested service-mode body;
all four documented length bounds count Unicode code points; and both list
leaves reject positional input. Coordinator uncached `internal/cli` tests and
`make check` pass serially. Final boundary checks remain exact: `devices.go`
`1 0`, clean `root.go`, and first-entry constructor placement with scaffold
comments untouched.

## Gateway Policies — `product/gateway-policies`

Initial verdict: changes required at `c1a0de4`, rework round 1.

- Blocking pinned ID bound: all rule and list resource identifiers use the
  shared `zero-trust-gateway_uuid-2` schema with `maxLength: 36`, but commands
  currently check only non-blank input. Add one shared pre-client validator,
  count Unicode code points, and cover exact 36/37 plus zero-request rejection
  across representative rule and list/item leaves.
- Definition-of-done gap: exact request construction is missing for `rule get`,
  `list get`, and a forced live `list delete`. Add those method/path assertions
  (and retain destructive dry-run/confirmation coverage).

All three sub-shard invariants pass: `gateway.go` is exactly `1 0`, `root.go`
is unchanged, and `newGatewayPoliciesCmd(g)` is first immediately after
`cmd.AddCommand(` with comments untouched. The 15-command scope is appropriate;
enum sets exactly match the pinned spec with no invented bounds. Rule and list
PUT read-merge-write, nested expiration/read-only stripping, unknown writable
preservation, partial enable/disable PATCHes, list item bodies, pagination loop,
confirmations, dry-run behavior, and current output tests are otherwise sound.

Final verdict: approved at `7be588e` after rework round 1.

Every rule/list path identifier now uses one pre-client 36-code-point validator,
with representative zero-request boundary coverage. Exact live requests now
cover rule get, list get, and forced list delete. Coordinator uncached
`internal/cli` tests and `make check` pass serially. Final `gateway.go` boundary
checks remain `1 0`, clean `root.go`, and first-entry constructor placement with
all scaffold comments untouched.

## Alerting — `product/alerting`

Initial verdict: changes required at `e01862c`, rework round 1.

- Blocking update semantics: policy update's pinned PUT request schema has no
  required fields, so scalar changes must be a changed-fields-only write with no
  prior GET; dry-run must be read-free. Add the schema's missing `name` field.
  `mechanisms` is a nested replacement object, so changing one mechanism family
  must read and merge only that nested object, preserving untouched email,
  webhook, and PagerDuty families; document that targeted dry-run read.
- Blocking pinned validation: create/update accept arbitrary `alert_type`
  despite the pinned enum. Validate and canonicalize before client work. Validate
  mechanism IDs against their referenced UUID maximum in Unicode code points,
  and reject blank create destinations rather than silently discarding them.
- Blocking validation ordering and conditional contract: policy test resolves a
  policy name before rejecting invalid severity/state-event values, and does not
  require `state_correlation_id` when `state_event` is set as the schema
  describes. Validate the complete local test body before client or lookup work;
  do not silently omit explicitly changed string flags.
- Required-object safety: webhook update is correctly read-merge-write because
  its PUT requires `name` and `url`, but the complete merged object is never
  validated. Reject an explicitly blank required field before client work and a
  malformed stored complete object before PUT while preserving unknown writable
  fields and stripping the documented response-only fields.
- Determinism: available-alerts iterates a map directly. Sort category keys (and
  retain API order within each category) so identical responses render stable
  tables.
- Definition-of-done gap: add a compact exact request matrix for all 16 leaves:
  six policy, five webhook, four PagerDuty, and available-alerts. Include scalar
  policy-update zero-read behavior, nested mechanism/webhook read-before-write,
  forced destructive writes, and destructive dry-run zero-write behavior.

Account scoping, ID-or-name resolution, policy and webhook tables, PagerDuty
endpoint shapes, confirmations, raw fallbacks, root-only registration, and the
selected Wave 4 scope otherwise match the pinned API.

Round 1 at `e875836`: scalar versus nested policy update behavior, alert-type
validation, test-fire ordering/pairing, webhook complete-body validation, and
catalog sorting are corrected. Final rework round 2 remains blocked on:

- The mechanism validator invents an exact 32-hex format. `aaa_uuid` specifies
  only `type: string` plus `maxLength: 32`; it has no `minLength`, `pattern`, or
  `format`. Enforce nonblank plus at-most-32 Unicode code points only, and cover
  short/non-hex acceptance plus multibyte 32/33 boundaries.
- The promised 16-leaf exact matrix is absent. Existing additions exercise
  helpers and policy-update semantics but still do not construct exact requests
  for webhook list/get/create/delete or PagerDuty list/connect/link, among other
  gaps. Add one explicit table/matrix covering every leaf's method, path, query,
  body, read count, and write count; include forced live webhook/PagerDuty
  deletes and zero-write destructive dry-runs.

Round 2 at `be7e71a` corrected the mechanism maximum but failed the matrix
blocker: the new table ignores command errors, feeds missing result bodies to
read-before-write cases, and checks call counts rather than exact requests. Per
the two-round limit, the original implementer is exhausted and the narrowly
scoped test/help correction is reassigned to recovery bee `CO.3e86`; the branch
remains blocked and unapproved.

Final verdict: approved at `a253180` after the two original rework rounds and a
narrow Codex recovery.

The recovery replaced the vacuous matrix with real root-command executions for
all 16 leaves, exact dry-run method/full URL/path/query/body assertions, exact
targeted-read sequences, and exact forced destructive calls. Policy delete now
has explicit zero-write dry-run coverage. The update help distinguishes
read-free scalar body construction by ID from name-resolution GETs; PagerDuty
link tokens enforce only the pinned 32-code-point maximum; and a non-null,
non-object current `mechanisms` value fails after the GET without a PUT.
Coordinator uncached `internal/cli` tests and `make check` pass serially;
standalone scope remains two product files plus one root constructor.

## DLP — `product/dlp`

Initial verdict: changes required at `e7a715c`, rework round 1.

- Blocking invented constraints: the custom-profile create/update and
  predefined-profile update request schemas declare `confidence_threshold` as
  a nullable/plain string, not the separate response-side `dlp_Confidence`
  enum. Accept and pass through changed string values instead of rejecting
  unknown values. Likewise, custom profile update has no documented
  `allowed_match_count` minimum/maximum; enforce 0–1000 only on custom create
  and predefined update, where those bounds actually appear. Do not reject an
  unchanged future response value while rebuilding a custom required body.
- Blocking predefined-update semantics: `dlp_PredefinedProfileUpdate` has no
  required fields. The type-dispatch GET remains necessary, including in
  dry-run, but the outgoing predefined PUT must contain only changed supported
  fields. Do not echo context awareness or deprecated entries from the read.
  Custom update correctly remains read-merge-write because its schema requires
  `name`, preserves unknown writable fields, and omits unchanged custom entries.
- Blocking documented formats: profile IDs, dataset IDs, shared-entry IDs, and
  profile-entry `entry_id` fields are `format: uuid`; validate canonical UUID
  input before client work (and complete merged values before write). Payload
  log `public_key` is documented as base64-encoded; validate a changed key before
  the read/client path. Add invalid-input zero-request regressions.
- Entry-shape safety: create must not accept update-only `entry_id`, and a word
  list entry must not silently carry custom-pattern-only `description`. Keep the
  two `dlp_EntryOfNewProfile` variants distinct and cover both rejections.
- Leaf grammar: add `cobra.NoArgs` to profile list/create, dataset list/create,
  and payload-log get/set so stray positionals fail before client work.
- Definition-of-done gap: add a compact exact request matrix for all 13 leaves,
  including dataset get, custom versus predefined update bodies, both upload
  steps, forced live profile/dataset deletes, and destructive dry-run/no-force
  zero-write behavior.

The 13-command scope is appropriately narrow for the 93-operation API and the
file documents the generated-layer escape hatches. Account scoping, profile
type dispatch, custom required-body preservation, dataset partial update,
two-step streamed upload, payload-log ambiguity handling, confirmations, list
tables, and standalone root registration are otherwise sound.

Final verdict: approved at `1f3e8e1` after rework round 1.

Write-side confidence values are now passed through as the pinned request
schemas require; match-count and encoding-version bounds are applied only to
the operations and integer widths that document them. Predefined updates emit
changed fields only after type dispatch, while custom updates retain their
required-name read-merge-write behavior. UUID and base64 inputs fail before
client work, entry variants are kept distinct, and a real request matrix covers
all 13 leaves, both streamed upload requests, and destructive live/dry-run
paths. Coordinator uncached `internal/cli` tests and `make check` pass serially;
standalone scope remains two product files plus one root constructor.
