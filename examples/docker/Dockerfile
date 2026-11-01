# An app image: the framework image plus one manifest. Build from the
# repository root:
#   docker build -t organon .
#   docker build -f examples/docker/Dockerfile -t tracker .
ARG ORGANON_IMAGE=organon
FROM ${ORGANON_IMAGE}
COPY examples/tracker /app
# Pick the environment file the server uses (env/<ORGANON_ENV>.json).
ENV ORGANON_ENV=dev
