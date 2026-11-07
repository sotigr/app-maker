# Organon app builder guide (for agents)

You build applications with Organon by writing a **manifest**: a directory
of JSON files, plus optional Python files, CSS, and email templates. You do
not write Go, JavaScript, SQL, or HTML. From the manifest, Organon serves:

- a REST, WebSocket, and SSE API;
- storage, permissions, and background jobs;
- an end-user web UI at `/`;
- an admin console at `/admin`.

This guide lists everything you can use. §19 maps common `organon
validate` errors to fixes, and §20 is the checklist to finish with. When in
doubt, copy from the example app `tracker` (see [§21](#21-reference-app)).

---

## 1. How Organon thinks

- **Features.** All data is *features*: typed JSON documents in **one
  tree**. Each feature has an `id`, a `type`, an optional `parent`, a
  `version`, and `data`. Children live under parents, for example
  project → task → comment. Permissions are granted on subtrees.
- **Types** (`types/*.json`) are JSON Schemas. Organon derives from them:
  storage, validation, indexes, and, for each audience, a read view and
  write views. The API never returns a field the caller may not read.
- **Policies** (`policies/*.json`) give one boolean expression per
  operation. Anything not allowed is denied.
- **Flows** (`flows/*.json`) are graphs of nodes. They hold all behaviour:
  business actions, validation before writes, reactions to events,
  schedules, and webhooks. A flow's writes commit in one transaction.
  Effects (email, webhooks, events) are delivered after the commit.
- **UI** (`ui/`) is JSON component trees that a built-in renderer draws.
  Behaviour stays in flows; the UI calls actions.
- **Expressions** appear everywhere: policies, flows, computed fields,
  guards, and UI. They are type-checked against your schemas.

---

## 2. Tools and the work loop

Use the official Docker image **`sotig/organon`** from Docker Hub. It
contains the CLI `organon`, the server `organond`, Python 3, and `uv`, so
you need no Go toolchain. Pull it once, then run every command through it:

```sh
docker pull sotig/organon
alias organon='docker run --rm -it --user "$(id -u):$(id -g)" -v "$PWD:/work" -w /work -p 8080:8080 sotig/organon organon'
```

`--user` keeps the files that `organon` writes owned by you. The `-p` port
mapping matters only for `organon dev`; run it with `--addr :8080` so it
listens inside the container.

The image also contains the example app at
`/usr/share/organon/examples/tracker`. To try it without any files of your
own:

```sh
docker run --rm -it -p 8080:8080 sotig/organon organon dev /usr/share/organon/examples/tracker --addr :8080
```

To serve an app in production, use the same image with the manifest
mounted read-only at `/app` and a persistent volume at `/data`:

```sh
docker run -d -p 8080:8080 -v "$PWD/myapp:/app:ro" -v appdata:/data \
  -e ORGANON_ENV=prod -e ORGANON_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
  -e ORGANON_BOOTSTRAP_ADMIN_PASSWORD='<long password>' -e ORGANON_CURSOR_KEY='<long random string>' \
  sotig/organon
```

Alternatively, build an app image with `FROM sotig/organon` and
`COPY myapp /app`.

If the binaries are installed locally, `organon` and `organond` work the
same without Docker.

The loop is:

```sh
organon init myapp                 # scaffold: one type, policy, flow, email template, 2 UI pages, seed, tests
# edit files, then after EVERY change:
organon validate myapp             # must print "ok: N types, ..."; warnings are printed but allowed
organon test myapp                 # must print "N passed, 0 failed"
organon dev myapp --addr :8080     # optional: run it (SQLite, seed data, fake LLM, hot reload)
```

- `validate` errors name a file and a JSON Pointer, for example
  `flows/x.json#/nodes/a/in/id: unknown field ...`. Fix exactly that
  location and run it again. **Do not stop until validate and test both
  pass.**
- `organon dev`:
  - keeps its state in `myapp/.organon/`;
  - loads `seed/dev.json` into a fresh database;
  - reloads within a second of any file change;
  - signs the admin in as `admin@localhost` / `admin-password-dev`;
  - uses the fake LLM unless you pass `--llm anthropic`.
- Other CLI commands:

  | Command | Does |
  | --- | --- |
  | `organon ui catalog [--json]` | Lists UI components, props, and steps. |
  | `organon ui schema --out .organon/schema` | Writes JSON Schemas for editor completion. |
  | `organon ui i18n <app> --lang <l> [--prune]` | Scaffolds a translation file. |
  | `organon codegen ts <app> --out <dir>` | Generates a typed TypeScript client. |
  | `organon seed`, `export`, `import`, `runs list`, `runs retry` | Data and run operations. |
  | `organon token create` | Creates an API key. |
  | `organon plan` / `organon apply` | Deploys to a server. |

---

## 3. Manifest layout

```
myapp/
  app.json                   # name, version, auth, roles, limits, LLM aliases, settings      §4
  env/<env>.json             # values for ${env.*} per environment (dev, prod, ...)          §4
  types/*.json               # feature types (JSON Schema)                                   §5
  traits/*.json              # reusable field groups merged into types                       §5
  policies/*.json            # who may do what                                               §6
  flows/*.json               # behaviour                                                     §7
  nodes/*.py                 # Python for `python` nodes (+ optional requirements.lock)      §9
  templates/email/*.txt      # templates for email.send (<name>.<lang>.txt per language)     §11
  templates/documents/*.json # PDF templates for document.render                             §11
  migrations/*.json          # data migrations after breaking type changes                   §17
  seed/<env>.json            # users, features, grants for organon dev / organon seed        §15
  tests/*.test.json          # manifest tests                                                §16
  ui/app.json                # UI shell                                                      §13
  ui/pages/*.json            # UI pages                                                      §13
  ui/i18n/<lang>.json        # UI translations                                               §14
  ui/styles.css, ui/assets/**  # look, images, fonts                                         §13
  modules/<name>/...         # optional grouping: types, traits, policies, flows, nodes, ui/pages
```

---

## 4. app.json, environments, secrets

```json
{
  "app": "tracker",
  "version": "1.0.0",
  "framework": ">=1.0 <2.0",
  "auth": {
    "password": { "enabled": true, "minLength": 12 },
    "oidc": [ { "provider": "google", "clientId": "${env.oidc.clientId}", "clientSecret": "${secret:oidc-google}",
                "signup": false, "allowedDomains": ["example.com"] } ],
    "mfa": { "requiredForRoles": ["admin"] }
  },
  "roles": { "viewer": [], "editor": ["viewer"], "admin": ["editor"] },
  "limits": { "requestsPerMinute": 600, "maxDocumentBytes": 262144 },
  "llm": { "aliases": { "default": "${env.llm.default}" }, "monthlyBudgetUsd": 50 },
  "python": { "allowedPackages": ["httpx"] },
  "settings": { "supportEmail": "${env.supportEmail}" },
  "secrets": { "stripe": { "hosts": ["api.stripe.com"] } }
}
```

| Key | Use |
| --- | --- |
| `roles` | Role names and the roles each includes. `admin` is special: it bypasses policies, but not `private` fields. |
| `auth.password` | Password sign-in. Admins create users; there is no self-signup (give customers share links, §6). |
| `auth.oidc[]` | OIDC providers. `google` and `gitlab` have built-in issuers; others need `issuer`. With `signup: true`, users are created on first login (optionally only for `allowedDomains`). |
| `auth.mfa` | TOTP for `requiredForRoles`, or `requiredForAll: true`. |
| `limits` | May only lower the server's defaults. |
| `llm.aliases` | Alias → `"<provider>:<model>"`. Built-in aliases are `default`, `large`, and `fast`. Flows use aliases only. |
| `settings` | Free-form values; flows read them as `env.<key>`. |
| `secrets` | Per secret, the `hosts` outbound requests may send it to (`*.domain` allowed). Required for every secret used by `http.request` or `ctx.http` (§11). |
| `python.allowedPackages` | When set, every package in a `requirements.lock` must be listed. |

- `${env.a.b}` is replaced from `env/<env>.json`. A missing value is a
  validation error.
- `${secret:name}` is resolved by the server from `ORGANON_SECRET_NAME`.
  **Never put secrets in files.** Their values never reach flow
  expressions, run records, or Python.
- A secret used for outbound requests lists the hosts it may be sent to
  (see §11).

---

## 5. Types

One file per type, `types/<name>.json`:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "urn:app:feature:task/v1",
  "title": "Task",
  "type": "object",
  "x-traits": ["urn:sys:trait:ownable", "urn:sys:trait:timestamped"],
  "x-allowedChildren": ["urn:app:feature:comment"],
  "properties": {
    "title":    { "type": "string", "maxLength": 200, "x-indexed": true, "x-searchable": true },
    "status":   { "enum": ["todo", "in_progress", "done"], "default": "todo", "x-indexed": true,
                  "x-write": "action:transition",
                  "x-stateMachine": { "initial": "todo", "transitions": [
                    { "from": "todo", "to": "in_progress", "action": "start" },
                    { "from": "in_progress", "to": "done", "action": "complete", "guard": "principal.id == feature.assignee" },
                    { "from": "done", "to": "todo", "action": "reopen" } ] } },
    "assignee": { "type": ["string", "null"], "x-kind": "ref", "x-ref": "urn:sys:trait:principal", "x-onDelete": "setNull", "x-indexed": true },
    "estimate": { "type": "string", "x-kind": "decimal" },
    "dueAt":    { "type": "string", "format": "date-time", "x-indexed": true },
    "isLate":   { "type": "boolean", "readOnly": true, "x-computed": "dueAt != nil && status != 'done' && now() > dueAt" },
    "internalScore": { "type": "number", "x-visibility": "private" },
    "attachment": { "x-kind": "blob", "x-maxBytes": 16777216, "x-contentTypes": ["image/*", "application/pdf"], "x-thumbnails": [64] }
  },
  "required": ["title"],
  "additionalProperties": false
}
```

**Identity.**
- `$id` is `urn:app:feature:<name>/v<N>`. `<name>` is used in URLs and in
  the UI.
- Traits are `urn:app:trait:<name>/v<N>`, kept in `traits/`.
- In a module, IDs are `urn:app:<module>:feature:<name>/v1`.

**Allowed JSON Schema.**
- Allowed: `type`, `properties`, `required`, `items`, `enum`, `const`,
  `default`, `title`, `description`, `minimum`/`maximum`
  (`exclusive*` too), `minLength`/`maxLength`, `pattern`,
  `minItems`/`maxItems`, `uniqueItems`, `readOnly`, `writeOnly`, `$defs`,
  `$ref`.
- `format`: `date-time`, `date`, `email`, `uri`, and `uuid` are enforced.
- `oneOf` is allowed only as a tagged union: each branch is an object with
  a shared `const` discriminator.
- **Rejected:** `anyOf`, `allOf`, `not`, `if`/`then`/`else`,
  `patternProperties`, `dependent*`, `contains`, `prefixItems`,
  `multipleOf`, `min`/`maxProperties`, `examples`, `$comment`, and lists of
  lists.
- Every type must have `"type": "object"` and
  `"additionalProperties": false`.

**Value kinds:**

| Kind | Declare | Wire |
| --- | --- | --- |
| string, boolean, number, enum, object, list | plain JSON Schema | JSON |
| integer | `"type": "integer"` | number (`"x-kind": "int64"` + `"type": "string"` sends a string) |
| decimal (money, exact) | `"type": "string", "x-kind": "decimal"` | `"3.50"` |
| time | `"type": "string", "format": "date-time"` | RFC 3339 UTC |
| ref | `"type": "string", "x-kind": "ref", "x-ref": "<type or trait ID>"` | ID |
| blob (file) | `"x-kind": "blob"` | `{id, size, contentType}`; reads add `url`, `thumbnails` |
| json (opaque) | `"x-kind": "json"` | any JSON; not queryable |

A nullable field is `"type": ["string", "null"]`, or has `null` in its
`enum`.

**Type keywords:**

| Keyword | Meaning |
| --- | --- |
| `x-traits` | Merges traits. System traits: `urn:sys:trait:ownable` (`owner`, built-in `transfer` action), `urn:sys:trait:timestamped` (`createdAt`, `updatedAt`), `urn:sys:trait:taggable` (`tags`), `urn:sys:trait:principal` (target for refs to users and teams). |
| `x-allowedChildren` | Types that may be created under this type. |
| `x-uniqueName` | A field that must be unique among siblings, such as `"slug"`. |
| `x-retention` | How long soft-deleted features are kept, such as `"720h"`. |
| `x-shareable` | `{"actions": ["approveEstimate"], "maxDays": 30}`: adds the built-in `share` action, which creates expiring links for people without accounts (§6). |

**Field keywords:**

| Keyword | Meaning |
| --- | --- |
| `x-indexed` | Required on **every field you filter or sort on**, in queries, UI query sources, and policies. |
| `x-searchable` | Includes the field in full-text search (`GET /v1/search`, the UI `search` source). |
| `x-visibility` | `public` (default), `member` (any signed-in user or service account, never anonymous visitors or share links: staff-only data on shared records), `owner` (owners, admins, system), `private` (system only), `system`. |
| `x-write` | `any` (default), `owner`, `admin`, `system`, `action:<name>` (only that action may write it), or `role:<name>` (only callers with that role on the feature, or on the parent for creates; admins always). A list allows any of its rules: `["action:addLine", "action:receive"]` lets several actions own a field; `["role:frontdesk", "action:approve"]`. The policy must allow the write too. |
| `x-immutable` | Set on create, never changed. |
| `readOnly` / `writeOnly` | Never accepted from clients / never returned. |
| `x-ref`, `x-onDelete` | Ref target. On delete of the target: `restrict`, `setNull` (the field must be nullable), or `dangle`. |
| `x-computed` | Expression evaluated on every write; implies read-only. Sees the fields by name plus `now()`. |
| `x-sequence` | Gapless numbering (invoices, orders): `{"format": "INV-{year}-{n:06}", "series": "<expr>", "on": "create" \| "action:<name>"}`. Numbers are drawn inside the commit: sequential per series, no gaps or duplicates under concurrency, none used by failed writes. `format` has `{n}`/`{n:06}` once, plus `{year}` `{yy}` `{month}` `{day}` (UTC) and `{series}`; `series` is an expression over the fields (default: one per type and field), e.g. `"'INV-' + now().Format('2006')"` for a series per year; `on` numbers at create or once when the action runs. The field must be a string, `x-indexed`, not `required`; nobody can write it. Deleting leaves a hole, so cancel with a status instead. |
| `x-aggregate` | Kept up to date from children in the background: `{"op": "count"\|"sum"\|"min"\|"max"\|"avg", "of": "<child type ID>", "field": "/x", "where": "<expr>"}`. |
| `x-stateMachine` | On an enum. Each transition `{from, to, action, guard?}` becomes an action. Pair it with `"x-write": "action:transition"`. |
| `x-stream` | Live-update behaviour: `{"mode": "state"\|"append", "maxRate": n}`. |
| `x-persist: false` | Ephemeral: streamed to watchers, never stored (presence, typing, LLM drafts). |
| `x-maxBytes`, `x-contentTypes`, `x-thumbnails` | Blob size limit (default 25 MiB), allowed types (`image/*` patterns), thumbnail sizes (JPEG, PNG, and GIF only). |

**System types** always exist: `root`, `user`, `team` (`members`),
`organization`, `serviceAccount`, `grant` (`subject`, `role`, `object`),
`flowRun`, `blob`, `migration`, `shareLink`.
- `user` has `email` (owner-visible), `displayName`, `roles`, `status`,
  and `locale`.
- Refer to people with `x-ref: urn:sys:trait:principal`.

---

## 6. Policies

```json
{
  "type": "urn:app:feature:task",
  "read":   "hasRole(feature, 'viewer') || isOwner(feature)",
  "create": "hasRole(parent, 'editor')",
  "update": "hasRole(feature, 'editor') || principal.id == feature.assignee",
  "delete": "hasRole(feature, 'admin') || isOwner(feature)",
  "move":   "hasRole(feature, 'admin')",
  "actions": { "start": "hasRole(feature, 'editor')", "complete": "principal.id == feature.assignee", "reopen": "hasRole(feature, 'admin')" }
}
```

- **Variables:**
  - `principal` is `{id, kind, roles, groups}`; `kind` is one of `user`,
    `team`, `organization`, `service`, `system`, `anonymous`;
  - `feature`; `parent` (create: the new parent; move: the target; other
    operations: the feature's parent, e.g. `"update": "parent.status == 'draft'"`);
    `patch` (update); `input`
    (actions).
- **Functions:**
  - `hasRole(f, role)`: the role held app-wide, or through a grant on `f`
    or any ancestor, including roles that include it;
  - `isOwner(f)`: owns `f` directly or through a team, or `f` is the
    caller's own user;
  - `inGroup(id)`; `now()`.
- **How rules apply:**
  - Every action, including each state-machine transition and the
    built-in `transfer`, needs a rule under `actions`. Transitions must
    also pass their `guard`.
  - Read rules become query filters, so lists only return readable
    features. `validate` warns when a read rule cannot be translated.
  - Callers who cannot read a feature get `404`; anonymous callers get
    `401`.
  - A policy on a trait applies to every type with that trait, for the
    operations the type's own policy does not define (a type's rule
    wins: `"transfer": "false"` turns transfer off).
  - Writes an action makes to its own feature (its transition, or its
    flow's update of `feature.id`) are authorized by `actions.<name>`;
    `update` applies to direct edits and other features. Field rules
    still apply. Setting a field to its current value needs only read.
- **Grants** give a role on a subtree. A grant is a `grant` feature created
  under the object: `{"subject": "<user/team id>", "role": "editor"}`.
  Teams and organizations can hold grants and own features.
- **Share links** give people without accounts (customers) access to one
  record:
  1. Mark the type `"x-shareable": {"actions": ["approveEstimate"], "maxDays": 30}`;
     each action must be a transition or a flow action.
  2. Allow sharing in the policy: `"actions": {"share": "hasRole(feature, 'editor')"}`.
  3. Staff run `share` (input `expiresInDays` default 7, `actions` subset,
     `note`); the output has `url` (`/s/<token>`), `token`, `expiresAt`.
  4. Whoever opens the URL can read that feature and its subtree (public
     fields only; mark staff-only fields `x-visibility: member`), and run the listed actions on it, including the
     action's own writes (its transition, or its flow's updates of the
     feature and creates under it). Nothing else; app policies are not
     consulted for them.
  - In expressions they are `principal.kind == 'link'`, with
    `principal.object` = the shared feature.
  - Revoke by deleting the `shareLink` feature (a child of the shared
    feature). Expired links stop working by themselves.
  - Send links privately: anyone with the URL has the access.
- **Defaults for system types** when you write no policy for them:
  - signed-in users read users, teams, organizations, and the root;
  - users update themselves;
  - owners manage their teams and service accounts;
  - grants are managed by holders of `admin` on the granted feature.

---

## 7. Flows

```json
{
  "flow": "completeTask",
  "title": "Complete a task",
  "trigger": { "action": "complete", "on": "urn:app:feature:task" },
  "input":  { "type": "object", "properties": { "note": { "type": "string", "maxLength": 2000 } }, "additionalProperties": false },
  "output": { "type": "object", "properties": { "late": { "type": "boolean" } } },
  "nodes": {
    "late":    { "node": "expr", "in": { "expr": "feature.dueAt != nil && now() > feature.dueAt" } },
    "setDone": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/status": "'done'" } } },
    "comment": { "node": "feature.create", "when": "input.note != nil && input.note != ''",
                 "in": { "type": "'comment'", "parent": "feature.id", "data": { "body": "input.note" } } },
    "notify":  { "node": "email.send", "when": "nodes.late.out == true",
                 "in": { "to": "env.supportEmail", "template": "'late-task'", "vars": { "title": "feature.title" } } }
  },
  "result": { "late": "nodes.late.out" }
}
```

**Strings in `in`, `when`, and `result` are expressions.** Literal text
needs inner quotes: `"'done'"`. The order between nodes is inferred from
`nodes.<x>` references; `edges: [["a", "b"]]` adds an order explicitly.
Only `nodes.<x>` references pass a skip on: a node after a `reject` that
did not fire still runs, even with an edge from it.

**Triggers.** A flow has exactly one:

| Trigger | Shape | Runs |
| --- | --- | --- |
| action | `{"action": "complete", "on": "<type ID>"}` | `POST /v1/actions/<type>/<id>/complete`, synchronously, as the caller; returns `result`. |
| beforeWrite | `{"beforeWrite": ["create", "update"], "on": "<type ID>", "when": "..."}` | Inside the write. It may stage more writes or `reject` the write. Only Pure, DataRead, DataWrite, and Control nodes are allowed. |
| event | `{"event": ["created", "updated"], "on": "<type ID>", "when": "changed('/assignee')"}` | In the background after the commit. Events are `created`, `updated`, `moved`, `deleted`, `purged`, or custom dotted names raised by `emit`. |
| schedule | `{"schedule": "0 8 * * *"}` | Cron (UTC), with no caller. To write or read across the tree, use `runAs: system` and `privileged: true` (see `daily-digest.json`). |
| webhook | `{"webhook": "issues", "secret": "${secret:issues-webhook}", "verify": {"scheme": "stripe"}}` | `POST /v1/hooks/issues`; the JSON body is `input`. The secret is required. The default signature is `X-Organon-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<unix>.<body>">` within 5 minutes; `verify` selects another scheme (§11). Returns `202`; the run happens in the background. |

Trigger `when` sees `event`, `before`, `after`, `feature`, `op`,
`principal`, and `changed(path)`. A `when` that fails to evaluate records a
failed run (`when_failed`); it is never silently false.

- beforeWrite flows run for every write: API writes, transitions, and
  writes made by other flows. `runAs: system` makes their staged writes as
  system; `feature.update` of `feature.id` sets fields of the feature
  being written (also on create). Nodes see `before` and `after`.
- Event nodes see `before` and `after`; for `deleted`/`purged` events,
  `feature` is the last state. A move recomputes aggregates on both
  parents, and aggregate updates never cause version conflicts.
- Input takes its schema's types (integers compare as integers).

**Flow keys:**

| Key | Meaning |
| --- | --- |
| `flow` | Unique name. |
| `title` | Description. |
| `input`, `output` | JSON Schemas. `input` is also the form an action shows in the UI. |
| `result` | The run's output. |
| `runAs` | `trigger` (default: the caller) or `system`, which requires `"privileged": true`. Prefer running as the caller. |
| `timeoutMs` | Budget. Defaults and maximums: beforeWrite 2 s/5 s, action 10 s/30 s, others 60 s/15 min. |
| `trace: true` | Stores node inputs and outputs on the run. |

**Node options:**
- `when`: skip unless true. Nodes depending only on skipped nodes are
  skipped too.
- `onError: "<handler node>"`: the handler sees `nodes.<failed>.error`.
- `retry: {"max": n, "backoff": "exponential"|"fixed"}`: External and
  Code nodes only.
- `secrets: ["name"]`: Code nodes only; the secrets `ctx.http` may use.

**Variables in nodes:**
- `feature` (the trigger feature), `input`, `event`, `trigger`,
  `principal`, `env`;
- `nodes.<name>.out` and `nodes.<name>.error`;
- inside `forEach`: `item`, `index`, `outer`.

**Nodes** (`*` = required input):

| Node | Kind | Inputs | Does |
| --- | --- | --- | --- |
| `expr` | Pure | expr* | Evaluates an expression. |
| `template` | Pure | text*, vars | Renders a Go text/template. |
| `feature.get` | DataRead | id* | Reads a feature as the run's principal. |
| `feature.query` | DataRead | type*, where, sort, scope, limit | Runs a query document (§12). |
| `feature.children` | DataRead | id*, type, limit | Lists children. |
| `feature.create` | DataWrite | type*, parent, data | Stages a create. |
| `feature.update` | DataWrite | id*, set, version | `set` maps `"/field"` → expression. |
| `feature.delete` | DataWrite | id*, version | Soft delete. |
| `feature.move` | DataWrite | id*, parent*, version | Moves a subtree. |
| `email.send` | Effect | to*, template*, subject, vars, attachments, lang | Sends `templates/email/<template>.txt` after commit; `lang` picks `<template>.<lang>.txt`; `attachments` are file values or `{blob, name}` (10 MiB in all). |
| `webhook.call` | Effect | url*, payload, secret | POSTs signed JSON after commit. |
| `emit` | Effect | event*, feature*, data | Raises a custom event such as `task.reminded`, which can trigger event flows. |
| `document.render` | External | template*, vars, type*, field* | Renders `templates/documents/<template>.json` to PDF, stored for that Blob field; output is the file value for `feature.update` (§11). |
| `http.request` | External | url*, method, headers, one of body / rawBody / form | Calls an outside HTTP API (private networks are blocked; `body` is sent as JSON, `rawBody` as text exactly as given (XML), `form` URL-encoded; bodies and responses ≤ 1 MiB; 4xx is output, 5xx and 429 are retryable). Output `{status, body, headers}`; `body` is JSON for JSON responses, text otherwise. `${secret:x}` allowed in header values, the URL query, and `form` values (§11). |
| `llm.complete` | External | prompt, model, system, messages, maxTokens, temperature, streamTo | §10. |
| `python` | Code | from `inputSchema` | §9. |
| `if` | Control | cond* | Branch with `when: "nodes.<if>.out"`. |
| `switch` | Control | value*, cases | Output is the matching case or `"default"`. |
| `forEach` | Control | items* | Runs the `do: {nodes, result}` subgraph per item (`maxItems` default 100, max 1000). Output is a list of results. |
| `subflow` | Control | flow*, input | Runs another flow inline (depth ≤ 4). |
| `reject` | Control | code*, message, status | Aborts an action or beforeWrite with that error. |
| `fail` | Control | message | Fails the run. |

**Actions.** `POST /v1/actions/<type>/<id>/<name>` resolves, in order:
1. a flow with that action trigger;
2. the state-machine transition with that name (`409 invalid_transition`
   from the wrong state);
3. the generic `transition`, with input `{"to": "done"}` or
   `{"action": "start"}`;
4. `transfer` (Ownable types), with input `{"to": "<principal id>"}`.

The policy rule `actions.<name>` and the transition guard must both allow
it.

**Background behaviour.** Background runs retry up to 5 times, then are
dead-lettered (`organon runs retry <id>`). Loops are stopped: a flow that
re-triggers itself on the same feature, or a chain deeper than 8.

**Writes and effects.** Flows write only through `feature.*` nodes, and
the writes commit together at the end. Effects are sent only if the
commit succeeds.

---

## 8. Expressions

The language is [expr](https://expr-lang.org), type-checked by `validate`.

- Literals: `'text'`, `42`, `3.5`, `true`, `nil`, `[1, 2]`, `{'a': 1}`.
- Operators: `== != < <= > >= && || !` (also `and`, `or`, `not`),
  `+ - * / %`, `in`, `contains`, `startsWith`, `endsWith`, `matches`,
  `??` (default), `a ? b : c`, `?.`.
- Built-ins: `len`, `lower`, `upper`, `trim`, `split`, `join`, `int`,
  `float`, `string`, `abs`, `round`, `floor`, `ceil`, `min`, `max`,
  `now()`, `duration('24h')`, `date(...)`.
- Framework functions where the context allows them: `changed(path)`,
  `isOwner(f)`, `hasRole(f, role)`, `inGroup(id)`, `uuid()`.
- Missing optional fields are `nil`, and comparing with `nil` is safe.
  Decimals support exact `+ - * /` and comparisons, also decimals read
  in a flow (`nodes.x.out.amount + feature.total`).
- Times: date-time fields are times everywhere (also when read in a flow);
  compare them with `< >`, subtract two for a duration
  (`(feature.endedAt - feature.startedAt).Minutes()`), add one with
  `t.Add(duration('48h'))` or `t.AddDate(0, int(feature.months), 0)`
  (`AddDate` takes `int`: wrap integer fields in `int(...)`), format with
  `t.Format('2006-01-02')`, and read parts with `t.Year()`, `t.Month()`,
  `t.Day()`. `date(x)` takes a time or text (`date('2026-11-07')`), and
  `date(timezone('Europe/Athens'), t)` moves a time into a zone.
- `round(x, places)` rounds half away from zero (`places` default 0,
  may be negative); `round`, `floor`, `ceil`, and `abs` keep decimals
  exact and decimal (`round(qty * unitPrice, 2)`). Floats are binary:
  `round(2.345, 2)` is 2.34, so round money as decimals.

**Where expressions appear and what they see:**

| Place | Sees |
| --- | --- |
| `x-computed`, `x-aggregate.where` | the feature's fields by name, `now()` |
| state-machine `guard` | fields, `feature`, `principal`, `now()` |
| policies | `principal`, `feature`, `parent`, `patch`, `input`, plus `hasRole`, `isOwner`, `inGroup` |
| flow `in`, `when`, `result` | §7 variables |
| migration `set` | `feature` (the old document) |
| UI | a subset, §13 |

---

## 9. Python code nodes

Use these only when expressions cannot do the job.

```json
"classify": {
  "node": "python", "code": "nodes/triage.py",
  "in": { "taskId": "feature.id" },
  "inputSchema":  { "type": "object", "properties": { "taskId": { "type": "string" } }, "required": ["taskId"] },
  "outputSchema": { "type": "object", "properties": { "label": { "type": "string", "enum": ["bug", "feature", "chore"] } }, "required": ["label"] },
  "limits": { "timeoutMs": 15000, "memoryMb": 512, "network": ["api.example.com:443"] },
  "retry": { "max": 1 }
}
```

```python
def run(input, ctx):
    task = ctx.get_feature(input["taskId"])
    return {"label": "bug" if "crash" in task["title"].lower() else "chore"}
```

- `ctx` provides:
  - `get_feature(id)`;
  - `query(type, where=, sort=, scope=, limit=)`;
  - `llm(prompt=, model=, system=, schema=, max_tokens=, messages=)`;
  - `http(method, url, headers=, body=, raw_body=, form=, auth=)`: an
    outside API through the egress policy, returning `{status, body,
    headers}` (as `http.request`, §11). Give at most one of `body` (JSON),
    `raw_body` (a `str` sent as is: XML; set `Content-Type` in `headers`),
    or `form` (a dict, URL-encoded).
    `auth={"secret": "stripe", "header": "Authorization", "prefix": "Bearer "}`
    names a secret listed in the node's `"secrets"`; the engine adds it, so
    your code never sees the value. `auth` may be a list (several secret
    headers), and `{"secret": "viva", "form": "client_secret"}` puts a
    secret into a form field;
  - `log(msg, **fields)`;
  - `idempotency_key`;
  - `remaining_ms()`.

  Errors are raised as `organon.HostError` (with a `.code`).
- **Code cannot write.** Return data, and write it with `feature.*` nodes.
- Code may run twice. Pass `ctx.idempotency_key` to outside APIs.
- `inputSchema` and `outputSchema` are required, and output is validated.
  Output is limited to 1 MiB.
- Direct network access (your own HTTP client) is limited to
  `limits.network` hosts (`host:port`, `host:*`, `*.domain:port`). Secrets
  are not in the environment; use `ctx.http` with `auth`.
- You may import sibling modules in `nodes/`.
- For dependencies, add `"requirements": "nodes/requirements.lock"`
  (pinned, with hashes) and optionally `app.json`
  `python.allowedPackages`.

---

## 10. LLM calls

```json
"summarize": {
  "node": "llm.complete",
  "in": { "model": "'default'", "system": "'Summarize in three sentences.'", "prompt": "nodes.prompt.out",
          "maxTokens": "1000", "streamTo": "{ 'feature': feature.id, 'path': '/summaryDraft' }" },
  "outputSchema": { "type": "object", "properties": { "summary": { "type": "string" },
                    "risk": { "type": "string", "enum": ["low", "medium", "high"] } }, "required": ["summary", "risk"] },
  "retry": { "max": 2 }
}
```

- `model` is an **alias** (`'default'`, `'fast'`, `'large'`, or one from
  `app.json`). Never use vendor model IDs.
- With `outputSchema`, the reply is a validated JSON object. Without it,
  the output is `{text}`.
- `messages` holds prior turns. `streamTo` streams partial text live into
  an `x-persist: false` field.
- Each call is recorded on the run and counted against
  `llm.monthlyBudgetUsd`.
- `dev` and `test` use a deterministic fake model: with a schema, it
  returns the simplest valid document.

---

## 11. Email, webhooks, outbound HTTP

`templates/email/<name>.txt`: the first line `Subject: ...` sets the
subject, and the body is a Go template over `vars`:

```
Subject: You were assigned: {{.title}}

You are now the assignee of "{{.title}}".
```

- Use `email.send` with `{"to": "...", "template": "'assigned'", "vars": {...}}`.
- **Languages:** `"lang": "nodes.customer.out.locale ?? 'el'"` uses
  `templates/email/<name>.<lang>.txt` when it exists (then the base
  language, then `<name>.txt`).
- **Attachments:** `"attachments": "[ { 'blob': nodes.pdf.out, 'name': 'invoice.pdf' } ]"`
  (file values, at most 10 MiB in all).
- **Sending:** emails go to the log unless the server is configured for
  SMTP (`ORGANON_EMAIL_*`, §18).
- `webhook.call` POSTs signed JSON after the commit.
- `http.request` calls an outside API synchronously.

**Request and response bodies.** Give at most one body (`validate` rejects
two):

| Input | Sent as | Content-Type unless a header sets one |
| --- | --- | --- |
| `body` | JSON (an object, list, or value; a text value becomes a JSON string) | `application/json` |
| `rawBody` | the text exactly as given, UTF-8 (XML, CSV, SOAP) | `text/plain; charset=utf-8` |
| `form` | `a=1&b=x+y`, URL-encoded; values are text, numbers, or booleans | `application/x-www-form-urlencoded` |

- A `Content-Type` header set by the flow always wins (e.g.
  `'application/xml; charset=utf-8'` with `rawBody`). Bodies are limited
  to 1 MiB.
- `${secret:name}` works in `form` values (OAuth `client_secret`), with
  the same host check as headers; never in `body` or `rawBody`. Build XML
  in a `template` or `python` node and pass it as `rawBody`.
- The response `body` is parsed JSON when the response is JSON (or has no
  type), and text otherwise (XML, HTML), decoded by its charset; parse XML
  in Python with `xml.etree.ElementTree`. `headers` holds the response
  headers.
- 4xx answers are output (`status` 400 with the provider's error document
  in `body`): check `nodes.x.out.status`. 5xx, 429, and network errors
  fail the node and are retried with `retry`.
- Run records keep node inputs, including bodies, only with
  `trace: true`.

**Authenticating to outside APIs.** Write `${secret:name}` inside header
values or the URL query string of `http.request`. The engine puts the
value into the request after evaluation, so it never appears in
expressions or run records. Each secret must list its hosts in `app.json`
`secrets`; other hosts are refused.

```json
"charge": { "node": "http.request",
  "in": { "method": "'POST'", "url": "'https://api.stripe.com/v1/payment_intents'",
          "headers": { "Authorization": "'Bearer ${secret:stripe}'" },
          "form": { "amount": "nodes.total.out", "currency": "'eur'" } } }
```

An XML API with two secret headers (AADE myDATA):

```json
"send": { "node": "http.request", "retry": { "max": 2, "backoff": "exponential" },
  "in": { "method": "'POST'", "url": "env.mydataUrl + '/SendInvoices'",
          "headers": { "aade-user-id": "'${secret:mydata-user}'", "ocp-apim-subscription-key": "'${secret:mydata-key}'",
                       "Content-Type": "'application/xml; charset=utf-8'" },
          "rawBody": "nodes.xml.out.xml" } }
```

- In Python, list `"secrets": ["mydata-user", "mydata-key"]` on the node and call
  `ctx.http("POST", url, raw_body=xml, headers={"Content-Type": "application/xml; charset=utf-8"},
  auth=[{"secret": "mydata-user", "header": "aade-user-id"}, {"secret": "mydata-key", "header": "ocp-apim-subscription-key"}])`.
- Secrets anywhere else (`body`, `rawBody`, URL path or host, other
  nodes) are validation errors. Missing secrets fail the node with
  `secret_missing`.

**PDF documents** (invoices, job cards, receipts). Describe the layout in
`templates/documents/<name>.json`; `document.render` fills it and stores
the PDF for a Blob field (its `x-maxBytes` and `x-contentTypes` apply, so
allow `application/pdf`); set the result with `feature.update`:

```json
"pdf":  { "node": "document.render", "in": { "template": "'invoice'", "type": "'invoice'", "field": "'/pdf'",
          "vars": { "invoice": "feature", "lines": "nodes.lines.out" } } },
"save": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/pdf": "nodes.pdf.out" } } }
```

```json
{
  "vars": { "type": "object", "properties": { "invoice": { "type": "object" }, "lines": { "type": "array", "items": { "type": "object" } } } },
  "page": { "size": "A4", "margin": 18 }, "locale": "el", "timezone": "Europe/Athens",
  "header": [ { "image": "'templates/documents/logo.png'", "width": 40 } ],
  "footer": [ { "text": "{{ vars.invoice.number }} · {{ page }}/{{ pages }}", "align": "center", "size": 8 } ],
  "body": [
    { "heading": "Τιμολόγιο {{ vars.invoice.number }}", "level": 1 },
    { "fields": [ { "label": "ΑΦΜ", "value": "{{ vars.invoice.vat }}" } ], "columns": 2 },
    { "table": { "rows": "vars.lines",
        "columns": [ { "label": "Περιγραφή", "value": "{{ row.description }}", "width": 4 },
                     { "label": "Ποσό", "value": "{{ formatNumber(row.amount, 2) }} €", "align": "right" } ],
        "footer": [ [ "Σύνολο", "{{ formatNumber(vars.invoice.total, 2) }} €" ] ] } },
    { "qr": "{{ vars.invoice.qrUrl }}", "size": 25, "align": "right", "when": "vars.invoice.qrUrl != nil" }
  ]
}
```

- Blocks: `heading` (`level` 1-3), `text` (`**bold**`, `align`, `size`),
  `fields` (`[{label, value}]`, `columns`), `table` (`rows` expression,
  `columns` `[{label, value, width, align}]` with `row`/`index`, `footer`
  rows; the header repeats per page), `image` (a file value or a
  `templates/...` PNG/JPEG/GIF; `width` mm), `qr` (e.g. the myDATA URL;
  `size` mm), `spacer` (mm), `pageBreak`. Any block may have `when`.
- Texts are literal with `{{ expr }}`; headers and footers also see `page`
  and `pages`. `formatNumber(n, digits)` and `formatDate(t, 'date' |
  'datetime' | 'time' | '<Go layout>')` follow `locale` and `timezone`.
  `formatNumber` rounds decimals and decimal text exactly; plain text
  values are shown as written, so wrap amounts in `formatNumber`.
- `vars` with `properties` is closed: misspelt names fail validation.
  The built-in font (Noto Sans) covers Latin, Greek, and Cyrillic.

**Inbound webhooks** verify the signature with `trigger.verify`:
- `{"scheme": "organon"}` (default): `X-Organon-Signature: t=<unix>,v1=<hex>`.
- `{"scheme": "stripe"}`: `Stripe-Signature`.
- `{"scheme": "hmac-sha256", "header": "X-Hub-Signature-256", "prefix": "sha256=", "encoding": "hex"|"base64"}`:
  other providers.

---

## 12. Data access the app gets for free

These are available to the UI, scripts, and `feature.query` nodes.

- **CRUD:**
  - `POST /v1/features/<type>?parent=<id>` creates;
  - `GET /v1/features/<type>/<id>` reads (the `ETag` is the version);
  - `PATCH` with `If-Match` applies a merge patch;
  - `DELETE` with `If-Match` soft-deletes the feature and its children;
  - `POST /v1/features/<id>/move` moves a subtree;
  - `GET /v1/features/<id>/children` lists children.
- **Transactions:** `POST /v1/transactions {"ops": [...]}`, up to 100
  ops. `$refs` link creates.
- **Queries:** `POST /v1/query`:

  ```json
  {"type": "task", "where": {"and": [{"field": "/status", "op": "ne", "value": "done"}]},
   "scope": {"descendantsOf": "<id>"}, "sort": [{"field": "/dueAt", "dir": "asc"}], "select": ["/title"], "limit": 50}
  ```

  - Operators: `eq ne lt lte gt gte in nin prefix icontains iprefix contains exists`
    (`icontains`/`iprefix` ignore case and accents on string fields, e.g. a
    customer search box: `{"field": "/name", "op": "icontains", "value": "state.q"}`;
    `contains` is for lists),
    combined with `and`, `or`, `not` (at most 20 predicates, depth 4).
  - Filter and sort only on `x-indexed` fields.
  - `select` limits the returned fields; `expand: ["/assignee"]` adds the
    referenced features in an `expanded` map.
  - The answer is `{items, next}`: pass `next` as `cursor` for the next
    page (at most 500 per page).
- **Full-text search:** `GET /v1/search?q=..&type=..` over `x-searchable`
  fields.
- **Files:**
  1. `POST /v1/uploads {type, field, feature|parent, size, contentType}`
     returns `{blob, uploadUrl}`.
  2. `PUT` the bytes to `uploadUrl`.
  3. Set `blob` on the field in a create or update.

  Reads return short-lived signed URLs. Images, PDF, video, and audio are
  served inline.
- **Live updates:**
  - WebSocket `/v1/ws` or SSE `/v1/stream`, subscribing to a feature, a
    parent's children, or a query (snapshot, then deltas);
  - ephemeral values: `POST /v1/ephemeral/<type>/<id>`.
- **Idempotency:** the `Idempotency-Key` header on any POST.
- **Optimistic concurrency:** updates, deletes, and moves need the current
  version (`If-Match`); a stale one answers `409 version_conflict`.
- **Permission checks:** `POST /v1/permissions {"checks": [{"id", "op"}]}`
  answers up to 100 `update`/`delete`/`action:<name>`/`create:<type>`
  questions (what the UI's `can()` uses).
- **Authentication:** password sign-in (`POST /v1/auth/password`, cookie
  session with CSRF header, or `"cli": true` for a bearer token), OIDC,
  TOTP MFA, and API keys (`organon token create`; service accounts own
  their keys). Admins create users with `POST /v1/auth/users` and OIDC
  invite links with `POST /v1/auth/users/<id>/oidc-invite`.
- **Teams and organizations:** `team` features with `members` (and
  `organization`) can own features and hold grants; members inherit them.
- **Clients:** `organon codegen ts` generates a typed TypeScript client
  from the views (private fields don't exist on it); `@organon/react`
  offers `useFeature`, `useQuery`, `useChildren`, `useAction`, `useSession`.
- **Errors** are RFC 9457 problems with a `code`:
  - `validation_failed`, `unauthenticated`, `forbidden`, `not_found`;
  - `version_conflict`, `invalid_transition`, `rate_limited`;
  - `flow_failed`, `flow_timeout`;
  - your `reject` codes.
- **Description:** OpenAPI at `GET /v1/schema`; type views at
  `GET /v1/schema/types/<name>`.

---

## 13. End-user UI

Served at `/`. It is optional: without `ui/app.json`, the root redirects to
the admin console. Run `organon ui catalog` for the exact props; **unknown
props are errors.**

### Shell: `ui/app.json`

```json
{ "title": "Tracker", "home": "projects", "requireLogin": true, "logo": "assets/logo.svg",
  "languages": ["en", "el"],
  "nav": [ { "label": "Projects", "page": "projects" },
           { "label": "My tasks", "page": "myTasks", "visible": "principal.kind == 'user'" } ],
  "notFound": "This page does not exist." }
```

- `requireLogin: false` lets anonymous visitors see the pages whose
  `visible` allows them.
- `linkHome: "<page>"` is the page a share link opens, with `params.id` =
  the shared feature. Give that page `"visible": "principal.kind == 'link'"`
  and staff pages and nav entries `"principal.kind != 'link'"`. Link
  holders never see the sign-in screen.
- A "Share" button: an `action` step with `"action": "share"`, then
  `set` a state variable to `result.output.url` and `open` a dialog that
  shows it (see the tracker's `ui/pages/task.json`).
- Sign-in by password, OIDC, and MFA is built in.

### Page: `ui/pages/<name>.json`

```json
{
  "page": "project",
  "title": "{{ data.project.title }}",
  "route": "/projects/:id",
  "params": { "id": { "type": "string", "format": "uuid" } },
  "visible": "principal.kind != 'anonymous'",
  "state": { "tab": { "type": "integer", "default": 0 } },
  "data": {
    "project": { "feature": "params.id", "type": "project" },
    "tasks":   { "children": "params.id", "type": "task", "limit": 200 },
    "open":    { "query": { "type": "task", "where": { "field": "/status", "op": "ne", "value": "'done'" },
                            "scope": { "descendantsOf": "params.id" }, "limit": 100 } },
    "found":   { "search": "state.q", "type": "task" }
  },
  "body": { "component": "stack", "children": [
    { "component": "heading", "text": "{{ data.project.title }}", "level": 1 },
    { "component": "button", "label": "Summarize", "visible": "can(data.project, 'action:summarize')",
      "on": { "click": [ { "do": "action", "type": "project", "id": "params.id", "action": "summarize" },
                         { "do": "toast", "text": "Risk: {{ result.output.risk }}", "tone": "success" } ] } },
    { "component": "board", "source": "tasks", "field": "/status",
      "card": { "component": "link", "text": "{{ item.title }}", "page": "task", "params": { "id": "item.id" } } }
  ] }
}
```

**Prop kinds. Getting these right avoids most UI errors:**

| Kind | Written as | Example |
| --- | --- | --- |
| text (`text`, `label`, `title`, `empty`, `submit`, `confirm`, ...) | literal text with `{{ expr }}` | `"Tasks of {{ data.project.title }}"` |
| expr (`value`, `visible`, `disabled`, `id`, `parent`, `params`, step inputs, query values) | an expression; literals need quotes | `"'done'"`, `"item.id"` |
| path (`field`, `path`, `groupBy`, `fields`) | JSON Pointer | `"/status"` |
| names (`type`, `page`, `action`, `source`, `dialog`) | plain names | `"task"` |

**Data sources:**
- `feature`, `children`, `query` (values are expressions), and `search`.
- Sources may read other sources; cycles are rejected.
- Queries with only `eq`/`in` predicates and no sort stream live. Others
  reload after changes.
- Query fields must be `x-indexed` and public. Private fields cannot be
  bound.

**Components:**

| Family | Components |
| --- | --- |
| Layout | `stack`, `row`, `columns`, `card`, `tabs` (with `state` for an integer state var), `dialog` (`name`, opened by an `open` step), `divider` |
| Text | `heading`, `text` (`tone`), `markdown` (no raw HTML), `badge`, `image` (`assets/<path>` or a URL), `link` (`page`+`params`, or `url`; `download` saves a file) |
| Data | `table` (columns by `field` or `value`, `display`, per-column `page`; `rowPage`/`rowParams`), `list` (`item` template), `field`, `detail`, `stat` (`format: number\|percent\|date`), `empty`, `preview` (images, PDF, video, audio from a blob field; `display: inline` (default) or `modal` for a thumbnail/link that opens a viewer), `transitions` (state-machine buttons the user may run now) |
| Forms | `form` (`mode: create\|update\|action`; generated from the user's create or update view, or the action flow's `input`; `fields: [{path, label, widget, help, labelField}]`; `on.submit` steps see `result` and `form.values`), `button` (`variant`, `on.click`), `menu` (`items: [{label, visible, steps}]`) |
| Charts | `board` (kanban by a state-machine field; dragging runs the transition), `chart` (`kind: bar\|line\|pie`; `source` + `groupBy` [+ `value`, `op: count\|sum\|avg`] or explicit `series`) |

Every component also takes `visible` (an expression) and `class` (for CSS).
Inside `table`, `list`, and `board` rows, `item` and `index` are in scope.

**Steps** (`on.click`, `on.submit`, menu `steps`), run in order:

| Step | Props |
| --- | --- |
| `action` | `type`, `id`, `action`, `input`; result `{output, feature}` |
| `create` | `type`, `parent`, `data`; result is the feature |
| `update` | `type`, `id`, `set` (`{"/field": expr}`) |
| `delete` | `type`, `id` |
| `navigate` | `page`, `params` |
| `back`, `close`, `logout` | none |
| `set` | `state`, `value` |
| `open` | `dialog` |
| `toast` | `text`, `tone` |

- API steps may take `confirm` (text) and `onError` steps.
- Each step sees `result` (the previous step's) and `error`.

**UI expressions** are a subset of §8:
- Allowed: literals, `.`, `[]`, `?.`, comparisons, `&& || !`, arithmetic,
  `in`, `contains`, `startsWith`, `endsWith`, `??`, `?:`, and the
  functions `len lower upper trim string int float join split abs round
  floor ceil min max now`.
- UI-only functions:

  | Function | Meaning |
  | --- | --- |
  | `can(f, op)` | Answered by the server. `op` is `'update'`, `'delete'`, `'action:<name>'`, or `'create:<type>'`. |
  | `formatDate(t, 'date'\|'datetime'\|'time'\|'relative')` | Formats a time. |
  | `formatNumber(n, digits)` | Formats a number; decimal fields (strings in the browser) are rounded exactly. |

- **Not available:** `filter`, `map`, pipes, `matches`, `hasRole`,
  `isOwner`, `uuid`. Put that logic in a computed field, an aggregate, or
  a flow.
- Variables: `params`, `data.<source>`, `state.<var>`, `principal`
  (`id`, `kind`, `roles`); `item` and `index` in rows; `result` and
  `error` in steps.
- File links (`field`, `detail`, table cells) open images, PDFs, video, and
  audio in a viewer over the page, with "Open in new tab"; other files
  download.
- Decimals are strings in the browser, so arithmetic on them is rejected;
  `round(d, 2)`, `floor`, `ceil`, and `abs` work on them exactly.

**The UI hides; policies protect.** Hide controls with `can(...)`, but
rely on policies for security.

**Styling.**
- In `ui/styles.css`, override the variables `--o-accent`, `--o-bg`,
  `--o-surface`, `--o-text`, `--o-muted`, `--o-line`, `--o-radius`,
  `--o-font`, and `--o-chart-0..5`.
- Style the stable classes such as `.o-card`, `.o-table`, `.o-board-card`,
  `.o-button-primary`, and `.o-badge-<enum value>`, or your own `class`
  names.
- Pages carry `data-page="<name>"`.
- Images and fonts go in `ui/assets/`. External URLs are blocked by the
  CSP.

---

## 14. UI languages

1. List the languages in `ui/app.json`: `"languages": ["en", "el"]`.
   **The first** is the language your pages are written in.
2. Add `ui/i18n/<lang>.json`:

   ```json
   {
     "messages": { "Projects": "Έργα", "Delete {{ data.task.title }}?": "Διαγραφή «{{ data.task.title }}»;",
                   "Sign in": "Σύνδεση", "@projects.intro": "…" },
     "pages":    { "projects": { "Create": "Δημιουργία έργου" } },
     "types":    { "task": { "fields": { "/status": "Κατάσταση" },
                             "values": { "/status": { "in_progress": "Σε εξέλιξη" } },
                             "actions": { "start": "Έναρξη" } } }
   }
   ```

3. Run `organon ui i18n myapp --lang el` to add empty entries for every
   untranslated text, then fill them in.

- **Keys:**
  - A text is its own key. Keep its `{{ }}` parts exactly; you may
    reorder them.
  - `"@key"` texts are explicit keys; their text goes in the first
    language's file. `@@` gives a literal `@`.
  - `pages.<page>` overrides a text on one page.
- **Labels from types:** field labels, enum values, and transition buttons
  are translated under `types`. The first language's file may give nicer
  labels too, such as `"in_progress": "In progress"`.
- **Built-in texts:** the renderer's own texts ("Sign in", "Save",
  "Nothing here yet.") are translated in `messages`.
- **Language choice:**
  - Users switch language in the header; the choice is saved on their
    `user` record.
  - Otherwise the server uses the browser's language.
  - Server messages are not translated.

---

## 15. Seed data and modules

`seed/dev.json` is loaded by `organon dev` into a fresh database, and by
`organon seed` against a running server:

```json
{
  "users": [ { "ref": "ana", "email": "ana@example.com", "password": "ana-password-123", "name": "Ana", "roles": ["editor"] } ],
  "features": [ { "ref": "launch", "type": "project", "owner": "ana", "data": { "title": "Launch", "slug": "launch" } },
                { "ref": "copy", "type": "task", "parent": "launch", "data": { "title": "Write copy", "assignee": "ana" } } ],
  "grants": [ { "subject": "ana", "object": "launch", "role": "editor" } ]
}
```

- `ref` names can be used wherever an ID is expected.
- `modules/<name>/` groups `types`, `traits`, `policies`, `flows`,
  `nodes`, and `ui/pages`. Modules share the tree and the deployment.

---

## 16. Tests

Write tests in `tests/<name>.test.json`. `organon test` runs each test on a
fresh in-memory instance, with the fake LLM, recorded emails, and HTTP
fixtures.

```json
[ {
  "name": "only the assignee can complete a task",
  "given": {
    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user", "roles": [] } },
    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T", "status": "in_progress", "assignee": "ben" } } ],
    "grants": [ { "subject": "ben", "object": "p", "role": "editor" } ]
  },
  "steps": [
    { "as": "ana", "action": "complete", "on": "t", "expect": { "error": "forbidden" } },
    { "as": "ben", "action": "complete", "on": "t", "input": { "note": "done" },
      "expect": { "ok": true, "feature": { "status": "done" }, "output": { "late": false },
                  "effects": [ { "node": "email.send", "count": 0 } ] } },
    { "page": "myTasks", "expect": { "visible": false } }
  ],
  "http": [ { "method": "GET", "url": "https://api.example.com/x", "status": 200, "body": { "ok": true } } ]
} ]
```

- **Steps:** `action`+`on`+`input`, `create`+`parent`+`data`+`ref`,
  `update`+`data`, `delete`, `read`, `query`, and `page` (UI visibility).
  A step without `as` is anonymous; `"as": "link:<ref>"` is a share link
  to that feature with all its type's shareable actions.
- **`expect`:** `ok`, `error` (a code), `feature` (a subset of fields;
  for `read` steps, of what the caller sees: `"staffNote": null` checks a
  field is hidden), `output`, `count`, `effects`, `visible`.
- Outbound HTTP that no fixture matches fails the step.
- **HTTP fixtures:** `{method, url, status, body}` answers JSON;
  `"text": "<ResponseDoc>...</ResponseDoc>"` with
  `"headers": {"Content-Type": "application/xml"}` answers text.
  `"request": {"contentType": "application/xml", "bodyContains": "<invoiceType>2.1</invoiceType>", "header": {...}}`
  makes the fixture answer only requests that match, so a test proves
  what was sent (a mismatch fails the step and says why).
- **Required coverage:**
  - at least one allowed and one denied case per policy decision and per
    action;
  - each flow's main path and its `reject` paths;
  - a `page` step for every page restricted with `visible`;
  - for shareable types: what a link can read and do, and what it cannot.

---

## 17. Changing data and deploying

**Changing data.** Breaking type changes (removing or retyping a field,
tightening a constraint) need a version bump, for example `/v1` → `/v2`,
plus `migrations/<id>.json`:

```json
{ "id": "0001-task-priority", "type": "urn:app:feature:task", "from": 1, "to": 2,
  "set": { "/priority": "feature.title contains 'urgent' ? 'high' : 'normal'", "/old": "nil" }, "batchSize": 500 }
```

- Each migration moves one version.
- For complex logic, use `"flow": "<name>"` instead of `set`; that flow
  needs `runAs: system` and `privileged: true`.
- Reads are upcast immediately; stored data is rewritten in the
  background.

**Deploying:**
- `organon plan myapp` lists:
  - type changes (breaking or not);
  - policy and flow changes, and newly privileged flows;
  - newly public fields, index builds, and migrations;
  - UI changes, and pages newly visible without login;
  - secrets whose allowed hosts changed.
- `organon apply myapp --plan <hash>` activates it without a restart.
- Alternatively, ship a new manifest directory and restart `organond`.
- **Show the plan to the user before applying.**

---

## 18. Running and operating

**The admin console** at `/admin` (admins only) offers:
- data browsing and editing with generated forms;
- the visual flow editor and the UI builder (they save files only under
  `organon dev`; elsewhere they are read-only);
- flow runs with retries of dead-lettered runs, deploys and migration
  progress, and the audit log.

**Server settings** come from YAML (`--config`) or environment variables:

| Variable | Use |
| --- | --- |
| `ORGANON_ENV` | Which `env/<env>.json` to use (default `dev`). |
| `ORGANON_MANIFEST` | The manifest directory (`/app` in the image). |
| `ORGANON_SECRET_<NAME>` | The value of `${secret:<name>}` (upper case; `-` and `.` become `_`). |
| `ORGANON_PUBLIC_URL`, `ORGANON_SECURE_COOKIES=true` | Required behind HTTPS (OIDC redirects, cookies). |
| `ORGANON_CURSOR_KEY` | Signs cursors and blob URLs; set a long random value in production. |
| `ORGANON_BOOTSTRAP_ADMIN_EMAIL`, `_PASSWORD` | The first admin, created on first start. |
| `ORGANON_DISABLE_ADMIN_CONSOLE=true` | `/admin` is not served. |
| `ORGANON_DISABLE_ADMIN_ACCOUNTS=true` | Users with the `admin` role cannot sign in and none are created. Deploy by restarting with a new manifest; new users then need OIDC `signup: true`. |
| `ORGANON_ALLOW_BREAKING=true` | Deploy a breaking manifest directory at startup. |
| `ORGANON_LLM_PROVIDER` | Force one LLM provider (`fake`, `anthropic`, or a configured one). |
| `ORGANON_EMAIL_DRIVER=smtp`, `ORGANON_EMAIL_ADDR`, `_FROM`, `_USERNAME`, `_PASSWORD` (a `${secret:<name>}`), `_TLS` | Send email over SMTP; otherwise emails are only logged. |

**Storage.** SQLite (one replica per database file) with blobs on disk and
background jobs in the same database. In the Docker image all state is in
`/data`; back up the database, the blob directory, and the data key.

**Limits** (defaults): request body 1 MiB (uploads use signed URLs);
document 256 KiB; nesting depth 8 and 1,000 list items; 100 operations
per transaction; 20 predicates and 500 rows per query page; 600 requests
per minute per principal and 60 per IP for anonymous callers; 10 login
attempts per account per 15 minutes; 200 live subscriptions per
connection.

**Observability.** Health probes on port 9090 (`/livez`, `/readyz`,
`/startupz`); JSON logs that never contain field values; OTLP traces and
metrics with `observability.otlpEndpoint`; `organon runs list` for flow
runs; `organon export`/`import` for a logical NDJSON copy of the data.

---

## 19. Common mistakes and fixes

| Symptom (validate or test) | Fix |
| --- | --- |
| `unknown name done` in a flow, policy, or UI expr prop | Quote literals: `"'done'"`. |
| `field ... is not queryable` | Add `"x-indexed": true` (and keep the field public). |
| `forbidden` on an action that should work | Add `actions.<name>` to the policy; check the transition `guard` and the current state. |
| `invalid_transition` | The feature is not in the transition's `from` state. |
| Field rejected on update | It has `readOnly`, `x-write`, `x-computed`, or `x-immutable`, or it is not visible to the caller. Change it through its action. |
| `setNull` ref error | Make the ref nullable: `"type": ["string", "null"]`. |
| `runAs system requires privileged` | Add `"privileged": true`, or run as the caller. |
| beforeWrite flow uses `email.send`, `http.request`, or `python` | Move that work to an event flow. |
| `unknown member` in a UI file | Check `organon ui catalog`; props are per component. |
| `page X needs parameter "id"` | Pass every route parameter in `params`. |
| UI expression uses `filter`, `map`, or `hasRole` | Use a computed field, an aggregate, or a flow, or `can(...)`. |
| A translation error about `{{ }}` | The translation must keep the same `{{ }}` expressions. |
| Python node wants to write | Return data; add a `feature.update` node. |
| Model ID in a manifest | Use an alias: `'default'` or `'fast'`. |
| `secrets may only be used in headers, url, and form` | Move the secret into a header value, the URL query string, or a `form` value; never in `body` or `rawBody`. |
| `http.request takes one of body, rawBody, or form` | Keep one body: `rawBody` for XML/text, `form` for form posts, `body` for JSON. |
| An API rejects the body (it wants XML or a form) | Use `rawBody` with a `Content-Type` header, or `form`; `body` always sends JSON. |
| `secret x has no allowed hosts` | Add `"secrets": {"x": {"hosts": ["api.example.com"]}}` to `app.json`. |
| `secret x may not be sent to <host>` (at run time) | Call the declared host, or add the host to the secret's list. |
| `secret_missing` | Set `ORGANON_SECRET_<NAME>` on the server. |
| `document.render`: `does not accept application/pdf` | Add `"application/pdf"` to the field's `x-contentTypes`. |
| `no document template ...` | Create `templates/documents/<name>.json`, or fix the name in the node. |
| `x-sequence fields ... do not list them in required` | Remove the field from `required`; it is filled in on commit. |
| Expected a sequence number in a test that depends on the year | Manifest tests use the real clock; keep `{year}` out of formats you assert on, or match only the `{n}` part. |
| Webhook answers `401` | Check `trigger.verify` matches the provider and the secret value. |

---

## 20. Finishing checklist

- [ ] Every type has `"type": "object"`, `"additionalProperties": false`,
      `required`, and `x-indexed` on every filtered or sorted field.
- [ ] Sensitive fields use `x-visibility`; fields owned by actions use
      `x-write: action:<name>`; money uses `decimal`.
- [ ] Every type has a policy; every action and transition has a rule
      under `actions`.
- [ ] Flows run as the caller unless they truly need `system`; no secrets
      in files (`${secret:...}`), and each secret used for outbound
      requests lists its hosts; models by alias.
- [ ] The UI: `ui/app.json`, a page for each main task, `can(...)` on
      buttons, and `params` for every route parameter. With `languages`,
      translations are filled in (`organon ui i18n`).
- [ ] `seed/dev.json` has users of each role and enough data to try the
      app.
- [ ] Tests cover allowed and denied cases, actions, flows, and restricted
      pages.
- [ ] `organon validate` prints `ok`, and `organon test` prints
      `N passed, 0 failed`.
- [ ] Optionally, `organon dev` and a check of `/` as each seeded user.

---

## 21. Reference app

`examples/tracker` (in the image: `/usr/share/organon/examples/tracker`)
uses nearly every feature:

| File | Shows |
| --- | --- |
| `types/task.json` | refs, decimal, time, state machine with a guard, computed, private, blob with thumbnails, stream |
| `types/project.json` | aggregates, unique name, owner-only and ephemeral fields |
| `types/task.json` `number` | gapless sequence numbering (`T-0001`) |
| `flows/task-report.json` + `templates/documents/task-report.json` + `templates/email/report(.el).txt` | PDF document, saved on a field, emailed as an attachment in the user's language |
| `policies/task.json` | roles through grants, owner, assignee, per-action rules |
| `flows/complete-task.json` | action flow with input, output, email |
| `flows/reject-spam.json` | beforeWrite with `reject` |
| `flows/notify-assignee.json` | event flow with `changed()` |
| `flows/daily-digest.json` | schedule with `forEach` |
| `flows/inbound-issue.json` | webhook |
| `flows/summarize-project.json` | LLM with structured output and streaming |
| `flows/triage-task.json` + `nodes/triage.py` | Python node calling an LLM |
| `tests/task-permissions.test.json`, `tests/ui-pages.test.json` | permission matrix, effects, page visibility |
| `ui/pages/project.json` | sources, board, chart, tabs, dialog with a create form, action button |
| `ui/pages/task.json` | update form, transitions, menu, inline file preview, comments list, Share button and dialog |
| `types/task.json` `x-shareable`, `ui/pages/shared-task.json`, `ui/app.json` `linkHome` | share links for people without accounts; `preview` with `display: modal` |
| `ui/i18n/en.json`, `ui/i18n/el.json`, `ui/styles.css` | translations, theme |
