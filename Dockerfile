# The framework image: fw (CLI) and fwd (server) with everything they need
# at run time. Apps are not part of it: mount a manifest at /app, or build
# FROM this image and COPY the manifest to /app. See docs/usage/10-docker.md.
#
#   docker build -t app-maker .
#   docker run --rm -v "$PWD/myapp:/app:ro" -v appdata:/data -p 8080:8080 \
#     -e FW_BOOTSTRAP_ADMIN_EMAIL=admin@example.com -e FW_BOOTSTRAP_ADMIN_PASSWORD=change-me-please app-maker

# ---- build ----------------------------------------------------------------
FROM golang:1.27.3-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Pure Go (SQLite included), so the binaries are static. The admin console
# (admin/dist) and the Python runtime (runner/python) are embedded.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fw ./cmd/fw && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fwd ./cmd/fwd

# ---- uv (Python environments for code nodes with requirements.lock) --------
FROM ghcr.io/astral-sh/uv:0.13.6 AS uv

# ---- runtime ----------------------------------------------------------------
FROM debian:trixie-slim
# python3 runs code nodes; ca-certificates for OIDC, LLM providers, webhooks;
# tzdata for schedules that format local times.
RUN apt-get update && \
    apt-get install -y --no-install-recommends python3 ca-certificates tzdata && \
    rm -rf /var/lib/apt/lists/*
COPY --from=uv /uv /usr/local/bin/uv
COPY --from=build /out/fw /out/fwd /usr/local/bin/
# /app holds the manifest (read-only is fine); /data holds the database,
# blobs, and the data key, and must be a persistent volume.
RUN useradd --uid 10001 --create-home --home-dir /home/fw --shell /usr/sbin/nologin fw && \
    mkdir -p /app /data && chown fw:fw /data
# The example app, writable so `fw dev` can keep its .fw state next to it.
COPY --chown=fw:fw examples/tracker /usr/share/fw/examples/tracker
USER fw
WORKDIR /data

# Defaults for fwd: a single replica on SQLite in /data. Override any FW_*
# variable (FW_ENV selects env/<env>.json, default dev), or pass --config.
# fw dev ignores these and keeps its state in <manifest>/.fw.
ENV FW_MANIFEST=/app \
    FW_HTTP_ADDR=:8080 \
    FW_INTERNAL_ADDR=:9090 \
    FW_REPOSITORY_DEFAULT_DRIVER=sqlite \
    FW_REPOSITORY_DEFAULT_DSN=/data/app.db \
    FW_CREDENTIALS_DRIVER=sqlite \
    FW_BLOBS_DIR=/data/blobs \
    FW_DATA_KEY_FILE=/data/secrets/data.key \
    FW_CREATE_DATA_KEY=true \
    FW_LOG_FORMAT=json \
    UV_CACHE_DIR=/tmp/uv-cache \
    UV_PYTHON_DOWNLOADS=never

# 8080: app API, admin console, streaming. 9090: /livez /readyz /startupz.
EXPOSE 8080 9090
CMD ["fwd"]

LABEL org.opencontainers.image.title="app-maker" \
      org.opencontainers.image.description="Schema-driven application framework: fw CLI and fwd server"
