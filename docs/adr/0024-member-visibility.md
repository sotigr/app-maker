# 0024: The member visibility level

Status: accepted (2026-11-07)

## Context
Field visibility had public, owner, private, and system tiers. Apps with
share links (ADR 0020) need fields that every signed-in user may read
(staff notes, the mechanic on a time entry) but that customers opening a
link must not see. `owner` does not fit: on types that are not Ownable it
hides the field from the staff too (motoshop gap #29). Decided with the
user (2026-11-07): a new level `member`.

## Decision
- `x-visibility: member` sits between `public` and `owner`. Audiences gain
  `member` between `public` and `owner`: public < member < owner < admin
  < system.
- The Authorizer gives `member` to signed-in principals (users, teams,
  organizations, service accounts) that neither own nor administer the
  feature; anonymous callers and share links get `public`. Owners and
  admins read member fields as before (`owner` reads everything up to
  owner).
- Query and live-query field checks use `member` for signed-in callers,
  so they may filter and sort on indexed member fields; UI query sources
  may use them (the server still rejects them for anonymous callers and
  links). Creates of types that are not Ownable use the member write view
  for signed-in callers.
- Search indexes a member tier; it matches for signed-in callers only.
- Views gain `member.<direction>`; `organon plan` reports a widening or
  narrowing per audience as before.
- Manifest tests: a `read` step's `expect.feature` checks the caller's
  view, so hidden fields read as absent (`null`).

## Field write rules (gaps #35, #45)
- `x-write` takes a list; any rule in it allows the write. Several
  actions can own one field without making it `system`.
- `role:<name>` allows callers who hold the role (app-wide or through a
  grant on the feature; on create, on the parent). Views treat such
  fields as writable for signed-in audiences; the write path checks the
  role. Admins and the system need no role. Policies still decide the
  operation itself.

## Consequences
- Generated views and their golden files include member views; for types
  without member fields they equal the public views.
- Apps that want customer-facing data on a link keep it public; staff
  data becomes `member` instead of relying on page `visible` rules.
