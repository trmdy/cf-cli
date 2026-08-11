# cf — an open-source Cloudflare CLI

Cloudflare has no general-purpose CLI. `wrangler` covers Workers, `flarectl`
is minimal and abandoned. `cf` aims to cover **the entire Cloudflare API**
(3,200+ operations) with a fast, single-binary CLI.

```
cf auth login                          # store an API token
cf dns list --zone example.com         # porcelain: nice UX for common work
cf dns create www.example.com --type A --content 192.0.2.1 --proxied
cf api r2 buckets-list                 # plumbing: every endpoint, generated
cf api raw GET /zones                  # escape hatch: any path
```

## Design

Two layers (the `gh api` / `gh pr` split):

- **Plumbing (`cf api ...`)** — generated from [Cloudflare's published
  OpenAPI spec](https://github.com/cloudflare/api-schemas) by `tools/gen`.
  The generator compiles the 22 MB spec into a compact operation registry
  (`internal/registry/data/registry.json.gz`, embedded in the binary) and
  derives a uniform command taxonomy from URL paths. Full API coverage,
  correct by construction, regenerated in seconds when Cloudflare ships new
  endpoints.
- **Porcelain (`cf dns ...`)** — hand-written commands for real workflows,
  built per product on top of `internal/api`. DNS is the reference
  implementation; see [docs/STYLE.md](docs/STYLE.md) for the contract.

Everything supports `--dry-run` (prints the exact HTTP request, redacted,
without sending it), `--output json|yaml|table`, profiles
(`--profile`, `$CF_PROFILE`), and credential resolution with
flag > env (`$CLOUDFLARE_API_TOKEN`, `$CLOUDFLARE_ACCOUNT_ID`,
`$CLOUDFLARE_ZONE_ID`) > profile precedence.

## Offline correctness

Contract tests validate the embedded registry against the spec with no
network access: every generated command must map to a real endpoint with
exactly the spec's parameters, every spec operation must have a command, and
all command names must be unique.

```sh
make spec   # download the OpenAPI spec (not committed; 22 MB)
make gen    # regenerate the registry + docs/generated/products.md
make check  # vet + tests (incl. contract tests) + build
```

## Layout

```
cmd/cf/               entrypoint
internal/cli/         command tree; one file per porcelain product
internal/api/         HTTP client: envelope, pagination, dry-run
internal/config/      profiles, credential resolution
internal/output/      json/yaml/table rendering
internal/registry/    embedded generated operation index
tools/gen/            spec -> registry compiler + curated taxonomy mapping
docs/STYLE.md         the style contract for contributors
```

## Status

Phase 0 (kernel) complete: core plumbing, full generated API coverage,
DNS porcelain exemplar, offline validation harness. Next: fan out porcelain
development per product (see `docs/generated/products.md` for the shard
list).

Note: the `cf` binary name collides with the Cloud Foundry CLI. We know.
