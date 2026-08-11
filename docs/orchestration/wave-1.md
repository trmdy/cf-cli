# Wave 1 — pilot (5 products)

Coordinator brief. Read docs/orchestration/PROCESS.md first; it defines
roles and the merge protocol. You are the wave-1 coordinator.

## Shards and harness assignment

| Product | Branch | Implementer harness | Scope notes |
|---|---|---|---|
| `zones` | `product/zones` | claude (opus) | list/get/create/delete zone, pause/resume, settings get/set for the common toggles (dev mode, ssl mode, always-https) |
| `r2` | `product/r2` | codex (gpt-5.6-terra, high) | bucket list/create/delete/info; object put/get/delete/list only if the API supports it cleanly — S3-compat proxying is OUT of scope |
| `kv` | `product/kv` | kimi k3 | namespace list/create/delete/rename; key get/put/delete/list; bulk put/delete from @file |
| `cache` | `product/cache` | grok | purge (everything, by url/tag/host/prefix), smart tiered cache get/set |
| `load-balancers` | `product/load-balancers` | claude (opus) | lb + pool + monitor CRUD, pool health view |

Use `cf api <product> --help` and `docs/generated/products.md` to see the
generated plumbing each porcelain sits on. Porcelain = the 3-8 workflows a
real user needs, not endpoint mirroring.

## What each implementer gets told

- Work ONLY in your assigned worktree and branch; one file
  `internal/cli/<product>.go` (+ `_test.go`), wire the command into
  `internal/cli/root.go` (that one-line AddCommand edit is allowed).
- Follow `docs/STYLE.md`; `internal/cli/dns.go` is the exemplar to copy.
- Tests for every command's request construction (httptest; no real API).
- `make check` must be green before opening a PR (this runs vet, the style
  linter, all tests, and the build).
- Deliverable: a PR to main titled `porcelain: <product>`, plus a seal:
  `{"product": "...", "branch": "...", "pr": N, "status": "ready|blocked",
  "notes": "..."}` and a buz message to you.

## Coordinator duties

1. Setup per shard: from the repo root run
   `git worktree add ../../worktrees/<product> -b product/<product> origin/main`
   then spawn the implementer with `--cwd` set to that worktree.
   Ground truth for spawn flags: `hive ps --wide` shows working invocations;
   use `--account auto` for codex/claude bees.
2. Ramp: start ONE implementer (cache — smallest shard), verify its PR
   passes your review end to end, then start the remaining four.
3. Review adversarially: correctness against the spec (use `--dry-run`
   output), STYLE.md conformance beyond what the linter catches, test
   quality, help-text/UX taste, no kernel-file edits (check the PR diff
   touches only its own files + the root.go wiring line).
4. Max 2 rework rounds per shard; after that reassign to a different
   harness or escalate to the maintainer.
5. On approval: PR review comment summarizing what you verified, then buz
   the maintainer (`hive buz send apiary-waggle-mso8zefe-1 --sender <you> -p
   '<json>'`) with `{product, pr, branch, verdict: approved}`.
6. Keep `docs/orchestration/wave-1-assignments.json` up to date in YOUR
   working copy only (do not commit it): shard -> bee id, state, pr, rounds.
   Reconcile from `hive fleet --json` at the top of every cycle; hold no
   fleet state in memory.
7. When all 5 shards are approved (or terminally blocked), send the
   maintainer a wave summary and idle.

## Stop conditions

- Shard done: PR approved and handed up.
- Shard blocked: 2 failed rework rounds + 1 reassignment attempt.
- Wave done: all shards done/blocked.
- Kill switch: the maintainer may retire the wave at any time; treat a buz
  "stop" as immediate.
