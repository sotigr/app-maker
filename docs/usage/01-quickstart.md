# 1. Quickstart

## Build

Requirements: Go (see `go.mod`), and `pnpm` only if you rebuild the admin
console or the TypeScript SDK. Python 3 is needed for Python code nodes.

```sh
make build          # bin/organond (server) and bin/organon (CLI)
make test           # go test -race ./...
```

## Scaffold an app

```sh
bin/organon init myapp
```

This creates `myapp/` with one type (`item`), its policy, a flow, an email
template, two UI pages, seed data, and tests. Check and test it:

```sh
bin/organon validate myapp   # schemas, policies, flows, UI pages; every expression is type-checked
bin/organon test myapp       # runs tests/*.test.json against in-memory instances
```

## Run it

```sh
bin/organon dev myapp
```

`organon dev` runs every role in one process on SQLite (`myapp/.organon/dev.db`),
applies `seed/dev.json` on a fresh database, uses a fake LLM, and reloads
when you edit the manifest. Default admin login: `admin@localhost` /
`admin-password-dev` (override with `ORGANON_BOOTSTRAP_ADMIN_EMAIL` and
`ORGANON_BOOTSTRAP_ADMIN_PASSWORD`).

Open <http://localhost:8080/> for the app's own UI (sign in as `ana@example.com` /
`ana-password-123`), and <http://localhost:8080/admin> for the admin console:
browse and edit data with generated forms, edit flows and UI pages
visually, inspect runs, deploys, and the audit log.

## Call the API

```sh
# Log in; "cli": true returns a bearer token instead of cookies.
TOKEN=$(curl -s localhost:8080/v1/auth/password \
  -d '{"login":"admin@localhost","password":"admin-password-dev","cli":true}' | jq -r .token)
H="Authorization: Bearer $TOKEN"

# Create a feature under the root, read it, run an action.
ID=$(curl -s -H "$H" localhost:8080/v1/features/item -d '{"title":"First"}' | jq -r .id)
curl -s -H "$H" localhost:8080/v1/features/item/$ID
curl -s -H "$H" -X POST localhost:8080/v1/actions/item/$ID/close -d '{}'

# Query.
curl -s -H "$H" localhost:8080/v1/query \
  -d '{"type":"item","where":{"field":"/status","op":"eq","value":"closed"}}'
```

## Next

- Change the data model: [3. Types](03-types.md)
- Decide who may do what: [4. Policies](04-policies.md)
- Add behaviour: [5. Flows](05-flows.md)
- Design the user interface: [11. User interface](11-ui.md)
- Study a complete app: [`examples/tracker`](../../examples/tracker)
