# cf CLI style guide

This document is the contract for everyone (human or agent) adding commands.
The DNS commands in `internal/cli/dns.go` are the reference implementation —
copy their shape. CI enforces what it can; reviewers enforce the rest.

## Architecture

Two layers, never mixed:

1. **Plumbing** — `cf api <product> <operation>`. Generated from the OpenAPI
   spec by `tools/gen`. Never hand-edit generated behavior; fix the generator
   or `tools/gen/mapping.yaml` instead.
2. **Porcelain** — `cf <product> <verb>`. Hand-written UX for the workflows
   people actually use. Built on `internal/api`, one file per product in
   `internal/cli/`, wired into root.go.

## Command naming

- Products are kebab-case nouns: `dns`, `r2`, `zero-trust`.
- Verbs come from a fixed vocabulary: `list`, `get`, `create`, `update`,
  `delete`, plus domain actions (`purge`, `tail`, `deploy`) when they read
  naturally as commands.
- `list` prints a table by default; `get` prints JSON by default. Both honor
  `--output json|yaml|table`.

## Flags

- kebab-case, no abbreviations except established ones (`--ttl`).
- Zone-scoped porcelain takes `--zone` accepting a zone **name or ID**
  (resolve names via `resolveZoneID`). Account/zone IDs otherwise come from
  the global `--account-id`/`--zone-id` flags, env, or profile — never add
  per-command variants.
- Boolean flags default to false; use `cmd.Flags().Changed(...)` to
  distinguish "unset" from "false" when building PATCH bodies.

## Behavior

- Every command must work with `--dry-run`: print the request via
  `client.Dump` and exit successfully without network I/O.
- Destructive commands (delete, purge) confirm on a TTY and require
  `--force` otherwise.
- Errors go to stderr and are actionable: say what was missing and how to
  provide it. Never print a stack trace.
- List commands paginate transparently (use `DoAutoPaginate`).
- Output goes to `cmd.OutOrStdout()`; notes/prompts to stderr. Stdout must
  stay machine-parseable in json/yaml modes.

## Code

- One product per file (`internal/cli/<product>.go`), constructor per
  command (`new<Product><Verb>Cmd`), no init() magic.
- Table rendering: explicit column lists via `output.RenderTable`; truncate
  long cells with `output.Cell`.
- Tests are required for logic (request building, flag mapping, parsing).
  Use `httptest` for API interactions; never hit the real API in tests.
- `make check` (vet + style lint + tests + build) must pass. The style
  linter (`tools/lint`) walks the whole command tree and machine-enforces
  the rules above. Contract tests (`tools/gen/contract_test.go`) verify
  plumbing against the spec — run `make spec gen test` before submitting.
- Render API results through `g.renderResult` / `g.renderValue` so the
  global `--query` (jq) and `--output` flags work uniformly. Never print
  results with raw fmt/json directly.

## Definition of done for a product's porcelain

- The 3–8 workflows a real user needs, not a mirror of every endpoint.
- Help text with at least one realistic example per command.
- Tests for every command's request construction.
- No naming collisions with existing commands; consistent with this guide.
