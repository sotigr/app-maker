# 9. Running the server

`organond` serves one app. It reads the manifest directory at startup and on
every `organon apply`.

```sh
bin/organond --manifest ./myapp --env prod --config organond.yaml
```

Flags: `--manifest`, `--env`, `--config` (YAML), `--roles`
(`api,gateway,worker,runner`), `--dev`. Every setting can also come from
`ORGANON_*` environment variables.

## Configuration

```yaml
roles: [api, gateway, worker, runner]    # default: all in one process
topology: single                         # single | replicated
env: prod
http:
  addr: ":8080"
  internalAddr: ":9090"                  # /livez /readyz /startupz
  publicURL: "https://app.example.com"   # OIDC redirect URIs
  secureCookies: true
  trustProxyHeaders: false               # client IP from X-Forwarded-For
allowBreaking: false                     # deploy a breaking manifest directory at startup
repositories:
  default: { driver: sqlite, dsn: /var/lib/organon/app.db }   # memory | sqlite
credentials: { driver: sqlite }          # defaults to the default repository's database
secrets:
  cursorKey: "..."                       # signs cursors and URLs; random per process if empty
  dataKeyFile: /etc/organon/data.key          # 64 hex chars; encrypts TOTP secrets
  createDataKey: false                   # create it on first start (the Docker image does)
bootstrap: { adminEmail: admin@example.com, adminPassword: "..." }   # first start only
admin:
  disableConsole: false                  # true: /admin is not served
  disableAccounts: false                 # true: admins cannot sign in; no bootstrap admin
limits: { requestsPerMinute: 600, anonymousPerMinute: 60 }
blobs: { driver: fs, dir: /var/lib/organon/blobs }
email: { driver: smtp, addr: "smtp.example.com:587", from: "shop@example.com", username: "shop", password: "${secret:smtp}", tls: false }   # default driver: log
runner: { isolation: process, maxWorkers: 4, cacheDir: /var/cache/organon-runner }
llm:
  providers:
    anthropic: { kind: anthropic, apiKey: "${secret:anthropic-key}" }
observability: { otlpEndpoint: "http://otel-collector:4318", sampleRatio: 1 }
audit: { retentionDays: 90 }
log: { level: info, format: json }
```

Environment variables: `ORGANON_ROLES`, `ORGANON_TOPOLOGY`, `ORGANON_DEV`, `ORGANON_MANIFEST`,
`ORGANON_ENV`, `ORGANON_HTTP_ADDR`, `ORGANON_INTERNAL_ADDR`, `ORGANON_SECURE_COOKIES`,
`ORGANON_PUBLIC_URL`, `ORGANON_REPOSITORY_DEFAULT_DRIVER`, `ORGANON_REPOSITORY_DEFAULT_DSN`,
`ORGANON_CREDENTIALS_DRIVER`, `ORGANON_CREDENTIALS_DSN`, `ORGANON_JOBS_DRIVER`,
`ORGANON_CURSOR_KEY`, `ORGANON_DATA_KEY_FILE`, `ORGANON_BOOTSTRAP_ADMIN_EMAIL`,
`ORGANON_BOOTSTRAP_ADMIN_PASSWORD`, `ORGANON_LOG_LEVEL`, `ORGANON_LOG_FORMAT`,
`ORGANON_LLM_PROVIDER`, `ORGANON_RUNNER_ISOLATION`, `ORGANON_RUNNER_LISTEN`,
`ORGANON_RUNNER_ADDR`, `ORGANON_BLOBS_DRIVER`, `ORGANON_BLOBS_DIR`, `ORGANON_OTLP_ENDPOINT`,
`ORGANON_TRUST_PROXY_HEADERS`, `ORGANON_CREATE_DATA_KEY`, `ORGANON_ALLOW_BREAKING`,
`ORGANON_DISABLE_ADMIN_CONSOLE`, `ORGANON_DISABLE_ADMIN_ACCOUNTS`,
`ORGANON_EMAIL_DRIVER` (`log` or `smtp`), `ORGANON_EMAIL_ADDR`, `ORGANON_EMAIL_FROM`,
`ORGANON_EMAIL_USERNAME`, `ORGANON_EMAIL_PASSWORD` (a `${secret:<name>}` reference),
`ORGANON_EMAIL_TLS` (implicit TLS, port 465; otherwise STARTTLS when offered), and
`ORGANON_SECRET_<NAME>` for `${secret:<name>}` references. Invalid
configuration stops startup with an error naming the key.

## Running without admins

For a production instance that should expose only the app:

