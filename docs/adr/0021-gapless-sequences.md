# 0021: Gapless sequence numbers

Status: accepted (2026-11-06)

## Context
Invoices in Greece (and the myDATA submission) need numbers that are
sequential per series without gaps. Apps could only approximate this with
a counter feature and retries on version conflicts, which leaves gaps
when a later step fails and conflicts under load. The user chose gapless
numbers assigned inside the commit (2026-11-06).

## Decision
- **Field keyword** on a top-level, indexed, non-required string field:
  `"x-sequence": {"format": "INV-{year}-{n:06}", "series": "<expr>", "on": "create" | "action:<name>"}`.
  - `format` is text with `{n}` or `{n:0W}` once, and optionally `{year}`,
    `{yy}`, `{month}`, `{day}` (commit time, UTC) and `{series}`.
  - `series` is an expression over the feature's fields (like
    `x-computed`) naming the counter, e.g. `"'INV-' + branch"`; the
    default is one counter per type and field. Series names are global
    to the app, so two fields may share one.
  - `on` numbers the feature when it is created (default) or when the
    named action (transition or flow action) runs, once.
  - The field is read-only to everyone, including the system principal's
    writes through the API.
- **Assigned in the repository transaction.** The pipeline records what to
  number; `Batch.Finalize` is called by the repository inside its commit
  transaction with a `next(series)` function, sets the values, and builds
  the batch's events, audit records, and jobs, so they carry the number.
  SQLite keeps `content_sequences(series, last)` and draws with
  `INSERT ... ON CONFLICT DO UPDATE ... RETURNING` in the same transaction;
  memory keeps counters under its lock and commits them with the batch. A
  commit that fails (conflict, constraint, crash) uses no number.

## Consequences
- Writes that draw from one series serialize on it (SQLite already has a
  single writer).
- Deleting a numbered feature leaves its number unused; for documents that
  must stay gapless, cancel them with a state instead of deleting.
- PostgreSQL will need the same counter table with row locks.
