# 7. HTTP API and clients

All endpoints are under `/v1`. Requests and responses are JSON. The full
OpenAPI description of your app is at `GET /v1/schema`.

## Authentication

| Method | How |
| --- | --- |
| Browser session | `POST /v1/auth/password {"login","password"}` sets an HttpOnly session cookie and returns `{principal, csrf}`. State-changing requests must send the `X-CSRF-Token` header (the value is also in the `organon_csrf` cookie). |
| Bearer token | `POST /v1/auth/password {"login","password","cli":true}` returns `{token}`; or create an API key with `POST /v1/auth/tokens` / `organon token create`. Send `Authorization: Bearer <token>`. |
| OIDC | Browser to `GET /v1/auth/oidc/<provider>/start?redirect=/app`. `GET /v1/auth/oidc` lists providers. Admins create invite links with `POST /v1/auth/users/<id>/oidc-invite`. |
| MFA | If a login answers `{"mfa": "verify"|"enroll", "ticket"}`, continue with `POST /v1/auth/mfa/verify {ticket, code}` or `POST /v1/auth/mfa/totp/enroll {ticket}` then `/confirm {ticket, code}`. Signed-in users enroll or disable with `/v1/auth/mfa/totp/{enroll,confirm,disable}`. |

Other: `GET /v1/auth/session`, `POST /v1/auth/logout`, `POST /v1/auth/users`
(admins create users).

## Features

| Request | Does |
| --- | --- |
| `POST /v1/features/<type>?parent=<id>` body = data | Create (root when no parent). `201` with the feature. |
| `GET /v1/features/<type>/<id>` | Read; `ETag` is the version. |
| `PATCH /v1/features/<type>/<id>` + `If-Match: "<version>"` | Merge patch (`null` removes a field). |
| `DELETE /v1/features/<type>/<id>` + `If-Match` | Soft delete (children too). |
| `POST /v1/features/<id>/move {"parent","version"}` | Move a subtree. |
| `GET /v1/features/<id>/children?type=&limit=&cursor=` | Children page. |
| `POST /v1/actions/<type>/<id>/<action>` body = input | Run an action; returns `{output, feature?}`. |
| `POST /v1/transactions {"ops": [...]}` | Up to 100 ops atomically: `{"op":"create","type","parent","ref":"$t1","data"}`, `{"op":"update","type","id","version","data"}`, `delete`, `move`. `$refs` link ops; the result maps refs to IDs. |

A feature looks like:

```json
{ "id": "01a1...", "type": "task", "parent": "01a0...", "version": 3,
  "createdAt": "...", "updatedAt": "...", "data": { "title": "Ship it", "status": "todo" } }
```

`Idempotency-Key: <unique>` on any POST makes retries safe: the stored
response is replayed.

## Queries and search

```json
POST /v1/query
{
  "type": "task",
  "where": { "and": [ { "field": "/status", "op": "ne", "value": "done" },
                      { "field": "/dueAt", "op": "lt", "value": "2026-12-01T00:00:00Z" } ] },
  "scope": { "descendantsOf": "<project id>" },
  "sort": [ { "field": "/dueAt", "dir": "asc" } ],
  "select": ["/title", "/dueAt"],
  "limit": 50
}
```

Operators: `eq ne lt lte gt gte in nin prefix contains exists`;
combine with `and`, `or`, `not` (at most 20 predicates, depth 4). Filter
and sort on `x-indexed` fields. The answer is `{items, next}`; pass
`"cursor": next` for the next page (max 500 per page). Only readable
features are returned.

`GET /v1/search?q=words&type=task` runs full-text search over
`x-searchable` fields, with `<mark>` highlights limited to fields you may
read.

## Errors

Errors are RFC 9457 problem details:

```json
{ "type": "about:blank", "title": "Bad Request", "status": 400, "code": "validation_failed",
  "detail": "the request does not match the schema", "errors": [ { "path": "/title", "code": "maxLength", "message": "..." } ] }
```

Common codes: `validation_failed` (400), `unauthenticated` (401),
`forbidden` (403), `not_found` (404), `version_conflict` (409, includes
`current` versions), `invalid_transition` (409), `rate_limited` (429, with
`Retry-After`), `flow_failed`, `flow_timeout`, and codes declared by
`reject` nodes.

## Live updates

