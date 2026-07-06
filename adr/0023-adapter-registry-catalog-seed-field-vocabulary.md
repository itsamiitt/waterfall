# 23. Adapter registry, catalog seeding, and Field-vocabulary extension for the 200-provider rollout

Date: 2026-07-06

## Status

Accepted

## Context

The engine shipped with three real API-first adapters (`hunter`, `prospeo`, `twilio-lookup`) and
the running binaries wired *mock* providers; the `providers` catalog table shipped empty. The
`Closo_Enrichment_Architecture_200_Tools` spreadsheet defines a ~200-tool target across pipeline
layers L1–L10. Scaling from 3 to ~150 real adapters exposed three structural gaps:

1. **No discovery seam.** Adapters were wired by hand-editing a `[]provider.Adapter` slice in each
   binary (`cmd/enrichapi`, `cmd/enrichd`). At 150 providers this is 150 binary edits and a
   guaranteed drift source.
2. **Catalog↔adapter drift.** A provider exists twice — as executable Go (auth scheme + capabilities
   in the adapter) and as a `providers` row (auth descriptor + `capabilities` jsonb). Nothing kept
   the two in sync, and there was no seed to populate the empty table.
3. **Field vocabulary too narrow.** `internal/domain/field.go` enforced 18 canonical Fields via
   `Field.Valid()`, but the L6–L8 providers (firmographics/technographics/intent) produce fields
   (`company_revenue`, `technographics`, `intent_topics`, …) not in that set — and the router
   silently drops a capability whose Field is non-canonical. The Glossary (`docs/00 §7`) already
   *named* several of these (`naics`, `sic`, `technographics`, `intent_topics`, `funding_stage`),
   so the code was behind the single source of truth.

## Decision

- **Adapter registry as single source of truth** — `internal/provider/adapters/registry.go`. An
  append-only `[]Registered{Slug, New, Category, Status, Regions, DocsURL}` list. `All(client)`
  builds the engine's `[]provider.Adapter`; `Hosts()` builds the egress SSRF allow-list. Adding a
  provider is one appended entry plus its `<slug>.go` file — no `init()` magic, matching the
  "explicit over dynamic" style of `engine.New`/`router.New`. `Slug` MUST equal the adapter's
  `NameV` and prefix its `KeyPoolSelector` (`<slug>:default`); `TestRegistry_Invariants` enforces
  this and that every advertised Field is canonical.
- **Catalog is a projection of the code.** `cmd/providerseed` constructs each registered adapter and
  UPSERTs a `providers` row from its *introspected* descriptor (`NameV`/`BaseURL`/`Auth`/`Caps`) +
  registry metadata, via the in-package `providers.Seed` helper (keeps the unexported column
  encoding encapsulated). Because both the runtime slice and the catalog row derive from the same
  registry entry, they cannot disagree. New rows land `op_state='disabled'` (DB default); a re-seed
  refreshes only the integration descriptor and never clobbers operator lifecycle state
  (`status`/`op_state`/`compliance_review_status`).
- **Binaries wire the registry through the egress choke.** `cmd/enrichapi` replaces its mock slice
  with `adapters.All(provider.NewEgressClient(NewHostAllowList(adapters.Hosts()...), keys))`. Keys
  resolve from `PROVIDER_KEYS` (dev/self-host static map) or, in the full platform, the rotation
  engine's `LeaseResolver`. With no key, a call fails auth and the waterfall falls through — no
  fabricated data. `cmd/enrichd` stays an offline mock demo but now also enumerates the registry.
- **Field vocabulary extended doc-first** (`doc-consistency`). `docs/00 §7` is updated first, then
  `internal/domain/field.go` (constants + `canonicalFields` in lockstep): the code catches up to the
  Glossary's already-named fields and adds `company_revenue`, `company_founded_year`,
  `company_hq_country`, `company_hq_city`, `company_type`, `company_linkedin_url`, `company_phone`,
  `duns_number`, `intent_score`, `buying_signal`. `technographics`/`intent_topics` are inherently
  multi-valued but stored as a **single normalized Observation value** (sorted, deduped,
  comma-joined), so the one-value-per-Field `field_versions` model (ADR-0006, `docs/06`) is
  unchanged — no migration.

## Options considered

- **Registry vs. dynamic reflection/codegen.** A reflective registry (build adapters from catalog
  rows at runtime) was rejected: it inverts the trust direction (DB drives code), can't be
  type-checked, and the auth/decode logic is genuinely code, not data. The explicit append-only
  slice keeps compile-time safety and matches the codebase ethos.
- **Repeated-field schema change for multi-valued technographics/intent** vs. **single normalized
  value.** The normalized single value was chosen to avoid a `field_versions` schema change and keep
  G5 provenance one-row-per-field; a repeated-field model can be revisited if a consumer needs
  per-technology provenance.
- **Seed via the HTTP Provider-CRUD API** vs. **in-process `providers.Seed`.** The in-process helper
  was chosen for a bulk 150-row seed (no HTTP round-trips, no operator session) while still running
  under `PlatformTx` RLS and reusing the exact column encoding.

## Consequences

- Adding a provider = append one `Registered` entry + one `<slug>.go` + fixtures/tests; the seeder
  and engine wiring pick it up automatically, and the invariant test guards the contract.
- The `providers` catalog can be populated deterministically and idempotently from code
  (`go run ./cmd/providerseed`), with operator lifecycle state preserved across re-seeds.
- The canonical Field set grows from 18 to 33; because the change is additive and `Field.Valid`
  gates at the edges, existing behavior is unchanged. Any further L6–L8 field must still be added to
  `docs/00 §7` first.
- `EXCLUDED` providers (defunct / no-API / scraping / infra) are never registered and never seeded;
  they are recorded in `docs/03 §6` per ADR-0009.
