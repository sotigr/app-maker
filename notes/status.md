# Status

_Last updated: 2026-10-31_

## Current phase: 6 – Manifest context + `fw` CLI (next) → completes the vertical slice

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

- **Phase 4** (part M4) SQLite + River:
  - `content/infrastructure/sqlite`: tables `content_features` (data JSON + `kinds` descriptors for
    lossless decode), `content_feature_ancestors`; expression indexes `(json_extract(..), id)` per
    x-indexed field, partial on type; `likely()` on the type filter and `INDEXED BY` for sorts;
    `Explain()`; passes `repotest` (incl. 100 writers) + EXPLAIN test.
  - `platform/sqlitedb` (WAL, immediate tx, MaxOpenConns 1, exclusive flock → second replica refused).
  - `platform/jobs/river.go`: River on SQLite, single kind `fw`, InsertTx in the commit tx, unique
    keys deduped per batch, per-kind timeouts.
  - SQLite credential store, SQL idempotency store; `internal/app` wires driver `sqlite`.
  - e2e suite runs on both memory and sqlite; jobs verified to run via River.

- **Phase 5** (M8) flow engine:
  - flow/domain: Definition (5 triggers), NodeRegistry/NodeType/Runtime ports, Compile (all §10.14 rules,
    inferred deps from `nodes.X` refs, typed envs, forEach subgraphs), cron.
  - flow/application: Engine (levels run concurrently, when/skip, onError, retries, forEach, subflow,
    write set with read-through, single commit via content FlowAccess, conflict retry x3, causation
    + loop_stopped, run records), built-in nodes (expr, template, feature.get/query/children,
    feature.create/update/delete/move, email.send, webhook.call, http.request, if, switch, forEach,
    subflow, reject, fail), triggers (EventHook commit hook → run_flow jobs, BeforeWrite staging,
    InvokeAction incl. transition/transfer, Webhook HMAC, Schedules), Catalog for the editor.
  - integration: templates (templates/email/*.txt, "Subject:" line), deliver_email/deliver_webhook.
  - platform/egress (private/metadata blocking), periodic jobs (River + in-process).
  - content: FlowAccess port + before-write staged ops join the batch.
  - Tracker flows: completeTask (action), notifyAssignee (event), rejectSpam (beforeWrite),
    dailyDigest (schedule, forEach), inboundIssue (webhook). e2e tests on memory + sqlite.
  - ADR 0007 records the engine choices.

### Next (phase 6): manifest context + fw CLI
- `cmd/fw`: init, dev (SQLite + River + fake LLM, hot reload, seed), validate, plan, apply, test
  (manifest tests §12.6), seed, export/import, runs, login/token create.
- manifest: deploy records, plan/diff (compat checker per type, policy/flow changes, privileged flows),
  migrations (expand/migrate/contract, migrate_batch jobs).
- Tracker manifest tests in examples/tracker/tests.

## Phase roadmap

| Phase | Content | Spec milestone | State |
|---|---|---|---|
| 0 | Bootstrap | – | done |
| 1 | Kernel + Schema | M1 | done |
| 2 | Content, memory repo, conformance, in-process jobs | M2 | done |
| 3 | Access, HTTP API, query, health, config, manifest loader, fwd | M3 | done |
| 4 | SQLite repo + River (riversqlite) | part M4 | done |
| 5 | Flow engine, triggers, actions, effects | M8 | done |
| 6 | Manifest context + `fw` CLI → **vertical slice complete** | M5 | next |
| 7 | Admin console + visual flow editor (React Flow) | M9 part | todo |
| 8 | Change log + gateway streaming (SSE/WS) | M6 | todo |
| 9 | TS SDK + codegen | M7 | todo |
| 10 | PostgreSQL + Valkey (needs Docker) | M4/M6 rest | todo |
| 11 | Search + aggregates | M9 | todo |
| 12 | Code nodes (Python), LLM, blobs, OIDC/MFA, limits, observability | M10 (no Helm/OCI) | todo |

## Open issues / deferred
- `x-aggregate` fields compile but are not maintained (recompute_aggregate jobs, phase 11).
- `emit` node, code nodes, `llm.complete`: not registered yet (phase 12).
- River `dead_letter` run status is not recorded yet (failures are recorded as failed per attempt).
- publish_events jobs are consumed by a no-op handler until the change log (phase 8).
- Rate limiting per principal/IP (§14.1) not yet (phase 12); login limiter exists.
- Query view for non-admins is the **public** query view (owner-tier fields not filterable).
- System principal is virtual (fixed ID `...0001`, no feature); root feature fixed ID `...0002`.
- Grants are children of their object (any type accepts grant children).
- Git: one commit per phase on main; user approved committing at each phase boundary without asking again (identity passed per commit: `git -c user.name=skp -c user.email=skp@archeiothiki.gr`).