Subscribe to a feature, a parent's children, or a query; you get a
snapshot, then deltas with only the fields you may read.

**WebSocket** `GET /v1/ws` (browsers use the session cookie; scripts pass
the subprotocol `organon.bearer.<token>` next to `organon`):

```json
→ {"t":"subscribe","sid":"s1","target":"feature","id":"<id>","paths":["/status","/progress"]}
→ {"t":"subscribe","sid":"s2","target":"children","parent":"<id>","type":"task"}
→ {"t":"subscribe","sid":"s3","target":"query","query":{"type":"task","where":{"field":"/status","op":"eq","value":"todo"}}}
← {"t":"snapshot","sid":"s1","features":[...]}
← {"t":"delta","sid":"s1","id":"...","version":4,"set":{"/status":"done"},"unset":[]}
← {"t":"delta","sid":"s2","id":"...","enter":true, ...}     ← {"t":"leave","sid":"s3","id":"..."}
→ {"t":"unsubscribe","sid":"s1"}
```

On `reset`, discard local state; a fresh snapshot follows. On `error`
(e.g. access revoked) the subscription is closed.

**SSE** `GET /v1/stream?target=feature&id=<id>` (or `?sub=<base64url JSON
target>`): one subscription per connection, same messages as events.

**Ephemeral values** (fields with `x-persist: false`):
`POST /v1/ephemeral/<type>/<id> {"presence": "Ana is here"}` (needs the
update policy; 30/s per feature).

## Files

1. `POST /v1/uploads {"type":"task","field":"attachment","feature":"<id>","size":1234,"contentType":"image/png"}`
   (or `"parent"` for a feature not created yet) → `{blob, uploadUrl, method: "PUT", headers}`.
2. `PUT <uploadUrl>` with the bytes and the given `Content-Type` (valid 15 minutes).
3. Set the returned `blob` (`{id, size, contentType}`) on the field in a
   normal create or update.

Reads return `url` (and `thumbnails`) with short-lived signed links.
Uploads never committed are deleted after 24 hours.

## Other endpoints

`GET /v1/ui` (the UI bundle for the caller, [guide 11](11-ui.md)),
`POST /v1/permissions {"checks": [{"id", "op"}]}` (up to 100 `can()` answers:
`update`, `delete`, `action:<name>`, `create:<type>`; `false` for features
you cannot read), `GET /v1/ui/catalog`, `GET /v1/ui/source`, and
`POST /v1/ui/validate` (admins; the builder), `PUT|DELETE
/v1/dev/manifest/ui/...` (builder saves, `organon dev` only),
`GET /v1/schema/types` and `/v1/schema/types/<name>` (types and their views),
`GET /v1/flows`, `POST /v1/flows/validate`, `POST /v1/flows/runs/<id>/retry`,
`POST /v1/hooks/<name>` (webhook triggers), `GET /v1/audit` (admins:
`?actor=&target=&action=&since=&before=&limit=`), `GET/POST /v1/deploy...`
(used by `organon plan`/`organon apply`). Health probes are on the internal port
(`:9090` by default).

## TypeScript SDK

```sh
bin/organon codegen ts myapp --out web/src/gen     # types, enums, schemas, typed client
```

```ts
import { createClient, type Task } from "./gen";

const client = createClient({ baseUrl: "/" });              // browser: cookies + CSRF
await client.auth.login("ana@example.com", "…");
const p = await client.project.create({ title: "Launch" });
const t: Task = await client.task.create({ title: "Write copy" }, p.id);
await client.task.update(t, { title: "New title" });          // If-Match, optimistic, rolls back on conflict
const r = await client.task.actions.complete(t, { note: "Shipped" });
const stop = client.watch(t.id);                              // live; cache updates by version
const blob = await client.upload({ type: "task", field: "attachment", feature: t.id }, file, file.type);
```

- Generated types come from your read/create/update views: private fields
  do not exist on the client.
- Live subscriptions use one WebSocket and fall back to SSE
  (`createClient({ transport: "auto" | "ws" | "sse" })`).
- `@organon/react` hooks: `useFeature`, `useQuery`, `useChildren`,
  `useAction`, `useSession`.
- `@organon/ui` renders manifest UI pages (`OrganonApp`, `PageView`); it is
  what `organond` serves at `/`.
- Scripts: `createClient({ baseUrl: "https://app.example.com", token })`.
