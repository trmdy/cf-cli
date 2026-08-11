# Wave 3 — 10 shards, full parallel, first sub-sharded products

Coordinator brief. Read docs/orchestration/PROCESS.md first (roles, local
branch merge protocol — NO GitHub PRs, no pushes). Waves 1-2 are complete;
their retros are in wave-1-summary.md / wave-2-summary.md. Read the
"Patterns established" section of the wave-1 summary — those conventions
are binding.

New this wave: **sub-sharded products.** workers and access are too big
for one bee, so multiple implementers share one command group. The group
scaffolds are already on main (`internal/cli/workers.go`, `access.go`,
kernel-owned, already wired into root.go): each sub-shard adds exactly ONE
constructor line to its group's AddCommand list plus its own
`workers_<part>.go` / `access_<part>.go` + test file. Sub-shards do NOT
touch root.go at all. The one-line group-file conflict across sibling
branches is expected; the maintainer resolves it at merge.

Also new: the kernel now has `Client.DoStream` (streaming request/response
bodies, ctx-bounded, no overall timeout) — use it for Worker bundle
uploads and any large transfer. `Request.ContentType` + multipart via
mime/multipart is established practice (see images).

## Shards and harness assignment

| Shard | Branch | File(s) | Harness | Scope notes |
|---|---|---|---|---|
| workers-scripts | `product/workers-scripts` | workers_scripts.go | claude (opus) | script list/get/delete, upload (multipart metadata+modules, DoStream for big bundles), script content download, secrets list/put/delete, subdomain enable/get |
| workers-platform | `product/workers-platform` | workers_platform.go | codex (gpt-5.6-terra, high) | cron triggers get/set, custom domains list/add/remove, deployments list, usage. Registers under the same `cf workers` group |
| workers-dispatch | `product/workers-dispatch` | workers_dispatch.go | codex (gpt-5.6-terra, high) | Workers for Platforms: dispatch namespace CRUD, scripts within a namespace (list/get/delete, upload) |
| access-apps | `product/access-apps` | access_apps.go | claude (opus) | Access application CRUD + per-app policy CRUD |
| access-identity | `product/access-identity` | access_identity.go | claude (opus) | identity providers CRUD, access groups CRUD, service tokens CRUD+rotate, users list + revoke sessions |
| rulesets | `product/rulesets` | rulesets.go | codex (gpt-5.6-terra, high) | account+zone ruleset list/get/create/delete, rule add/edit/delete within a ruleset, phase entrypoint get/update. Dual scope like logpush |
| ssl-certs | `product/ssl-certs` | ssl_certs.go | codex (gpt-5.6-terra, high) | zone cert packs list/order, origin CA cert create/list/get/revoke (root-scoped /certificates), mtls cert list/upload |
| waiting-room | `product/waiting-room` | waiting_room.go | grok | waiting room CRUD, events CRUD, rules, status/preview |
| spectrum | `product/spectrum` | spectrum.go | grok | spectrum app CRUD (zone scope) |
| web-analytics | `product/web-analytics` | web_analytics.go | grok | RUM site CRUD (account scope: /rum paths), rules |

## Rules (unchanged from wave 2, plus sub-shard mechanics)

- Worktrees from repo root: `git worktree add ../../worktrees/<shard> -b
  product/<shard> origin/main`; spawn each implementer with `--cwd` there.
- Unset GOROOT/GOBIN in implementer envs.
- Local branches only. No pushes, no PRs. Deliverable = committed branch +
  seal + buz.
- Standalone shards (rulesets, ssl-certs, waiting-room, spectrum,
  web-analytics): own file + one root.go AddCommand line, as in wave 2.
- Sub-shards (workers-*, access-*): own file + ONE line in the group file
  (workers.go / access.go) AddCommand list. Never touch root.go. Reject
  any sub-shard branch that edits root.go or a sibling's file.
- Kernel files off limits; `make check` on current main green before
  handoff (includes fmt-check); max 2 rework rounds then reassign or
  escalate.
- Approvals/blockers/summary to the maintainer (apiary-waggle-mso8zefe-1)
  via buz: {product, branch, verdict, review_notes}.
- Assignments in docs/orchestration/wave-3-assignments.json (local only,
  never commit). Reconcile from `hive fleet --json` every cycle.

## Review emphasis for this wave

- Sub-shard boundary discipline is the new risk: check every workers-*/
  access-* diff touches only its own file + its one group-file line.
- workers-scripts upload is the hardest single command in the project so
  far (multipart module rules, main-module metadata). Insist on dry-run
  fixtures proving exact wire format against the spec.
- rulesets: enforce the API's versioned-ruleset semantics honestly (rule
  edits create new versions); no invented flags for things the API cannot
  do.
