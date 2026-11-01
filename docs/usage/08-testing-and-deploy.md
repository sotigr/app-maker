# 8. Testing, deploying, migrating

## Manifest tests

`tests/*.test.json` describe fixtures and steps; `organon test` runs each test
on a fresh in-memory instance (fake LLM, recorded emails, HTTP fixtures).

```json
[
  {
    "name": "only the assignee can complete a task",
    "given": {
      "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user", "roles": [] } },
      "features": [
        { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
        { "ref": "t", "type": "task", "parent": "p", "owner": "ana",
          "data": { "title": "T", "status": "in_progress", "assignee": "ben" } }
      ],
      "grants": [ { "subject": "ben", "object": "p", "role": "editor" } ]
    },
    "steps": [
      { "as": "ana", "action": "complete", "on": "t", "expect": { "error": "forbidden" } },
      { "as": "ben", "action": "complete", "on": "t", "input": { "note": "done" },
        "expect": { "ok": true, "feature": { "status": "done" }, "output": { "late": false },
                    "effects": [ { "node": "email.send", "count": 0 } ] } }
    ],
    "http": [ { "method": "GET", "url": "https://api.example.com/x", "status": 200, "body": { "ok": true } } ]
  }
]
```

Steps (one operation each, run as `as`):

| Step | Fields |
| --- | --- |
| action | `action`, `on` (ref), `input` |
| create | `create` (type), `parent` (ref), `data`, `ref` (name the result) |
| update | `update` (ref), `data` |
| delete | `delete` (ref) |
| read | `read` (ref) |
| query | `query` (query document) |
| page | `page` (UI page name); expect `visible` |

`expect`: `ok`, `error` (code), `feature` (subset of fields after the
step), `output` (action result subset), `count` (query results),
`effects` (`[{node, count}]`), `visible` (page steps). Unmatched outbound HTTP requests fail the
step.

## Plan and apply

```sh
bin/organon login --server https://app.example.com --email admin@example.com   # stores a token in ~/.config/organon/credentials.json
bin/organon plan myapp            # diff against the running server
bin/organon apply myapp --plan <hash>
```

The plan lists type changes (and whether they are breaking), policy and
flow changes, newly privileged flows, newly public fields, index builds,
migrations, changed `ui/` files, and pages newly visible without login. Breaking changes need a migration (or
`--allow-breaking`). Apply activates the new manifest without a restart.
`ORGANON_SERVER` and `ORGANON_TOKEN` override the stored credentials.

Alternatively, ship the manifest with the server (a new app image or new
files in the mounted directory): `organond` deploys a manifest directory it has
never run at startup, with the same breaking-change check
([Docker](10-docker.md#how-the-server-picks-its-manifest)).

## Data migrations

To change the shape of existing data: bump the type version
(`.../task/v2` → `/v3`), change the schema, and add `migrations/<id>.json`:

```json
{
  "id": "0001-task-priority",
  "type": "urn:app:feature:task",
  "from": 2,
  "to": 3,
  "set": {
    "/priority": "feature.title contains 'urgent' ? 'high' : 'normal'",
    "/progress": "nil"
  },
  "batchSize": 500
}
```

- Each migration moves one version (`to = from + 1`). `set` keys are
  top-level fields of the new version; `"nil"` drops an old field.
  `feature` in expressions is the **old** document.
- Right after the deploy, reads of old features are upcast in memory, so
  clients always see the new shape.
- Background `migrate_batch` jobs rewrite stored features in batches (with
  version checks; resumable; re-running is harmless). Progress is in the
  admin console (Deploys) and in `migration` features. Queries on new
  fields match stored data once a feature has been rewritten.
- For logic beyond expressions use `"flow": "<name>"` instead of `set`;
  that flow must have `runAs: system` and `privileged: true` and is run
  once per feature.
- Migrations are not reversed automatically; write a reverse migration to
  roll back data.

## Operations CLI

| Command | Does |
| --- | --- |
| `organon seed [dir] --env dev` | Loads `seed/<env>.json` into the server. |
| `organon export > data.ndjson` / `organon import data.ndjson` | Every readable feature as NDJSON, parents first. |
| `organon runs list --flow x --status dead_letter` | Recent flow runs. |
| `organon runs retry <run id>` | Retries a dead-lettered run. |
| `organon token create --name ci [--principal <id>]` | API key for yourself or a service account you own. |
| `organon codegen ts [dir] --out src/gen` | TypeScript client. |
| `organon ui catalog [--json]` | UI components, props, and steps. |
| `organon ui schema [--out .organon/schema]` | JSON Schemas of `ui/app.json` and pages, for editor completion via `"$schema"`. |
