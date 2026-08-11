# Review: product/ssl-certs — porcelain: ssl-certs

- **Implementer:** codex gpt-5.6-terra high (wave 3)
- **Coordinator approval:** CO.3f1fb (gpt-5.6-sol) at aa73351, after 2
  rework rounds
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 78d8889, 2026-08-11

## What was checked

- **Spec conformance:** `/zones/{zone_id}/ssl/certificate_packs` list +
  `/order`, root-scoped `/certificates` (Origin CA) create/list/get/revoke,
  `/accounts/{account_id}/mtls_certificates` list/upload — all verified
  against the pinned spec (pack path helper confirmed to target
  ssl/certificate_packs).
- **Dry-run honesty:** pack order performs a documented zone read for apex
  validation under --dry-run but never POSTs — the dns-exemplar convention
  applied to a mutating flow.
- **Origin CA hostname validation:** FQDN/wildcard shape enforced
  client-side.
- **Gate:** full make check green at aa73351; build/fmt/tests re-verified
  on main post-merge.
- **Scope:** ssl_certs.go + ssl_certs_test.go + root.go wiring only.

## Not in scope (available via cf api)

Cert pack quota, keyless SSL, custom certificates, client certificates,
total TLS.
