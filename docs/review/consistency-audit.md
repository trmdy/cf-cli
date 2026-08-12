# Cross-product consistency audit (post wave 4)

Method: full command-tree dump (370 porcelain leaves across 38 products),
style-linter pass, verb-vocabulary sweep, review-record reconciliation.
Verdict: the tree is coherent; no blocking inconsistencies. Divergences
below are documented as accepted (with rationale) or as conventions for
future contributions.

## Verified uniform

- Verb vocabulary: list/get/create/update/delete everywhere, plus
  domain actions (purge, upload, rotate, pause/resume, enable/disable).
- Destructive commands: confirm-on-TTY + --force, enforced by lint.
- Zone-scoped commands: --zone name-or-id via resolveZoneInteractive.
- Output: table for lists, JSON for details, --output/--query global.
- Dry-run: universal, including multipart bodies and documented
  read-before-write exceptions (named in help text).
- Updates against full-schema APIs: read-merge-write preserving unknown
  fields (turnstile convention, applied in 9 products).

## Accepted divergences (do not churn)

1. Singular vs plural sub-resource groups (`gateway policy rule` vs
   `devices posture rules`). Both read naturally; renaming ~10 groups
   would break nothing and help nothing. Convention going forward:
   SINGULAR resource nouns.
2. `rulesets rule edit` uses `edit` (not `update`) deliberately: ruleset
   rule mutations are version-creating, and the coordinator review kept
   the distinct verb to signal that. Documented in its help text.
3. `cache smart-tiered set --value on` predates the zones
   flag-per-setting convention. Single toggle, low traffic; kept.
   Convention going forward: flag-per-setting tables (zones pattern).
4. `alerting available-alerts` and `logpush datasets fields` are
   noun-form catalog reads; acceptable as read-only catalogs.

## Known non-user-facing debt (tracked, not fixed here)

- Per-product httptest CLI helpers are near-duplicates; hoist a shared
  helper when next touching two of them.
- The dns exemplar's unit-only test style predates the end-to-end
  pattern; new products should copy cache/queues tests instead.
