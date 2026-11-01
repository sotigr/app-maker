# 0014: Docker image and deploying manifest directories at startup

Status: accepted (2026-11-01)

## Context
The user asked for one Docker image that ships `fw` and `fwd` with their
run-time dependencies, used for development and deployment, with apps
supplied by volume mounts or by images built `FROM` it. Spec 14.10 has
replicas load the active manifest from the database, and `fwd` preferred
the last stored deploy over the manifest directory at startup. A new app
image would therefore have kept running the previously stored manifest.

## Decision
- `Dockerfile`: Go build stage (`golang:1.27.3-trixie`, `CGO_ENABLED=0`,
  static binaries with the admin console and fwnode embedded), runtime
  `debian:trixie-slim` with `python3`, `ca-certificates`, `tzdata`, and
  `uv` (0.13.6). Non-root user `fw` (uid 10001); `/app` for the manifest,
  `/data` for state; `CMD ["fwd"]`; ports 8080 and 9090. Environment
  defaults: SQLite in `/data/app.db` for features and credentials, blobs in
  `/data/blobs`, data key `/data/secrets/data.key` created on first start
  (`FW_CREATE_DATA_KEY`, now a configuration option). The tracker example is
  included at `/usr/share/fw/examples/tracker`. No HEALTHCHECK in the image
  (`fw dev` has no health port); compose and orchestrators probe `:9090`.
- Startup deploy rule: outside development, a manifest directory whose hash
  was never recorded is a new release and becomes the active deploy, after
  `fw apply`'s breaking-change check (a breaking change without a migration
  stops startup unless `allowBreaking` / `FW_ALLOW_BREAKING`). A directory
  recorded before yields to the active deploy, so `fw apply` survives
  restarts. No setting chooses between image-based and `fw apply`
  deployments; both work.
- New environment variables: `FW_ALLOW_BREAKING`, `FW_CREATE_DATA_KEY`,
  `FW_TRUST_PROXY_HEADERS`.

## Consequences
- Re-deploying an older image after an `fw apply` does not roll back to its
  manifest (its hash was seen); roll back with `fw apply` of the old
  manifest or a new image tag with a changed manifest.
- A refused startup leaves the old container serving under rolling updates.
- The image runs one replica per volume (SQLite lock) until PostgreSQL.
