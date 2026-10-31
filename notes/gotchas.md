# Gotchas and environment facts

## Environment (2026-10-31)
- Arch Linux (CachyOS). Go 1.27.3, golangci-lint 2.15.0 (v2 config format). Node, python3, sqlite3 present.
- Docker installed; daemon was not running at start (needed only from phase 10).
- `go.mod` was created by the user (`module app-maker`, `go 1.27.3`).

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
  - Unique jobs: `UniqueOpts{ByArgs: true}` + `river:"unique"` struct tags; a **duplicate unique key
    within one InsertMany batch is an error** → dedupe the batch first; across batches it is skipped.
  - Workers are typed by args type; framework uses ONE River kind `fw` whose args carry the real kind,
    and dispatches via `platform/jobs.Registry`.
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
