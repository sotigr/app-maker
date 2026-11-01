# AGENTS.md

Instructions for AI agents working in this repository. There are two kinds
of tasks; identify which one you have.

1. **Building an application**: you write a *manifest* (JSON Schemas,
   policies, flow graphs, optional Python nodes, UI pages). You do not
   write Go or JavaScript.
2. **Changing the framework**: you change the Go server, CLI, SDK, or
   admin console.

Human-oriented documentation: [docs/usage](docs/usage/README.md). Read the
guide for the area you touch before editing.

## 1. Building an application

### Workflow

```sh
make build                              # bin/organon, bin/organond
bin/organon init <app>                       # or copy examples/tracker
# edit files, then after every change:
bin/organon validate <app>                   # must print "ok: ..."
bin/organon test <app>                       # must print "N passed, 0 failed"
bin/organon dev <app> --addr 127.0.0.1:8080  # optional: run and try it
```

Without a Go toolchain, use the image (`docker build -t organon .`) and
run the same commands as
`docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" -w /work organon organon validate <app>`
([Docker guide](docs/usage/10-docker.md)).

`organon validate` errors name the file and a JSON Pointer
(`flows/x.json#/nodes/a/in/id: ...`). Fix exactly that location and
re-run. Do not stop until validate and test pass.

### Where things go

