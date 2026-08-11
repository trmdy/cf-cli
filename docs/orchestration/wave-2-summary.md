# Wave 2 retrospective (complete 2026-08-11)

Coordinator: CO.9dc4 (codex gpt-5.6-sol xhigh). Wall clock is measured from
the first shard-bee spawn to the landed product commit. Final implementation
mix: 4 Claude Opus, 4 Codex Terra/high, and 2 Grok.

| Product | Bee | Harness | Rework | Wall clock | Landed | Notable findings |
|---|---|---|---:|---:|---|---|
| pages | CL.91a | claude opus high | 1 | 31m48s | 68e9d78 | Required `production_branch`; corrected Direct Upload and production-only rollback help. |
| tunnel | CL.b9bf | claude opus high | 2 | 1h03m52s | a7d163b + 03c10a0 | Removed an invented secret maximum; enforced route bounds/IDs before lookup; removed deprecated connection counts; corrected config help. |
| queues | CO.6517 | codex terra high | 1 | 24m21s | 78a67e4 | Resolved queue names to IDs for every scoped operation; accepted every valid JSON message shape. |
| d1 | CO.0350 | codex terra high | 1 | 30m07s | c21b690 | Normalized location enums, rejected contradictory placement flags, and aligned empty inline/file SQL handling. |
| hyperdrive | CO.d2ed | codex terra high | 1 | 28m42s | 7b0338f | Canonicalized SSL mode on the wire and enforced the 5–100 connection limit. |
| vectorize | CL.9327 | claude opus high | 1 | 50m20s | 4bb7762 | Rejected null filters, enforced conditional query limits, fixed discard semantics, and added vector byte/type bounds. |
| images | GR.bb2d | grok | 1 | 36m34s | 50aee76 | Preserved explicit empty creator filters, rejected null metadata, and removed a cross-shard helper collision. |
| stream | GR.e41a | grok | 1 | 19m02s | 7e1855c | Removed a helper collision, bounded token/upload inputs, and made the 1,000-video windowing tradeoff explicit. |
| turnstile | CL.3937a | claude opus high | 1 | 53m06s | f1c9c96 | Enforced filter grammar, exposed create-only region, and checked widget/domain bounds on create and replacement updates. |
| logpush | CO.ecc5 | codex terra high | 1 | 43m33s | 7221223 | Kept dataset create-only, enforced delivery-control ranges, and closed endpoint/rendering coverage gaps. |

## What held

- No implementer-authored diff touched `internal/api`, `internal/config`,
  `internal/output`, `internal/registry`, `tools/`, Makefile, CI, or a Wave 1
  product.
- Every approved branch passed the current-main gate with GOROOT/GOBIN unset;
  assembled main at `03c10a0` passes fmt-check, vet, lint, all tests, and build.
- All 10 product worktrees and local branches were removed only after the
  maintainer's landed acknowledgement; all Wave 2 implementers are retired.
- The local-branch-only protocol held after its mid-wave introduction. There
  are no open GitHub PRs.

## Patterns established

- Validate complete local input contracts before client construction or
  name-resolution traffic; retain defensive checks in request builders.
- Decode JSON flags through `any` and assert object/array/scalar shape so
  `null` cannot silently pass as a nil map.
- Product-prefix package helpers to prevent sibling shard collisions.
- Do not imply shared auto-pagination for APIs whose response metadata cannot
  support it safely; expose explicit range/window controls instead.
- Check serialized canonical values, not just case-insensitive input
  acceptance, and test both sides of every documented boundary.

## Open product tradeoffs

- Stream libraries over 1,000 videos require explicit `--after`/`--before`
  windows because the endpoint lacks shared-paginator metadata.
- Images uses a tested product-local page loop for its nested V1 response.
- Name resolution for queue/tunnel references performs a read during dry-run;
  this matches the DNS porcelain exemplar and is documented.

No new kernel debt was introduced by Wave 2. Vectorize and Images used the
shared content-type support already landed on main rather than bypassing the
kernel boundary.

## Incident log

- Kimi K3 first crashed from unbound-account spawns, then both bound retries
  stopped on expired authentication. No agent attempted human authentication;
  the maintainer-approved final assignment used Terra for Hyperdrive and Opus
  for Vectorize.
- The merge protocol changed mid-wave from GitHub PRs to local branches. Early
  PRs were closed unmerged, no later product branch was pushed, and the
  maintainer alone squash-merged and pushed main.
- Stream exposed that `make check` did not enforce gofmt. Main added
  `fmt-check`; every later approval used the current-main Makefile gate.
- A delayed Tunnel review buz clarified one help finding after `a7d163b` had
  landed. Cleanup was held; the maintainer landed the reviewed correction as
  `03c10a0`, reran the gate, and then authorized cleanup.

## Orchestration record

Selected primitive: direct 10-way `hive spawn` fan-out with seal/buz fan-in;
no Hive flow or Pollinate run was needed. The launch forms were:

```sh
env -u GOROOT -u GOBIN hive spawn claude --name cf-wave2-<product> --cwd <worktree> --account auto -- --model opus --effort high
env -u GOROOT -u GOBIN hive spawn codex --name cf-wave2-<product> --cwd <worktree> --account auto -- -m gpt-5.6-terra -c model_reasoning_effort=high
env -u GOROOT -u GOBIN hive spawn grok --name cf-wave2-<product> --cwd <worktree> --yolo
```

Ground truth and handoffs were collected with:

```sh
hive fleet --json
hive wait <bee-id> --seal --timeout-ms 45000 --json
hive buz inbox cf-wave2-coordinator --json
hive buz send apiary-waggle-mso8zefe-1 --sender cf-wave2-coordinator --tier queue --subject wave2.approval.<product> -p '<json>'
```

Each landed shard was stopped and cleaned with explicit resolved targets:

```sh
git worktree remove /Users/trmd/Projects/trmd/cf-cli/worktrees/<product>
git branch -D product/<product>
hive retire <bee-id>
```
