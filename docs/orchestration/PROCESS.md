# Porcelain development process (two-layer orchestration)

Linus-style layered merging with two orchestration layers above the
implementers.

## Roles

- **Maintainer (Claude Fable, hive bee `apiary-waggle-mso8zefe-1`)** — owns
  the kernel (`internal/*`, `tools/*`, taxonomy mapping), spawns and
  coordinates wave coordinators, and is the **final reviewer**. Only the
  maintainer merges to `main`, and only work a coordinator has approved.
- **Wave coordinator (one per wave; codex, gpt-5.6-sol, xhigh)** — owns a
  wave of product shards. Spawns implementer bees, assigns exactly one
  product per bee, reviews their PRs adversarially, drives rework until the
  shard meets the bar, then approves and hands the PR up to the maintainer.
  Coordinators never merge and never push to `main`.
- **Implementer (one per product; balanced mix of claude opus, codex
  gpt-5.6-terra high, kimi k3, grok)** — implements porcelain for one
  product in its own git worktree on branch `product/<name>`, per
  `docs/STYLE.md` with `internal/cli/dns.go` as the exemplar. Opens a PR
  when `make check` passes.

## Merge protocol

1. Implementer: worktree + branch `product/<name>` → implement → `make
   check` green → push → `gh pr create` → seal JSON → buz coordinator.
2. Coordinator: deep review (correctness, STYLE.md conformance, test
   quality, UX taste). Findings go back via buz; implementer reworks. Max 2
   rework rounds; then reassign or escalate.
3. Coordinator approval: PR review comment summarizing what was checked +
   seal + buz the maintainer with `{product, pr, branch, verdict}`.
4. Maintainer: final review of the approved PR, merge (squash) or bounce
   back with reasons. Kernel-level fixes discovered in review go to the
   maintainer, not the implementer.

## Hard rules for coordinators and implementers

- Never push to `main`; never merge PRs.
- Never modify `internal/api`, `internal/config`, `internal/output`,
  `internal/registry`, `tools/gen`, `tools/lint`, the Makefile, or CI
  workflows. If a shard needs a kernel change, buz the maintainer and block
  on the answer.
- One product per implementer bee, one wave per coordinator.
- Every status change is a seal or a buz message — no silent state.
- `make check` green is a precondition for review, not a substitute for it.

## Wave sizing

Wave 1 is a 5-product pilot (see wave-1.md). Later waves scale to ~10
products once the pilot proves the task packet. Big shards (radar 277 ops,
access 161) are split or scheduled last.
