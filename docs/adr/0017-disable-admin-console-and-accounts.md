# 0017: Switching off the admin console and admin accounts

Status: accepted (2026-11-01)

## Context
Spec 8.4 always creates an initial admin, and spec 13.3 always serves the
admin console at `/admin`. A production instance that serves only the end-user
UI (ADR 0016) should be able to run with neither: no console to probe, and no
account that bypasses every policy.

## Decision
Two operator settings, off by default:

| Setting | Effect |
| --- | --- |
| `admin.disableConsole` / `ORGANON_DISABLE_ADMIN_CONSOLE` | `/admin` and `/admin/*` are not served (404); without a UI the root is 404 instead of redirecting to the console. |
| `admin.disableAccounts` / `ORGANON_DISABLE_ADMIN_ACCOUNTS` | `AuthService.Resolve` refuses any principal with the `admin` role, so password and OIDC sign-in, MFA, sessions, and API keys all fail for admins (password sign-in says "admin accounts are disabled"). No bootstrap admin is created and `CreateUser` refuses the admin role. |

Decided with the user (2026-11-01):
- Existing admins are refused, not downgraded to editor.
- No exception for admin API keys: deploys over HTTP, user creation, the
  audit log, and retries become unavailable. Deploy by restarting with a new
  manifest directory (ADR 0014).

The system principal is unaffected (it never signs in). Per-feature `admin`
relation roles granted through grants are ordinary policy roles and stay.

## Consequences
- Turning admin accounts back on is a restart; admin users are kept.
- `organon dev` honours both variables (the builder needs an admin).
