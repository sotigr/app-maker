# 10. Docker

One image contains the framework: `organon` (CLI), `organond` (server), Python 3 and
`uv` for code nodes, and CA certificates. It contains no app. You provide
the manifest by mounting it at `/app` or by building an image `FROM` it.

```sh
make docker            # = docker build -t organon .
```

| Path | Purpose |
| --- | --- |
| `/app` | The manifest (`ORGANON_MANIFEST=/app`). Read-only is fine for `organond`. |
| `/data` | Server state: `app.db` (SQLite: features, credentials, jobs, deploys, audit), `blobs/`, `secrets/data.key`. **Must be a persistent volume.** |
| `/usr/share/organon/examples/tracker` | The example app, for trying things out. |

Ports: `8080` (API, admin console at `/admin`, streaming) and `9090`
(health: `/livez`, `/readyz`, `/startupz`). The container runs as user
`organon` (uid 10001) and starts `organond` by default.

## Develop in a container

The CLI runs anywhere the image runs; no Go toolchain needed.

```sh
# Run every organon command with your manifest mounted and your user ID, so the
# files organon writes (organon init, .organon/dev.db) belong to you.
alias organon='docker run --rm -it --user "$(id -u):$(id -g)" -v "$PWD:/work" -w /work -p 8080:8080 organon organon'

organon init myapp
organon validate myapp
organon test myapp
organon dev myapp --addr :8080        # http://localhost:8080/admin, admin@localhost / admin-password-dev
```

`organon dev` keeps its state in `myapp/.organon/` (database, blobs, data key) and
reloads within a second when you edit files on the host. Use `--addr :8080`
so the server listens on all interfaces inside the container.

To try the example without any files of your own:

```sh
docker run --rm -it -p 8080:8080 organon organon dev /usr/share/organon/examples/tracker --addr :8080
```

State is lost when the container stops, which is fine for a demo.

## Run in production

### Option A: mount the manifest

```sh
docker volume create appdata
docker run -d --name myapp -p 8080:8080 \
  -v "$PWD/myapp:/app:ro" -v appdata:/data \
  -e ORGANON_ENV=prod \
  -e ORGANON_BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
  -e ORGANON_BOOTSTRAP_ADMIN_PASSWORD='a-long-password' \
  -e ORGANON_CURSOR_KEY='a-long-random-string' \
  -e ORGANON_PUBLIC_URL=https://app.example.com \
  -e ORGANON_SECURE_COOKIES=true \
  organon
```

### Option B: build an app image

```dockerfile
FROM organon
COPY myapp /app
ENV ORGANON_ENV=prod
```

The app image is the unit you ship: versioned, reproducible, rolled back by
running the previous tag. See [`examples/docker`](../../examples/docker)
for a Dockerfile and a compose file with both a development and a
production service.

### How the server picks its manifest

`organond` records every manifest it runs in `/data`. At startup:

- **A manifest directory it has never run is a new release** (a new app
  image, or new files in the mount). It is deployed, after the same
  breaking-change check as `organon apply`: breaking changes need a migration in
  `migrations/`, or `ORGANON_ALLOW_BREAKING=true`. Otherwise startup fails and
  the previous container keeps serving if you deploy with a rolling
  update.
- **A directory it has run before** yields to anything deployed later with
  `organon apply`. So you can also run one generic container and update it with
  `organon plan` / `organon apply` from your machine; a restart does not undo that.

Both styles work. Pick one per app; mixing them works but makes it harder
to see what runs where.

### Settings worth setting

| Variable | Why |
| --- | --- |
| `ORGANON_ENV` | Which `env/<env>.json` to use (default `dev`). |
| `ORGANON_BOOTSTRAP_ADMIN_EMAIL`, `ORGANON_BOOTSTRAP_ADMIN_PASSWORD` | Required on the first start (empty database); ignored afterwards. |
| `ORGANON_CURSOR_KEY` | Keeps pagination cursors and signed file URLs valid across restarts. |
| `ORGANON_PUBLIC_URL`, `ORGANON_SECURE_COOKIES=true` | Behind HTTPS (required for OIDC redirects). |
| `ORGANON_TRUST_PROXY_HEADERS=true` | Only behind a reverse proxy you control: rate limits then use the client IP from `X-Forwarded-For`. |
| `ORGANON_SECRET_<NAME>` | Values of `${secret:<name>}` in the manifest (OIDC, webhooks, LLM keys). |
| `ORGANON_DATA_KEY_FILE` | The data key that encrypts TOTP secrets. The image creates `/data/secrets/data.key` on first start (`ORGANON_CREATE_DATA_KEY=true`). For stronger separation, mount the key from a secret store and set `ORGANON_CREATE_DATA_KEY=false`. |
| `ORGANON_OTLP_ENDPOINT` | Send traces and metrics to a collector. |
| `--config /etc/organon/organond.yaml` | Everything else ([guide 9](09-operations.md)); mount the file. |

Use Docker or Compose secrets (or your orchestrator's) rather than plain
`-e` for passwords and keys.

### Limits of the current image

- **One replica per volume.** SQLite takes an exclusive lock on
  `/data/app.db`, so a second container on the same volume refuses to
  start. Scale up (CPU, memory), not out. PostgreSQL support, which allows
  several replicas, is not available yet.
- **Code nodes run as processes** inside the container. That is fine for
  code you write yourself; the container boundary is the outer sandbox.
  Give the container memory limits that cover `runner.maxWorkers` ×
  `memoryMb`.
- **Python dependencies** (`nodes/requirements.lock`) are installed by
  `uv` on first use into `/tmp`, so they are rebuilt after a restart. Code
  nodes need network access to the package index for that, or none at all
  when you use only the standard library.
- **Backups**: copy `/data` while the container is stopped, or use
  `sqlite3 /data/app.db ".backup ..."` (not in the image; run it from
  another container that mounts the volume) and copy `blobs/` and
  `secrets/`.

## Check a container

```sh
docker exec myapp organon --help                          # the CLI is in the image too
curl -s localhost:8080/v1/auth/session               # 401 until you log in
docker run --rm --network container:myapp organon \
  python3 -c "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:9090/readyz').status)"
```
