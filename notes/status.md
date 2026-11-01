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
| 11 | Search + aggregates | M9 | done (Bleve in memory) |
| 12 | Code nodes (Python), LLM, blobs, OIDC/MFA, limits, observability | M10 (no Helm/OCI) | todo |

## Open issues / deferred
- **Data migrations** (§12.5 migrations/*.json, migrate_batch jobs, upcast on read) not implemented;
  `plan` lists migration files and treats their presence as permission for breaking changes. M5's
  "100,000-feature migration while serving" criterion is open.
- Manifest activation is in-process only (single replica); multi-replica `manifest-activated` events
  and the 30 s fallback poll belong with the change log (phase 8/10).
- `x-aggregate` fields compile but are not maintained (recompute_aggregate jobs, phase 11).
- `emit` node: not registered yet. Code nodes: process isolation only (container/microvm need Docker).
- River `dead_letter` run status is not recorded yet (failures are recorded as failed per attempt).
- Streaming resume replays nothing: an expired cursor sends reset, a valid one gets a fresh snapshot
  (clients treat both alike, §9.11). Gateway restart/multi-gateway tests need the Valkey log (phase 10).
- Rate limiting per principal/IP (§14.1) not yet (phase 12); login limiter exists.
- Query view for non-admins is the **public** query view (owner-tier fields not filterable).
- System principal is virtual (fixed ID `...0001`, no feature); root feature fixed ID `...0002`.
- Grants are children of their object (any type accepts grant children).
- Git: one commit per phase on main; user approved committing at each phase boundary without asking again (identity passed per commit: `git -c user.name=skp -c user.email=skp@archeiothiki.gr`).

## Phase 11 progress
- 11a aggregates DONE: `content/application/aggregates.go` (AggregateHook enqueues recompute_aggregate per
  parent; worker recomputes count/sum/min/max/avg from live children, writes only on change, retries
  conflicts); e2e `test/e2e/aggregates_test.go` (20 concurrent creates, filtered doneCount, delete).
- River adapter no longer applies cross-batch unique keys (River treats running jobs as duplicates →
  lost updates); unique-key jobs are idempotent recomputes. In-process adapter still coalesces pending.
- 11b search DONE: query/api SearchIndex port; query/infrastructure/bleve (mem-only); content IndexHook →
  index_features jobs; HandleIndexFeatures writes one doc per tier (public, owner; private never);
  Service.Search re-checks + projects hits, highlights only readable fields; GET /v1/search?q=&type=.
  Tracker project gained owner-only searchable `notes`. e2e `test/e2e/search_test.go`.
- Gaps: Bleve index is in memory → empty after restart under SQLite (needs startup reindex or file
  index); no search UI in admin console; PostgreSQL FTS adapter with phase 10.

## Phase 12 progress
- 12a llm.complete DONE (ADR 0008): runtime/api LLM port; runtime/domain/llm (aliases, prices, cost);
  runtime/application Router (alias→provider, force provider, monthly budget in memory, call log);
  runtime/infrastructure/llm: Anthropic (anthropic-sdk-go v1.84, beta Messages + fallbacks "default",
  output_config.format with StrictSchema, no temperature), OpenAICompatible (HTTP, SSE), Fake.
  Engine: CompiledNode.OutputV validates outputs with outputSchema (retryable for External/Code);
  run node status gets `llm` call records; Runtime.LLM/Ephemeral/OutputSchema; streamTo → gateway
  ephemeral (≤5/s). Config `llm.provider`/`llm.providers` (FW_LLM_PROVIDER); `fw dev --llm fake` default.
  Tracker: project summary + summaryDraft (ephemeral), flow summarize-project (action summarize).
  e2e test/e2e/llm_test.go.
- 12b code nodes DONE (ADR 0009): api/proto/codenode/v1 (+ `make proto`; plugins in ~/go/bin);
  runtime/api code port; runtime/application Runner + pools; runtime/interfaces/grpc server;
  runtime/infrastructure/grpcrunner client; runtime/infrastructure/process (Envs: bundles read-only,
  fwnode embedded from runner/python, uv venvs; Starter: sh ulimit + python -I -c bootstrap, egress
  proxy per worker); platform/egress.Proxy; flow `python` node + host callbacks + ctx.llm; config
  `runner.*` (runner role now in defaults; private unix socket). Tracker: task `label`, nodes/triage.py
  + rules.py, flow triage-task (action triage). Tests: process_test (real python3), grpcrunner
  round trip incl. restart, e2e code_test. Fake LLM calls cost 0 (Free marker).
- Also fixed in 12b: (1) data race in flow engine execGraph (node goroutines wrote `states` while the
  scheduler read it; now per-level results merged after Wait) — present since phase 5; (2) flaky
  TestStreamingChildrenAndQuery: test helper `next` dropped messages of other subs (gateway serves a
  connection's subs in map order); wsClient now keeps a backlog.
- 12c blobs DONE (ADR 0010): system type blob (intents), integration BlobStore/BlobFS/BlobSigner/
  Thumbnail, integration/application Blobs (CreateUpload, Upload, Verify, Link, Download, Jobs hook,
  blob_state/blob_purge/cleanup_blobs), integration/interfaces/http (POST /v1/uploads, PUT/GET
  /v1/blobs/{id}); content Deps.Blobs (SetBlobs), verifyBlobs in prepareAll, link() on views,
  AuthorizeBlobField; access Handler.Signed prefixes; LimitBody exemptions; config blobs.driver/dir
  (fw dev: .fw/blobs). Tracker task `attachment` (x-thumbnails [64]). TS SDK BlobRef + upload().
  e2e test/e2e/blobs_test.go. Gotcha: advancing the fake clock 25h expires sessions → log in again.
- 12d limits DONE: platform/httpserver RateLimit + MemoryLimiter (GCRA; per principal 600/min lowered
  by app.json requestsPerMinute, anonymous per IP 60/min, /v1/blobs/ exempt, Retry-After); config
  limits.requestsPerMinute/anonymousPerMinute, http.trustProxyHeaders; gateway MaxConnsPerUser 20
  (Connect returns error; WS connects before Accept); content checkShape (nesting 8, list 1000).
  e2e limits_test.go (startCfg helper). Memory limiter only → replicated topology needs Valkey (phase 10).
- NEXT 12e: OIDC + MFA (TOTP); then OpenTelemetry + audit log, `emit` node, python.allowedPackages
  check, dead_letter status, S3 adapter (needs Docker/minio).
  Environment facts: protoc at /usr/bin/protoc; python3 3.14.8; no grpcio, no uv, no buf; Docker down.

- OLD NEXT (kept for context): phase 12 parts that need no Docker (Python code nodes w/ process isolation, llm.complete with
  fake/Anthropic/OpenAI-compatible adapters, blobs on local fs + uploads, rate limits, OIDC/MFA,
  OpenTelemetry). Phase 10 still blocked on Docker.
