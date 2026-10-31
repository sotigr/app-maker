# 0006. Relationship index read from grant and team features

Date: 2026-10-31 · Status: accepted

## Context

Section 8.6 describes the relationship index as a projection of tuples maintained by
`update_relationships` jobs or in the same transaction as the triggering write, stored in the
repository so every replica reads the same index. Grants are features
(`urn:sys:feature:grant`) and memberships are the `members` list of team features, both with
`x-indexed` fields.

## Decision

- The index is read directly from the grant and team features through indexed repository
  queries (`/subject in S`, `/members contains p`), behind the `access/domain.RelationshipIndex`
  port (`internal/access/infrastructure/relindex`). There is no separate tuple table yet.
- A grant is a child of the object it grants a role on; its `object` field must equal its
  parent. Any type accepts grant children. `hasRole(f, r)` therefore matches grants whose
  object is `f` or an ancestor of `f`.
- App-level roles in a principal's `roles` field act as grants on every feature.
- Transitive group membership is resolved at authentication and stored in `Principal.Groups`
  (depth limit 8). Grant lookups are memoized per request.

## Consequences

- Index updates are immediate and transactional with the grant or membership write; no
  `update_relationships` job is needed while grants and principals share one repository.
- Cross-repository grants (a grant stored apart from its object) are not supported until the
  index is materialized; the compiler already requires children to share the parent's
  repository, so grants always live with their object.
- If lookups become a bottleneck (many grants per principal), a materialized tuple table can
  replace the adapter without changing the port.
