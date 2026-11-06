# 0023: Write-path semantics (motoshop fixes, wave R1)

Status: accepted (2026-11-07)

## Context
Building a full app (motoshop, 2026-11-06) showed write-path behaviour that
was surprising or unsafe: a trait default allowed `transfer` although the
type's policy denied it; beforeWrite flows skipped transitions and flow
writes; `deleted` event flows never ran; guards failed open; aggregate
recomputes caused version conflicts. Decisions with the user (2026-11-07):
aggregates keep the version, and an action's own writes are authorized by
the action's rule.

## Decision
- **Policies:** a type's own policy decides every operation it defines;
  trait policies (including built-in defaults such as Ownable `transfer`)
  apply only to operations the type's policy leaves out.
- **Actions:** writes an action makes to the feature it runs on (its
  transition, or its flow's updates of `feature.id`) are authorized by
  `actions.<name>`, not by `update`. Field rules (`readOnly`, `x-write`)
  still apply; writes to other features still need their rules.
- **No-op updates:** values equal to the current ones are dropped before
  any check; an update that changes nothing needs only read access and
  succeeds, and the run's effects are still delivered.
- **beforeWrite flows** run for every write, including transitions and
  writes made by flows; the writes they stage do not trigger beforeWrite
  flows again. Staged writes of a flow with `runAs: system` are made as
  system. A staged `feature.update` of the feature being written (even
  one being created) merges into that write.
- **Events:** event runs get `before` and `after` (also in beforeWrite
  nodes). Runs for `deleted` and `purged` events use the feature's last
  state as `feature`. Moves recompute aggregates on the old and the new
  parent.
- **Guards:** a trigger `when` that fails to evaluate records a failed run
  (`when_failed`) instead of being skipped; an aggregate `where` that fails
  fails the recompute. Decimal `==`/`!=` with nil is false, and decimals
  compare with their wire strings.
- **Aggregates** are written with `KeepVersion`: merged into the stored row
  without a version check or bump; their events carry version 0 so clients
  apply them. Ordinary updates of types with aggregates preserve the stored
  aggregate values, so neither write undoes the other.
- **Inputs** of action, webhook, and subflow runs are converted to their
  schema's types (integer → int64, date-time → time) however they were
  decoded.
- **Flow graphs:** only data dependencies (`nodes.<name>` references) pass
  a skip on; `edges` only order.

## Consequences
- Apps that relied on a trait default to widen a type's own rule must say
  so in the type's policy.
- A client PATCH that repeats current values no longer fails for readers.
- Aggregate fields can change without a new ETag.
