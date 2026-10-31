# Status

_Last updated: 2026-10-31_

## Current phase: 10 – PostgreSQL + Valkey (next; needs Docker running).

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

- **Phase 6** (M5 core) manifest + CLI → vertical slice complete:
  - `cmd/fw`: init (scaffold), validate, test (manifest tests §12.6), dev (SQLite+River, seed on fresh
    db, hot reload via app.Server), login (token in $XDG_CONFIG_HOME/fw/credentials.json), token create,
    seed (via API), runs list, export/import (NDJSON, ID remap), plan, apply (--plan hash | --auto-approve,
    --allow-breaking).
  - `internal/app`: RunManifestTests (fixtures, effect recording, HTTP fixtures), ApplySeed, Switch,
    Server (Activate swaps runtimes; Start prefers the last applied manifest outside dev), Deployer
    (/v1/deploy/plan|apply, GET /v1/deploy), MakePlan (compat per type, policies, flows, privileged,
    exposures, index builds), DeployStore (memory, sqlite `manifest_deploys`).
  - manifest loader: `ReadDir` + `LoadFiles` (bundles), modules/<name>/... supported.
  - tracker: tests/task-permissions.test.json (3 tests), seed/dev.json, owners can read tasks/comments.

- **Phase 7** admin console + visual flow editor (ADR 0002):
  - `admin/` React 19 + Vite 8 + @xyflow/react 12, built with **pnpm** (npm is not installed);
    `make ui`; `admin/dist` is committed and embedded (`admin/embed.go`, served at /admin with SPA
    fallback and CSP).
  - Pages: login, data (types, lists, create/update forms from views, transition + transfer buttons,
    children, delete), flows list, flow editor (palette from catalog, canvas with explicit (solid) +
    inferred (dashed) edges, inspector with typed inputs/map editor/when/onError/forEach body, flow
    properties, validate, save, auto layout, JSON view), runs, deploys.
  - Server: `POST /v1/flows/validate` (admins), `PUT /v1/dev/manifest/flows/{name}` (dev only; writes
    the file; fw dev hot reload activates it); `GET /v1/features/_/{id}` reads any type.
  - Screenshot: docs/img/flow-editor.png (taken with headless Brave via CDP; script idea in gotchas).

- **Phase 8** (M6 core) change log + streaming:
  - change/domain ChangeLog port + Cursor; change/infrastructure/memory Log (retention 24h, seq cursors).
  - change/application: Publisher (publish_events worker), Gateway (Connect/Conn/Sub; targets feature,
    children, query (one type, eq/in, scope, no sort); buffered gap-free subscribe; version-aware deltas
    limited to readable+requested paths; enter/leave; sseq; state-mode coalescing with x-stream maxRate;
    append overflow → reset; access re-check on grant/team/user events → error forbidden; suspension
    closes; Ephemeral writes (update policy, x-persist:false only, 30/s/feature, last values in snapshot);
    resume: expired cursor → reset + snapshot).
  - change/interfaces/http: SSE GET /v1/stream (?target=&id=&paths= or ?sub=base64url JSON; Last-Event-ID),
    WS /v1/ws (subscribe/unsubscribe/publish), POST /v1/ephemeral/{type}/{id}.
  - tracker project has `presence` (x-persist:false). e2e stream tests on memory + sqlite.

- **Phase 9** (M7 core) TS SDK + codegen:
  - `sdk/ts/client` (@fw/client): FwClient (request w/ CSRF or bearer, FwError), auth, Cache (normalized,
    version-aware applyDelta), get/create/update (optimistic + rollback)/delete/query/children/action/
    ephemeral, WS subscriptions with reconnect + resubscribe, watch/live helpers, TypeApi.
  - `sdk/ts/react` (@fw/react): useFeature, useQuery, useChildren, useAction, useSession.
  - `fw codegen ts [dir] --out --client-import`: one index.ts (types from owner views; private absent;
    enums, transitions, action input/output types, schemas, createClient).
  - Non-browser WS auth: subprotocol `fw.bearer.<token>` (server selects subprotocol `fw`).
  - Checks: `make sdk` (codegen + tsc 7 typecheck incl. example/usage.ts with @ts-expect-error cases);
    `node sdk/ts/smoke/smoke.ts <url> <login> <password>` against fw dev (live delta, optimistic rollback).
  - Not done: React demo UI for "two browsers" criterion; SSE fallback in the SDK.

### Next (phase 10): PostgreSQL + Valkey (needs `sudo systemctl start docker`)
- Postgres repository (§5.3 layout: data jsonb, ancestors uuid[] GIN, expression indexes CONCURRENTLY),
  riverpgxv5 jobs, Postgres credential/idempotency/deploy stores, repotest via testcontainers, EXPLAIN tests.
- Valkey Streams change log (64 partitions, XREAD), PostgreSQL LISTEN/NOTIFY change log, Valkey rate
  limiter + last-value store; replicated topology wiring; manifest-activated events across replicas.

## Phase roadmap

| Phase | Content | Spec milestone | State |
|---|---|---|---|
| 0 | Bootstrap | – | done |
| 1 | Kernel + Schema | M1 | done |
| 2 | Content, memory repo, conformance, in-process jobs | M2 | done |
| 3 | Access, HTTP API, query, health, config, manifest loader, fwd | M3 | done |
| 4 | SQLite repo + River (riversqlite) | part M4 | done |
| 5 | Flow engine, triggers, actions, effects | M8 | done |
| 6 | Manifest context + `fw` CLI → **vertical slice complete** | M5 | done (migrations deferred) |
| 7 | Admin console + visual flow editor (React Flow) | M9 part | done |
| 8 | Change log + gateway streaming (SSE/WS) | M6 | done (memory log only) |
| 9 | TS SDK + codegen | M7 | done (no React demo UI) |
| 10 | PostgreSQL + Valkey (needs Docker) | M4/M6 rest | next |
| 11 | Search + aggregates | M9 | todo |
| 12 | Code nodes (Python), LLM, blobs, OIDC/MFA, limits, observability | M10 (no Helm/OCI) | todo |

## Open issues / deferred
- **Data migrations** (§12.5 migrations/*.json, migrate_batch jobs, upcast on read) not implemented;
  `plan` lists migration files and treats their presence as permission for breaking changes. M5's
  "100,000-feature migration while serving" criterion is open.
- Manifest activation is in-process only (single replica); multi-replica `manifest-activated` events
  and the 30 s fallback poll belong with the change log (phase 8/10).
- `x-aggregate` fields compile but are not maintained (recompute_aggregate jobs, phase 11).
- `emit` node, code nodes, `llm.complete`: not registered yet (phase 12).
- River `dead_letter` run status is not recorded yet (failures are recorded as failed per attempt).
- Streaming resume replays nothing: an expired cursor sends reset, a valid one gets a fresh snapshot
  (clients treat both alike, §9.11). Gateway restart/multi-gateway tests need the Valkey log (phase 10).
- Rate limiting per principal/IP (§14.1) not yet (phase 12); login limiter exists.
- Query view for non-admins is the **public** query view (owner-tier fields not filterable).
- System principal is virtual (fixed ID `...0001`, no feature); root feature fixed ID `...0002`.
- Grants are children of their object (any type accepts grant children).
- Git: one commit per phase on main; user approved committing at each phase boundary without asking again (identity passed per commit: `git -c user.name=skp -c user.email=skp@archeiothiki.gr`).
