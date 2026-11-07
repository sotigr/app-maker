# 0027: Admin account operations and the People console

Status: accepted (2026-11-07)

## Context
The admin console had no account screens. Users were created through the
generic Data form (`POST /v1/features/user`), which sets no password, so
new users could not sign in. Roles and team members were edited as raw
JSON, grants and transfers took typed UUIDs, and API keys and OIDC invites
(both supported by the server) could not be issued from the console. The
server also lacked operations an admin expects: listing and revoking API
keys, resetting a user's password or MFA, and ending a user's sessions.
Decided with the user (2026-11-07): console changes plus small server
additions, English-only console, phased delivery with people first.

## Decision
- **Server (Access context).** Admin-only unless noted, audited, and no
  secret (password hash, key hash, TOTP secret) is ever returned:
  - `GET /v1/admin/meta`: app roles with inclusions, OIDC providers,
    password minimum, MFA rules (`AuthConfig.Roles` from app.json).
  - `GET /v1/auth/users/{id}/credentials` (also the principal itself).
  - `POST /v1/auth/users/{id}/password`: sets a password (creating the
    credential for invite-only users from their email) and deletes the
    user's sessions.
  - `DELETE /v1/auth/users/{id}/mfa`, `DELETE /v1/auth/users/{id}/sessions`
    (the latter also for oneself).
  - `GET /v1/auth/tokens?principal=` and `DELETE /v1/auth/tokens/{prefix}?principal=`
    follow `CreateAPIKey`'s rule: admins, the principal, or the owner of a
    service account.
  - `POST /v1/auth/users` takes `"invite": true` to create a user without a
    password, who signs in through `oidc-invite`.
  - Credential stores gain `APIKeysOf`, `DeleteAPIKey`, `PasswordOf`, and
    `IdentitiesOf` (SQLite and memory, shared storetest).
  - Suspension needs no new operation: every request re-resolves the
    principal and refuses non-active ones.
- **Console.** A shared kit (`admin/src/ui`): toasts, accessible dialogs
  and confirmations, labelled fields with inline errors, badges, empty and
  loading states, `IdChip` (labels instead of UUIDs), `RefPicker` (search
  by name with `icontains`, or paste an ID), `RolePicker`. A **People**
  section (users, teams, service accounts) and an **Access** section on
  every record. Server field errors are shown on their inputs; a 409 offers
  a reload.

## Consequences
- Further phases (data browser, operations screens, editors and
  accessibility) build on the same kit without new server operations.
- `organon token create` and the console use the same key rules.
