# Status

_Last updated: 2026-10-31_

## Current phase: 4 – SQLite repository + River (next)

### Done
- **Phase 0** bootstrap: notes, ADRs 0001–0006, Makefile, depguard generator (`scripts/gen-depguard.sh`).
- **Phase 1** (M1) kernel + schema: profile checker, x-vocabulary, compiler, views + golden files,
  jsonschema validators, compatibility checker (§4.8 rows), 10-min fuzz: no panics.
- **Phase 2** (M2) content: Feature aggregate, memory repo, `repotest` conformance suite, in-process
  jobs, write pipeline (Execute, `$` refs, write rules, state machines + guards, computed, timestamps,
  owner, cascade delete, restrict/setNull, unique names, publish_events job), EnsureRoot.
- **Phase 3** (M3) access + HTTP + query:
  - access: policies (`policies/*.json` + built-in defaults for system types), roles hierarchy,
    Authorizer (Check/Audience/Scope/Bindings; admin bypass), policy→predicate translation (AST),
    relationship index read from grant/team features (ADR 0006), Argon2id, sessions + CSRF
    double-submit, API keys, login limiter, admin-only user creation, bootstrap admin.
  - query: parser + planner (field checks vs query view, capabilities, cost), HMAC cursors bound to
    the query; Content `RunQuery` (scope, post-filter when inexact, select, expand→`expanded` map).
  - HTTP: §6.2 endpoints (features CRUD, children, move, query, transactions, schema, auth),
    problem details, ETag/If-Match (428/409), idempotency keys, anonymous → 401.
  - platform: config (YAML + FW_ env, topology checks), health probes on internal port,
    httpserver middleware. manifest: loader (app.json, `${env.*}`, modules, hash).
  - `cmd/fwd` runs the tracker: `FW_BOOTSTRAP_ADMIN_EMAIL=.. FW_BOOTSTRAP_ADMIN_PASSWORD=.. ./bin/fwd --manifest examples/tracker --dev`
  - Tests: `test/e2e` permission matrix through HTTP, no private-field leaks, protocol rules.

### Next (phase 4)
- SQLite repository (§5.3 layout, JSON TEXT, expression indexes per x-indexed field,
  `feature_ancestors` table, exclusive file lock) passing `repotest`.
- River via `riversqlite` with transactional job insertion (platform/jobs River adapter), periodic
  jobs. SQLite credential store + idempotency store. Config driver `sqlite`.

## Phase roadmap

| Phase | Content | Spec milestone | State |
|---|---|---|---|
| 0 | Bootstrap | – | done |
| 1 | Kernel + Schema | M1 | done |
| 2 | Content, memory repo, conformance, in-process jobs | M2 | done |
| 3 | Access, HTTP API, query, health, config, manifest loader, fwd | M3 | done |
| 4 | SQLite repo + River (riversqlite) | part M4 | next |
| 5 | Flow engine, triggers, actions, effects | M8 | todo |
| 6 | Manifest context + `fw` CLI → **vertical slice complete** | M5 | todo |
| 7 | Admin console + visual flow editor (React Flow) | M9 part | todo |
| 8 | Change log + gateway streaming (SSE/WS) | M6 | todo |
| 9 | TS SDK + codegen | M7 | todo |
| 10 | PostgreSQL + Valkey (needs Docker) | M4/M6 rest | todo |
| 11 | Search + aggregates | M9 | todo |
| 12 | Code nodes (Python), LLM, blobs, OIDC/MFA, limits, observability | M10 (no Helm/OCI) | todo |

## Open issues / deferred
- `x-aggregate` fields compile but are not maintained (recompute_aggregate jobs, phase 11).
- Before-write flows: hook exists (`content/application/beforewrite.go`), runner in phase 5.
- Actions endpoint `POST /v1/actions/{type}/{id}/{action}` not yet served (phase 5); the pipeline
  already supports `WriteContext.Action` (unlocks x-write action fields, enforces transitions).
- publish_events jobs are consumed by a no-op handler until the change log (phase 8).
- Rate limiting per principal/IP (§14.1) not yet (phase 12); login limiter exists.
- Query view for non-admins is the **public** query view (owner-tier fields not filterable).
- System principal is virtual (fixed ID `...0001`, no feature); root feature fixed ID `...0002`.
- Grants are children of their object (any type accepts grant children).
- Nothing committed to git yet (ask user at phase boundaries).