| Setting | Effect |
| --- | --- |
| `ORGANON_DISABLE_ADMIN_CONSOLE=true` | `/admin` is not served (404). The admin API stays, for admins. |
| `ORGANON_DISABLE_ADMIN_ACCOUNTS=true` | Any user or service account with the `admin` role is refused: password and OIDC sign-in return `403 admin accounts are disabled`; its sessions and API keys return 401. No bootstrap admin is created, and admins cannot be created. Other users are unaffected. |

With admin accounts disabled, everything that needs an admin is
unavailable: `organon plan`/`deploy` over HTTP, creating users with
passwords, the audit log, flow-run and job retries, and the manifest
editors. Deploy by restarting `organond` with the new manifest directory
(`--manifest`; see below). Existing users keep signing in; new users
need an OIDC provider with `signup: true` (optionally limited by
`allowedDomains`), so create them before switching admins off otherwise.
Unset the variable and restart to get the admins back; their accounts
are kept.

## Which manifest runs

`organond` records every manifest it runs in its database. At startup, a
manifest directory it has never run is deployed (after the `organon apply`
breaking-change check; `ORGANON_ALLOW_BREAKING=true` overrides it), while a
directory it has run before yields to later `organon apply` deploys. New app
images and `organon apply` therefore both work. `organon dev` always runs the
directory.

## People and access in the console

`/admin` → **People** manages accounts without typing IDs or JSON:

- **Users:** search by name or email, filter by status and role. **Add
  user** asks for email, name, roles, and how they sign in: a password you
  set (or generate; shown once) or, with OIDC providers, an invite link to
  send. A user's page sets app-wide roles, lists their grants (add one by
  searching for the record and choosing a role), and handles security:
  set a new password, create an OIDC invite, reset two-factor, sign out
  everywhere, suspend or reactivate. API keys are created (shown once) and
  revoked there too, and recent activity links to the audit log.
- **Teams:** members are picked by name; the team's grants and roles
  apply to every member.
- **Service accounts:** roles, grants, and API keys for integrations.
- Every record's page in **Data** has an **Access** section: who holds
  which role on it, directly or through a parent, with add and remove.

## Roles

| Role | Does |
| --- | --- |
| `api` | REST API, actions, admin console. |
| `gateway` | WebSocket and SSE streaming. |
| `worker` | Background jobs: event flows, schedules, effects, aggregates, search indexing, migrations, cleanups. |
| `runner` | Python code-node workers (gRPC on a private Unix socket, or `runner.listen`; the engine connects to `runner.addr` when the runner runs elsewhere). |

## Storage and topology

What is available today:

- **SQLite** (`driver: sqlite`): one replica per database file (the server
  takes an exclusive lock). Jobs then run on River in the same database,
  inserted in the same transaction as the writes that cause them.
- **memory**: for tests and demos; data is lost on restart.
- Blobs on the local filesystem; full-text search in memory (rebuilt from
  the database when a worker starts); the change log in memory.

`topology: replicated` refuses single-machine adapters (memory, SQLite,
filesystem blobs, process isolation). PostgreSQL, Valkey, S3, and container
isolation are planned and need those services.

## Limits

| Limit | Default |
| --- | --- |
| Request body | 1 MiB (uploads use signed URLs) |
| Document size | 256 KiB (`app.json` may lower) |
| Nesting depth / list items | 8 / 1,000 |
| Transaction operations | 100 |
| Query predicates / depth / page | 20 / 4 / 500 |
| Requests per principal | 600 per minute (`app.json` may lower) |
| Anonymous requests per IP | 60 per minute |
| Login attempts | 10 per account per 15 minutes |
| WebSocket subscriptions per connection | 200 |
| Streaming connections per principal | 20 |
| Ephemeral writes per feature | 30 per second |

## Observability

- **Traces and metrics** go to an OTLP/HTTP collector when
  `observability.otlpEndpoint` (or `OTEL_EXPORTER_OTLP_ENDPOINT`) is set.
  A trace follows a request into the jobs it causes. Spans: HTTP routes,
  `job <kind>`, `flow <name>`, `llm <alias>`, `code <node>`. Metrics:
  `http.server.request.duration`, `job.duration`, `flow.runs`,
  `llm.tokens`, `llm.cost_usd`, `codenode.cold_start_ms`,
  `stream.delivery_latency_s`.
- **Logs** are JSON with `trace_id`/`span_id`; field values are never logged.
- **Audit log**: one row per change (who, what, which fields; never the
  values) plus login and MFA events, written with the change. Admins read
  it at `GET /v1/audit` or in the console; kept `audit.retentionDays`.
- **Flow runs** are `flowRun` features: status, per-node timing, LLM calls,
  errors; `organon runs list`.

## Backups

Back up the SQLite file (it is in WAL mode: use `sqlite3 app.db ".backup
out.db"` or stop the server), the blob directory, and the data key file.
`organon export` gives a logical export of readable features.
