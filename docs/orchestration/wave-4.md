# Wave 4 — 13 shards, final porcelain wave

Coordinator brief. Read docs/orchestration/PROCESS.md first (roles, local
branch protocol, and the sub-shard boundary invariant — enforce its three
mechanical checks at every sub-shard handoff). Waves 1-3 retros in
wave-*-summary.md are binding precedent, especially:

- read-merge-write for partial updates against full-schema PUT/PATCH APIs
- deterministic multipart dry-run + DoStream live for big/non-JSON bodies
- resolveZoneInteractive for all zone-scoped commands
- validation before client construction; endpoint-local pagination where
  result metadata demands it

This is the FINAL porcelain wave: after it, remaining products stay on the
generated `cf api` layer by design. Scope discipline matters more than
coverage — porcelain is the 3-8 workflows a human actually runs.

## Shards and harness assignment

Group scaffolds `internal/cli/gateway.go` and `internal/cli/devices.go` are
on main. The dns-config shard adds one line to the existing
`internal/cli/dns.go` AddCommand list (same invariant).

| Shard | Branch | Group/file | Harness | Scope notes |
|---|---|---|---|---|
| gateway-policies | `product/gateway-policies` | gateway_policies.go → cf gateway | claude (opus) | Gateway rules CRUD (dns/http/l4 filtering policies, precedence, enabled toggle), Zero Trust lists CRUD + item add/remove |
| gateway-config | `product/gateway-config` | gateway_config.go → cf gateway | codex (gpt-5.6-terra, high) | locations CRUD, account gateway settings get/set (common toggles), gateway certificates list/activate |
| devices-fleet | `product/devices-fleet` | devices_fleet.go → cf devices | claude (opus) | device list/get/revoke, WARP settings profiles (default + custom policies) CRUD |
| devices-posture | `product/devices-posture` | devices_posture.go → cf devices | codex (gpt-5.6-terra, high) | posture rules CRUD, posture integrations CRUD |
| dlp | `product/dlp` | dlp.go | claude (opus) | profiles list/get, custom profile CRUD, predefined profile update, datasets list/create/upload basics, payload-log settings. STRICT scope: dlp has 93 ops; take the core ~25, document the rest as cf api |
| dex | `product/dex` | dex.go | grok | synthetic tests list/details, fleet status (devices/live), traceroute results — read-only analytics, invest in table output |
| ai-gateway | `product/ai-gateway` | ai_gateway.go | codex (gpt-5.6-terra, high) | gateway CRUD, logs list/get with filters, datasets basics |
| ai | `product/ai` | ai.go | grok | Workers AI: `cf ai run <model>` (-f fields or --data, render response readably), model list/search with table |
| addressing | `product/addressing` | addressing.go | codex (gpt-5.6-terra, high) | BYOIP prefix list/get + advertisement status/toggle, address maps CRUD + membership, LOA document upload/download (DoStream for download) |
| secondary-dns | `product/secondary-dns` | secondary_dns.go | codex (gpt-5.6-terra, high) | incoming/outgoing zone transfer config, peers CRUD, TSIGs CRUD, force-AXFR/notify actions |
| dns-config | `product/dns-config` | dns_config.go → cf dns | claude (opus) | dnssec get/enable/disable, zone DNS settings get/set, dns-firewall clusters CRUD (account scope). One line added to dns.go AddCommand |
| alerting | `product/alerting` | alerting.go | grok | notification policies CRUD, available-alerts catalog, webhook/pagerduty destination CRUD, policy test-fire if API supports |
| custom-hostnames | `product/custom-hostnames` | custom_hostnames.go | grok | custom hostname CRUD + SSL status view, fallback origin get/set |

## Rules (unchanged) + emphasis

- Worktrees from repo root, `--cwd` per bee, GOROOT/GOBIN unset, kimi
  unavailable.
- Local branches only; no pushes/PRs; `make check` on current main green
  before handoff; max 2 rework rounds; buz approvals to
  apiary-waggle-mso8zefe-1 as `{product, branch, verdict, review_notes}`.
- Sub-shard handoffs: run the three boundary-invariant checks BEFORE
  starting content review; bounce immediately on violation.
- dlp and gateway-policies are the taste-risk shards: reject invented
  constraints (verify every bound against the pinned spec) and reject
  endpoint mirroring beyond the scoped workflows.
- Destructive ops this wave include device revoke and dnssec disable —
  confirm + --force per STYLE.md, with accurate consequence wording.
- Track assignments in docs/orchestration/wave-4-assignments.json (local
  only). Reconcile from `hive fleet --json` every cycle.
- Wave summary at the end in the wave-3 format (incl. incident log and
  boundary-invariant effectiveness assessment).
