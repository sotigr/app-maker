# Gotchas and environment facts

## Environment (2026-10-31)
- Arch Linux (CachyOS). Go 1.27.3, golangci-lint 2.15.0 (v2 config format). Node, python3, sqlite3 present.
- Docker installed; daemon was not running at start (needed only from phase 10).
- `go.mod` was created by the user (`module organon`, `go 1.27.3`).

## Libraries
- **expr v1.17.8**: typed envs via `github.com/expr-lang/expr/types` (`types.Map` is strict: unknown
  fields are compile errors; `types.Extra` key makes it open). Nil comparison on typed fields works
  (`dueAt != nil`). The nil literal's static type is `interface{}`; detect it with
  `p.Node().Nature().Nil`. Operator overloading (`expr.Operator`) gives Decimal arithmetic;
  missing overload for (Decimal, float) = compile error, as spec 10.8 wants. Built-in `now()` is
  disabled and replaced by an env func so the Clock is injectable. Int results come back as `int`;
  `program.Eval` normalizes to int64.
- **jsonschema v6**: `$id` must not contain a fragment (`#`). View IDs are `<typeID>/views/<aud>.<dir>`.
  Error messages need `golang.org/x/text/message` printer → validator lives in
  `schema/infrastructure/jsonschema` (domain gets a `ValidatorFactory` port).
  Validate wire docs with numbers as `json.Number`.
- **River v0.50 + riversqlite + modernc.org/sqlite** work together (prototype verified):
  `sql.Open("sqlite", "file:x.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate")`,
  `db.SetMaxOpenConns(1)`, `rivermigrate.New(riversqlite.New(db), nil).Migrate(ctx, DirectionUp, nil)`.
  - With MaxOpenConns(1) **never touch the pool while a tx is open** (deadlock) — do everything via the tx.
  - `FetchPollInterval` must be >= `FetchCooldown` (100ms default).
  - River unique jobs MUST include `running` in ByState → a job inserted while the same key runs is
    skipped → lost updates for recompute jobs. Adapter therefore ignores UniqueKey across batches.
  - (old) Unique jobs: `UniqueOpts{ByArgs: true}` + `river:"unique"` struct tags; a **duplicate unique key
    within one InsertMany batch is an error** → dedupe the batch first; across batches it is skipped.
  - Workers are typed by args type; framework uses ONE River kind `organon` whose args carry the real kind,
    and dispatches via `platform/jobs.Registry`.
  - `git stash --include-untracked` + pop does NOT restore empty directories (lost examples/tracker/flows
    etc. once) → keep `.gitkeep` in empty manifest dirs; verify commits in a `git worktree` instead.
  - `pkill -f <pattern>` can kill the calling shell if the pattern is in the command line (exit 144).

## Design facts worth remembering
- `x-computed` and `x-aggregate.where` see top-level fields as bare variables (spec 4.3 example:
  `dueAt != nil && ...`), plus `feature` for computed. Guards/policies use `feature.x`.
- Expression built-ins (`now`, `isOwner`, `hasRole`, `inGroup`, `changed`, `uuid`) are declared in
  `rt.Env.Funcs` and their implementations passed in the vars map at Eval time under the same name.
- `additionalProperties` absent = treated as false (closed objects); present must be `false`.
- Visibility/write rules apply to top-level properties only; nested fields inherit them.
- Audience read rights: public→public; owner/admin→public+owner; system→all (private is server-only).
- Root feature: every feature except root has a parent; top-level app features are children of
  the root feature, so grants on root act as app-wide roles. (Decided in phase 1, used in phase 2/3.)
- Tests are excluded from depguard (ADR 0004 addendum).

## Commands
- `make test` → `go test -race ./...`
- `make lint` → `golangci-lint run ./...`
- Golden views: `go test ./internal/schema/domain -run TestViewsGolden -update`
- Fuzz: `make fuzz` (10 min)
- `./scripts/gen-depguard.sh` regenerates `.golangci.yml` (do not edit it by hand)

## Frontend / browser checks
- npm is NOT installed; use **pnpm** (12.x). `cd admin && pnpm install && pnpm run build`.
- Visual check: `brave --headless=new --remote-debugging-port=9233 --user-data-dir=<tmp>` then a Node
  script using the built-in WebSocket against CDP (`/json/list` → Page.navigate, Runtime.evaluate to
  log in via fetch, Page.reload, Page.captureScreenshot). The user's own Brave may be running: never
  pkill brave broadly; kill by PID. In bash, `cmd & ` after `S=...&&` chains loses variables.

## Code nodes (phase 12b)
- `python -I` ignores PYTHONPATH and does not add the script dir: the worker bootstrap inserts the
  organon dir into sys.path from argv (`-I -c <bootstrap> <organon> <bundle> <entry>`).
- Bundle dirs are read-only (0555): t.TempDir cleanup fails unless `Envs.RemoveAll` chmods first.
- `sh -c 'ulimit ...'` adds PWD and SHLVL to the worker environment.
- Regenerate protobuf with `PATH=$PATH:$HOME/go/bin make proto` (protoc 36 at /usr/bin).
- The `organon` Python package keeps the protocol on dup'ed fds 0/1; user print() goes to stderr → runner log.
- Vite alias objects match a key as a prefix followed by "/": `@organon/ui/base.css?inline` fell through to the
  `@organon/ui` alias. Use anchored regex aliases (admin and web vite.config.js).
- CDP scripts that log in with fetch must clear cookies first (`Network.clearBrowserCookies`): with a session cookie
  present, POST /v1/auth/password needs the CSRF header, so a second login silently keeps the old user.
- Node type stripping (node sdk/ts/ui/test/expr.test.ts) rejects TS parameter properties; declare fields.

