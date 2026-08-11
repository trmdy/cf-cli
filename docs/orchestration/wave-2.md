# Wave 2 — 10 products, full parallel

Coordinator brief. Read docs/orchestration/PROCESS.md first; you are the
wave-2 coordinator. Wave 1 (zones, r2, kv, cache, load-balancers) runs
concurrently under a different coordinator — its shards are NOT yours.

Unlike wave 1 there is no ramp: spawn all ten implementers immediately and
review branches as they arrive.

## Shards and harness assignment

| Product | Branch | Implementer harness | Scope notes |
|---|---|---|---|
| `pages` | `product/pages` | claude (opus) | project list/get/create/delete, deployment list/get/rollback, domain add/remove |
| `tunnel` | `product/tunnel` | claude (opus) | tunnel list/get/create/delete, token, config get/set, route list/add/remove |
| `queues` | `product/queues` | codex (gpt-5.6-terra, high) | queue CRUD, consumer add/remove, message send/pull/ack |
| `d1` | `product/d1` | codex (gpt-5.6-terra, high) | database CRUD, query execution (--command / @file), export |
| `hyperdrive` | `product/hyperdrive` | kimi k3 | config CRUD |
| `vectorize` | `product/vectorize` | kimi k3 | index CRUD, insert/upsert from @file, query, metadata index ops |
| `images` | `product/images` | grok | image list/get/upload/delete, variant CRUD, usage stats |
| `stream` | `product/stream` | grok | video list/get/delete, upload (tus is out of scope; direct-upload URL creation is in), signed tokens |
| `turnstile` | `product/turnstile` | claude (opus) | widget CRUD, secret rotation |
| `logpush` | `product/logpush` | codex (gpt-5.6-terra, high) | job CRUD (account+zone scope), dataset fields/list, ownership validation |

## Rules (same as wave 1)

- Worktrees from repo root: `git worktree add ../../worktrees/<product> -b
  product/<product> origin/main`; spawn each implementer with `--cwd` there.
- Unset GOROOT/GOBIN in implementer environments (mise Go 1.23 shadows the
  required brew Go 1.25).
- Implementers follow docs/STYLE.md, dns.go exemplar, tests required,
  `make check` green before handoff; commits stay on the local branch (NO GitHub PRs, no pushes).
- root.go one-line AddCommand conflicts across branches are expected; the
  maintainer resolves them at merge. Implementers do not rebase onto other
  product branches.
- Kernel files are off limits (see PROCESS.md). Reject violating branches.
- Max 2 rework rounds, then reassign to a different harness or escalate.
- Approvals, blockers, and the wave summary go to the maintainer
  (`apiary-waggle-mso8zefe-1`) via buz, JSON payloads as in PROCESS.md.
- Track assignments in docs/orchestration/wave-2-assignments.json (local
  only, do not commit). Reconcile from `hive fleet --json` every cycle.
