# Review: product/queues — porcelain: queues

- **Implementer:** codex gpt-5.6-terra high (wave 2)
- **Coordinator approval:** CO.9dc4 (gpt-5.6-sol) at e4ac222, after 1
  rework round (name→ID resolution on all paths, JSON message values)
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 78a67e4, 2026-08-11

## What was checked

- **Spec conformance:** queue/consumer/message paths match
  `/accounts/{account_id}/queues...` in the pinned spec (CRUD, consumers,
  messages, messages/ack, messages/pull all real).
- **Gate:** gofmt (checked explicitly — approval predated the fmt-check
  gate update), vet, style lint, all tests, build green at e4ac222 and on
  main post-merge.
- **Scope:** queues.go + queues_test.go + root.go wiring only.
- **UX:** queue-scoped commands accept a queue *name* and resolve it to
  the resource ID — the same convention as zone name resolution.

## Accepted risk (documented by coordinator)

A queue name that is exactly 32 hex chars is treated as an ID and skips
lookup. Same accepted ambiguity as zone resolution; consistent trade-off.

## Not in scope (spec has more)

`messages/batch`, `peek`, `preview`, `purge`, `metrics` exist in the spec
and are reachable via `cf api queues ...`; porcelain covers the core
workflows per the brief. `cf queues purge` is a natural fast-follow.
