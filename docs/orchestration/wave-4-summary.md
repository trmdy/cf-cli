# Wave 4 retrospective (complete 2026-08-12)

Coordinator: CO.6c02 (codex gpt-5.6-sol xhigh). Wall clock is measured from
the first shard-bee spawn to the landed product commit. Initial implementation
mix: 4 Claude Opus, 5 Codex Terra/high, and 4 Grok. One narrowly scoped Codex
recovery completed Alerting after the original implementer reached the two-round
rework limit.

| Product | Bee | Harness | Rework | Wall clock | Landed | Notable findings |
|---|---|---|---:|---:|---|---|
| gateway-policies | CL.acd9 | claude opus high | 1 | 1h47m15s | cd96e68 | Applied the pinned 36-code-point rule/list ID maximum and completed exact request coverage while retaining full-object rule/list merge semantics. |
| gateway-config | CO.a6a3 | codex terra high | 2 | 1h47m14s | c365c01 | Corrected the Browser Isolation wire field and made nullable max-TTL clearing explicit, mutually exclusive, and true-only. |
| devices-fleet | CL.2961 | claude opus high | 1 | 1h47m09s | a53a06d | Removed an invented service-mode/proxy-port dependency, fixed Unicode length accounting, and tightened leaf grammar. |
| devices-posture | CO.1be3 | codex terra high | 1 | 1h47m10s | aa6d089 | Filled the six missing list/get/delete request and destructive-path tests; update and scope semantics already matched the pinned API. |
| dlp | CL.1823 | claude opus high | 1 | 1h47m12s | a66e996 | Separated response enums from write contracts, corrected predefined/custom update shapes, validated UUID/base64 inputs, and covered all 13 leaves plus both upload requests. |
| dex | GR.d1a0 | grok | 2 | 1h47m11s | fafdb93 | Fixed endpoint pagination limits and termination, documented timestamp compatibility, Unicode ID bounds, and malformed-result fallback output. |
| ai-gateway | CO.c650 | codex terra high | 1 | 1h47m06s | da6ba1e | Added a real 12-leaf request matrix, authoritative total-count pagination, and lossless malformed-result handling. |
| ai | GR.083c | grok | 2 | 1h47m05s | 3a1015a | Required model input and constrained `--data` to a non-null JSON object before client construction. |
| addressing | CO.8534 | codex terra high | 1 | 1h47m04s | 9419c1b | Made map PATCH changed-fields-only, restored boolean tri-state, removed invented ID character bans, verified PDF signatures, and covered all 17 leaves. |
| secondary-dns | CO.6de5 | codex terra high | 1 | 1h47m16s | 9dabf62 | Added IPv4/IPv6 validation to merged peer writes and a real 22-leaf request/read-before-write matrix. |
| dns-config | CL.91a9 | claude opus high | 1 | 1h47m13s | 6cb7b93 | Enforced DNS Firewall ID/name bounds in Unicode code points and completed exact Firewall get coverage. |
| alerting | GR.42fc → CO.3e86 | grok → codex terra high | 2 + recovery | 1h47m07s | 42244b1 | Split scalar partial writes from nested mechanism merge reads, fixed pinned enums/bounds and deterministic output, then replaced a vacuous matrix with exact 16-leaf coverage. |
| custom-hostnames | GR.f769 | grok | 2 | 1h47m08s | 7371b0b | Enforced Unicode hostname/origin maxima, restored the pinned list-filter exclusion after a coordinator correction, and added strict leaf grammar. |

## What held

- No implementer-authored diff touched `internal/api`, `internal/config`,
  `internal/output`, `internal/registry`, `tools/`, Makefile, CI, or a prior-wave
  product. The assembled change consists of 26 new product/test files plus the
  expected parent-group and root registration lines.
- Every approved branch passed an uncached `internal/cli` test run and
  `make check` with GOROOT/GOBIN unset. Fully assembled main at `0436d05`
  independently passes fmt-check, vet, lint, all tests, and build; the
  maintainer also reported green CI after pushing main.
- All 13 product worktrees and local branches were removed only after the
  maintainer's landed acknowledgement. All 14 implementation sessions,
  including the Alerting recovery, were retired.
- The local-branch-only protocol held: no coordinator or implementer push,
  merge, or GitHub PR occurred. Only the maintainer squash-merged and pushed.
- The no-ramp launch held. Twelve implementers launched within three seconds;
  the thirteenth landed in the fleet 14.2 seconds after the first because its
  initial spawn hit transient index reconciliation. Every coordination cycle
  rebuilt its state from `hive fleet --json`.

## Patterns established

- JSON Schema `maxLength` is a Unicode code-point limit. Do not substitute byte
  length, infer an exact UUID pattern, or reuse a response-only enum in a write
  schema when the pinned request declares only a string or maximum.
- Request-construction coverage is strongest when it executes the real root
  command and asserts method, full escaped path, query, JSON-equivalent or exact
  body, read sequence, write count, and destructive live/dry-run behavior.
- A true partial PATCH/PUT sends changed fields without a read. A replaced
  nested object gets a targeted read-merge-write; a full-schema write gets a
  raw-object read-merge-write that preserves unknown writable fields, strips
  known read-only fields, and validates the complete outgoing body.
