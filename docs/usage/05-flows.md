# 5. Flows

A flow is a JSON graph of nodes in `flows/`. Each node takes inputs
written as expressions and produces an output (`nodes.<name>.out`) that
later nodes can use. A run commits all of its writes in **one transaction**
at the end; effects (email, webhooks, emitted events) are delivered after
the commit.

```json
{
  "flow": "completeTask",
  "title": "Complete a task",
  "trigger": { "action": "complete", "on": "urn:app:feature:task" },
  "input":  { "type": "object", "properties": { "note": { "type": "string", "maxLength": 2000 } }, "additionalProperties": false },
  "output": { "type": "object", "properties": { "late": { "type": "boolean" } } },
  "timeoutMs": 10000,
  "nodes": {
    "project": { "node": "feature.get", "in": { "id": "feature.parentId" } },
    "late":    { "node": "expr", "in": { "expr": "feature.dueAt != nil && now() > feature.dueAt" } },
    "setDone": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/status": "'done'" } } },
    "comment": { "node": "feature.create", "when": "input.note != nil && input.note != ''",
                 "in": { "type": "'comment'", "parent": "feature.id", "data": { "body": "input.note" } } },
    "notify":  { "node": "email.send", "when": "nodes.late.out == true",
                 "in": { "to": "env.supportEmail", "template": "'late-task'",
                         "vars": { "title": "feature.title", "project": "nodes.project.out.title" } } }
  },
  "edges": [["late", "setDone"]],
  "result": { "late": "nodes.late.out" }
}
```

## Anatomy

| Key | Meaning |
| --- | --- |
| `flow` | Unique name (no spaces or slashes). |
| `trigger` | Exactly one trigger, see below. |
| `input` / `output` | JSON Schemas for action/webhook input and the result. |
| `nodes` | Map of node name → `{node, in, when?, onError?, retry?, ...}`. |
| `edges` | Extra ordering `[from, to]`. Dependencies are also **inferred** from `nodes.<name>` references, so most flows need no edges. |
| `result` | Expressions forming the run's output (returned by actions). |
| `runAs` | `trigger` (default: the caller, or the principal who caused the event) or `system` (requires `privileged: true`). |
| `timeoutMs` | Budget; defaults/maximums: beforeWrite 2 s/5 s, action 10 s/30 s, others 60 s/15 min. |
| `trace` | Store node inputs and outputs on the run record (admin-only, 7 days). |

**Strings in `in`, `when`, and `result` are expressions.** A string literal
needs quotes inside: `"'done'"`. Numbers, booleans, and objects may be
written as JSON literals or expressions; object values are expressions too.

Node options: `when` (skip unless true; a node whose `nodes.<name>`
inputs all come from skipped nodes is skipped too, while `edges` only
order and never pass a skip on), `onError: "<handler node>"` (the handler runs with
`nodes.<failed>.error`), `retry: {"max": N, "backoff": "exponential"|"fixed"}`
(External and Code nodes only).

## Triggers

