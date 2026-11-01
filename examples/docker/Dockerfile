# An app image: the framework image plus one manifest. Build from the
# repository root:
#   docker build -t app-maker .
#   docker build -f examples/docker/Dockerfile -t tracker .
ARG FW_IMAGE=app-maker
FROM ${FW_IMAGE}
COPY examples/tracker /app
# Pick the environment file the server uses (env/<FW_ENV>.json).
ENV FW_ENV=dev
