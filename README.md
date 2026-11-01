# Organon

A schema-driven application framework. You describe an application as
**JSON Schemas** (data types), **policy expressions** (who may do what),
**flow graphs** (behaviour, as JSON DAGs editable in a visual editor), and
**UI pages** (component trees, designed in a visual builder or by hand). One
server binary, `organond`, runs that description; the `organon` CLI validates, tests,
runs, and deploys it. No application code is compiled into the server.

```sh
make build                      # bin/organond and bin/organon
bin/organon init myapp               # scaffold a manifest
bin/organon validate myapp && bin/organon test myapp
bin/organon dev myapp                # the app's UI at http://localhost:8080, admin console at /admin
```

Or with Docker only (no Go toolchain):

```sh
docker build -t organon .
docker run --rm -it -p 8080:8080 organon organon dev /usr/share/organon/examples/tracker --addr :8080
```

- Usage guide: [docs/usage/](docs/usage/README.md) ([Docker](docs/usage/10-docker.md))
- For AI agents: [AGENTS.md](AGENTS.md)
- Example app: [examples/tracker](examples/tracker)
- Design decisions: [docs/adr](docs/adr)
