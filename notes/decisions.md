# Decisions

## Taken with the user (2026-10-31)

| Topic | Decision |
|---|---|
| Binaries | Follow the spec: `cmd/fwd` (server, takes the manifest dir as input) + `cmd/fw` (CLI) |
| "Graph programming tool" | Flows are JSON DAG files (§10.4) **plus** a visual node-graph editor that reads/writes them |
| Ordering | Vertical slice first (deviates from §15.9.2, see ADR 0001), then remaining milestones |
| Toolchain | User installs Go (latest stable) and starts Docker |
| Module path | `app-maker` |
| Editor / admin tech | React + React Flow (xyflow), Vite build, embedded in `fwd` via `go:embed` |
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
