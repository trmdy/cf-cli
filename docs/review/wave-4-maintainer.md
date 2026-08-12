# Wave 4 — maintainer final review record

All 13 shards approved by CO.6c02 (gpt-5.6-sol xhigh); adversarial review
findings per shard are in the coordinator ledger (docs/review/wave-4.md).
Maintainer verification before landing:

- **Approval integrity:** 11/13 merged SHAs matched the structured approval
  records in the buz queue byte-for-byte; the remaining 2 (dlp@1f3e8e1,
  alerting@a253180) matched the coordinator's direct final-approval
  message. Branch heads == approved commits for all 13.
- **Scope scan:** every branch changed exactly 3 files (product file, test
  file, one registration line); zero kernel/tool/CI/prior-wave edits.
- **Merge train:** 13 serial squash-merges; predictable AddCommand
  collisions resolved in gateway.go (+2), devices.go (+2), dns.go (+1),
  root.go (+8) — confirmed by the coordinator's independent registration
  audit. Build + gofmt verified after every merge.
- **Assembled gate:** full make check (fmt-check, vet, style lint, all
  tests, build) green at 9dabf62 with GOROOT/GOBIN unset. CI green on
  push.

Landed commits: addressing 2ea9b30->, ai 3019d26->, ai-gateway 74c08cb->,
alerting a253180->, custom-hostnames f9fbdf0->, devices-fleet 7a8700a->,
devices-posture 91c2a02->, dex d590b70->, dlp 1f3e8e1->, dns-config
3255f2b->, gateway-config 6c03cc8->, gateway-policies 7be588e->,
secondary-dns 0dfacc6-> (approved-commit -> squashed onto main; final
assembled main 9dabf62 + review-record commit).

## Incidents this wave

- Host pressure crashed four Claude implementers and three coordinator
  gates mid-wave; coordinator revived the exact sessions and serialized
  gates. No repo damage; no duplicate spawns.
- ai took the only 2-round rework (request-body contract: non-null JSON
  object required before client construction).
- alerting recovered after hitting the round limit (reassignment per
  protocol).
