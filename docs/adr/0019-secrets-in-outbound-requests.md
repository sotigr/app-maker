# 0019: Secrets in outbound requests and webhook verifiers

Status: accepted (2026-11-06)

## Context
Spec 14.7 keeps secrets away from flows and code. Webhook signing,
OIDC, and LLM keys already worked that way, but `http.request` and Python
code had no way to authenticate to payment, SMS, or tax APIs (an
app builder's report, 2026-11-06). Inbound webhooks verified only
`X-Organon-Signature: sha256=<hex>`, a format that differed from the
`t=<unix>,v1=<hex>` our own `webhook.call` sends, and could not verify
providers such as Stripe.

## Decision
- **Secrets are substituted after evaluation, by the engine.**
  `${secret:name}` may appear in `http.request` header values and the
  URL query string (for example `"'Bearer ${secret:stripe}'"`). The run
  record, traces, and errors keep the reference; the value exists only in
  the outgoing request. Only names written literally in the node's
  inputs are substituted, so data that happens to contain
  `${secret:…}` cannot pull in other secrets.
- **Every secret used by flows declares its hosts** in `app.json`:
  `"secrets": {"stripe": {"hosts": ["api.stripe.com"]}}` (`*.domain`
  allowed). The host is fixed before substitution; a request to another
  host fails with `forbidden` and is not sent. `organon plan` lists
  changed bindings for review.
- **Code nodes** declare `"secrets": ["stripe"]` and call
  `ctx.http(method, url, headers=, body=, auth={"secret", "header", "prefix"})`.
  The engine adds the header, so the Python process never holds the value.
  (A response that echoes request headers would reveal it; the host
  binding limits this to the provider itself.)
- **Inbound verifiers** on the webhook trigger:
  `"verify": {"scheme": "organon" | "stripe" | "hmac-sha256", "header", "prefix", "encoding": "hex" | "base64"}`.
  `organon` (default) accepts the timestamped format of outbound
  deliveries with a 5-minute window, and the older `sha256=` form.
  `stripe` reads `Stripe-Signature`, the same timestamped scheme.
  `hmac-sha256` covers providers that sign the body into a header.
- Missing secrets fail the node with `secret_missing` (not retried).

## Consequences
- Secrets in `body`, paths, hosts, or other nodes are validation errors.
- The old `sha256=` inbound form will be removed in a later release.
