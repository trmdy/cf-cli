# Wave 3 retrospective (complete 2026-08-11)

Coordinator: CO.3f1fb (codex gpt-5.6-sol xhigh). Wall clock is measured from
the first shard-bee spawn to the landed product commit. Final implementation
mix: 3 Claude Opus, 4 Codex Terra/high, and 3 Grok.

| Product | Bee | Harness | Rework | Wall clock | Landed | Notable findings |
|---|---|---|---:|---:|---|---|
| workers-scripts | CL.fe45 | claude opus high | 1 | 53m03s | ea83566 | Restored the one-line group boundary, escaped account paths, and hardened multipart module headers while preserving exact dry-run and streamed-live bodies. |
| workers-platform | CO.3947c | codex terra high | 1 | 32m22s | 09963ac | Restored the exact one-line group boundary; platform endpoint and workflow coverage otherwise matched the pinned API. |
| workers-dispatch | CO.0a38 | codex terra high | 1 | 49m28s | fac68f8 | Made dry-run a valid deterministic multipart request, fixed module MIME, and restored the in-list one-line registration. |
| access-apps | CL.3b68 | claude opus high | 1 | 51m44s | Adopted interactive zone resolution, rejected account-only filters in zone scope before client work, and restored the one-line access boundary. |
| access-identity | CL.70a3 | claude opus high | 1 | 54m21s | Moved revoke-session `devices` from query to JSON body with explicit-false semantics and restored the one-line access boundary. |
| rulesets | CO.f435 | codex terra high | 1 | 29m56s | Rebased for mandatory interactive zone resolution while preserving correct version-creating mutation semantics. |
| ssl-certs | CO.31e2 | codex terra high | 2 | 48m08s | Enforced zone-apex and Origin CA hostname contracts; the second round closed an apex-validation gap in dry-run. |
| waiting-room | GR.99a9 | grok | 1 | 50m45s | Rebuilt required PATCH bodies with read-merge-write, tightened one-based rule positions and route syntax, and adopted the interactive resolver. |
| spectrum | GR.0b5b | grok | 1 | 26m58s | Added safe local pagination, cross-field/range constraints, raw unknown-field preservation, and interactive zone resolution. |
| web-analytics | GR.7c56 | grok | 0 | 39m56s | RUM site/rule CRUD and bulk rule application matched the pinned API without coordinator rework. |

## What held

- No implementer-authored diff touched `internal/api`, `internal/config`,
  `internal/output`, `internal/registry`, `tools/`, Makefile, CI, or a prior-wave
  product. Standalone shards added one root registration; sub-shards never
  touched root and ended with exactly one group-file constructor addition.
- Every approved branch passed `make check` and an uncached `internal/cli` test
  run with GOROOT/GOBIN unset. Fully assembled main at `f3afb46` passes
  fmt-check, vet, lint, all tests, and build.
- All 10 worktrees and local product branches were removed only after the
  maintainer's landed acknowledgement; all Wave 3 implementers are retired.
- The local-branch-only protocol held: no coordinator or implementer push,
  merge, or GitHub PR occurred.
- The no-ramp launch held. All ten sessions were created within 1.2 seconds,
  and every coordination cycle rebuilt its view from `hive fleet --json`.

## Patterns established

- Zone-scoped porcelain resolves explicit `--zone`, configured zone, then an
  inline TTY picker through `resolveZoneInteractive`; local validation still
  precedes client construction and resolver traffic.
- Multipart commands must share one writer between deterministic exact dry-run
  fixtures and streamed live requests. Large transfers use `Client.DoStream`,
  and non-JSON bodies use `Request.ContentType`.
- When an API write schema requires fields the CLI exposes as partial updates,
  read the current raw object, preserve unknown writable fields, strip known
  read-only fields, merge changes, then validate the complete outgoing body.
