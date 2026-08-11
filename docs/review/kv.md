# Review: product/kv — porcelain: kv

- **Implementer:** grok (wave 1; shard reassigned from kimi k3 after the
  harness auth outage)
- **Coordinator approval:** CO.8a97 (gpt-5.6-sol) at 9bf1ea7, after 1
  rework round
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 324d951, 2026-08-11

## What was checked

- **Spec conformance:** all six paths under
  `/accounts/{account_id}/storage/kv/namespaces...` (namespaces, keys,
  values, bulk, bulk/delete) match the pinned spec.
- **Raw semantics (the shard that drove the kernel's DoRaw primitive):**
  single-key get writes exact bytes to stdout unless `--output json` is
  explicit; single-key put sends `text/plain` via DoRaw; per-key metadata
  is intentionally routed through bulk put (multipart out of scope),
  documented in help.
- **Gate:** vet, style lint, all tests (1,141-line test file), build green
  at the approved commit and on main post-merge.
- **Scope:** kv.go + kv_test.go + root.go wiring + the maintainer-authorized
  merge of main (for the raw primitive).

## Notes for the consistency pass (non-blocking)

- Namespace/key subcommand nesting (`cf kv namespace ...`, `cf kv key ...`)
  is a two-level pattern other products (r2 bucket/object) should mirror.
- Bulk file format examples live in help text; consider a `docs/kv.md`
  when docs get a proper site.
