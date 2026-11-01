# 9. Running the server

`fwd` serves one app. It reads the manifest directory at startup and on
every `fw apply`.

```sh
bin/fwd --manifest ./myapp --env prod --config fwd.yaml
```

Flags: `--manifest`, `--env`, `--config` (YAML), `--roles`
(`api,gateway,worker,runner`), `--dev`. Every setting can also come from
`FW_*` environment variables.

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
  default: { driver: sqlite, dsn: /var/lib/fw/app.db }   # memory | sqlite
credentials: { driver: sqlite }          # defaults to the default repository's database
secrets:
  cursorKey: "..."                       # signs cursors and URLs; random per process if empty
  dataKeyFile: /etc/fw/data.key          # 64 hex chars; encrypts TOTP secrets
  createDataKey: false                   # create it on first start (the Docker image does)
bootstrap: { adminEmail: admin@example.com, adminPassword: "..." }   # first start only
limits: { requestsPerMinute: 600, anonymousPerMinute: 60 }
blobs: { driver: fs, dir: /var/lib/fw/blobs }
runner: { isolation: process, maxWorkers: 4, cacheDir: /var/cache/fw-runner }
llm:
  providers:
    anthropic: { kind: anthropic, apiKey: "${secret:anthropic-key}" }
observability: { otlpEndpoint: "http://otel-collector:4318", sampleRatio: 1 }
audit: { retentionDays: 90 }
log: { level: info, format: json }
```

Environment variables: `FW_ROLES`, `FW_TOPOLOGY`, `FW_DEV`, `FW_MANIFEST`,
`FW_ENV`, `FW_HTTP_ADDR`, `FW_INTERNAL_ADDR`, `FW_SECURE_COOKIES`,
`FW_PUBLIC_URL`, `FW_REPOSITORY_DEFAULT_DRIVER`, `FW_REPOSITORY_DEFAULT_DSN`,
`FW_CREDENTIALS_DRIVER`, `FW_CREDENTIALS_DSN`, `FW_JOBS_DRIVER`,
`FW_CURSOR_KEY`, `FW_DATA_KEY_FILE`, `FW_BOOTSTRAP_ADMIN_EMAIL`,
`FW_BOOTSTRAP_ADMIN_PASSWORD`, `FW_LOG_LEVEL`, `FW_LOG_FORMAT`,
`FW_LLM_PROVIDER`, `FW_RUNNER_ISOLATION`, `FW_RUNNER_LISTEN`,
`FW_RUNNER_ADDR`, `FW_BLOBS_DRIVER`, `FW_BLOBS_DIR`, `FW_OTLP_ENDPOINT`,
`FW_TRUST_PROXY_HEADERS`, `FW_CREATE_DATA_KEY`, `FW_ALLOW_BREAKING`, and
`FW_SECRET_<NAME>` for `${secret:<name>}` references. Invalid
configuration stops startup with an error naming the key.

## Which manifest runs

`fwd` records every manifest it runs in its database. At startup, a
manifest directory it has never run is deployed (after the `fw apply`
breaking-change check; `FW_ALLOW_BREAKING=true` overrides it), while a
directory it has run before yields to later `fw apply` deploys. New app
images and `fw apply` therefore both work. `fw dev` always runs the
directory.

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
  errors; `fw runs list`.

## Backups

Back up the SQLite file (it is in WAL mode: use `sqlite3 app.db ".backup
out.db"` or stop the server), the blob directory, and the data key file.
`fw export` gives a logical export of readable features.
