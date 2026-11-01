# app-maker

A schema-driven application framework. You describe an application as
**JSON Schemas** (data types), **policy expressions** (who may do what), and
**flow graphs** (behaviour, as JSON DAGs editable in a visual editor). One
server binary, `fwd`, runs that description; the `fw` CLI validates, tests,
runs, and deploys it. No application code is compiled into the server.

```sh
make build                      # bin/fwd and bin/fw
bin/fw init myapp               # scaffold a manifest
bin/fw validate myapp && bin/fw test myapp
bin/fw dev myapp                # http://localhost:8080, admin console at /admin
```

Or with Docker only (no Go toolchain):

```sh
docker build -t app-maker .
docker run --rm -it -p 8080:8080 app-maker fw dev /usr/share/fw/examples/tracker --addr :8080
```

- Usage guide: [docs/usage/](docs/usage/README.md) ([Docker](docs/usage/10-docker.md))
- For AI agents: [AGENTS.md](AGENTS.md)
- Example app: [examples/tracker](examples/tracker)
- Design decisions: [docs/adr](docs/adr)
