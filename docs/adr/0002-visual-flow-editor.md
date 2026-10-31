# 0002. Visual flow editor

Date: 2026-10-31 · Status: accepted (requested by the project owner)

## Context

The spec defines flows as JSON DAGs (section 10.4) and does not describe a visual editor.
The project owner wants application behavior to be authored with a graph programming tool.
Section 15.9.1 requires anything not in the spec to be recorded in an ADR. Section 12 forbids
editing definitions directly in production outside `dev`.

## Decision

- Flow JSON files in the manifest remain the single source of truth. The editor is a view
  over them and never stores state of its own except layout hints.
- The editor is part of the admin console (React + Vite + React Flow / xyflow), embedded in
  `fwd` with `go:embed` and served at `/admin/flows`.
- Node palette, node input/output schemas and kinds come from the server's `NodeRegistry`
  through a read-only endpoint, so built-in and future nodes appear without editor changes.
- Diagnostics come from the same validation as `fw validate` (section 10.14), run server side
  on the edited document.
- Saving writes the flow JSON file back into the manifest directory through a dev-only
  endpoint, `PUT /v1/dev/manifest/flows/{name}`, registered only when `fwd` runs under
  `fw dev` (topology `single`, dev mode). Hot reload then activates it. In every other mode
  the editor is read-only and changes go through `fw plan` and `fw apply`.
- Node positions are stored in an optional `"x-layout": {"<node>": {"x":0,"y":0}}` member of
  the flow document, ignored by the engine.

## Consequences

- One new endpoint and one new flow-document member beyond the spec, both dev or annotation only.
- Requires Node.js to build the UI; the built assets are embedded so the server stays one binary.
