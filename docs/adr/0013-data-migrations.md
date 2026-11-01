# 0013: Data migrations

Status: accepted (2026-11-01)

## Context
Spec 12.5: expand, migrate, contract. Each feature records the type
version it was written with; reads of older features are upcast in memory
by the migration's `set` expressions; migrate_batch jobs apply the change
with version checks, are resumable, report progress, and are no-ops when
re-run; migrations may name a privileged system flow instead of `set`.

## Decision
- `migrations/*.json`: `{id, type, from, to = from + 1, set | flow,
  batchSize (default 500, max 5000)}`, validated by `fw validate`: the
  type exists and is at least at `to`; one migration per starting version;
  `set` keys are top-level paths of the new type (or `"nil"` to drop an old
  field); expressions see the old document as `feature` (untyped, since
  only the current schema is known) plus `now()` and `uuid()`; flows must
  be privileged and run as system.
- Upcast: `Content.typeOf` (every load, query, children, and search path)
  applies consecutive `set` steps from the stored version, coerced to the
  new field types; a failing expression leaves the stored shape. The
  feature keeps its version; its next write persists the new type ID.
  Repositories accept a type ID change only to a newer version of the same
  base type.
- Background: workers call `StartMigrations` on start (so after every
  deploy). Per type and target version there is one `urn:sys:feature:migration`
  record (`key = <type>@<version>`, state running/done, processed,
  migrated, failed, first errors). A chain of `migrate_batch` jobs pages
  through the type's features by ID; each feature is brought to the current
  version (`set` steps through upcast, `flow` steps by running the flow as
  system, then bumping the type ID) and written without events, with a
  `migrated` audit row. Finished records are not restarted.
- Query predicates on new fields match stored data only once rewritten;
  reads are correct immediately.

## Consequences
- Progress is counted in processed features, not as a share of batches,
  because the total is unknown without a count query.
- Upcasting covers top-level fields; nested object changes need a flow.
- Rollback is a new manifest with an explicit reverse migration (spec).
