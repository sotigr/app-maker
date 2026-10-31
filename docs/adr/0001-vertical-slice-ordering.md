# 0001. Vertical slice before strict milestone order

Date: 2026-10-31 · Status: accepted (decided by the project owner)

## Context

Section 15.9.2 says to work milestone by milestone and not to start a milestone before the
previous one meets its criteria. In that order the flow engine (the part that turns an app
from "schemas only" into "schemas plus behavior graphs") arrives at milestone 8, after
PostgreSQL, streaming and the TypeScript SDK. The project owner's goal is that applications
are written with JSON Schemas and flow graphs only, so a runnable end-to-end app with flows
is the most useful early result.

## Decision

Build a vertical slice first, in this order:

1. Kernel and Schema (M1)
2. Content with the memory repository and conformance suite (M2)
3. Access, generic HTTP API, query (M3)
4. SQLite repository with River via `riversqlite` (part of M4)
5. Flow engine, triggers, actions, effects (M8)
6. Manifest context and `fw` CLI (M5): the slice is complete here

Then: admin console and visual flow editor, change log and streaming (M6), TypeScript SDK (M7),
PostgreSQL and Valkey (rest of M4 and M6), search and aggregates (M9), code nodes, LLM and
hardening (M10, without the Helm chart and OCI image).

Within each phase the per-context order of 15.9.3 still applies (domain, application with
memory adapters, infrastructure, interfaces), and each phase's spec acceptance criteria that
can be met with the adapters available at that point are tests.

## Consequences

- Acceptance criteria that need PostgreSQL (EXPLAIN index use, 1M-feature latency, multi-replica)
  are deferred to phase 10.
- `fw dev` can run the tracker example before PostgreSQL exists, matching section 12.3
  (SQLite + River + memory change log + fake LLM).
- The replicated topology cannot start until phase 10; until then only `single` is accepted.
