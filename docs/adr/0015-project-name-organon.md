# 0015: The project is named Organon

Status: accepted (2026-11-01)

## Context
The project was called app-maker, with binaries `fw` and `fwd` taken from
the design document. The user chose the name Organon (Aristotle's
"instrument": a tool for building other things). Nothing had been released,
so every name could change at once.

## Decision
| Was | Now |
| --- | --- |
| Go module `app-maker` | `organon` |
| `fw` (CLI), `cmd/fw` | `organon`, `cmd/organon` |
| `fwd` (server), `cmd/fwd` | `organond`, `cmd/organond` |
| `FW_*` environment variables, `FW_SECRET_<NAME>` | `ORGANON_*`, `ORGANON_SECRET_<NAME>` |
| `@fw/client`, `@fw/react`, `FwClient`, `FwError` | `@organon/client`, `@organon/react`, `OrganonClient`, `OrganonError` |
| Python runtime `fwnode` | `organon` (`from organon import HostError`) |
| `.fw/` (fw dev state), `~/.config/fw/credentials.json` | `.organon/`, `~/.config/organon/credentials.json` |
| Cookies `fw_session`, `fw_csrf`, `fw_oidc`, `fw_mfa` | `organon_session`, `organon_csrf`, `organon_oidc`, `organon_mfa` |
| Headers `X-Fw-Signature`, `X-Fw-Delivery` | `X-Organon-Signature`, `X-Organon-Delivery` |
| WebSocket subprotocols `fw`, `fw.bearer.<token>` | `organon`, `organon.bearer.<token>` |
| River job kind `fw`; Docker image `app-maker`, user `fw` | `organon`; image `organon`, user `organon` |

The design document and ADRs 0001–0014 keep the old names as historical
records; read `fw`/`fwd` there as `organon`/`organond`.

## Consequences
- Existing development databases may hold queued River jobs of kind `fw`;
  delete `.fw/` directories (they are no longer git-ignored) and start fresh.
- Stored CLI credentials and browser sessions from before the rename are
  not picked up; log in again.
- The repository directory itself is still named `app-maker`.
