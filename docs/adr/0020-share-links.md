# 0020: Share links

Status: accepted (2026-11-06)

## Context
Apps for customers (a repair shop: "track my bike, approve the estimate")
need people without accounts to see one record and act on it. Users are
created by admins or by OIDC signup, and policies cannot see a token in a
URL, so allowing anonymous reads would expose every record. The user chose
share links over self-signup (2026-11-06).

## Decision
- **Opt-in per type:** `"x-shareable": {"actions": ["approveEstimate"], "maxDays": 30}`.
  The type gets the built-in action `share` (policy rule `actions.share`).
  `validate` checks that each action is a state-machine transition or a
  flow action.
- **`share`** takes `{expiresInDays (default 7, at most maxDays), actions
  (a subset), note}` and returns `{id, token, url, expiresAt, actions}`.
  The token (`<link id>.<32 random bytes>`) is shown once; a system type
  `shareLink` stores its hash under the shared feature (so it is deleted
  with it), with `object`, `actions`, `expiresAt`, `createdBy`, `note`.
  Its creator and admins of the shared feature read and delete (revoke) it.
- **Link holders are principals of kind `link`** (`principal.object` is
  the shared feature). They come from `Authorization: Link <token>` or from
  the `organon_link` cookie that `GET /s/<token>` sets (HttpOnly,
  SameSite=Lax, until expiry; the token leaves the address bar at once),
  with the same double-submit CSRF check as sessions (the CSRF value is
  derived from the token). Expiry and revocation are checked on every
  request.
- **Authorization does not consult app policies for links:** read the
  shared feature and its subtree (queries are scoped to it); run the link's
  actions on the shared feature; inside such an action (its transition, or
  its flow's writes) update the shared feature and create features in its
  subtree. No other writes, no delete, move, upload, or `share`. Field
  values are projected with the **public** view.
- Flows caused by a link action (event flows) run as the link principal.
- **UI:** `ui/app.json` `linkHome` names the page a link opens, with
  `params.id` set to the shared feature; pages show to links through
  `visible: "principal.kind == 'link'"`; link holders never see the
  sign-in screen. An invalid link lands on the sign-in page with a message.
- **Tests:** `"as": "link:<ref>"` in manifest tests uses a real link to
  that feature with all of its type's shareable actions.

## Consequences
- Anyone holding the URL has the access until expiry or revocation; links
  are bearer credentials and must be sent over private channels.
- Field-level `x-write` rules still apply inside link actions.
- Link holders are rate-limited per link like other principals.
