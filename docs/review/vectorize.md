# Review: product/vectorize — porcelain: vectorize

- **Implementer:** claude opus CL.9327 (wave 2; shard originally assigned
  to kimi k3, which never produced work due to the auth outage)
- **Coordinator approval:** CO.9dc4 (gpt-5.6-sol) at 139dced, after 1
  rework round (null-filter rejection, conditional topK bounds 100/50,
  discard-vs-error insert modes)
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 4bb7762, 2026-08-11

## What was checked

- **Spec conformance:** vectorize v2 index CRUD, `/insert`, `/upsert`
  (NDJSON via Request.ContentType — second consumer of the kernel
  primitive), `/query`, `/metadata_index/{create,delete,list}` all match
  the pinned spec.
- **Gate:** full make check green at 139dced; build/fmt/tests re-verified
  on main post-merge. 60 product tests.
- **Scope:** vectorize.go + vectorize_test.go + root.go wiring only.

## Not in scope (available via cf api vectorize)

`get_by_ids`, `delete_by_ids`, `/info`, `/list` vector enumeration.
