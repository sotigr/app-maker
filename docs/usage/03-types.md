# 3. Types

A feature type is one JSON Schema (draft 2020-12 subset) in `types/`. The
framework compiles it into storage, validation, read/write views per
audience, query rules, and generated client types.

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "urn:app:feature:task/v2",
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
                    { "from": "in_progress", "to": "done", "action": "complete", "guard": "principal.id == feature.assignee" } ] } },
    "assignee": { "type": ["string", "null"], "x-kind": "ref", "x-ref": "urn:sys:trait:principal", "x-onDelete": "setNull", "x-indexed": true },
    "estimate": { "type": "string", "x-kind": "decimal" },
    "dueAt":    { "type": "string", "format": "date-time", "x-indexed": true },
    "progress": { "type": "integer", "minimum": 0, "maximum": 100, "x-stream": { "mode": "state", "maxRate": 10 } },
    "isLate":   { "type": "boolean", "readOnly": true, "x-computed": "dueAt != nil && status != 'done' && now() > dueAt" },
    "internalScore": { "type": "number", "x-visibility": "private" },
    "attachment": { "x-kind": "blob", "x-maxBytes": 1048576, "x-contentTypes": ["image/*", "application/pdf"], "x-thumbnails": [64] }
  },
  "required": ["title"],
  "additionalProperties": false
}
```

## Identity and versions

- `$id` is `urn:<namespace>:feature:<name>/v<N>` (`urn:app:...` for apps,
  `urn:sys:...` is reserved). The short name (`task`) is used in URLs.
- Traits use `urn:<ns>:trait:<name>/v<N>` and are merged into types listed
  in `x-traits`.
- Every stored feature remembers the version it was written with. Bump the
  version for breaking changes and add a migration ([guide 8](08-testing-and-deploy.md#data-migrations)).

## Allowed JSON Schema

Allowed: `type`, `properties`, `required`, `items`, `enum`, `const`,
`default`, `title`, `description`, `minimum`, `maximum`,
`exclusiveMinimum`, `exclusiveMaximum`, `minLength`, `maxLength`,
`pattern`, `minItems`, `maxItems`, `uniqueItems`, `format` (`date-time`,
`date`, `email`, `uri`, `uuid` are enforced), `readOnly`, `writeOnly`,
`$defs`, `$ref`; restricted: `oneOf` (tagged unions of objects whose
branches share a `const` discriminator), `additionalProperties` (must be
`false` on types).

Rejected (validation fails): `anyOf`, `allOf`, `not`, `if`/`then`/`else`,
`patternProperties`, `dependent*`, `contains`, `prefixItems`,
`multipleOf`, `minProperties`/`maxProperties`, `examples`, `$comment`, and
the other dynamic keywords. Lists of lists are not supported.

## Value kinds

The kind is inferred from `type`/`enum`/`format`, or set with `x-kind`:

| Kind | Declare as | Wire form |
| --- | --- | --- |
| string, boolean, number, enum, object, list | plain JSON Schema | JSON |
| integer (64-bit) | `"type": "integer"` | number; `"x-kind": "int64"` with `"type": "string"` sends a string |
| decimal (exact) | `"type": "string", "x-kind": "decimal"` | string, e.g. `"3.50"` |
| time | `"type": "string", "format": "date-time"` | RFC 3339 UTC string |
| ref (feature ID) | `"type": "string", "x-kind": "ref", "x-ref": "<type or trait ID>"` | ID string |
| blob (file) | `"x-kind": "blob"` | `{id, size, contentType}`; reads add `url`, `thumbnails` |
| json (opaque) | `"x-kind": "json"` | any JSON; not queryable |

Nullable fields use `"type": ["string", "null"]` or `null` in an enum.

## The x- vocabulary

On the type:

| Keyword | Meaning |
| --- | --- |
| `x-traits` | Trait IDs merged into the type (e.g. `urn:sys:trait:ownable` adds `owner`, `urn:sys:trait:timestamped` adds `createdAt`/`updatedAt`, `urn:sys:trait:taggable` adds `tags`). |
| `x-allowedChildren` | Types that may be created under features of this type. |
| `x-uniqueName` | A field that must be unique among siblings (e.g. `"slug"`). |
| `x-shareable` | `{"actions": ["approve"], "maxDays": 30}`: the type gets the built-in `share` action, which creates expiring links for people without accounts ([Policies](04-policies.md#share-links)). |
| `x-retention` | How long soft-deleted features are kept before purge (Go duration, e.g. `"720h"`). |
| `x-repository` | Repository name from the server configuration (default `default`). |

On fields:

| Keyword | Meaning |
| --- | --- |
| `x-indexed` | Index for queries and sorting (scalars and lists of scalars). Unindexed fields can only be filtered on small types. |
| `x-searchable` | Include in full-text search (strings). |
| `x-visibility` | `public` (default), `member` (signed-in users, teams, and service accounts; not anonymous visitors or share links), `owner` (owners, admins, system), `private` (system only; admins cannot read either), `system`. |
| `x-write` | Who may write: `any` (default, subject to policy), `owner`, `admin`, `system`, `action:<name>` (only that action, e.g. state machines use `action:transition`), or `role:<name>` (callers holding the role app-wide or through a grant on the feature; on create, on the parent; admins always). A list allows any of its rules: `["action:addLine", "action:receive", "role:manager"]`. Policies still decide whether the write happens at all. |
| `x-immutable` | Set on create, never changed. |
| `readOnly` / `writeOnly` | Never accepted from clients / never returned. |
| `x-ref`, `x-onDelete` | Target of a ref, and what happens when the target is deleted: `restrict`, `setNull` (field must be nullable), or `dangle`. |
| `x-computed` | Expression evaluated on every write; read-only. |
| `x-sequence` | Gapless numbers for a string field (invoices): `{"format": "INV-{year}-{n:06}", "series": "'INV'", "on": "create"}`. See [Sequence numbers](#sequence-numbers). |
| `x-aggregate` | Maintained in the background from children: `{"op": "count"\|"sum"\|"min"\|"max"\|"avg", "of": "<child type>", "field": "/x", "where": "<expr>"}`. |
| `x-stateMachine` | On an enum: `{"initial": "...", "transitions": [{"from", "to", "action", "guard"?}]}`. Each transition is an action; `guard` is an expression. |
| `x-stream` | Live update behaviour: `{"mode": "state" \| "append", "maxRate": <per second>}`. State mode coalesces bursts. |
| `x-persist: false` | Ephemeral field: streamed to subscribers, never stored (presence, typing indicators, LLM drafts). |
| `x-maxBytes`, `x-contentTypes`, `x-thumbnails` | Blob limits (default 25 MiB), allowed types (`image/*` patterns), thumbnail sizes in pixels (JPEG, PNG, and GIF only). |

## Sequence numbers

```json
"number": { "type": "string", "x-indexed": true,
            "x-sequence": { "format": "INV-{year}-{n:06}", "series": "'INV-' + branch", "on": "action:issue" } }
```

- The number is drawn inside the commit that creates the feature (or runs
  the `on` action): numbers are sequential per series with no gaps, also
  under concurrent writes, and a failed write uses no number.
- `format`: `{n}` or `{n:06}` (zero-padded) once; `{year}`, `{yy}`,
  `{month}`, `{day}` (UTC commit time); `{series}`.
- `series`: an expression over the fields naming the counter (default:
  one per type and field). Use it for a series per year
  (`"'INV-' + now().Format('2006')"`), branch, or document kind.
- `on`: `create` (default) or `action:<name>`, a transition or flow
  action; the field is numbered once.
- The field must be a string, `x-indexed`, and not `required`; nobody can
  write it.
- Deleting a numbered feature leaves a hole: cancel invoices with a
  status instead.

Expressions in `x-computed`, `x-aggregate.where`, and guards see the
feature's fields directly (`dueAt`, `status`) plus `now()`; guards also see
`principal` and `feature`. See [Expressions](05-flows.md#expressions).

## Views

For each audience (`public`, `owner`, `admin`, `system`) and direction
(read, create, update, query, event) the compiler derives a view: the
fields that audience can see or write. The API always projects through the
caller's view, so a client never receives a field it may not read.
`GET /v1/schema/types/<name>` returns the views as JSON Schemas; codegen
builds client types from them.

## System types

Always present: `root`, `user`, `team` (with `members`), `organization`,
`serviceAccount`, `grant` (`subject`, `role`, `object`), `flowRun`,
`blob`, `migration`, `shareLink` (`object`, `actions`, `expiresAt`, `createdBy`, `note`). Apps may write policies for them; the defaults let
signed-in users read users and teams, and grants are managed by principals
with the `admin` role on the granted feature (see [Policies](04-policies.md#defaults-for-system-types)).

## Checklist

- [ ] `additionalProperties: false` on every type.
- [ ] Index every field you filter or sort on.
- [ ] Use `x-visibility` instead of relying on clients to hide fields.
- [ ] Use `x-write: action:<name>` for fields that only change through business actions.
- [ ] Run `organon validate`; errors name the file and JSON Pointer.
