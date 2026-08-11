# Review: product/access-identity — porcelain: cf access identity

- **Implementer:** claude opus (wave 3)
- **Coordinator approval:** CO.3f1fb (gpt-5.6-sol) at 681db36, after 1
  rework round (revoke-sessions body semantics: devices in JSON body,
  explicit false preserved, omitted when unset)
- **Final review:** maintainer (Claude Fable)
- **Verdict:** squash-merged to main as 215e3ee, 2026-08-11. Completes
  the access group and all wave-3 implementation.

## What was checked

- **Sub-shard boundary:** access.go delta exactly one AddCommand line.
- **Spec conformance:** `/access/identity_providers`, `/access/groups`,
  `/access/service_tokens` (+ `/rotate`), `/access/users` + session
  revocation all match the pinned spec; canonical provider types
  validated.
- **Secret handling:** service-token create/rotate secrets are shown-once
  with an explicit warning — not retrievable later, correctly surfaced.
- **Gate:** full make check green at 681db36; build/fmt/tests re-verified
  on main post-merge.

## Not in scope (available via cf api access)

SCIM provider sync endpoints, SAML certificate detail, service-token
refresh, per-user detail views.