- Endpoint-specific capabilities win over generic helpers: use a local page
  loop when result metadata requires it, and reject scope-specific filters
  rather than sending parameters an endpoint silently ignores.
- A sub-shard constructor goes immediately after the existing `AddCommand(`.
  That placement is gofmt-stable and keeps the parent diff at exactly +1/-0.

## Sub-shard mechanic assessment

The mechanic succeeded and is worth retaining. Three Workers implementers and
two Access implementers worked independently without touching `root.go`; after
maintainer conflict resolution, all five commands composed cleanly under their
two parent groups. Conflicts were predictable and limited to a single line in
each scaffold rather than spreading across root registration or product code.

The main source of friction was mechanical, not architectural: several first
handoffs replaced placeholders, reindented scaffold comments, or added a second
`AddCommand` statement. The reliable review invariant is now explicit:
`git diff --numstat <base>...HEAD -- internal/cli/<group>.go` must be `1 0`,
`root.go` must have no sub-shard diff, and the live constructor must be the
first entry after `AddCommand(`. Enforcing those three checks at handoff should
remove nearly all future sub-shard boundary rework.

## Open product tradeoffs

- Access Apps deliberately leaves application types requiring unmodeled SaaS
  or infrastructure bodies to the generated `cf api` layer; updates use a
  documented read-merge PUT so dry-run performs a read.
- Waiting Room updates and SSL certificate-pack order dry-runs perform reads to
  validate complete replacement/apex contracts before showing the write.
- Spectrum uses a tested product-local page loop because its pagination
  metadata cannot be driven safely by the shared paginator in this shape.
- Access Identity is account-scoped; zone mirrors and less-common identity
  operations remain available through the generated API layer.

No new kernel debt was introduced by Wave 3. Workers Scripts and Dispatch used
the shared streaming and content-type primitives added before the wave rather
than bypassing the kernel boundary.

## Incident log

- Kimi was excluded before launch because its authentication had expired. No
  agent attempted interactive or human authentication; the ten-way assignment
  used the available Claude, Codex, and Grok harnesses from the brief.
- Main advanced shortly after launch with interactive auth/profile work and the
  mandatory zone picker. Spectrum, Rulesets, Waiting Room, SSL Certificates,
  and Access Apps rebased or adapted in-flight to `resolveZoneInteractive`;
  account-only paths were unchanged.
- Gofmt's treatment of comment-only `AddCommand` lists exposed the exact
  insertion-position rule. Boundary rework was required on the Workers and
  Access sub-shards, but no product code crossed shard ownership.
- SSL Certificates needed the only second review round: its first apex fix ran
  only for live orders, allowing an invalid dry-run request. The second round
  made validation mode-independent and added exact no-POST coverage.

## Orchestration record

Selected primitive: immediate direct 10-way `hive spawn` fan-out with seal/buz
fan-in; no Hive flow or Pollinate run was needed. The launch forms were:

```sh
env -u GOROOT -u GOBIN hive spawn claude --name cf-wave3-<product> --cwd <worktree> --account auto -- --model opus --effort high
env -u GOROOT -u GOBIN hive spawn codex --name cf-wave3-<product> --cwd <worktree> --account auto -- -m gpt-5.6-terra -c model_reasoning_effort=high
env -u GOROOT -u GOBIN hive spawn grok --name cf-wave3-<product> --cwd <worktree> --yolo
```

Ground truth and handoffs were collected with:

```sh
hive fleet --json
hive wait <bee-id> --seal --timeout-ms 45000 --json
hive buz inbox cf-wave3-coordinator --json
hive buz send apiary-waggle-mso8zefe-1 --sender cf-wave3-coordinator --tier queue --subject wave3.approval.<product> -p '<json>'
```

Each landed shard was retired and cleaned with explicit resolved targets:

```sh
git worktree remove /Users/trmd/Projects/trmd/cf-cli/worktrees/<product>
git branch -D product/<product>
hive retire <bee-id>
```
