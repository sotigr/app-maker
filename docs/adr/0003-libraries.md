# 0003. Libraries

Date: 2026-10-31 · Status: accepted (versions to be pinned when the toolchain is installed)

## Context

Section 15.8 lists the libraries and asks for verification and an ADR.

## Decision

Use the section 15.8 list:
`santhosh-tekuri/jsonschema/v6`, `expr-lang/expr`, `jackc/pgx/v5`, `valkey-io/valkey-go`,
`blevesearch/bleve`, standard `net/http` ServeMux, `coder/websocket`, `spf13/cobra`,
`google/uuid` (UUIDv7), `cockroachdb/apd/v3`, `go.opentelemetry.io/otel`, `google.golang.org/grpc`,
`testcontainers/testcontainers-go`, `riverqueue/river` with `riverpgxv5`, `riversqlite`, `riverui`.

Addition: `modernc.org/sqlite` as the SQLite driver (pure Go, so `fwd` builds without CGO).
`riversqlite` works on `database/sql`; if it turns out to require a specific driver, this ADR is
updated.

Additional small dependencies are allowed when they replace security-sensitive hand-written
code: `golang.org/x/crypto/argon2` (password hashing, section 8.2).

## Consequences

Versions are recorded in `go.mod`; notable quirks go to `notes/gotchas.md`.
