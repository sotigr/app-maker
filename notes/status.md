# Status

_Last updated: 2026-11-06_

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
  - platform: config (YAML + ORGANON_ env, topology checks), health probes on internal port,
    httpserver middleware. manifest: loader (app.json, `${env.*}`, modules, hash).
  - `cmd/organond` runs the tracker: `ORGANON_BOOTSTRAP_ADMIN_EMAIL=.. ORGANON_BOOTSTRAP_ADMIN_PASSWORD=.. ./bin/organond --manifest examples/tracker --dev`
  - Tests: `test/e2e` permission matrix through HTTP, no private-field leaks, protocol rules.

- **Phase 4** (part M4) SQLite + River:
  - `content/infrastructure/sqlite`: tables `content_features` (data JSON + `kinds` descriptors for
    lossless decode), `content_feature_ancestors`; expression indexes `(json_extract(..), id)` per
    x-indexed field, partial on type; `likely()` on the type filter and `INDEXED BY` for sorts;
    `Explain()`; passes `repotest` (incl. 100 writers) + EXPLAIN test.
  - `platform/sqlitedb` (WAL, immediate tx, MaxOpenConns 1, exclusive flock → second replica refused).
  - `platform/jobs/river.go`: River on SQLite, single kind `organon`, InsertTx in the commit tx, unique
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
  - `cmd/organon`: init (scaffold), validate, test (manifest tests §12.6), dev (SQLite+River, seed on fresh
    db, hot reload via app.Server), login (token in $XDG_CONFIG_HOME/organon/credentials.json), token create,
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
    the file; organon dev hot reload activates it); `GET /v1/features/_/{id}` reads any type.
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
  - `sdk/ts/client` (@organon/client): OrganonClient (request w/ CSRF or bearer, OrganonError), auth, Cache (normalized,
    version-aware applyDelta), get/create/update (optimistic + rollback)/delete/query/children/action/
    ephemeral, WS subscriptions with reconnect + resubscribe, watch/live helpers, TypeApi.
  - `sdk/ts/react` (@organon/react): useFeature, useQuery, useChildren, useAction, useSession.
  - `organon codegen ts [dir] --out --client-import`: one index.ts (types from owner views; private absent;
    enums, transitions, action input/output types, schemas, createClient).
  - Non-browser WS auth: subprotocol `organon.bearer.<token>` (server selects subprotocol `organon`).
  - Checks: `make sdk` (codegen + tsc 7 typecheck incl. example/usage.ts with @ts-expect-error cases);
    `node sdk/ts/smoke/smoke.ts <url> <login> <password>` against organon dev (live delta, optimistic rollback).
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
| 3 | Access, HTTP API, query, health, config, manifest loader, organond | M3 | done |
| 4 | SQLite repo + River (riversqlite) | part M4 | done |
| 5 | Flow engine, triggers, actions, effects | M8 | done |
| 6 | Manifest context + `organon` CLI → **vertical slice complete** | M5 | done (migrations deferred) |
| 7 | Admin console + visual flow editor (React Flow) | M9 part | done |
| 8 | Change log + gateway streaming (SSE/WS) | M6 | done (memory log only) |
| 9 | TS SDK + codegen | M7 | done (no React demo UI) |
| 10 | PostgreSQL + Valkey (needs Docker) | M4/M6 rest | next |
| 11 | Search + aggregates | M9 | done (Bleve in memory) |
| 12 | Code nodes (Python), LLM, blobs, OIDC/MFA, limits, observability | M10 (no Helm/OCI) | todo |

## Open issues / deferred
- Data migrations implemented (ADR 0013); M5's "100,000-feature migration while serving" not load-tested.
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
  ephemeral (≤5/s). Config `llm.provider`/`llm.providers` (ORGANON_LLM_PROVIDER); `organon dev --llm fake` default.
  Tracker: project summary + summaryDraft (ephemeral), flow summarize-project (action summarize).
  e2e test/e2e/llm_test.go.