| Need | File | Guide |
| --- | --- | --- |
| A kind of record | `types/<name>.json` (JSON Schema, `$id` `urn:app:feature:<name>/v1`) | [types](docs/usage/03-types.md) |
| Who may read/write | `policies/<name>.json` | [policies](docs/usage/04-policies.md) |
| Behaviour (validation, side effects, schedules, webhooks, business actions) | `flows/<name>.json` | [flows](docs/usage/05-flows.md) |
| Logic expressions cannot do | `nodes/<name>.py` + a `python` node | [code nodes](docs/usage/06-code-and-llm.md) |
| AI generation/classification | `llm.complete` node | [LLM](docs/usage/06-code-and-llm.md#llmcomplete) |
| Changing the shape of existing data | bump the type version + `migrations/<id>.json` | [migrations](docs/usage/08-testing-and-deploy.md#data-migrations) |
| Behaviour checks | `tests/<name>.test.json` | [tests](docs/usage/08-testing-and-deploy.md#manifest-tests) |
| Config values | `app.json` `settings` + `env/<env>.json`, read as `env.<key>` | [manifest](docs/usage/02-manifest.md) |
| End-user UI shell (title, home, nav, login) | `ui/app.json` | [UI](docs/usage/11-ui.md) |
| A screen of the end-user UI | `ui/pages/<name>.json` (component tree) | [UI](docs/usage/11-ui.md) |
| UI look | `ui/styles.css` (override `--o-*` variables, `o-*` classes), `ui/assets/**` | [UI](docs/usage/11-ui.md#styling) |
| UI in more languages | `ui/app.json` `languages` + `ui/i18n/<lang>.json` | [UI](docs/usage/11-ui.md#languages) |

### Rules that prevent most mistakes

- Strings in flow `in`, `when`, `result`, policies, guards, `x-computed`
  are **expressions**. String literals need inner quotes: `"'done'"`, not
  `"done"`.
- Every type: `"type": "object"`, `"additionalProperties": false`,
  `required` for mandatory fields. Use only allowed JSON Schema keywords
  (no `anyOf`, `allOf`, `if/then`, `patternProperties`, `multipleOf`).
- Add `"x-indexed": true` to every field you filter or sort on.
- Refs: `"type": "string", "x-kind": "ref", "x-ref": "<type ID>"`;
  nullable refs with `x-onDelete: "setNull"` need `"type": ["string","null"]`.
- Money and exact numbers: `"type": "string", "x-kind": "decimal"`.
- Hide data with `x-visibility` (`owner`, `private`), not by convention.
- Status fields: an `enum` with `x-stateMachine` and
  `"x-write": "action:transition"`; each transition is an action. Give each
  action a policy rule under `actions`.
- Writes in a flow happen through `feature.create/update/delete/move` nodes
  and commit together at the end; effects (`email.send`, `webhook.call`,
  `emit`) run after the commit. Python code cannot write; it returns data.
- `beforeWrite` flows may only use Pure, DataRead, DataWrite, and Control
  nodes; use `reject` to refuse a write.
- `runAs: "system"` requires `"privileged": true`; prefer running as the
  caller.
- Never put secrets in the manifest; use `${secret:<name>}`.
- Manifests use model aliases (`'default'`, `'fast'`), never vendor model
  IDs.
- Write at least one test per policy decision and per action, including a
  denied case (`"expect": {"error": "forbidden"}`).

### UI pages

- Run `bin/organon ui catalog` for the components, their props, and steps;
  do not invent props (unknown members are errors).
- In UI files, **text** props (`text`, `label`, `title`, `empty`, ...) are
  literal text with `{{ expr }}` interpolations; **expression** props
  (`value`, `visible`, `id`, `parent`, `params`, step inputs, query values)
  follow the flow rule: `"'done'"`. Field props are JSON Pointers
  (`"/status"`); `type`, `page`, `action`, `source` are plain names.
- Load data in the page's `data` (`feature`, `children`, `query`,
  `search`); components refer to sources by name. Rows (`table`, `list`,
  `board`) see `item`; steps see `result` of the previous step.
- Behaviour belongs in actions and flows: a button runs an `action` step.
  UI expressions are a subset (no `filter`/`map`, pipes, `hasRole`); hide
  controls with `can(data.x, 'update' | 'delete' | 'action:<name>' | 'create:<type>')`.
  Policies still protect the data.
- Give every route parameter a `params` entry, and every `link`/`navigate`
  the target page's route parameters.
- Add a `page` test step for each page restricted with `visible`.
- `bin/organon ui schema` writes JSON Schemas for `"$schema"` completion.
- Translations: write pages in the first of `languages`; a text is its own
  key in `ui/i18n/<lang>.json` `messages` (keep its `{{ }}` parts), use
  `"@key"` texts for long ones (their text goes in the first language's
  file), and translate field labels, enum values, and transition buttons
  under `types`. After adding texts run
  `bin/organon ui i18n <app> --lang <l>` and fill the empty entries.

### Minimal working examples

Copy patterns from [examples/tracker](examples/tracker):
`types/task.json` (all field features), `policies/task.json`,
`flows/complete-task.json` (action), `flows/reject-spam.json`
(beforeWrite), `flows/notify-assignee.json` (event),
`flows/daily-digest.json` (schedule, forEach), `flows/inbound-issue.json`
(webhook), `flows/summarize-project.json` (LLM), `flows/triage-task.json`
+ `nodes/triage.py` (Python), `tests/task-permissions.test.json`,
`ui/app.json`, `ui/pages/project.json` (sources, board, chart, tabs,
dialog with a create form, action button), `ui/pages/task.json` (update
form, transitions, menu, file preview, comments list), `ui/i18n/el.json`
(translations; `ui/i18n/en.json` for `@key` texts and labels),
`tests/ui-pages.test.json`.

The node catalog with inputs is in
[docs/usage/05-flows.md](docs/usage/05-flows.md#node-reference); the server
also lists it at `GET /v1/flows` (admins).

## 2. Changing the framework

### Before you start

- Read `notes/status.md` (current state, open issues), `notes/gotchas.md`
  (library quirks, commands), and the ADRs in `docs/adr/` for the area.
- The design document `docs/Schema-Driven Application Framework Design
  Document.md` is the source of truth; deviations need an ADR.
- Ask the user before decisions the spec leaves open; record the answer in
  `notes/decisions.md` and an ADR.

### Layout

- `internal/<context>/{api,domain,application,infrastructure,interfaces}`
  for contexts schema, content, access, query, change, flow, runtime,
  integration, manifest, ui. `internal/kernel` holds shared value types.
  `internal/app` wires everything. `platform/` holds technical pieces with
  no domain logic. `cmd/organond`, `cmd/organon` are the binaries.
- Layer rules are enforced by depguard (`make depguard` regenerates
  `.golangci.yml`): contexts talk only through `api` packages; domain code
  imports no infrastructure; application code may own goroutines.
- `sdk/ts` (client, React hooks, `ui` = the end-user renderer), `admin/`
  (React console incl. flow editor and UI builder, built into `admin/dist`
  and embedded), `web/` (the renderer app served at `/`, built into
  `web/dist` and embedded), `runner/python/organon` (Python runtime,
  embedded), `api/proto` (code-node gRPC).
- The UI component catalog lives in `internal/ui/domain/catalog.go`; a new
  component needs its catalog entry, its checks in `compile.go`, its
  renderer in `sdk/ts/ui/src/components/`, and a tracker usage. UI
  expression semantics are shared through `testdata/uiexpr/cases.json`.

### Checks before you finish

```sh
go build ./...
go test -race ./...           # all packages; e2e tests in test/e2e
golangci-lint run ./...       # must report 0 issues (gofmt included)
bin/organon test examples/tracker
make sdk                      # when the SDK, codegen, or tracker types change (runs the UI expression vectors)
make ui                       # when admin/src or sdk/ts/ui changes (commit admin/dist)
make web                      # when sdk/ts/ui or web/ changes (commit web/dist)
make proto                    # when api/proto changes (needs protoc plugins)
make docker                   # when the Dockerfile or runtime dependencies change (needs Docker)
```

- Golden view files: `go test ./internal/schema/domain -run TestViewsGolden -update`
  after changing tracker or system types; review the diff.
- New behaviour gets a test that names the spec section it covers.
- Match the surrounding code style and comment density.
- Update `notes/status.md` when you finish a piece of work.
- Commit only when asked; never commit `todo.md`.
