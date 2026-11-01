# Usage guide

An application is a directory of JSON files (the **manifest**). The
framework reads it at startup; there is nothing to compile.

| Guide | What it covers |
| --- | --- |
| [1. Quickstart](01-quickstart.md) | Build, scaffold, run, and call your first app |
| [2. The manifest](02-manifest.md) | Directory layout, `app.json`, environments, secrets, seed data |
| [3. Types](03-types.md) | Feature types in JSON Schema and the `x-` vocabulary |
| [4. Policies](04-policies.md) | Read/create/update/delete/move/action rules, roles, grants |
| [5. Flows](05-flows.md) | Triggers, nodes, expressions, actions, the visual editor |
| [6. Code nodes and LLMs](06-code-and-llm.md) | Python nodes and `llm.complete` |
| [7. HTTP API and clients](07-api.md) | REST endpoints, queries, streaming, files, auth, TypeScript SDK |
| [8. Testing, deploying, migrating](08-testing-and-deploy.md) | Manifest tests, plan/apply, data migrations, operations CLI |
| [9. Running the server](09-operations.md) | `fwd`, configuration, roles, observability, limits |

The running example throughout is [`examples/tracker`](../../examples/tracker):
projects contain tasks, tasks contain comments.

Terms used everywhere:

- **Feature**: one record with an ID, a type, an optional parent, a
  version, and field values. Features form a tree under a root.
- **Principal**: who acts: a user, a team, an organization, a service
  account, or the system.
- **View**: the subset of a type a given audience may read or write.
  Clients only ever see their view.