- 12b code nodes DONE (ADR 0009): api/proto/codenode/v1 (+ `make proto`; plugins in ~/go/bin);
  runtime/api code port; runtime/application Runner + pools; runtime/interfaces/grpc server;
  runtime/infrastructure/grpcrunner client; runtime/infrastructure/process (Envs: bundles read-only,
  organon embedded from runner/python, uv venvs; Starter: sh ulimit + python -I -c bootstrap, egress
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
  (organon dev: .organon/blobs). Tracker task `attachment` (x-thumbnails [64]). TS SDK BlobRef + upload().
  e2e test/e2e/blobs_test.go. Gotcha: advancing the fake clock 25h expires sessions → log in again.
- 12d limits DONE: platform/httpserver RateLimit + MemoryLimiter (GCRA; per principal 600/min lowered
  by app.json requestsPerMinute, anonymous per IP 60/min, /v1/blobs/ exempt, Retry-After); config
  limits.requestsPerMinute/anonymousPerMinute, http.trustProxyHeaders; gateway MaxConnsPerUser 20
  (Connect returns error; WS connects before Accept); content checkShape (nesting 8, list 1000).
  e2e limits_test.go (startCfg helper). Memory limiter only → replicated topology needs Valkey (phase 10).
- 12e OIDC + MFA DONE (ADR 0011): access/domain TOTP + SecretSealer + OIDCProvider/ExternalClaims +
  Identity/TOTP/Ticket store methods (memory, sqlite, storetest suite); access/infrastructure/crypto
  Sealer (AES-GCM), oidc client; application mfa.go (finish → session|ticket, VerifyMFA, Begin/Confirm
  TOTP [with ticket], DisableTOTP) and oidc.go (OIDCStart/Callback, invites, signup rules); HTTP
  mfa.go endpoints; app/auth.go (checkAuth in compile, authConfig, dataKey); config secrets.dataKeyFile,
  http.publicURL (ORGANON_DATA_KEY_FILE, ORGANON_PUBLIC_URL); manifest AuthConfig typed OIDC/MFA. SDK auth
  helpers; admin Login handles MFA + OIDC buttons. e2e auth_test.go (fake IdP with RSA JWKS).
- 12f observability + audit DONE (ADR 0012): kernel/telemetry.go (Tel facade), kernel/audit.go;
  platform/observability (Setup, Tel, HTTP + Route, LogHandler); JobRequest.Trace + jobs traced();
  spans/metrics in engine, LLM router, runner, gateway; content Batch.Audit + AuditStore (memory, sqlite
  content_audit, repotest AuditLog) + audit.go (auditRecords, AppendAudit, Audit, ExpireAudit) + GET
  /v1/audit; auth audit events; config observability.*, audit.retentionDays. e2e audit_test.go.
- 12g DONE: `emit` Effect node + custom dotted event triggers (CustomEvent regexp) + emit_event job
  (HandleEmitEvent, causation continues → loop_stopped); Runtime.Causation(); kernel JobAttempt ctx set by
  both job adapters → final failed attempt recorded as dead_letter with retryArgs (private flowRun field);
  POST /v1/flows/runs/{id}/retry (admin) + `organon runs retry`; checkPython (python.allowedPackages vs lock
  files) at validate; Content.ReindexAll at worker start (Bleve in memory). Tests: emit_test,
  deadletter_test (memory only: River retry scheduling ~5 s/attempt), river_test, python_test,
  TestSearchReindexAfterRestart. Hardened e2e tests against aggregate-recompute version bumps (409 retry).
- Data migrations DONE (ADR 0013): content/domain/migration.go (CompileMigrations), Feature.Upcast,
  content/application/migrate.go (upcast in typeOf, StartMigrations at worker start, HandleMigrateBatch,
  migrateOne, persistMigrated, recordMigration), system type migration, Engine.RunForFeature, repos
  accept forward type IDs (sqlite UPDATE sets type_id). Tests: e2e migration_test.go (sqlite restart
  v2→v3), app TestMigrationValidation.
- Admin console: Audit page (#/audit, filters, paging), dead_letter retry button on Runs, migration
  progress on Deploys, Blob fields upload via file input (Api.upload) and render as links/thumbnails.
  admin has no tsconfig (never typechecked; vite build only).
- SDK SSE fallback DONE: ClientOptions.transport auto|ws|sse; auto switches after 2 WS errors (or no
  WebSocket); one fetch-streamed SSE per subscription (?sub=base64url JSON with sid), reconnect on
  reset/close with backoff, terminal on error events / 4xx. `node sdk/ts/smoke/smoke.ts <url> <login>
  <pw> [ws|sse]` passes both against organon dev.
- REMAINING (no Docker): React demo UI ("two browsers" M7 criterion); change-log events carrying trace
  context.
- RENAMED to Organon (ADR 0015): module organon, cmd/organon + cmd/organond, ORGANON_* env vars, @organon/* SDK,
  runner/python/organon, .organon/ dev dir. Older notes below were rewritten mechanically.
- Docker DONE (ADR 0014, NOT built yet: Docker daemon was down): Dockerfile, .dockerignore, `make docker`,
  examples/docker (app Dockerfile FROM image + compose with dev and app services), docs/usage/10-docker.md.
  Server.Start deploys never-seen manifest dirs (MakePlan breaking check; ORGANON_ALLOW_BREAKING); config
  ORGANON_CREATE_DATA_KEY, ORGANON_TRUST_PROXY_HEADERS. Image env defaults verified by running the static organond with
  the same ORGANON_* values. TODO when Docker runs: build the image, run organon dev + organond + compose, then remove this note.
- Usage docs DONE (2026-11-01): README.md, AGENTS.md (app-building + framework-change instructions for
  agents), docs/usage/01..09 (quickstart, manifest, types, policies, flows + node reference, code/LLM,
  HTTP API + SDK, testing/deploy/migrations, operations). Keep them in sync when behaviour changes
  (node catalog table, config keys, env vars, endpoints). Quickstart commands verified against organon dev.
  NEEDS DOCKER: Phase 10 (PostgreSQL, Valkey), S3 blob adapter, container isolation for code nodes.
  Environment facts: protoc at /usr/bin/protoc; python3 3.14.8; no grpcio, no uv, no buf; Docker down.

- OLD NEXT (kept for context): phase 12 parts that need no Docker (Python code nodes w/ process isolation, llm.complete with
  fake/Anthropic/OpenAI-compatible adapters, blobs on local fs + uploads, rate limits, OIDC/MFA,
  OpenTelemetry). Phase 10 still blocked on Docker.

## End-user UI (ADR 0016, 2026-11-01)
Plan: ~/.claude/plans/read-the-docs-then-reflective-valiant.md (milestones M1–M5).
- M1 DONE format + validation: manifest `UI` group (in hash; modules may add ui/pages only); `internal/ui/domain`
  (catalog.go = components/steps/prop kinds; compile.go = pages, routes, params, sources, query fields vs public
  query view, read/write paths vs owner views, actions, dialogs, state; expr.go = subset AST check + pipe/backtick
  scan + `{{ }}` templates; schema.go = JSON Schemas from the catalog); rt built-ins can/formatDate/formatNumber
  (type-check only); UI feature types map decimal + string-wire int64 to string. `organon ui catalog|schema`;
  plan lists `ui` diffs + `newPublicPages`. Tests: rule table with pointers, tracker compile, schemas accept tracker,
  shared vectors `testdata/uiexpr/cases.json` (Go half).
- M2 DONE runtime: `internal/ui/application` (Bundle per principal: pages filtered by principal-only `visible`,
  nav filtered, field metadata per audience incl. ref targets, action inputs), `internal/ui/interfaces/http`
  (GET /v1/ui, /v1/ui/catalog, /assets/styles.css, /assets/app/*, /assets/organon/* (web/dist), GET / SPA
  fallback with CSP; no ui → / redirects to /admin/); `POST /v1/permissions` (content `Service.Can`: policy +
  state-machine from-state + guard; unreadable → false; ≤100). `/assets/` exempt from rate limits.
  `sdk/ts/ui` (@organon/ui): expr.ts interpreter (vectors: `node sdk/ts/ui/test/expr.test.ts`), runtime/sources/
  steps/render, components layout|data|forms|charts, base.css; `web/` (Vite, `make web`, dist committed, embedded
  by `web/embed.go`). Tracker `ui/` (projects, project with board/chart/tabs/dialog, task, myTasks, styles.css).
  e2e `test/e2e/ui_test.go`. Browser-checked with headless Brave (act.mjs-style CDP script): login, create via
  dialog, board drag (allowed + refused), comments, transitions per user, live updates.
- M3 DONE (board + chart shipped with M2).
- M4 DONE builder: `internal/app/uieditor.go` (GET /v1/ui/source incl. all type metadata + dev flag, POST
  /v1/ui/validate {files: path→content|null}, dev-only PUT/DELETE ui/pages/{name} (rename via `from`), PUT ui/app,
  ui/styles, PUT/DELETE ui/assets/{path...}; every write validates the whole ui/ first → 422 with problems).
  Admin: `admin/src/uitree.ts`, `pages/UiBuilder.tsx` (palette, outline DnD, iframe preview with its own React root
  + @organon/ui editor frames, inspector from catalog prop kinds, steps/objects/map editors, page settings, live
  validation, JSON view, param pickers, "as" principal), `pages/UiPages.tsx` (list, app shell, styles + assets).
  Test `internal/app/uieditor_test.go`. Screenshot docs/img/ui-builder.png.
- M5 DONE: manifest test `page` step (`expect.visible`), tracker `tests/ui-pages.test.json`, `organon init` scaffolds
  ui/app.json + items/item pages + tests/ui.test.json, docs/usage/11-ui.md + README/02/07/08/10/quickstart, AGENTS.md
  (UI rules, examples, layout, make web), root README.
- File previews DONE: `preview` component (image/PDF iframe/video/audio, URL kept stable across live re-signing,
  falls back to the file link on error); `link` `download`; blob field type has url + thumbnails. Blob GET: inline for
  viewable types, `frame-ancestors 'self'`, http.ServeContent (Range) when the store is seekable; `Link` takes the
  content type and gives thumbnails only for JPEG/PNG/GIF; a missing thumbnail serves the original (no-store).
  Tracker attachment: video/* + audio/*, 16 MiB, Attachment card. e2e `TestBlobPreviews`.
- Admin switches DONE (ADR 0017): `ORGANON_DISABLE_ADMIN_CONSOLE` (no /admin routes; root without UI is 404, and
  /admin is 404 rather than the SPA), `ORGANON_DISABLE_ADMIN_ACCOUNTS` (`AuthConfig.NoAdmins`: Resolve refuses admin
  principals, BootstrapAdmin skipped, CreateUser refuses the admin role); organon dev honours both. e2e
  `TestAdminDisabled` (restart on the same SQLite DB), config `TestAdminSwitches`.
- Tracker theme: `examples/tracker/ui/styles.css` restyles the base (palette + dark variant, status badges by
  `o-badge-<value>`, board columns coloured by order, segmented tabs, sign-in page, motion with reduced-motion
  fallback, phone layout). Charts now draw at 1:1 scale (ResizeObserver width; `height` is pixels; bars max 72px).
- UI translations DONE (ADR 0018): `ui/app.json` `languages`, `ui/i18n/<lang>.json` (messages keyed by text or
  `@key`, per-page overrides, per-type field/enum/transition labels); compile checks in `internal/ui/domain/i18n.go`
  (same `{{ }}` multiset, stale/missing warnings, @key text type-checked at each use); renderer texts listed in
  `internal/ui/domain/texts.json` (kept in sync by `sdk/ts/ui/test/texts.test.ts`, run by `make sdk`);
  `organon ui i18n --lang <l> [--prune]`; bundle `i18n` (filtered to visible pages) + translated field metadata;
  user `locale` field → `Principal.Locale` → session; language switch in header/login, `<html lang dir>`, locale
  formatting, logical CSS properties. Tracker: en (labels, `@projects.intro`) + el. Not done: plurals, server
  messages, editing translations in the builder.
- Fixed: a page with a non-streamable query source (sorted, e.g. the tracker's projects list) re-queried every
  400 ms forever, because each reload's cache puts notified the reload listener. `Cache.put` now notifies only
  when the feature changed (version, pending, or content).
- App-builder gaps plan (approved 2026-11-06; plan in ~/.claude/plans, ADRs 0019-0024): phases A1 secrets, A2 share
  links, A3 sequences, A4 documents/PDF/SMTP/attachments, B1 query groups, B2 query flows + flow sources, B3 calendar,
  C small items, then examples/workshop.
- A1 DONE (ADR 0019): `${secret:x}` substituted by the engine in http.request header values and URL query
  (`flow/application/nodes.go` withSecrets/secretURL, `nodeRuntime.Secret`), only names literal in the node and only to
  `app.json secrets.<x>.hosts` (`flow/domain/secrets.go`); url.Error redacted; code nodes `secrets: [...]` +
  `ctx.http(..., auth=)` host call; webhook `trigger.verify` organon (t=,v1= + legacy sha256=) / stripe / hmac-sha256
  (`flow/domain/verify.go`); plan lists `secretHosts`. Tests: flow compile rules, TestVerifyWebhook, e2e
  TestSecretsInOutboundRequests, TestWebhookTimestampedSignature.
- A2 DONE (ADR 0020): `x-shareable` (schema/domain/fields.go, ShareDef), system type `shareLink`, default policy,
  `AuthService.Share`/`AuthenticateLink`/`linkPrincipal` (access/application/links.go), `Principal.Link` + kind link,
  `Authorizer.linkCheck` (+ `CheckRequest.InAction` set from the write context), Scope/Audience for links, middleware
  `Authorization: Link` + `organon_link` cookie with derived CSRF, `GET /s/{token}`, engine `Deps.Share`, app compile
  `shareProblems`, UI `linkHome` + share action metadata, renderer link start page, manifest tests `as: "link:<ref>"`.
  Tracker: task shareable (start), Share menu + dialog, `sharedTask` page. Tests: TestShareableKeyword,
  TestShareableActionsExist, UI linkHome rules, tracker test, e2e TestShareLinks; checked in headless Brave.
- A3 DONE (ADR 0021): `x-sequence` (schema/api/sequence.go format + SequenceDef, fields.go/views.go compile),
  `Batch.Finalize` + `NextFunc` (content/domain), sqlite `content_sequences` drawn in the tx, memory counters committed
  with the batch, pipeline `pendingSequences` (create or wc.Action) + `Feature.AssignSequence` (joins the pending event),
  `commit` split into `batch`/`finishCommit`; app compile checks `on: action:x`. Tracker: task `number` T-{n:04}.
  Tests: repotest Sequences (concurrency, failed commit), TestSequenceKeyword, TestFormatSequence, tracker test, e2e
  TestSequences (concurrent creates, on action).
- A4 DONE (ADR 0022): `flow/domain/document.go` (templates, checks, Evaluate with locale/time zone formatting),
  `kernel/document.go`, `platform/pdf` (fpdf, embedded Noto Sans + OFL, QR, wrapped tables with repeated headers,
  page numbers), node `document.render` → `Runtime.Document` → `app/documents.go` (images from blobs or templates/,
  `Blobs.StoreGenerated`), `email.send` attachments + lang (Delivery reads `Blobs.Bytes`), `SMTPSender` + config
  `email.*`, tzdata embedded, manifest-test `expect.feature` nested subset match. Tracker: `report` action
  (task-report.json, flows/task-report.json, report(.el).txt, menu item). Tests: platform/pdf TestRender,
  TestDocumentRules/Evaluate, TestSMTPSender (fake server), tracker test, e2e TestDocumentsAndAttachments.
  Phase A complete; next B1 (query groups).
- Motoshop gaps plan (approved 2026-11-07, 51 items, waves R1-R6 before phase B). R1 DONE (ADR 0023):
  `PolicySet.Deciding` (#1); `WriteContext.ActionFeature` skips the update rule for the action's own writes (#40);
  unchanged keys dropped, no-op updates need read only, effects of no-op runs enqueued (#22); decimal ==/!= nil-safe
  and string-aware (#33), `when` errors → failed run `when_failed` (runFlowArgs.WhenError), aggregate where errors
  fail the recompute; beforeWrite runs for flow writes/transitions (no SkipBeforeWrite in FlowPorts.Commit) (#13/#30),
  `Result.System` → `Op.AsSystem` (#31), staged updates of batch features merge (#6, `run.writing`); event jobs carry
  before/after/type/parent, deleted/purged runs use them (#32/#34); `DomainEvent.FromParent` + both parents
  recomputed (#5); `Mutation.KeepVersion/Patch/Preserve` + `WriteContext.KeepVersion` for aggregates (#23), Stage
  fills type when a version is given; `typedValue` input conversion (#14); `CompiledNode.DataDeps` skip rule (#17);
  `client.search()` + search source (#2); reported after R1: decimals of unknown static type (node outputs) dispatch
  at run time (#52, expression `dynamicPatcher`, operands checked alone because expr masks type errors under patched
  operators). Tests: internal/app/gaps_test.go (TestGap01/13/17/22/31/32/33), repotest
  MaintainedFields, e2e test/e2e/gaps_test.go (TestGap05And23, TestGap14), expression TestGap33DecimalEquality,
  TestGap52DecimalsOfUnknownType, app TestGap52DecimalsFromReads.
- R2 in progress: #38/#53 `parent` in read/update/delete/action rules (Authorizer.SetParentLoader ← content RefByID,
  loaded when a deciding rule's source mentions parent, cached per request; TestGap38). #15 `round(x, places)`,
  decimal `floor/ceil/abs` (kernel.Decimal Round/Floor/Ceil/Abs; expression `numericFunctions` with Validate hooks,
  decimal text in → text out for UI; expr.ts `decRound`; vectors). #42 document and UI `formatNumber` exact for
  decimals/decimal text. #4 `icontains`/`iprefix` (kernel.FoldText: lower + strip marks + final sigma; SQLite
  `organon_fold` registered in platform/sqlitedb; x/text/unicode/norm added to depguard pure libs; repotest cases).
  #29 `x-visibility: member` (ADR 0024): VisMember/AudMember between public and owner; Authorizer.Audience,
  query/gateway queryAudience, createAudience give member to signed-in non-owners; search member tier
  (SearchQuery.Members); UI query fields checked against the member view; golden views regenerated; manifest-test
  read steps check the caller's view (TestGap29).
  #35/#45 `x-write` lists and `role:<name>` (WriteRule Kind WriteRole/WriteAnyOf, ParseWriteRules, Unlocks,
  RolesFor; pipeline checkWritable checks roles via the hasRole binding on the feature or create parent;
  TestGap35And45). #18 `date()` accepts times (expression dateFunction wraps expr's date; TestGap18), date helpers
  documented. R2 DONE.
- R5 #50 DONE (ADR 0025, ahead of R3/R4 on request): `http.request` `rawBody`/`form` (requestBody, MaxHTTPBody 1 MiB,
  responseBody by content type + htmlindex charset; x/text/encoding/htmlindex added to depguard pure libs); validate
  rejects two bodies and secrets outside headers/url/form; `ctx.http(raw_body=, form=, auth=[...])` (engine codeAuth);
  manifest-test fixtures `text`, `headers`, `request{contentType, bodyContains, header}`. Tests: e2e TestHTTPRequestBodies,
  TestPythonHTTPRawBodyAndAuthList; app TestGap50HTTPFixtures; flow domain validation cases. #51 (unsigned webhooks) open.
  Known flake: e2e TestRequestLimits can count 601 under full -race load (bucket refill); passes alone.
- Bugfix (2026-11-07): live deltas carried Blob values without the signed url, so a file set while a page was open
  (e.g. a rendered PDF) did not preview until reload. Gateway `deliver` now calls `StreamReader.LinkBlobs` (content
  `linkBlob`, shared with reads). Test: e2e TestStreamingBlobURLs (upload and document.render, feature and children
  subscriptions).
- UI (2026-11-07): `preview` prop `display: inline|modal` (default inline); file links of viewable types (image,
  PDF, video, audio) open a modal viewer (`FileModal`/`FileView` in sdk/ts/ui components/data.tsx; Escape/backdrop
  close; modified clicks keep new-tab); text "Open in new tab"; tracker shared-task uses modal. Checked in headless
  Brave over CDP (inline PDF, modal opens on click without navigating, Escape closes).
- Open: no browser-automation tests in CI (checked by hand with headless Brave); preview "as" changes only
  `principal` in expressions (data is read as the admin); assets upload limited by the 1 MiB body limit.
- Admin console usability plan (approved 2026-11-07; 4 phases). Phase 1 DONE (ADR 0027): server account operations
  (access/application/admin.go, interfaces/http/admin.go; store APIKeysOf/DeleteAPIKey/PasswordOf/IdentitiesOf;
  `invite` users; AuthConfig.Roles), console kit (admin/src/ui: kit.tsx toasts/dialogs/confirm/field/badges,
  refs.tsx IdChip/RefPicker/RolePicker/useMeta, grants.tsx GrantList), People section (pages/people: users with
  add dialog, user page with roles/grants/security/keys/activity, teams with members, service accounts), Access
  section and dialogs on FeaturePage, SchemaForm inline errors and 409 dialog, router query strings and 404.
  Tests: e2e TestAdminAccountOperations, storetest "admin lookups"; browser walkthrough (headless Brave over CDP):
  add user (short-password error, generated password, sign-in works), grant via pickers, create/revoke key, team
  member, project Access. Next: phase 2 (data browser), 3 (runs/audit/deploys), 4 (editors, a11y, layout).
- Dictation DONE (2026-11-09, ADR 0028): ui/app.json `speech` {mode, maxSeconds, position} (compile.go speech(), AppSchema,
  catalog form field `speech` opt-out); bundle app.speech with `server` flag; runtime Transcriber port +
  infrastructure/speech (openai-compatible /audio/transcriptions, fake); config `speech` + ORGANON_SPEECH_*; UI endpoint
  POST /v1/ui/transcribe (1 MiB, signed-in/link, lang → primary subtag or caller's language); Permissions-Policy
  microphone=(self) on `/`. Renderer: speechtext.ts (speechLang, insertAt; Greek capitals keep accents), speech.ts
  (registry useDictationTarget, engines browser/server with silence stop, Brave/service fallback),
  components/speech.tsx MicDock (one shared button, Ctrl/Cmd+Shift+Space, Escape), client.transcribe. Tracker turns it
  on (slug opted out), Greek texts. Tests: speech adapter, config, UI compile, e2e TestSpeechTranscription,
  sdk/ts/ui/test/speech.test.ts (make sdk); headless Brave with fake media (lang=el, insertion, focus, shortcut,
  opt-out). Browser engine itself to be checked by hand in Chrome.
- Stale session cookie on page loads FIXED (2026-11-11): the access Middleware lets a GET/HEAD outside /v1/ with an
  expired or revoked session or link cookie go on as anonymous (cookies cleared), so the renderer shows sign-in instead
  of a 401 JSON document; API calls still get 401. e2e TestStaleSessionCookiePageLoad. Dictation: without an engine
  (browser mode in Firefox/Brave) the mic shows disabled with "Speech recognition is not available."; APP-BUILDER
  §18 has a self-hosted CPU Whisper (Speaches) compose example.
- Framework version 1.1.0 (2026-11-11): platform/buildinfo (Version const, Commit via ldflags or Go VCS info);
  `organon version`/`--version`, `organond --version`, version in the startup log; Makefile `docker` tags
  $(IMAGE):latest and :$(VERSION) with COMMIT/VERSION build args (OCI labels). APP-BUILDER: `organon version`,
  pinned tags, "since 1.1" on speech. Open from the agent's issue: framework range check, dev waiting on an invalid
  startup manifest.