- Dry-run may read only when the preview body or name-to-ID resolution cannot
  be built locally. That exception belongs in command help and tests; all other
  dry-runs remain network-free.
- Local validation precedes client construction and scope resolution. This
  includes operation-local numeric bounds, Unicode lengths, canonical UUIDs,
  IP addresses, base64 keys, document signatures, and cross-field contracts.
- Endpoint pagination metadata remains authoritative over short-page
  heuristics, and malformed collection results must render raw or fail clearly
  rather than silently producing an empty table.
- The prior-wave multipart convention held: deterministic dry-run and streamed
  live transfer share request construction, and large transfers use
  `Client.DoStream`.

## Sub-shard mechanic assessment

The boundary invariant succeeded without product-code cross-contamination. All
five sub-shard branches across Gateway, Devices, and DNS Config passed all three
checks at every handoff: the parent file was exactly `1 0`, `root.go` had no
diff, and the live constructor was the first entry after `AddCommand(` with the
scaffold comments untouched.

The maintainer's assembled diff is exactly Gateway `+2/-0`, Devices `+2/-0`,
DNS `+1/-0`, and root `+8/-0` for the eight standalone shards. The two shared
parent-file collisions were predictable one-line conflicts and resolved by
retaining both constructors. This mechanic should remain the default whenever
multiple shards share a command group.

## Open product tradeoffs

- The one-constructor sub-shard boundary keeps ownership safe but produces deep
  paths such as `cf gateway policy list item add` and the awkward
  `cf gateway policy list list`. Flattening those paths would require a shared
  group API rather than independent one-line registration.
- DNS Firewall is account-scoped but lives beneath the zone-oriented
  `cf dns config` group. Help calls the scope out; moving it to a sibling would
  require relaxing the shard boundary.
- Nested DNS settings, Gateway rule/list full writes, webhook updates, selected
  DLP updates, and similar replacement contracts intentionally read during
  dry-run. Their previews cannot be truthful without current server state.
- DLP, Gateway, Devices, DNS Firewall, and other very broad APIs deliberately
  expose only the common human workflows. Long-tail analytics, bulk upload,
  registration-level, specialized settings, and deprecated operations remain
  available through `cf api`.
- Product-local pagination loops remain where result metadata cannot safely
  drive the shared paginator. These loops are tested for non-advancing pages and
  authoritative totals rather than forcing a kernel abstraction.

No new kernel debt was introduced by Wave 4. The final porcelain wave used the
streaming, content-type, pagination, interactive zone-resolution, and output
primitives established in the earlier waves.

## Incident log

- Kimi was unavailable at launch. The exact assigned mix used the available
  Claude, Codex, and Grok harnesses with `--account auto` where applicable and
  GOROOT/GOBIN unset.
- Main advanced from the first green baseline `6f900dc` to `3f269033` during
  launch because release engineering landed. All 13 branches were created from
  the newer commit and the baseline gate was rerun there before review.
- Secondary DNS's first spawn attempt encountered active-index reconciliation;
  fleet inspection showed no orphan and the named session succeeded once on
  retry.
- The coordinator initially overlapped several full review gates. Host pressure
  killed three test processes and crashed four Claude sessions. Their
  worktrees were intact; the exact sessions were revived, no duplicate work was
  launched, and all subsequent full gates ran strictly one at a time.
- The Custom Hostnames review initially asked to remove a filter exclusion that
  the pinned spec actually requires. The coordinator corrected the finding in
  round 2, restored the exclusion, and recorded the error explicitly.
- Alerting reached the two-round limit with a test matrix that still ignored
  command errors and exact requests. Per PROCESS.md it was reassigned to a
  narrow Codex recovery, which repaired tests/help without changing the scoped
  product design.
- The maintainer session was found crashed immediately before assembly. Exact-
  session revival preserved its state; queued approvals were collected and the
  local merge train completed without coordinator merge authority expanding.

## Orchestration record

Selected primitive: immediate direct 13-way `hive spawn` fan-out with seal/buz
fan-in, followed by one narrow recovery spawn after the rework limit. No Hive
flow or Pollinate run was needed. The launch forms were:

```sh
env -u GOROOT -u GOBIN hive spawn claude --name cf-wave4-<product> --cwd <worktree> --account auto -- --model opus --effort high
env -u GOROOT -u GOBIN hive spawn codex --name cf-wave4-<product> --cwd <worktree> --account auto -- -m gpt-5.6-terra -c model_reasoning_effort=high
env -u GOROOT -u GOBIN hive spawn grok --name cf-wave4-<product> --cwd <worktree> --yolo
```

Ground truth, handoffs, and recovery were managed with:

```sh
hive fleet --json
hive wait <bee-id> --seal --timeout-ms 45000 --json
hive buz send apiary-waggle-mso8zefe-1 --tier next-tool -p '<json>'
hive revive <exact-bee-id>
```

After the maintainer acknowledged all landed commits and green assembled CI,
the resolved cleanup targets were removed and retired:

```sh
git worktree remove /Users/trmd/Projects/trmd/cf-cli/worktrees/<product>
git branch -D product/<product>
hive retire <bee-id>
```
