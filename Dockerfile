# The framework image: organon (CLI) and organond (server) with everything they need
# at run time. Apps are not part of it: mount a manifest at /app, or build
# FROM this image and COPY the manifest to /app. See docs/usage/10-docker.md.
#
#   docker build -t organon .
#   docker run --rm -v "$PWD/myapp:/app:ro" -v appdata:/data -p 8080:8080 \
#     -e ORGANON_BOOTSTRAP_ADMIN_EMAIL=admin@example.com -e ORGANON_BOOTSTRAP_ADMIN_PASSWORD=change-me-please organon

# ---- build ----------------------------------------------------------------
FROM golang:1.27.3-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# Pure Go (SQLite included), so the binaries are static. The admin console
# (admin/dist), the UI renderer (web/dist), and the Python runtime
# (runner/python) are embedded.
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/organon ./cmd/organon && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/organond ./cmd/organond

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
COPY --from=build /out/organon /out/organond /usr/local/bin/
# /app holds the manifest (read-only is fine); /data holds the database,
# blobs, and the data key, and must be a persistent volume.
RUN useradd --uid 10001 --create-home --home-dir /home/organon --shell /usr/sbin/nologin organon && \
    mkdir -p /app /data && chown organon:organon /data
# The example app, writable so `organon dev` can keep its .organon state next to it.
COPY --chown=organon:organon examples/tracker /usr/share/organon/examples/tracker
USER organon
WORKDIR /data

# Defaults for organond: a single replica on SQLite in /data. Override any ORGANON_*
# variable (ORGANON_ENV selects env/<env>.json, default dev), or pass --config.
# organon dev ignores these and keeps its state in <manifest>/.organon.
ENV ORGANON_MANIFEST=/app \
    ORGANON_HTTP_ADDR=:8080 \
    ORGANON_INTERNAL_ADDR=:9090 \
    ORGANON_REPOSITORY_DEFAULT_DRIVER=sqlite \
    ORGANON_REPOSITORY_DEFAULT_DSN=/data/app.db \
    ORGANON_CREDENTIALS_DRIVER=sqlite \
    ORGANON_BLOBS_DIR=/data/blobs \
    ORGANON_DATA_KEY_FILE=/data/secrets/data.key \
    ORGANON_CREATE_DATA_KEY=true \
    ORGANON_LOG_FORMAT=json \
    UV_CACHE_DIR=/tmp/uv-cache \
    UV_PYTHON_DOWNLOADS=never

# 8080: app API, admin console, streaming. 9090: /livez /readyz /startupz.
EXPOSE 8080 9090
CMD ["organond"]

LABEL org.opencontainers.image.title="organon" \
      org.opencontainers.image.description="Schema-driven application framework: organon CLI and organond server"
