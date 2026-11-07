# 0025: Outbound request bodies (integrations II)

Status: accepted (2026-11-07)

## Context
`http.request` and Python's `ctx.http` always JSON-encoded the body and
forced `Content-Type: application/json`. APIs that take XML (AADE myDATA,
SOAP services) or form-encoded bodies (OAuth 2.0 token endpoints, Stripe,
Viva Wallet) were out of reach, and secrets never reach Python, so code
could not work around it (motoshop gap #50; request of 2026-11-07).

## Decision
- `http.request` takes at most one of `body` (JSON, unchanged: a text
  value is still sent as a JSON string), `rawBody` (text sent as its
  UTF-8 bytes, default `text/plain; charset=utf-8`), and `form` (an object
  of scalars sent `application/x-www-form-urlencoded`). A `Content-Type`
  header set by the flow always wins. `validate` and the run time both
  reject two bodies. Every body kind is limited to 1 MiB.
- `${secret:name}` is allowed in `form` values, substituted after
  evaluation with the same host check as headers. `body` and `rawBody`
  never take secrets (a validation error).
- Responses: JSON (or untyped, or `text/plain` holding JSON, as before)
  is parsed; any other type is text decoded by its charset (UTF-8 by
  default, `golang.org/x/text/encoding/htmlindex` for others). 4xx
  answers are output; 5xx, 429, and network errors stay retryable.
- `ctx.http(..., raw_body=, form=, auth=)` mirrors the node. `auth` is one
  entry or a list; an entry adds `${secret:name}` to a header (default
  `Authorization`, with `prefix`) or, with `form`, to a form field. Only
  secrets the node declares resolve, for their hosts.
- Run records store bodies like other node inputs: only with
  `trace: true`. Bodies are never logged.
- Manifest-test HTTP fixtures answer text (`text`) with response
  `headers`, and `request: {contentType, bodyContains, header}` makes a
  fixture answer only matching requests; a mismatch fails the step with
  the reason.

## Consequences
- Responses whose content type is neither JSON nor text/plain are no
  longer parsed as JSON even when they happen to be valid JSON.
- Unsigned inbound webhooks (gap #51: URL token, GET handshake, IP list)
  are a separate part of this wave and get their own section here when
  implemented.
