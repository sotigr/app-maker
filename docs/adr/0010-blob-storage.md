# 0010: Blob storage, uploads, and downloads

Status: accepted (2026-11-01)

## Context
Spec 14.4: a BlobStore port (S3-compatible, local filesystem), uploads via
signed URLs after policy and schema checks, blob IDs committed in normal
writes, short-lived signed download URLs for readers, cleanup of
unreferenced blobs after 24 hours, and thumbnails for `x-thumbnails`.
Spec 14.10 requires upload intents to live in the repository.

## Decision
- Upload intents are features of the system type `urn:sys:feature:blob`
  (no policy, so only admins and the system read them), written through
  the Content system ports; the blob ID is the intent's feature ID. States:
  pending → uploaded → committed → released → deleted.
- Integration owns the `BlobStore` port (`integration/domain`), the
  filesystem adapter `BlobFS` (`<dir>/<id[:2]>/<id>` + `.json` info,
  written atomically; exact length enforced) and the `Blobs` service.
  S3 is refused by config validation until implemented; `fs` is refused in
  the replicated topology.
- `POST /v1/uploads {type, field, feature | parent, size, contentType}`:
  update policy on the feature (or create policy under parent, root by
  default), the field must be a writable top-level Blob field, size ≤
  `x-maxBytes` (default 25 MiB), content type in `x-contentTypes` (`*/*`
  patterns). Returns `{blob, uploadUrl, method: PUT, headers, expiresAt}`
  valid 15 minutes.
- Signed URLs: HMAC-SHA256 over op, id, expiry with a key derived from the
  instance key (`secrets.cursorKey`, random per process in single
  topology). `/v1/blobs/` is exempt from session/CSRF handling and from
  the 1 MiB body limit for PUT; the handler limits to the declared size.
- Writes: Content calls `Blobs.Verify` for every blob value a write sets
  or replaces (not for unchanged values, system, or fixture writes): the
  intent must be uploaded (not yet committed), for this type and field,
  by the same principal, with matching size and content type.
- A commit hook enqueues `blob_state` jobs (committed with the feature ID;
  replaced values released) and `blob_purge` for purged features; commit
  jobs also render thumbnails (box-averaged, JPEG stays JPEG, others PNG,
  ≤ 40 MP sources) stored as `<id>_t<size>`.
- Reads: Content adds `url` (and `thumbnails`) to readable top-level Blob
  values in feature responses (get, children, query, search, writes);
  expiry rounds to five minutes so URLs are stable briefly. Downloads send
  `nosniff`, a sandbox CSP, and `attachment` except for raster images.
- `cleanup_blobs` runs hourly: pending/uploaded intents older than 24 hours
  and blobs released more than 24 hours ago lose their bytes (and
  thumbnails) and become `deleted`.
- TS SDK: `BlobRef`, `FwClient.upload(target, body, contentType)`; codegen
  maps Blob fields to `BlobRef`.

## Consequences
- Verification and the commit job are not atomic: two features committing
  the same just-uploaded blob at the same moment could both succeed.
- Blob values nested inside object fields are neither verified nor linked.
- Streams (SSE/WS) carry blob values without URLs; clients re-read the
  feature for a link.
- No admin console widget for Blob fields yet.