| Trigger | Example | Runs |
| --- | --- | --- |
| action | `{"action": "complete", "on": "<type>"}` | `POST /v1/actions/<type>/<id>/complete`, synchronously, as the caller; returns `result`. |
| beforeWrite | `{"beforeWrite": ["create","update"], "on": "<type>", "when": "..."}` | Inside the write; may stage more writes to the same repository or `reject` it. Only Pure, DataRead, DataWrite, and Control nodes. |
| event | `{"event": ["created","updated"], "on": "<type>", "when": "changed('/assignee')"}` | In the background after commit. Lifecycle events: `created`, `updated`, `moved`, `deleted`, `purged`; custom dotted events from `emit` (e.g. `task.reminded`). |
| schedule | `{"schedule": "0 8 * * *"}` | Cron (UTC), as system. |
| webhook | `{"webhook": "issues", "secret": "${secret:issues-webhook}"}` | `POST /v1/hooks/issues` with header `X-Organon-Signature: t=<unix>,v1=<hex HMAC-SHA256 of "<unix>.<body>">` (within 5 minutes). Add `"verify"` for other providers, see [Secrets](#secrets-and-signatures). |

Trigger `when` conditions see `event` (`kind`, `changed`, `type`, `id`,
`data`), `before`, `after`, `feature`, `op`, `principal`, and
`changed(path)`. A condition that fails to evaluate records a failed run
(`when_failed`) instead of silently skipping the flow.

- **beforeWrite** flows run for every write of their type: API writes,
  state-machine transitions, and writes made by other flows (but not the
  writes they stage themselves). With `runAs: system` (and `privileged`)
  their staged writes are made as system. A `feature.update` of
  `feature.id` sets fields of the feature being written, even on create.
  Nodes see `before`, `after`, and `trigger.op`.
- **event** flows also see `before` and `after` in nodes. For `deleted`
  and `purged` events, `feature` is the feature's last state.
- Action, webhook, and subflow input takes its schema's types: an
  `"integer"` compares as an integer however the client sent it.

Background runs retry on failure (up to 5 attempts); the last failure is
recorded as `dead_letter` and can be retried with `organon runs retry <id>` or
from the admin console. Causation is tracked: a flow that would re-trigger
itself on the same feature, or a chain deeper than 8, is stopped
(`loop_stopped`).

## Secrets and signatures

`http.request` may use `${secret:name}` in header values and the URL query
string. The engine puts the value into the request after evaluating the
inputs, only for hosts listed in `app.json` `secrets.<name>.hosts`; run
records keep the reference.

```json
"charge": { "node": "http.request",
  "in": { "method": "'POST'", "url": "'https://api.stripe.com/v1/payment_intents'",
          "headers": { "Authorization": "'Bearer ${secret:stripe}'" },
          "body": { "amount": "nodes.total.out" } } }
```

Inbound webhooks choose how they are signed with `trigger.verify`:

| `scheme` | Checks |
| --- | --- |
| `organon` (default) | `X-Organon-Signature: t=<unix>,v1=<hex>` as sent by `webhook.call` (the older `sha256=<hex>` also works) |
| `stripe` | `Stripe-Signature` (same timestamped scheme, 5-minute window) |
| `hmac-sha256` | `header` holding `prefix` + HMAC-SHA256 of the body, `encoding` `hex` (default) or `base64` |

```json
"trigger": { "webhook": "payments", "secret": "${secret:stripe-webhook}", "verify": { "scheme": "stripe" } }
```

## Documents

`templates/documents/<name>.json` describes a PDF; `document.render` fills
it with `vars` and stores the file for a Blob field (respecting its
`x-maxBytes` and `x-contentTypes`). Set it on the feature, and attach it to
emails:

```json
"pdf":    { "node": "document.render",
            "in": { "template": "'invoice'", "type": "'invoice'", "field": "'/pdf'",
                    "vars": { "invoice": "feature", "lines": "nodes.lines.out" } } },
"save":   { "node": "feature.update", "in": { "id": "feature.id", "set": { "/pdf": "nodes.pdf.out" } } },
"mail":   { "node": "email.send", "in": { "to": "nodes.customer.out.email", "template": "'invoice'",
            "lang": "nodes.customer.out.locale ?? 'el'",
            "attachments": "[ { 'blob': nodes.pdf.out, 'name': feature.number + '.pdf' } ]" } }
```

```json
{
  "vars": { "type": "object", "properties": {
    "invoice": { "type": "object" }, "lines": { "type": "array", "items": { "type": "object" } } } },
  "page": { "size": "A4", "margin": 18 },
  "locale": "el",
  "timezone": "Europe/Athens",
  "header": [ { "image": "'templates/documents/logo.png'", "width": 40 } ],
  "footer": [ { "text": "{{ vars.invoice.number }} · {{ page }}/{{ pages }}", "align": "center", "size": 8 } ],
  "body": [
    { "heading": "Τιμολόγιο {{ vars.invoice.number }}", "level": 1 },
    { "fields": [ { "label": "Ημερομηνία", "value": "{{ formatDate(vars.invoice.issuedAt, 'date') }}" },
                  { "label": "ΑΦΜ", "value": "{{ vars.invoice.vat }}" } ], "columns": 2 },
    { "table": { "rows": "vars.lines",
        "columns": [ { "label": "Περιγραφή", "value": "{{ row.description }}", "width": 4 },
                     { "label": "Ποσό", "value": "{{ formatNumber(row.amount, 2) }} €", "align": "right" } ],
        "footer": [ [ "Σύνολο", "{{ formatNumber(vars.invoice.total, 2) }} €" ] ] } },
    { "qr": "{{ vars.invoice.qrUrl }}", "size": 25, "align": "right", "when": "vars.invoice.qrUrl != nil" }
  ]
}
```

| Block | Members |
| --- | --- |
| `heading` | text; `level` 1-3 |
| `text` | text (`**bold**` spans); `align` left/center/right; `size` (points) |
| `fields` | `[{label, value}]`; `columns` 1-4 |
| `table` | `rows` (an expression giving a list), `columns` `[{label, value, width, align}]` (cells see `row` and `index`), `footer` rows of texts (bold); the header repeats on each page |
| `image` | an expression: a file value or a `templates/...` path (PNG, JPEG, GIF); `width` (mm), `align` |
| `qr` | text to encode (e.g. the myDATA URL); `size` (mm), `align` |
| `spacer` | height in mm |
| `pageBreak` | `true` |

- Every block may have `when`. Texts are literal with `{{ expr }}`;
  headers and footers also see `page` and `pages`.
- `formatNumber(n, digits)` and `formatDate(t, 'date' | 'datetime' |
  'time' | '<Go layout>')` follow `locale` and `timezone`; numbers and
  dates written without them are formatted the same way.
- `vars` with `properties` is closed: a misspelt name is a validation
  error. Text uses the built-in Noto Sans (Latin, Greek, Cyrillic).

## Variables available to nodes

`feature` (the trigger feature, storage view), `input`, `event`,
`trigger`, `principal`, `env` (`app.json` settings), `nodes.<name>.out`
and `nodes.<name>.error`; inside `forEach`: `item`, `index`, `outer`.

## Node reference

`*` = required input.

| Node | Kind | Inputs | Does |
| --- | --- | --- | --- |
| `expr` | Pure | expr* | Evaluates an expression. |
| `template` | Pure | text*, vars | Renders a Go text/template string. |
| `feature.get` | DataRead | id* | Reads a feature as the run's principal (sees the run's own staged writes). |
| `feature.query` | DataRead | type*, where, sort, scope, limit | Runs a query document. |
| `feature.children` | DataRead | id*, type, limit | Lists readable children. |
| `feature.create` | DataWrite | type*, parent, data | Stages a create. |
| `feature.update` | DataWrite | id*, set, version | Stages a merge patch; `set` maps JSON Pointers to values. |
| `feature.delete` | DataWrite | id*, version | Stages a soft delete. |
| `feature.move` | DataWrite | id*, parent*, version | Stages a move. |
| `email.send` | Effect | to*, template*, subject, vars, attachments, lang | Sends `templates/email/<template>.txt` after commit (first line `Subject: ...` sets the subject); `lang` picks `<template>.<lang>.txt` when it exists; `attachments` are file values or `{blob, name}` (10 MiB in all). |
| `webhook.call` | Effect | url*, payload, secret | POSTs signed JSON after commit. |
| `emit` | Effect | event*, feature*, data | Raises a custom dotted event after commit; flows triggered by it run next. |
| `http.request` | External | url*, method, headers, body | Calls an HTTP API through the egress policy (private networks blocked). Output `{status, body, headers}`. |
| `document.render` | External | template*, vars, type*, field* | Renders `templates/documents/<template>.json` to PDF and stores it for that Blob field; output is the file value to set with `feature.update` ([Documents](#documents)). |
| `llm.complete` | External | prompt, model, system, messages, maxTokens, temperature, streamTo | Calls a model ([guide 6](06-code-and-llm.md)). |
| `python` | Code | from `inputSchema` | Runs `nodes/<file>.py` ([guide 6](06-code-and-llm.md)). |
| `if` | Control | cond* | Output is the condition; branch with `when: "nodes.<if>.out"`. |
| `switch` | Control | value*, cases | Output is the matching case or `"default"`. |
| `forEach` | Control | items* | Runs the `do` subgraph per item, at most `maxItems` (default 100, max 1000); outputs a list of the subgraph `result`s. |
| `subflow` | Control | flow*, input | Runs another flow inline (depth ≤ 4). |
| `reject` | Control | code*, message, status | Aborts a beforeWrite or action flow with that error code and HTTP status. |
| `fail` | Control | message | Fails the run. |

Kinds matter: beforeWrite flows allow only Pure, DataRead, DataWrite, and
Control nodes; External and Code nodes may `retry`.

## Expressions

Expressions use the [expr](https://expr-lang.org) language and are
type-checked against the schema by `organon validate`.

- Literals: `'text'`, `42`, `3.5`, `true`, `nil`, `[1, 2]`, `{'a': 1}`.
- Operators: `== != < <= > >= && || !`, `+ - * / %`, `in`, `contains`,
  `startsWith`, `endsWith`, `matches`, `??` (default), `a ? b : c`.
- Built-ins: `len`, `lower`, `upper`, `trim`, `split`, `join`, `int`,
  `float`, `string`, `now()`, `duration('24h')`, `date(...)`, plus the
  framework functions `changed(path)`, `isOwner(f)`, `hasRole(f, role)`,
  `inGroup(id)`, `uuid()` where the context allows them.
- Missing optional fields are `nil`; comparisons with `nil` are safe.
- Decimal fields support `+ - * /` and comparisons exactly.

## The visual editor

`/admin` → **Flows** shows every flow as a graph. In `organon dev` you can add
nodes from the palette (the same catalog as above), connect them, edit
inputs, and save: the server validates the graph and writes
`flows/<name>.json` (node positions go into `x-layout`), then reloads. In
production the editor is read-only; change flows in the manifest and
deploy.

## Actions

`POST /v1/actions/<type>/<id>/<action>` with a JSON input runs, in order of
preference:

1. a flow whose trigger is that action on the type (it may set a
   state-machine field to the transition's target state);
2. a state-machine transition with that name (`start`, `complete`, ...);
   a transition not allowed from the current state answers
   `409 invalid_transition`;
3. the generic `transition` action: `{"action": "start"}` or
   `{"to": "in_progress"}` picks the matching transition;
4. the built-in `transfer` action of Ownable types: `{"to": "<principal id>"}`.

The action's policy rule (`actions.<name>`) and the transition `guard` must
both allow it.
