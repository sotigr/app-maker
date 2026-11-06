# Decisions

## Taken with the user (2026-10-31)

| Topic | Decision |
|---|---|
| Binaries | Follow the spec: `cmd/organond` (server, takes the manifest dir as input) + `cmd/organon` (CLI) |
| "Graph programming tool" | Flows are JSON DAG files (§10.4) **plus** a visual node-graph editor that reads/writes them |
| Ordering | Vertical slice first (deviates from §15.9.2, see ADR 0001), then remaining milestones |
| Toolchain | User installs Go (latest stable) and starts Docker |
| Module path | `organon` |
| Editor / admin tech | React + React Flow (xyflow), Vite build, embedded in `organond` via `go:embed` |
| Slice storage | Memory repo + SQLite repo with River (`riversqlite`) |
| Non-Go scope | TS SDK + codegen, Python code-node runner, admin console. Helm chart / OCI image excluded for now |
| Commits | One commit per phase; commit at each phase boundary without asking again (approved 2026-10-31) |

## ADRs (`docs/adr/`)

| # | Title | Summary |
|---|---|---|
| 0001 | Vertical slice ordering | Build a runnable tracker on memory+SQLite with flows before Postgres/streaming/SDK |
| 0002 | Visual flow editor | React Flow editor in the admin console; dev-only endpoint writes flow JSON to the manifest |
| 0003 | Libraries | Spec §15.8 list + `modernc.org/sqlite`; versions pinned when Go is installed |
| 0004 | Layer import rules | Domain may import other contexts' `api` (published language) and pure libs (expr, jsonschema, apd, uuid); tests exempt |
| 0005 | Integer wire format | `type: integer` = Int64 with JSON number wire; `x-kind: int64` on string = string wire |
| 0007 | Flow engine choices | inferred deps, actions served by Flow, flow commits skip before-write flows, webhook signature format, env secrets |
| 0008 | LLM providers | port in runtime/api; anthropic (official SDK, fallbacks, output_config), openai-compatible (HTTP), fake; aliases "<provider>:<model>"; keys as secret refs; organon dev → fake |
| 0009 | Code nodes | gRPC engine↔runner (codenode.proto), stdio JSON frames to the stdlib-only `organon` Python package, process isolation + egress proxy, uv venvs by lock hash, python node inputs from inputSchema |
| 0010 | Blob storage | intents as urn:sys:feature:blob features, BlobFS, signed PUT/GET URLs, Verify on write, commit/release jobs, thumbnails, hourly cleanup_blobs |
| 0011 | OIDC + MFA | PKCE RP with JWKS verify via egress; sealed state cookie; signup per provider (default off) + admin invite links; TOTP tickets verify/enroll; data key file |
| 0012 | Observability + audit | kernel.Tel facade, OTLP/HTTP exporters, route-named HTTP spans, job trace carriers, domain metrics; audit rows in commit tx + auth events, GET /v1/audit, 90-day expiry |
| 0013 | Data migrations | migrations/*.json validated; upcast in typeOf; migrate_batch chain per type with migration records; flow steps as system; repos accept forward type changes |
| 0014 | Docker image + startup deploys | one image (organon, organond, python3, uv); apps via /app mount or FROM; never-seen manifest dirs deploy at startup with breaking check; ORGANON_ALLOW_BREAKING, ORGANON_CREATE_DATA_KEY |
| 0015 | Name: Organon | module organon; binaries organon/organond; ORGANON_* env; @organon/* SDK; Python package organon; wire names renamed |
| 0016 | End-user UI from manifest | ui/app.json + ui/pages/*.json + styles.css + assets; runtime interpreter (@organon/ui, web/dist) at `/`; text vs expr props; expression subset checked in Go, evaluated in TS; builder in /admin via dev endpoints |
| 0006 | Relationship index from features | hasRole/groups read grant & team features via indexed queries; grants are children of their object |

## Phase 3 decisions (asked 2026-10-31)

| Topic | Decision |
|---|---|
| App `admin` role | Bypasses every policy check (superuser) and gets the admin audience; still cannot read `private` fields (server-only) |
| Response shape | Envelope `{id, type, parent, version, createdAt, updatedAt, data:{...}}`; SDK may flatten |
| Password users | Admin-created only (plus bootstrap admin and OIDC first-login provisioning); no self-signup |
| System type policies | Built-in defaults: users readable by authenticated, self/admin update; teams/orgs authenticated read, any authenticated create (owner), owner/admin update+delete; grants create/delete by holders of `admin` role on the object, readable by subject and object admins. Manifest policies for these types override the defaults |

## Smaller decisions taken while implementing (no user input needed)
- Anonymous callers get 401 instead of 403/404 on content endpoints (no existence leak).
- Create under an unreadable parent → 404 (never distinguishes missing and hidden).
- Login body returns the session token only with `"cli": true`; browsers get cookies only.
- Query view audience: system→system, app admin→admin, everyone else→public.
- `expand` results are returned in a separate `expanded` map keyed by ID (data keeps ref IDs).
- Move without a `move` policy = update on feature AND create on new parent.

## 2026-11-01 (phase 12b, code nodes)
- Runner transport: gRPC engine↔runner (Unix socket, api/proto/codenode/v1), stdio length-prefixed
  JSON frames runner↔Python worker; the `organon` Python package (was fwnode) is pure stdlib (no grpcio).
- Python deps: uv builds a venv per requirements.lock hash when uv is on PATH; nodes without
  requirements run on system python3; requirements without uv fail with a clear error.
- OIDC first login (2026-11-01): per-provider `signup` (default false) + optional `allowedDomains`; with
  signup off an admin issues a one-time invite link that binds the first OIDC login. Never match by email.
- Docker (2026-11-01): one framework image; apps via volume mount at /app or `FROM` + COPY; used for both
  development (`organon dev` in the container) and deployment (`organond`).
- Project name (2026-11-01): Organon. CLI `organon`, server `organond` (ADR 0015). The design document and
  older ADRs still say fw/fwd.

## 2026-11-01 (end-user UI, ADR 0016)
| Topic | Decision |
|---|---|
| Audience | End-user app served by organond; the admin console holds only the builder |
| Rendering | Runtime interpreter of JSON component trees (no code generation) |
| UI logic | Expression subset, type-checked by validate, evaluated by a TS interpreter |
| Styling | Base theme (CSS variables, `o-*` classes) + optional raw `ui/styles.css`; no token file, no custom components |
| Builder | Palette + outline tree, live preview with the real renderer, inspector, JSON view |
| Mount | Site root `/` |
| v1 components | Layout + text, data display, forms + actions, board + charts |
| File previews | `preview` component using the browser's viewers (no PDF.js); blob downloads of images/PDF/video/audio inline, sandboxed, frameable by the app only, with Range; thumbnails only for JPEG/PNG/GIF, falling back to the original until rendered |

## 2026-11-01 (admin switches, ADR 0017)
| Topic | Decision |
|---|---|
| Disabling admin accounts | Admin-role principals are refused at sign-in (sessions, API keys too), not downgraded; no bootstrap admin |
| Operations without admins | No exceptions (no admin API keys): deploy by restarting with a new manifest |
| Switches | `ORGANON_DISABLE_ADMIN_CONSOLE`, `ORGANON_DISABLE_ADMIN_ACCOUNTS` (YAML `admin.disableConsole`, `admin.disableAccounts`) |

## 2026-11-02 (UI translations, ADR 0018)
| Topic | Decision |
|---|---|
| Keys | Hybrid: the text is its own key; exactly `@key` is an explicit key (source text in the first language's file); per-page sections disambiguate; field/enum/transition labels per type |
| User preference | `locale` field on the system user type (owner-visible, owner-writable); saved choice > `?lang` (browser's remembered choice) > Accept-Language > first language |
| Scope | UI only; server messages, flow `reject` messages, and emails stay untranslated |
| Example | Tracker in English and Greek (`ui/i18n/en.json`, `ui/i18n/el.json`) |

## 2026-11-06 (app builder gaps; plan approved)
| Topic | Decision |
|---|---|
| Scope | All gaps of the shop-builder report, in phases A (secrets, share links, sequences, PDFs), B (aggregation, flow sources, calendar), C (smaller items) |
| Customer access | Share links only (no self-signup) |
| PDF | Pure-Go layout from JSON templates, embedded Noto fonts; no browser |
| Sequences | Gapless, assigned inside the commit transaction |
| Outbound secrets (ADR 0019) | Substituted by the engine in http.request headers/query and ctx.http auth; per-secret host allowlist in app.json; inbound verifiers organon/stripe/hmac-sha256 |
| Share links (ADR 0020) | x-shareable types get a `share` action; link principals (kind link) read the subtree, run listed actions (and that action's writes in the subtree), public view only; `/s/<token>` sets an HttpOnly cookie; `ui/app.json linkHome`; manifest tests `as: "link:<ref>"` |
| Documents (ADR 0022) | `templates/documents/*.json` blocks → `kernel.Document` → `platform/pdf` (fpdf + embedded Noto Sans, rsc.io/qr); `document.render` stores a PDF blob for a Blob field; `email.send` attachments + lang; SMTP sender (`email.*`, `ORGANON_EMAIL_*`) |
| Sequences (ADR 0021) | `x-sequence` {format, series expr, on create or action:<name>} on indexed, non-required string fields; numbers drawn in the repository transaction through `Batch.Finalize`; deleting leaves a hole (cancel instead) |

## 2026-11-07 (motoshop gaps; plan R1-R6 approved)
| Topic | Decision |
|---|---|
| Aggregates vs user writes (#23) | Aggregates keep the version (merged, no check); ordinary updates preserve stored aggregate fields |
| Update rule vs actions (#40) | An action's own writes to its feature are authorized by `actions.<name>`; field rules still apply |
| Staff vs links (#29) | New `x-visibility: member` (signed-in users and service accounts; not anonymous or links) |
| Unsigned webhooks (#51) | `verify.scheme: token` (URL secret), `allowIPs`, `get` handshake reply |
| Policy combination (#1) | A type's own policy decides ops it defines; traits only fill undefined ops (ADR 0023) |
