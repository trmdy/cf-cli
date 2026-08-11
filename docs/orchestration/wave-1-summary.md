# Wave 1 retrospective (complete 2026-08-11)

Coordinator: CO.8a97 (codex gpt-5.6-sol xhigh). Wall clock measured from
first shard-bee spawn to landed commit.

| Product | Harness | Rework | Wall clock | Landed |
|---|---|---|---|---|
| cache | grok | 1 | 10m54s | 03634b5 |
| zones | claude opus | 0 | 17m08s | f4c25c2 |
| kv | grok (after 2 kimi crashes) | 1 | 22m21s | 324d951 |
| r2 | codex terra high | 2 | 39m30s | 565b8d1 |
| load-balancers | claude opus | 1 | 45m41s | 794e0b7 |

## What held

- No implementer touched kernel/tools/Makefile/CI across all 5 shards.
- Every branch passed gofmt, current-main `make check`, uncached tests,
  and boundary review; landed code compared against approved files.
- Escalation discipline: kv's raw-endpoint gap and r2's streaming need
  were escalated as kernel requests instead of hacked around.

## Patterns established (now conventions)

- Real-command-tree httptest via `--base-url` (cache).
- Settings tables with validated per-setting flags (zones).
- Two-level resource nesting `<product> <resource> <verb>` (kv, r2).
- Raw byte semantics: exact stdout bytes unless explicit `--output json`
  (kv).

## Open kernel debt

- Streaming raw primitive (io.Reader body, streamed response) — blocks
  `cf r2 object cp`.

## Incidents

- Kimi k3: startup crashes from unbound account, then expired auth;
  shards reassigned to grok. Harness usable again after operator re-auth.
- gofmt gate gap (caught by CI on stream, wave 2): make check now runs
  fmt-check; closed for all subsequent reviews.
