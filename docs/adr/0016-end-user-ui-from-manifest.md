# 0016: End-user UI described in manifest files

Status: accepted (2026-11-01, requested by the project owner)

## Context
Spec §1.2 and §13.3 rule out generating end-user UI: only the admin console
is generated. The project owner wants apps to ship a UI for their end users,
built like flows: described entirely in manifest files, written by humans
in a visual builder in the admin console or by agents as JSON, validated by
`organon validate`, and working on a plain `organond` with no build step.

## Decision
- **Files.** `ui/app.json` (shell: title, home page, nav, login),
  `ui/pages/<name>.json` (one page each), optional `ui/styles.css` (raw CSS
  loaded after the base theme), and `ui/assets/**` (served at
  `/assets/app/<path>`). Modules may hold `ui/pages/`. All of them are part
  of the manifest hash and of plan/apply.
- **Runtime interpreter, not code generation.** A page is a JSON component
  tree from a fixed catalog. A generic React renderer (`@organon/ui`, built
  into `web/dist` and embedded in organond) interprets it on the SDK and is
  served at `/`. `/v1`, `/admin`, and `/assets` stay reserved.
- **Text vs expressions.** Every prop has a kind in the catalog. `text`
  props are literal text with `{{ expr }}` interpolations; `expr` props are
  expressions as in flows. This avoids the most common quoting mistake.
- **Expression subset.** UI expressions use expr syntax restricted to
  literals, member access, operators (no `matches`, no ranges), `??`,
  ternaries, and the functions `len lower upper trim string int float join
  split abs round floor ceil min max now`, plus UI built-ins `can`,
  `formatDate`, `formatNumber`. The server type-checks them; the browser
  evaluates them with a small interpreter. Shared test vectors
  (`testdata/uiexpr/cases.json`) keep both evaluators in agreement.
- **Behaviour stays in actions and flows.** UI steps call the public API
  (`action`, `create`, `update`, `delete`) as the signed-in user; the UI
  only hides, the API enforces. `can(f, op)` asks the server
  (`POST /v1/permissions`) so buttons can be hidden by policy.
- **Validation.** Types, fields (owner read view; `private` is an error),
  indexed query fields, writable form fields, actions, routes and
  navigation params, and every expression are checked with JSON-Pointer
  problems, like flows.
- **Builder.** `/admin` → UI: palette and outline tree, a live preview drawn
  by the real renderer, and an inspector generated from the catalog. Saving
  goes through dev-only endpoints `PUT /v1/dev/manifest/ui/...` (as ADR 0002),
  with layout hints in `x-layout`. Read-only outside `organon dev`.
- **Styling.** A base theme of CSS variables and stable `o-*` class names,
  plus `ui/styles.css`. No theme-token file and no custom code components.

- **File previews** (added 2026-11-01). The `preview` component shows a
  file field in the page with the browser's own viewers: `<img>`,
  `<iframe>` for PDF, `<video>`, `<audio>`. To make that possible, blob
  downloads of images (not SVG), PDF, video, and audio are served `inline`
  with `frame-ancestors 'self'` and `Range` support; the response keeps
  `sandbox` in its CSP (Chromium's PDF viewer works under it) and
  `nosniff`. No PDF.js bundle. Thumbnail URLs are given only for types the
  server can thumbnail (JPEG, PNG, GIF) and fall back to the original image
  until the thumbnail exists, so a fresh upload never shows a broken image.

## Consequences
- The framework now owns an end-user surface: the `web/` bundle is
  embedded like `admin/dist` and rebuilt with `make web`.
- The design document is not rewritten; this ADR supersedes the non-goal.
- Behaviour that expressions cannot do belongs in flows, not in the UI.
