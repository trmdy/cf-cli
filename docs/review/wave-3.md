# Wave 3 porcelain review ledger

Coordinator: `cf-wave3-coordinator` (`CO.3f1fb`). This local ledger records
adversarial review findings and maintainer communication for the ten Wave 3
shards. The authoritative handoff is a local product branch; no GitHub PR or
branch push is used.

## Spectrum — `product/spectrum`

Initial verdict: changes required at `781d80f`, rework round 1.

- Blocking: list sends `per_page=100` without `page`, although the Spectrum
  endpoint documents `page` as required to use pagination parameters and to
  receive `result_info`. The shared paginator cannot simply be seeded with
  `page=1` because it treats an explicit page as caller-pinned. Implement and
  test a product-local transparent page loop, including exact two-page query
  propagation.
- Blocking: create and update do not enforce documented cross-field schema
  constraints: dynamic edge IPs require CNAME, static edge IPs require ADDRESS,
  Argo Smart Routing requires direct TCP/UDP, IP Firewall requires TCP, and
  `origin_port` requires a complete `origin_dns` configuration. Validate both
  fresh creates and post-merge update transitions before writes.
- Blocking: protocol and origin-port ranges do not enforce the API's equal-width
  rule, and `virtual_network_id` is accepted as any non-empty string despite its
  UUID schema. Add validation and invalid/boundary tests.
- Correctness claim: update says it preserves unknown API fields, but decoding
  into `spectrumApp` drops unknown keys before the body is rebuilt. Preserve the
  raw object safely or correct the claim and prove that every modeled mutable
  field survives replacement.

The initial branch is correctly limited to `internal/cli/root.go`,
`internal/cli/spectrum.go`, and `internal/cli/spectrum_test.go`. Both the
coordinator full gate and uncached Spectrum tests are green at `781d80f`, but
green tests do not cover the contract gaps above. Findings were sent by buz
`019ff108-f928-71c9-a1ef-57c44b35112c` and direct notification.

