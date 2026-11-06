# 4. Policies

A policy file in `policies/` gives one boolean expression per operation of
a type (or trait). Anything not allowed is denied.

```json
{
  "type": "urn:app:feature:task",
  "read":   "hasRole(feature, 'viewer') || isOwner(feature)",
  "create": "hasRole(parent, 'editor')",
  "update": "hasRole(feature, 'editor') || principal.id == feature.assignee",
  "delete": "hasRole(feature, 'admin') || isOwner(feature)",
  "move":   "hasRole(feature, 'admin')",
  "actions": {
    "start":    "hasRole(feature, 'editor')",
    "complete": "principal.id == feature.assignee",
    "reopen":   "hasRole(feature, 'admin')"
  }
}
```

## Variables and functions

| Name | Meaning |
| --- | --- |
| `principal` | `{id, kind, roles, groups}` of the caller (`kind`: `user`, `team`, `organization`, `service`, `system`, `anonymous`). |
| `feature` | The feature acted on (its fields plus `id`, `parentId`, `owner`). For create it is the new data. |
| `parent` | The parent (create, move). |
| `patch` | The merge patch of an update. |
| `input` | Action input. |
| `hasRole(f, role)` | True when the caller holds `role` (or a role that includes it, per `app.json` `roles`) app-wide, or through a grant on `f` or any ancestor. |
| `isOwner(f)` | True when the caller owns `f` (directly or through a team) or `f` is the caller's own principal feature. |
| `inGroup(id)` | True when the caller is a member of team/organization `id`. |
| `now()` | Current time. |

## How rules apply

- **Reads** are also turned into query filters, so lists and searches only
  return readable features. `organon validate` warns when a read rule cannot be
  translated (it then needs to scan).
- Callers who cannot read a feature get `404`, never `403`, so existence is
  not leaked. Anonymous callers get `401`.
- **Actions** need their action rule; state-machine transitions also need
  their `guard`. Fields with `x-write: action:<name>` change only inside
  that action.
- Field rules (`x-visibility`, `x-write`) apply on top of policies.
- The app role `admin` bypasses policies but still cannot read `private`
  fields. The system principal (flows with `runAs: system`, jobs) bypasses
  everything.
- Policies on a trait apply to every type with that trait (e.g. the
  built-in `transfer` action of `urn:sys:trait:ownable`).

## Grants and teams

A grant gives a role on a subtree: it is a `grant` feature created as a
child of the object it applies to.

```sh
curl -H "$H" "localhost:8080/v1/features/grant?parent=$PROJECT" \
  -d '{"subject":"<user or team id>","role":"editor"}'
```

Teams (`team` features with `members`) and organizations can hold grants
and own features; members inherit them.

## Share links

A type with `x-shareable` gets the action `share` (give it a rule under
`actions`, like any action). It returns `{url, token, expiresAt, ...}`;
anyone with the URL can, until it expires or is revoked:

- read the shared feature and everything under it (public fields only);
- run the actions listed in `x-shareable.actions` on the shared feature,
  including that action's own writes (its transition, or its flow's
  updates of the feature and creates under it).

Nothing else: no other writes, deletes, uploads, or new links. App
policies are not consulted for link holders. In expressions they are
`principal.kind == 'link'` and `principal.object` is the shared feature.

```json
"x-shareable": { "actions": ["approveEstimate"], "maxDays": 30 }
```

```sh
curl -H "$H" localhost:8080/v1/actions/repairOrder/$ID/share -d '{"expiresInDays": 14, "note": "for the customer"}'
```

- Input: `expiresInDays` (default 7, at most `maxDays`), `actions` (a
  subset), `note`.
- `GET /s/<token>` stores the link in an HttpOnly cookie and opens the
  app; scripts send `Authorization: Link <token>`.
- Revoke by deleting the `shareLink` feature (a child of the shared
  feature); its creator and admins of the shared feature may.
- Flows that run because of a link action run as the link holder.

## Defaults for system types

When the manifest has no policy for them: signed-in users read `root`,
`user`, `team`, `organization`; users update themselves; owners manage
their teams, organizations, and service accounts; grants are readable by
their subject and managed by holders of `admin` on the granted feature.
Run records (`flowRun`), blob intents, migrations, and the audit log are
admin-only.

## Testing policies

Write a permission matrix as a manifest test ([guide 8](08-testing-and-deploy.md#manifest-tests)):
for each principal, assert `ok` or the expected error (`forbidden`,
`not_found`).
