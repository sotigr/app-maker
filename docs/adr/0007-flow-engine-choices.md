# 0007. Flow engine implementation choices

Date: 2026-10-31 · Status: accepted

## Context

Section 10 specifies the flow engine; several details are left open.

## Decisions

- **Dependencies.** A node depends on the nodes in `edges` and, in addition, on every node its
  `in`, `when`, or `onError` references as `nodes.<name>`. Inferred dependencies make a node
  never read the output of a node that has not run. Cycles over either kind are rejected.
- **Skipping.** `when: false` skips a node; a node all of whose dependencies were skipped is
  skipped too ("nodes depending only on it", section 10.4). `onError` handlers run when the
  failing node routed its error to them; the error is available as `nodes.<failed>.error`.
- **Actions** are served by the Flow context (`POST /v1/actions/...`), because actions are flows.
  Order: the generated `transition` action dispatches to a declared transition; then a flow with
  an `action` trigger; then the built-in `transfer` (Ownable, default policy `isOwner(feature)`);
  then a declared state-machine transition without a flow. Action policies come from
  `policies/*.json` `actions`.
- **Writes of flows** are committed through Content with `SkipBeforeWrite` so that a flow's own
  writes do not recursively run before-write flows; event triggers still fire for them, with the
  causation chain enforcing the loop limits of section 10.9.
- **Before-write flows** return their staged writes; Content resolves and prepares them after the
  triggering operations and commits everything in one batch. A before-write flow may not change
  the feature being written (that would change it twice in one batch).
- **Typing.** Node outputs are typed by the node type's declared output (`any` for `expr`);
  `input` and `output` are typed from the flow's JSON Schemas; `feature` from the trigger type.
- **Webhooks** carry `X-Fw-Signature: sha256=<hex HMAC-SHA256(secret, body)>`. Outbound webhook
  deliveries use `t=<unix>,v1=<hex HMAC over "<unix>.<body>">` so receivers can reject replays.
- **Secrets** referenced as `${secret:<name>}` resolve from `FW_SECRET_<NAME>` until the secret
  store (section 14.7) is implemented.
- **Successful before-write runs are not recorded** as flowRun features (they run on every write);
  failures and rejections are.
- The `emit` node and the code and LLM nodes are not registered yet; manifests using them fail
  `validate` with "unknown node type".

## Consequences

The visual editor can show both explicit and inferred edges; the catalog endpoint
`GET /v1/flows` lists flows and node types for it.
