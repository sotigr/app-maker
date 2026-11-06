# 2. The manifest

## Layout

```
myapp/
  app.json                 # name, version, auth, roles, limits, LLM aliases, settings
  env/dev.json             # values for ${env.*} per environment (dev, staging, prod, ...)
  types/*.json             # feature types (JSON Schema)          → 3. Types
  traits/*.json            # reusable field groups (JSON Schema)  → 3. Types
  policies/*.json          # access rules                         → 4. Policies
  flows/*.json             # behaviour graphs                     → 5. Flows
  nodes/*.py               # Python code-node sources             → 6. Code nodes
  nodes/requirements.lock  # optional Python dependencies (with hashes)
  templates/email/*.txt    # email templates for email.send
  migrations/*.json        # data migrations                      → 8. Testing and deploying
  seed/dev.json            # data loaded by organon dev / organon seed
  tests/*.test.json        # manifest tests                       → 8. Testing and deploying
  ui/app.json              # end-user UI shell                    → 11. User interface
  ui/pages/*.json          # UI pages (component trees)           → 11. User interface
  ui/i18n/<lang>.json      # UI translations                      → 11. User interface
  ui/styles.css, ui/assets/**  # app CSS, images, fonts           → 11. User interface
  modules/<name>/...       # optional: groups of types/traits/policies/flows/nodes
```

Every file is plain JSON (or text for templates and Python). Keep the
directory in version control; production servers are changed only through
`organon plan` / `organon apply` (editing in place is limited to `organon dev`).

## app.json

```json
{
  "app": "tracker",
  "version": "1.0.0",
  "framework": ">=1.0 <2.0",
  "repositories": { "default": "default" },
  "auth": {
    "password": { "enabled": true, "minLength": 12 },
    "oidc": [
      { "provider": "google", "clientId": "${env.oidc.clientId}", "clientSecret": "${secret:oidc-google}",
        "signup": false, "allowedDomains": ["example.com"] }
    ],
    "mfa": { "requiredForRoles": ["admin"] }
  },
  "roles": { "viewer": [], "editor": ["viewer"], "admin": ["editor"] },
  "limits": { "requestsPerMinute": 600, "maxDocumentBytes": 262144 },
  "llm": { "aliases": { "default": "${env.llm.default}" }, "monthlyBudgetUsd": 50 },
  "python": { "allowedPackages": ["httpx", "pydantic"] },
  "settings": { "supportEmail": "${env.supportEmail}" }
}
```

| Key | Meaning |
| --- | --- |
| `roles` | Role names and what each includes (`editor` includes `viewer`). Roles are granted per subtree (grants) or app-wide (a user's `roles`). |
| `auth.password` | Password login; users are created by admins (no self-signup). |
| `auth.oidc[]` | OpenID Connect providers. `google` and `gitlab` have default issuers; others need `issuer`. `signup: false` (default) means only identities linked through an admin invite can log in; `signup: true` creates users on first login, limited to verified emails in `allowedDomains` when set. |
| `auth.mfa` | TOTP second factor for `requiredForRoles`, or `requiredForAll: true`. |
| `limits` | May only lower the server defaults. |
| `llm.aliases` | Model aliases used by flows (`default`, `fast`, `large`, or your own) mapped to `"<provider>:<model>"`. Without a prefix the provider is `anthropic`. Defaults: `default` and `large` → `anthropic:claude-opus-5-5`, `fast` → `anthropic:claude-haiku-5-5`. |
| `python.allowedPackages` | When set, every package in `nodes/*.lock` must be listed. |
| `settings` | Free-form values exposed to flows as `env.<key>`. |

## Environments and secrets

- `${env.a.b}` is replaced with `a.b` from `env/<env>.json`. The environment
  is chosen with `--env` (CLI) or `ORGANON_ENV` (server); default `dev`. A missing
  value is a validation error.
- `${secret:name}` is never written into the manifest. The server resolves
  it from the environment variable `ORGANON_SECRET_<NAME>` (upper case, `-` and
  `.` become `_`), e.g. `${secret:oidc-google}` → `ORGANON_SECRET_OIDC_GOOGLE`.
  Secrets are used for OIDC client secrets, webhook signing keys, LLM
  provider keys, and API keys of outbound requests. Their values never
  reach flow expressions, run records, or code nodes.
- A secret used by `http.request` or `ctx.http` must list the hosts it may
  be sent to (`*.domain` allowed); `organon plan` shows changes:

  ```json
  "secrets": { "stripe": { "hosts": ["api.stripe.com"] } }
  ```

## Seed data

`seed/<env>.json` is loaded by `organon dev` on a fresh database and by
`organon seed` against a running server:

```json
{
  "users": [ { "ref": "ana", "email": "ana@example.com", "password": "ana-password-123", "name": "Ana", "roles": ["editor"] } ],
  "features": [
    { "ref": "launch", "type": "project", "owner": "ana", "data": { "title": "Launch", "slug": "launch" } },
    { "ref": "copy", "type": "task", "parent": "launch", "data": { "title": "Write copy", "assignee": "ana" } }
  ],
  "grants": [ { "subject": "ana", "object": "launch", "role": "editor" } ]
}
```

`ref` names are local to the file; use them anywhere an ID is expected.

## Modules

`modules/<name>/` may contain its own `types/`, `traits/`, `policies/`,
`flows/`, `nodes/`, and `ui/pages/`. Type IDs in a module carry its name
(`urn:app:crm:feature:contact/v1`). Modules share one tree, one set of
principals, and one deployment; they organize files, they do not isolate.
