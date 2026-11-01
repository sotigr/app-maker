# 0011: OIDC login and TOTP multi-factor authentication

Status: accepted (2026-11-01)

## Context
Spec 8.2/8.3: OIDC authorization code flow with PKCE, identities matched by
provider and subject (never email alone), a user created on first login;
TOTP as a second step for all users or per role; TOTP secrets encrypted
with the instance data key; OIDC state in an encrypted cookie (14.10).
The user decided (2026-10-31) that password users are admin-created only,
and (2026-11-01) that OIDC signup is a per-provider setting, off by
default, with admin invite links binding the first login.

## Decision
- app.json `auth.oidc[]`: `{provider, issuer?, clientId, clientSecret:
  ${secret:..}, signup?, allowedDomains?, scopes?}`; `google` and `gitlab`
  have default issuers; issuers must be https (localhost excepted).
  `auth.mfa`: `{requiredForRoles?, requiredForAll?}`; roles are checked
  by `fw validate`.
- Relying party (`access/infrastructure/oidc`): discovery, PKCE S256,
  `client_secret_post`, ID token verification (RS256, ES256 via JWKS with
  refresh on unknown kid at most once a minute; issuer, audience/azp,
  exp/iat with 60 s skew, nonce). Calls go through the egress client.
- Flow: `GET /v1/auth/oidc/{provider}/start?redirect=&invite=` sets the
  AES-GCM sealed state cookie (key derived from the instance key, 10 min,
  path /v1/auth/oidc/) and redirects; `/callback` checks state, redeems the
  code, and logs in the linked principal. Unlinked identities bind through
  a one-time invite (`POST /v1/auth/users/{id}/oidc-invite`, admins, 7
  days) or, with `signup: true`, create a user (requires a verified email
  in `allowedDomains` when set). Redirects allow same-origin paths only.
- MFA: password and OIDC logins go through `finish`: a session, or a
  ticket (`fwt_`, hashed, 10 min, one-time) for `verify` (TOTP enabled) or
  `enroll` (required but not enrolled). Endpoints: `/v1/auth/mfa/verify`,
  `/v1/auth/mfa/totp/{enroll,confirm,disable}`; after OIDC the ticket
  travels in an HttpOnly cookie (path /v1/auth/mfa) and the redirect gets
  `?mfa=verify|enroll`. Wrong codes put the ticket back while the login
  limiter allows; codes of used steps are refused (replay). Required MFA
  cannot be disabled.
- TOTP: RFC 6238 SHA-1, 6 digits, 30 s, ±1 step. Secrets sealed with
  AES-256-GCM under the instance data key from `secrets.dataKeyFile` (64
  hex chars); `fw dev` creates `.fw/data.key`; memory credential stores
  use a per-process key; otherwise enrollment is unavailable.
- Credential store gains identities, TOTP secrets, and tickets (memory and
  SQLite, conformance suite `access/infrastructure/storetest`).
- SDK `client.auth` gains providers/oidcStart/verifyMfa/enrollTotp/
  confirmTotp/disableTotp; the admin console login handles both MFA steps
  and lists OIDC providers.

## Consequences
- The data key is a plain key file, not envelope encryption under a KMS
  master key; KMS integration is future work.
- No recovery codes; an admin can remove a user's TOTP only by database
  intervention for now.
- Provider tokens are never stored (no offline access).
