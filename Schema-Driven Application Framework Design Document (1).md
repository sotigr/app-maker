# Schema-Driven Application Framework: Design Document

Oct 25, 2026 · @Sotiris Papaioannou

## 1. Overview

The framework lets developers build applications by writing an app manifest: JSON Schemas for data, expression-based policies, and flows for behavior. From that manifest it serves a generic HTTP API, a streaming API, a CLI, and a typed browser SDK. The server is written in Go and structured with Domain-Driven Design (DDD), which is a hard requirement for every package (section 15). The system is cloud native: every role runs as multiple replicas behind a load balancer (section 14.10).

This document is the specification for implementation. Where it says MUST, the implementation is required to follow it. Where it says SHOULD, deviations need a written reason in the pull request.

### 1.1 Goals

1. A developer can build a working data-centric application (for example, a team task tracker) without writing server code, only a manifest.
2. Storage is database agnostic. Every backend implements the same repository interfaces and passes the same conformance test suite.
3. Every change to data is observable. Any field marked subscribable can be streamed live to authorized clients.
4. Behavior is added in layers: declarative schema rules, then expressions, then flows, then code nodes. No layer requires the one after it.
5. Security is structural. Visibility and permissions are enforced in one place that every read, write, query, event, and search result passes through.
6. Everything the developer writes is type-checked at deploy time against the schemas, so errors appear in `validate`, not in production.

### 1.2 Non-goals for the first major version

- Generating end-user UI from schemas. Only an admin console is generated (section 13.3).
- Long-running or pausable workflows inside the flow engine. Long processes are modeled as feature state (section 10.7).
- Collaborative real-time text editing (CRDT fields). The `Kind` system leaves room to add it later.
- Cross-repository transactions. References across repositories are weak by design.
- Offline-first clients. The SDK cache is designed so offline support can be added later.
- Hosting several apps or several clients in one instance. Each client gets its own instance running one app (section 14.6).

### 1.3 Glossary

| Term | Meaning |
| --- | --- |
| Feature | An entity instance with identity: an ID, a type, an optional parent, a version, and field values. The "folder" in the original folder/file analogy. |
| Field | A named, typed value on a feature. The "file" in the analogy. Object-typed fields are values without identity. |
| FeatureType | A JSON Schema document (restricted profile plus `x-` vocabulary) that defines the shape and rules of a kind of feature. |
| Trait | A reusable schema fragment mixed into feature types (for example Principal, Ownable, Timestamped). Supports polymorphic queries and policies. |
| Compiled type | The internal representation produced from a FeatureType at registration. All runtime components use it; none read raw JSON Schema. |
| View | A projection of a compiled type for one audience and direction (read, write, query, event). |
| Principal | Anything that can act or own: users, organizations, teams, service accounts, the system principal. |
| Repository | A storage backend implementing the repository interfaces, selected by dependency injection. |
| Change log | The retained stream of events produced by every committed write, used for real-time streaming. It is not ordered per feature; consumers order events by feature version (section 9.2). |
| Flow | A run-to-completion directed acyclic graph of nodes, triggered by an API request, a committed write, a schedule, or an inbound webhook. |
| Node | One step in a flow, with typed input and output schemas and a kind (Pure, DataRead, DataWrite, Effect, External, Code, or Control; section 10.5). |
| App manifest | The directory of schemas, policies, actions, flows, jobs, and seed data that defines one application. |
| Instance | One deployment of the framework (all its replicas and backing services) running exactly one app for one client. |
| Job | A unit of background work stored in River (section 2.5), inserted in the same transaction as the write that causes it, and run at least once by a worker replica. |

### 1.4 Design principles

- **One source of truth.** Each FeatureType is authored once. Every view, API contract, client type, and storage layout is derived from it.
- **Thin abstraction over real databases.** Repositories expose native strengths through capability flags instead of reducing every backend to the weakest one. This guards against the inner-platform effect.
- **One egress path.** All data leaving the server passes through the projector. No endpoint, transport, or worker may serialize a feature directly.
- **Fail at deploy, not at runtime.** Schemas, expressions, policies, and flows are validated and type-checked by the CLI before they can be applied.
- **Long-running state lives in data.** Processes that span time are features with state machines, advanced by short flows.

## 2. Architecture and bounded contexts

The system is one Go module, deployed as one binary that can run any combination of roles. It is divided into nine bounded contexts plus a small shared kernel. Contexts talk to each other only through application services, published domain events, or ports (interfaces) owned by the consuming context.

&#91;embedded content: bounded contexts · 9 contexts and how they depend on each other\]

Schema supplies compiled types to every context; Content commits every write together with its jobs; those jobs publish events to Change for streaming, start event-triggered flows, and deliver effects through Integration, and flows call back into Content.

### 2.1 Runtime roles

The binary takes a `--roles` flag. All roles in one process is the development default; production runs them separately and scales each independently.

| Role | Responsibility | Scaling |
| --- | --- | --- |
| `api` | HTTP API: generic feature API, transactions, actions, queries, auth endpoints | Stateless, horizontal |
| `gateway` | Streaming connections (SSE, WebSocket), subscription registry, per-subscriber projection | Horizontal; clients reconnect on node loss |
| `worker` | Runs River jobs: event publishing, event-triggered and scheduled flows, effect delivery, aggregates, search indexing, migrations, maintenance | Horizontal, competing consumers |
| `runner` | Hosts code-node workers (Python first) behind the code-node protocol | Separate process or container pool |

### 2.2 Bounded contexts

| Context | Owns | Type |
| --- | --- | --- |
| Schema | FeatureTypes, traits, the JSON Schema profile, the compiler, views, compatibility checking | Core |
| Content | Features, values, containment, references, transactions, the generic API use cases | Core |
| Access | Principals, credentials, sessions, the authorizer, the relationship index | Core |
| Query | Query AST, planning, capability negotiation, the search index port | Supporting |
| Change | Events, publishing, change log, subscriptions, streaming delivery | Core |
| Flow | Flow definitions, triggers, the execution engine, node registry, flow runs | Core |
| Runtime | Expression evaluation (expr), code-node protocol, sandbox management, LLM providers | Supporting |
| Integration | Side-effect delivery: email, webhooks, outbound HTTP, file and blob storage | Generic |
| Manifest | App manifests, environments, deploy plans, migrations, seed data | Supporting |

The shared kernel is deliberately small: `ID` and `IDGenerator` (UUIDv7), the `Kind` enumeration and `Value` sum type, `Version`, domain error codes, the `Clock` interface, and `JobRequest` with its `JobEnqueuer` port (section 2.5). Anything else shared between contexts is a sign the boundary is wrong.

### 2.3 Context relationships

- **Schema is upstream of almost everything.** It publishes the compiled type as its published language. Other contexts read compiled types through a `TypeRegistry` port and never parse JSON Schema themselves.
- **Content depends on Access through a port.** Content defines an `Authorizer` interface; Access implements it. Content never imports Access internals.
- **Content emits domain events; Change transports them.** Content enqueues a job carrying its events in the same transaction as the data (section 2.5). Change owns everything after that.
- **Flow orchestrates Content.** Flow nodes call Content application services with the flow's principal, never repositories directly. This keeps validation, permissions, and events in force.
- **Query and search are fed through an anti-corruption layer.** The index\_features job worker translates features into index documents, so search engines can be swapped without touching Content.
- **Integration is downstream only.** It implements the workers for effect jobs and has no knowledge of features beyond the payload it is given.

### 2.4 Lifecycle of a write

1. The `api` role authenticates the request and resolves the caller's `Principal`.
2. The Content application service loads the compiled type and the current feature version.
3. Each operation is authorized (section 8.5), then before-write flows run (section 10): reads and computation only, with no external calls and no transaction open.
4. The service applies the patch, enforces field write rules and state machines, and validates the merged document against the storage and write views.
5. One short transaction applies all writes with version checks and enqueues jobs (section 2.5) for event publishing, matching event-triggered flows, effects, and index updates.
6. The response is projected through the caller's read view.
7. A `worker` publishes the events to the change log; `gateway` nodes deliver them to subscribers; `worker` nodes run the other jobs enqueued at commit.

### 2.5 Background jobs

All background work runs as jobs in [River](https://github.com/riverqueue/river), a Go job queue stored in the same database as the features. River replaces components that would otherwise be custom: the transactional outbox, the outbox relay, the scheduler, retry and dead-letter handling, and leader election.

- **Transactional enqueue.** Jobs are inserted in the same transaction as the writes that cause them. A rolled-back write leaves no jobs, and a committed write never misses one.
- **Drivers.** PostgreSQL (`riverpgxv5`) and SQLite (`riversqlite`). River's tables are created by its own migrations, which the server applies at startup with the framework's migrations. The memory repository pairs with an in-process adapter that runs jobs in goroutines after commit; it is not durable and is refused in the replicated topology (section 14.10).
- **Exactly-once scheduling.** Scheduled flows and maintenance tasks are River periodic jobs. Every worker replica registers the same set from the active manifest at startup and updates it when a new manifest is activated; River enqueues each occurrence once, using its own leadership in the database. Framework code contains no leader election.
- **At-least-once execution.** A job can run more than once (retries, or rescue after a replica crash), so every job worker MUST be idempotent. Job arguments carry IDs and versions rather than documents wherever the worker can read current state; `publish_events` is the exception and carries the events.
- **Failures.** Each job kind declares maximum attempts and a retry policy. Jobs that exhaust their attempts are discarded by River, and discarded jobs can be inspected and retried in the admin console (section 13.3).

Domain and application code never import River. The kernel defines `JobRequest` and a `JobEnqueuer` port; `platform/jobs` adapts them to River, and each context registers its job workers through a `jobs.Registry` at the composition root.

```go
package kernel

type JobRequest struct {
    Kind        string          // e.g. "run_flow"
    Args        json.RawMessage
    Queue       string          // "events", "flows", "effects", "maintenance"
    UniqueKey   string          // optional; deduplicates pending jobs with the same key
    ScheduledAt *time.Time
    MaxAttempts int
}
```

| Job | Enqueued by | Queue | Purpose |
| --- | --- | --- | --- |
| `publish_events` | Every commit | `events` | Publish the batch's events to the change log (section 9.2) |
| `run_flow` | Commits matching an event trigger, inbound webhooks, schedules | `flows` | Run one flow (section 10) |
| `deliver_email`, `deliver_webhook` | Effect nodes | `effects` | Deliver a side effect (section 14.5) |
| `index_features` | Commits touching searchable fields | `maintenance` | Update the search index; unique per feature so bursts coalesce |
| `recompute_aggregate` | Commits changing children of a type with `x-aggregate` | `maintenance` | Recompute from the children; unique per parent and field |
| `update_relationships` | Grant, membership, or owner changes stored in another repository | `maintenance` | Update the relationship index (section 8.6) |
| `migrate_batch`, `build_index`, `retire_type_version` | `apply` | `maintenance` | Data migrations, index builds, the contract step (section 12.4) |
| `purge_deleted`, `cleanup_blobs`, `expire_flow_runs`, `erase_principal` | Periodic schedules or admin actions | `maintenance` | Retention and erasure |

Each queue has its own worker concurrency, so slow effects never delay event publishing, and `events` has the highest priority. Some things are deliberately not jobs: streaming fan-out to gateways (every gateway must see every event within milliseconds, which is the change log's job, section 9), ephemeral field writes, request-triggered flows, and code-node execution.

## 3. Core domain model

The Feature is the central aggregate. It is an entity with identity, a type, an optional parent for containment, a version for concurrency, and a map of field values keyed by JSON Pointer path. Features are stored flat; the tree is formed by each child's `ParentID`, not by child lists on the parent.

### 3.1 The Feature aggregate

```go
package content // internal/content/domain

type Feature struct {
    id        kernel.ID
    typeID    schema.TypeID   // e.g. "urn:app:feature:project/v3"
    parentID  *kernel.ID      // nil for root features
    ownerID   *kernel.ID      // set when the type includes the Ownable trait
    version   kernel.Version  // incremented on every committed change
    fields    map[string]kernel.Value // JSON Pointer path -> value
    createdAt time.Time
    updatedAt time.Time
    deletedAt *time.Time      // soft delete marker
    pending   []DomainEvent   // collected, drained by the repository on commit
}
```

Fields are unexported. State changes only through methods that enforce invariants and record domain events, for example `Apply(patch Patch, by Principal) error`, `MoveTo(parent ID) error`, and `MarkDeleted(by Principal) error`.

Invariants the aggregate MUST enforce:

- `typeID` never changes after creation. Changing a feature's type is a migration, not an edit.
- `parentID` may only point to a feature whose type lists this type in `x-allowedChildren`. The parent check uses a type lookup, not a parent load.
- A feature cannot be its own ancestor. Moves check ancestry before committing.
- `version` increases by exactly one per committed change.
- After validation, `fields` always conforms to the storage view of `typeID`.

### 3.2 Value kinds

`kernel.Value` is a sealed sum type: an interface with an unexported method, implemented only inside the kernel package. Every kind has a canonical wire format.

| Kind | Go representation | JSON wire format | Notes |
| --- | --- | --- | --- |
| String | `string` | string | UTF-8; max length from schema |
| Int64 | `int64` | string | Strings avoid precision loss above 2^53 in JavaScript |
| Float | `float64` | number | NaN and Infinity rejected |
| Decimal | `apd.Decimal` or equivalent | string | For money; never float |
| Bool | `bool` | boolean |  |
| Time | `time.Time` (UTC) | RFC 3339 string | Stored and emitted in UTC |
| Ref | `kernel.ID` + target type | string | Association to another feature (section 3.3) |
| Blob | `BlobRef` | object `{id, size, contentType}` | Bytes live in blob storage (section 14.4) |
| Object | ordered map of Values | object | Value semantics, no identity |
| List | `[]Value` of one element kind | array | Max items from schema |
| Enum | `string` | string | Allowed values from schema |
| JSON | raw JSON | any | Freeform; not indexable or queryable |

### 3.3 Containment versus association

The model distinguishes two kinds of relationship. Mixing them was the main weakness of the original design.

- **Containment** is the parent/child tree. A child has exactly one parent. Containment implies ownership of lifecycle: deleting a parent deletes its children. It defines the default scope for permission inheritance and for listing.
- **Association** is a `Ref` field. A feature can be referenced by any number of others. References do not imply lifecycle or permissions. Each `Ref` field declares `x-onDelete`: `restrict` (block deletion of the target), `setNull`, or `dangle` (the default, required for cross-repository references).

Rule for modelers: if a thing needs its own ID, permissions, history, or independent streaming, it is a child feature. Otherwise it is an object-typed field.

### 3.4 Identity and naming

- IDs are generated by the application, never by a database. The default is UUIDv7 (time-ordered, good index locality). The `IDGenerator` interface lives in the kernel.
- A FeatureType MAY declare `x-uniqueName` on one string field, making it unique among siblings. That enables optional path addressing (`/projects/launch/tasks/write-copy`) for CLI and admin use. Paths are never the primary key.

### 3.5 Concurrency

All writes are optimistic. Every update and delete carries the version the caller last saw. The repository applies the write only if the stored version matches, otherwise it returns `ErrVersionConflict` with the current version. There are no pessimistic locks and no transactions held open across external calls.

### 3.6 Deletion

Deletion is soft by default: `deletedAt` is set, the feature disappears from reads and queries, and a `deleted` event is emitted. Children are soft-deleted in the same transaction. A purge job hard-deletes after a retention period configured per type (`x-retention`), and erasure requests purge immediately (section 8.8).

### 3.7 System feature types

The framework ships these types in the reserved `urn:sys:` namespace. Applications may extend some through traits but cannot redefine them.

| Type | Purpose |
| --- | --- |
| `urn:sys:feature:root` | Root of the feature tree; owned by the system principal |
| `urn:sys:feature:user` | Base user principal; apps add profile fields through traits |
| `urn:sys:feature:organization`, `team` | Group principals |
| `urn:sys:feature:serviceAccount` | Non-human principals (API keys, integrations) |
| `urn:sys:feature:featureType` | Stored FeatureType definitions, validated by the meta-schema |
| `urn:sys:feature:flow`, `flowRun` | Flow definitions and execution records |

## 4. Schema system

FeatureTypes are authored in JSON Schema draft 2020-12, restricted to a profile and extended with an `x-` vocabulary. At registration, the Schema context compiles each type into an internal compiled type, from which it derives every view. JSON Schema is the external grammar; the compiled type is what every other context consumes.

### 4.1 The profile

The compiler MUST reject any schema that uses keywords outside the profile, with an error naming the keyword and its JSON Pointer.

| Category | Keywords |
| --- | --- |
| Allowed | `type`, `properties`, `required`, `items`, `enum`, `const`, `default`, `title`, `description`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `minLength`, `maxLength`, `pattern`, `minItems`, `maxItems`, `uniqueItems`, `format` (asserted), `readOnly`, `writeOnly`, `$id`, `$defs`, `$ref` to local `$defs` |
| Restricted | `oneOf` only as a discriminated union (every branch has a `const` on the same tag property). `additionalProperties` must be `false`, except on fields of kind JSON. `type` arrays only as `[T, "null"]` for nullable fields. |
| Rejected | `anyOf`, `allOf`, `not`, `if`/`then`/`else`, `patternProperties`, `dependentSchemas`, `dependentRequired`, `propertyNames`, `unevaluatedProperties`, `unevaluatedItems`, `contains`, recursive `$ref`, remote `$ref` |

Asserted formats: `date-time`, `date`, `email`, `uri`, `uuid`. The `format` keyword is configured to assert, not annotate.

### 4.2 The x- vocabulary

The vocabulary is declared with a `$vocabulary` URI so standard validators treat these keywords as annotations and the schemas stay valid for external tools.

| Keyword | Applies to | Meaning |
| --- | --- | --- |
| `x-kind` | field | Overrides the inferred kind: `int64`, `decimal`, `time`, `ref`, `blob`, `json` |
| `x-indexed` | scalar field | Repository builds an index; field becomes filterable and sortable |
| `x-visibility` | field | `public`, `owner`, `private`, or `system` (section 4.6). Default `public` |
| `x-write` | field | Who may write: `any` (default), `owner`, `admin`, `system`, or `action:<name>` |
| `x-immutable` | field | Writable on create only |
| `x-ref` | Ref field | Target type ID or trait ID |
| `x-onDelete` | Ref field | `restrict`, `setNull`, or `dangle` (default) |
| `x-computed` | field | An expr expression over the feature; read-only, evaluated on write and stored |
| `x-aggregate` | field | A materialized aggregate over children, e.g. `{"op":"count","of":"urn:app:feature:task","where":"status == 'done'"}` |
| `x-stream` | field | `{"mode":"state"\|"append","maxRate":10}`; makes the field subscribable |
| `x-persist` | field | `false` for ephemeral streamed fields that skip storage |
| `x-stateMachine` | Enum field | Allowed transitions and who may perform them (section 4.10) |
| `x-traits` | type | List of trait IDs mixed into the type |
| `x-allowedChildren` | type | Type or trait IDs allowed as children |
| `x-uniqueName` | type | Field that must be unique among siblings |
| `x-retention` | type | Duration before soft-deleted features are purged |
| `x-repository` | type | Name of the repository that stores this type (section 5.4) |
| x-searchable | String field | Indexed for full-text search (section 7.5) |
| x-maxBytes, x-contentTypes | Blob field | Upload size limit and allowed content types (section 14.4) |
| x-thumbnails | Blob field | Thumbnail sizes generated for images by a system flow (section 14.4) |

### 4.3 Example FeatureType

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "urn:app:feature:task/v2",
  "title": "Task",
  "type": "object",
  "x-traits": ["urn:sys:trait:ownable", "urn:sys:trait:timestamped"],
  "x-allowedChildren": ["urn:app:feature:comment"],
  "properties": {
    "title":    { "type": "string", "maxLength": 200, "x-indexed": true },
    "status":   { "enum": ["todo", "in_progress", "done"], "default": "todo",
                  "x-indexed": true, "x-write": "action:transition" },
    "assignee": { "type": ["string", "null"], "x-kind": "ref",
                  "x-ref": "urn:sys:trait:principal", "x-onDelete": "setNull" },
    "estimate": { "type": "string", "x-kind": "decimal" },
    "dueAt":    { "type": "string", "format": "date-time", "x-indexed": true },
    "progress": { "type": "integer", "minimum": 0, "maximum": 100,
                  "x-stream": { "mode": "state", "maxRate": 10 } },
    "isLate":   { "type": "boolean", "readOnly": true,
                  "x-computed": "dueAt != nil && status != 'done' && now() > dueAt" },
    "internalScore": { "type": "number", "x-visibility": "private" }
  },
  "required": ["title"],
  "additionalProperties": false
}
```

### 4.4 Compiler pipeline

1. Parse the document and validate it against the framework meta-schema (profile plus vocabulary).
2. Resolve local `$ref`s and merge traits. A property defined by two sources is an error unless the definitions are identical.
3. Infer each field's `Kind` from `type`, `format`, and `x-kind`. Ambiguity is an error.
4. Compile every expression (`x-computed`, `x-aggregate.where`, state-machine guards) with expr against a typed environment built from the type. Type errors are compile errors.
5. Build the field table: JSON Pointer path, kind, flags, visibility, write rule.
6. Derive the views (section 4.6) and compile a validator for each.
7. If a previous version is registered, run the compatibility checker (section 4.8) per view.
8. Store the type as a `urn:sys:feature:featureType` feature and publish it to the `TypeRegistry`.

### 4.5 Compiled type

```go
package schema // internal/schema/domain

type CompiledType struct {
    ID            TypeID
    Name          string
    Version       int
    Traits        []TraitID
    Fields        []FieldDef           // ordered by path
    Children      []TypeID             // resolved from x-allowedChildren
    UniqueName    string               // path, or empty
    Repository    string
    Retention     time.Duration
    Views         map[ViewKey]*View
    Computed      []ComputedField      // compiled expr programs
    Aggregates    []AggregateDef
    StateMachines map[string]*StateMachine
}

type FieldDef struct {
    Path       string       // JSON Pointer, e.g. "/address/city"
    Kind       kernel.Kind
    Nullable   bool
    Required   bool
    Indexed    bool
    Immutable  bool
    Visibility Visibility
    Write      WriteRule
    RefTarget  string
    OnDelete   OnDelete
    Stream     *StreamSpec  // nil if not subscribable
    Persist    bool
}

type ViewKey struct {
    Audience  Audience  // Public, Owner, Admin, System
    Direction Direction // Read, Create, Update, Query, Event
}

type View struct {
    Readable   map[string]bool
    Writable   map[string]bool
    Queryable  map[string]bool
    JSONSchema []byte              // the published projection
    Validator  *jsonschema.Schema  // compiled once
}
```

### 4.6 Visibility tiers and views

Visibility works like member access in a class, but the tier that applies is decided per request by the Access context.

| Tier | Readable by | Typical fields |
| --- | --- | --- |
| `public` | Anyone allowed to read the feature | Titles, names, statuses |
| `owner` | The feature's owner principals and admins | Email, settings, roles |
| `private` | Server-side code only: computed fields, flows running as system | Risk scores, internal flags |
| `system` | The framework only | Bookkeeping maintained by the framework |

JSON Schema's `readOnly` removes a field from create and update views; `writeOnly` removes it from all read views. For every type, the compiler derives one view per (audience, direction): read views per audience, create and update views (update views have no `required` list because updates are merge patches), a query view (indexed fields readable by the audience), and an event view (fields included in change events for that audience). Each view is itself a valid JSON Schema, published through OpenAPI and used for client code generation.

### 4.7 Traits

Traits are schemas with `$id` in `urn:...:trait:...` and no `x-allowedChildren`. They are composed into types with `x-traits`, never with `allOf`. Policies, queries, and `x-ref` targets can name a trait, so a query for `urn:sys:trait:taggable` spans all types that include it. Built-in traits: `principal`, `ownable`, `timestamped`, `taggable`.

### 4.8 Compatibility rules

The checker compares the new and previous compiled types view by view. `apply` refuses breaking changes unless the manifest includes a migration (section 12.5) or the operator passes `--allow-breaking`.

| Change | Storage | Public API views |
| --- | --- | --- |
| Add optional field | Safe | Safe |
| Add required field without default | Needs migration | Breaking for create |
| Add required field with default | Safe (backfilled) | Safe |
| Remove field | Needs migration | Breaking |
| Rename field | Needs migration | Breaking |
| Widen type (Int64 to Decimal, add enum value) | Safe | Safe for writes, breaking for strict readers |
| Narrow type or tighten constraint | Needs migration (existing data validated) | Breaking |
| Visibility public to owner or private | Safe | Breaking for that audience |
| Visibility private to public | Safe | Safe; flagged for review as a data exposure |
| Add `x-indexed` | Index build (online) | Safe |

### 4.9 Write validation

Updates are JSON Merge Patches (RFC 7396). The service applies the patch to the current document, recomputes computed fields, then validates the result against the storage view and the patch against the caller's update view. Validation errors are returned as a list of `{path, code, message}`. Errors on fields the caller cannot read are replaced with a generic `invalid_state` error so private fields are never revealed.

### 4.10 State machines

An Enum field with `x-stateMachine` can only change through declared transitions. Each transition names the action that performs it and an optional guard expression.

```json
"x-stateMachine": {
  "initial": "todo",
  "transitions": [
    { "from": "todo",        "to": "in_progress", "action": "start" },
    { "from": "in_progress", "to": "done",        "action": "complete",
      "guard": "principal.id == feature.assignee" },
    { "from": "done",        "to": "in_progress", "action": "reopen",
      "guard": "'admin' in principal.roles" }
  ]
}
```

## 5. Storage and repositories

Storage is abstracted at the level of whole features, not individual fields. Each backend chooses its own physical layout, declares what it can do through capability flags, and must pass the shared conformance suite. Backends are selected by dependency injection at startup.

### 5.1 Ports

The interfaces below are owned by the Content context (`internal/content/domain`). Implementations live in `internal/content/infrastructure/<backend>`.

```go
type FeatureRepository interface {
    Get(ctx context.Context, id kernel.ID) (*Feature, error)
    GetMany(ctx context.Context, ids []kernel.ID) ([]*Feature, error)
    Children(ctx context.Context, parent kernel.ID, page PageRequest) (Page[*Feature], error)
    Query(ctx context.Context, q query.Plan) (Page[*Feature], error)
    // Apply commits all mutations atomically, with a version check on each
    // update and delete, and inserts the batch's jobs in the same transaction.
    Apply(ctx context.Context, batch Batch) (CommitResult, error)
    Capabilities() Capabilities
}

type Batch struct {
    Mutations []Mutation          // Create, Update, Move, Delete, Purge
    Events    []DomainEvent       // produced by the aggregates
    Jobs      []kernel.JobRequest // publish_events, run_flow, effects, index updates
    Causation CausationChain      // section 10.9
}

type CommitResult struct {
    Versions map[kernel.ID]kernel.Version
}

type Capabilities struct {
    Transactions      bool // multi-feature atomic Apply
    TransactionalJobs bool // jobs inserted in the commit transaction
    SecondaryIndexes  bool
    Sort              bool
    RangeFilters      bool
    FullText          bool // native full-text, optional
    SubtreeQueries    bool // efficient "descendants of"
    MaxBatchSize      int
}
```

Repositories return domain errors from the kernel: `ErrNotFound`, `ErrVersionConflict{ID, Current}`, `ErrConstraint{Path, Code}`, `ErrUnsupported{Capability}`. They never return driver errors to callers.

### 5.2 Batch semantics

- A batch is all or nothing. If any version check fails, nothing is written and the error lists every conflicting feature.
- A batch may only touch features stored in one repository. A batch spanning repositories is rejected with `ErrCrossRepository` before any write.
- Repositories without `Transactions` accept only single-mutation batches. Multi-mutation batches return `ErrUnsupported`.
- Jobs are inserted with River in the same transaction as the data: no job exists for a rolled-back write, and no committed write is missing its jobs. This replaces a hand-written transactional outbox.

### 5.3 Reference implementations

**Memory** (`infrastructure/memory`) is the reference implementation, used by unit and manifest tests; `fw dev` uses SQLite (section 12.3). It holds features in maps guarded by a `sync.RWMutex`, supports every capability except `FullText`, and hands jobs to the in-process job adapter after each commit (section 2.5).

**PostgreSQL** (`infrastructure/postgres`) is the first production backend. Proposed layout:

```sql
CREATE TABLE features (
  id          uuid        PRIMARY KEY,
  type_id     text        NOT NULL,
  parent_id   uuid,
  owner_id    uuid,
  ancestors   uuid[]      NOT NULL DEFAULT '{}',  -- materialized path, root first
  version     bigint      NOT NULL,
  data        jsonb       NOT NULL,
  created_at  timestamptz NOT NULL,
  updated_at  timestamptz NOT NULL,
  deleted_at  timestamptz
);
CREATE INDEX features_children ON features (parent_id) WHERE deleted_at IS NULL;
CREATE INDEX features_type     ON features (type_id)   WHERE deleted_at IS NULL;
CREATE INDEX features_anc      ON features USING gin (ancestors);

-- River's tables (river_job and others) are created by River's own
-- migrations, applied at startup with the framework's migrations.
```

Each `x-indexed` field becomes an expression index on `data`, for example `CREATE INDEX ... ON features ((data->>'status')) WHERE type_id = '...'`, built with `CREATE INDEX CONCURRENTLY` during `apply`. Int64, Decimal, and Time values are indexed through casting expressions so ordering is numeric or chronological, not lexical.

The `ancestors` array makes subtree queries and permission inheritance one indexed lookup. A move rewrites the `ancestors` of every descendant inside the same transaction; moves of subtrees larger than `MaxMoveSize` (default 10,000) are rejected and must be run as a migration job.

**SQLite** (`infrastructure/sqlite`) is the second backend, for development, embedded use, and single-binary deployments. It uses the PostgreSQL layout with documents stored as JSON `TEXT`, SQLite JSON functions for field access, expression indexes for `x-indexed` fields, a `feature_ancestors` table in place of the array column, and FTS5 for the `FullText` capability. SQLite allows one writer at a time on one machine, so a SQLite deployment runs exactly one replica of each role. The server takes an exclusive lock on the database file and refuses to start a second replica against it. SQLite must pass the full conformance suite except the multi-replica tests.

**Later backends** (MongoDB, a key-value store) implement the same ports. A backend without `SecondaryIndexes` still works for get, children, and full scans with a hard row limit.

### 5.4 Repository routing

The `RepositoryRegistry` maps names to implementations, configured at startup:

```go
reg := content.NewRepositoryRegistry()
reg.Register("default", postgres.New(pool, cfg))
reg.Register("metrics", timeseries.New(tsClient))
```

A type's `x-repository` names its repository (default `"default"`). The application service resolves the repository from the type on every call. Children must live in the same repository as their parent. References across repositories are allowed but must use `x-onDelete: dangle`; the compiler enforces this.

### 5.5 Conformance suite

`internal/content/repotest` contains a test suite that takes a factory function and runs against any implementation:

```go
func TestPostgres(t *testing.T) {
    repotest.Run(t, func(t *testing.T) content.FeatureRepository {
        return postgres.New(testPool(t), testConfig)
    })
}
```

It covers CRUD, version conflicts, batch atomicity, transactional job insertion (jobs exist exactly when the commit succeeds), soft delete and purge, children paging, subtree queries, every query operator the repository claims, index-backed sorting of each kind, and concurrent writers (100 goroutines updating one feature must produce exactly one success per version). Tests for capabilities a backend does not declare must assert `ErrUnsupported`. A backend is not mergeable until it passes the suite.

## 6. Generic API and transactions

Every FeatureType automatically gets create, read, update, delete, list, and query endpoints, plus a batch transaction endpoint. The generic API is the default behavior of each type. It is not logic-free: every call goes through validation, permissions, write rules, computed fields, before-write flows, and projection.

### 6.1 Conventions

- Base path: `/v1`. JSON request and response bodies, UTF-8.
- Authentication: a session cookie (`HttpOnly`, `Secure`, `SameSite=Lax`) for browsers, or `Authorization: Bearer <token>` for service accounts and the CLI.
- Concurrency: responses carry `ETag: "<version>"`. Updates and deletes MUST send `If-Match`; without it the request fails with `428 Precondition Required`.
- Idempotency: mutating requests MAY send `Idempotency-Key`. The server stores the response for 24 hours and replays it for repeated keys.
- Errors use RFC 9457 problem details with a stable `code` field.
- Type names in URLs are the type's short name from the manifest (`task`), resolved to the current version's `$id`.

### 6.2 Endpoints

| Method and path | Purpose |
| --- | --- |
| `POST /features/{type}` | Create; body is the create view; optional `parent` |
| `GET /features/{type}/{id}` | Read, projected through the caller's read view |
| `PATCH /features/{type}/{id}` | Merge patch; requires `If-Match` |
| `DELETE /features/{type}/{id}` | Soft delete; requires `If-Match` |
| `GET /features/{id}/children?type=&cursor=&limit=` | List children, optionally of one type |
| `POST /features/{id}/move` | Change parent; body `{parent, version}` |
| `POST /query` | Query (section 7); body is the query document |
| `POST /transactions` | Atomic batch (section 6.3) |
| `POST /actions/{type}/{id}/{action}` | Invoke a named action (section 10.3) |
| `GET /schema` | OpenAPI 3.1 document for the caller's audience |
| `GET /schema/types/{type}` | Read and write views of one type for the caller |
| `GET /stream` | SSE stream; `/ws` for WebSocket (section 9) |
| `POST /auth/*` | Login, logout, refresh, OAuth callbacks (section 8) |
| GET /search | Full-text search (section 7.5) |
| POST /ephemeral/{type}/{id} | Write an ephemeral field (section 9.7) |
| POST /uploads | Request a signed upload URL (section 14.4) |
| POST /hooks/{name} | Inbound webhook trigger (section 10.2) |

### 6.3 Transactions

A transaction is a list of operations committed atomically by one repository. Operations may reference features created earlier in the same transaction through temporary references beginning with `$`.

```json
POST /v1/transactions
{
  "ops": [
    { "op": "create", "type": "project", "ref": "$p",
      "data": { "title": "Launch" } },
    { "op": "create", "type": "task", "parent": "$p", "ref": "$t1",
      "data": { "title": "Write copy" } },
    { "op": "update", "type": "task", "id": "0192f4c1-...", "version": 7,
      "data": { "assignee": null } },
    { "op": "delete", "type": "task", "id": "0192f4c2-...", "version": 3 }
  ]
}
```

The response maps every temporary reference to its real ID and returns each affected feature's new version. Rules:

- Maximum 100 operations per transaction (configurable, capped by the repository's `MaxBatchSize`).
- Each operation is authorized and validated individually; one failure rejects the whole transaction and the error identifies the operation index.
- Temporary references are resolved in order; a reference used before its creating operation is an error.
- Fields with `x-write: action:<name>` cannot be changed through transactions.

### 6.4 Application service pipeline

Every write use case in the Content application layer follows the same steps, implemented once in a shared pipeline:

1. Resolve the compiled type and repository.
2. Load current features for updates, deletes, and moves (one `GetMany` call).
3. Authorize each operation with the `Authorizer` port (section 8.5).
4. Run before-write flows for each operation (section 10); their staged writes join this batch.
5. Apply patches to aggregates, enforce write rules and state machines, recompute computed fields.
6. Validate against the storage view and the caller's write view.
7. Commit with one `Apply` call: mutations, domain events, and jobs.
8. Project results through the caller's read view and return.

Use cases in this layer: `CreateFeature`, `GetFeature`, `UpdateFeature`, `DeleteFeature`, `MoveFeature`, `ListChildren`, `RunQuery`, `ExecuteTransaction`, `InvokeAction`. HTTP handlers only translate between HTTP and these use cases.

### 6.5 Pagination

All list and query endpoints use opaque cursor pagination. The cursor encodes the sort key values and the ID of the last item, signed with HMAC so clients cannot forge it. The signing key is an instance secret shared by all replicas, so a cursor issued by one replica is accepted by any other. Default `limit` is 50, maximum 500. Offset pagination is not supported.

### 6.6 Error codes

| HTTP | `code` | Meaning |
| --- | --- | --- |
| 400 | `validation_failed` | Body fails the write view; `errors` lists paths |
| 400 | `invalid_query` | Query uses non-queryable fields or unsupported operators |
| 401 | `unauthenticated` | No valid session or token |
| 403 | `forbidden` | Authenticated but not allowed |
| 404 | `not_found` | Missing or not visible to the caller (never distinguishes the two) |
| 409 | `version_conflict` | `If-Match` does not match; body includes the current version |
| 409 | `invalid_transition` | State machine does not allow this change |
| 412 | `precondition_failed` | Action guard expression returned false |
| 422 | `unsupported_capability` | Repository cannot perform the operation |
| 428 | `version_required` | Missing `If-Match` |
| 429 | `rate_limited` | Includes `Retry-After` |
| 504 | `flow_timeout` | A request-triggered flow exceeded its budget |
| 400 | query\_too\_expensive | No index-backed predicate or scope on a large type (section 7.3) |
| 400 | cross\_repository | A transaction touches features in more than one repository (section 5.2) |
| Declared | Declared by the flow | A reject node stopped a beforeWrite or action flow (section 10.5) |
| 500 | flow\_failed | A request-triggered flow failed without a declared error (section 10.10) |

## 7. Query and search

Structured queries are expressed as a JSON document, parsed into an AST, validated against the caller's query view, merged with the caller's permission scope, and translated by the repository into native queries. Full-text search is a separate index fed by index\_features jobs enqueued at commit. Neither ever returns a feature the caller cannot see, and neither allows filtering or sorting on a field the caller cannot read.

### 7.1 Query document

```json
POST /v1/query
{
  "type": "task",
  "scope": { "descendantsOf": "0192f4c0-..." },
  "where": {
    "and": [
      { "field": "/status", "op": "in", "value": ["todo", "in_progress"] },
      { "field": "/dueAt", "op": "lt", "value": "2026-11-01T00:00:00Z" }
    ]
  },
  "sort": [ { "field": "/dueAt", "dir": "asc" } ],
  "select": ["/title", "/status", "/dueAt", "/assignee"],
  "expand": ["/assignee"],
  "limit": 50,
  "cursor": null
}
```

`type` may be a type or a trait. `scope` is one of `childrenOf`, `descendantsOf`, or omitted (whole app). `select` limits returned fields to a subset of the read view. `expand` inlines referenced features one level deep, each projected through the caller's read view for its own type.

### 7.2 Operators

| Operator | Kinds | Notes |
| --- | --- | --- |
| `eq`, `ne` | All scalar kinds, Ref, Enum | `null` allowed for nullable fields |
| `lt`, `lte`, `gt`, `gte` | Int64, Float, Decimal, Time, String | Requires `RangeFilters` |
| `in`, `nin` | Scalar kinds, Ref, Enum | Max 100 values |
| `prefix` | String | Case sensitive |
| `contains` | List of scalars | Element membership |
| `exists` | All | Field present and not null |
| `and`, `or`, `not` | Composite | Max depth 4, max 20 leaf predicates |

### 7.3 Planning

The Query context turns a query document into a `query.Plan` before any repository sees it.

1. Parse into the AST. Unknown operators or malformed values fail with `invalid_query`.
2. Resolve `type` to its compiled types (several if it is a trait).
3. Check every field in `where`, `sort`, and `select` against the caller's query view. A field the caller cannot read is reported as unknown, not as forbidden.
4. Ask the `Authorizer` for the caller's visibility scope and add it as a mandatory predicate (section 8.5).
5. Check repository capabilities. Unsupported operators fail with `unsupported_capability`; the framework never emulates them in memory beyond the row limit below.
6. Estimate cost. At least one predicate or the scope must be index-backed, unless the type has fewer than `ScanLimit` features (default 10,000). Otherwise the query fails with `query_too_expensive`.
7. Hand the plan to the repository.

```go
package query // internal/query/domain

type Plan struct {
    Types    []schema.TypeID
    Scope    Scope
    Where    Predicate // includes the permission predicate
    Sort     []SortKey // always ends with ID for stable cursors
    Select   []string
    Limit    int
    After    *Cursor
}

type Predicate interface{ isPredicate() }
type Compare struct { Path string; Op Op; Value kernel.Value }
type And     struct { Terms []Predicate }
type Or      struct { Terms []Predicate }
type Not     struct { Term Predicate }
type InScope struct { Field string; IDs []kernel.ID } // permission predicate
```

### 7.4 Joins and traversals

Version 1 does not support filtering on fields of referenced features ("tasks whose project's owner is X"). Applications get the same result through `x-computed` fields copying the value they need, or `x-aggregate` fields on the parent. `expand` is the only cross-feature read and is limited to one level and 100 expanded features per page. This keeps every query a single-type, index-backed operation on every backend.

### 7.5 Full-text search

String fields marked `x-searchable: true` (an additional vocabulary keyword, valid only on String fields) are indexed by the search subsystem.

```go
type SearchIndex interface {
    Upsert(ctx context.Context, docs []SearchDoc) error
    Delete(ctx context.Context, ids []kernel.ID) error
    Search(ctx context.Context, q SearchQuery) (SearchResult, error)
}

type SearchDoc struct {
    ID      kernel.ID
    Type    schema.TypeID
    Tier    schema.Visibility  // one doc per tier that has searchable fields
    Owners  []kernel.ID         // principals with owner access
    Scope   []kernel.ID         // ancestors, for permission filtering
    Text    map[string]string   // path -> text
    Facets  map[string]kernel.Value
}
```

- A `worker` runs index\_features jobs, enqueued at commit and unique per feature, so bursts of writes coalesce into one index update. Search is eventually consistent; typical lag is under one second.
- Each feature is indexed once per visibility tier, containing only that tier's fields. A public search never matches text in owner or private fields, so the existence of a hidden match cannot leak.
- Results are filtered in the index by `Scope` and `Owners`, then re-checked against the current authorizer and projected before return. Results that fail the re-check are dropped, and the response reports `approximateTotal` rather than an exact count.
- Adapters: PostgreSQL full-text search (the production default, shared by all replicas), Meilisearch (for larger or typo-tolerant search), and Bleve (embedded; development and single-replica SQLite deployments only). The adapter is chosen by dependency injection like repositories.
- `GET /search?q=&type=&limit=&cursor=` is the endpoint. Highlights are returned only for fields in the caller's read view.

## 8. Identity, authentication, and authorization

Users, organizations, teams, and service accounts are features that include the Principal trait. Credentials are not features: they live in a sealed store that only the Access context can read. Authorization is default-deny, expressed as expr policies per type, and backed by a relationship index so permission checks and query scoping never walk the graph at request time.

### 8.1 Principals

```json
{
  "$id": "urn:sys:trait:principal",
  "properties": {
    "displayName": { "type": "string", "maxLength": 120 },
    "kind":   { "enum": ["user", "organization", "team", "service", "system"],
                "readOnly": true, "x-visibility": "public" },
    "roles":  { "type": "array", "items": { "type": "string" },
                "x-visibility": "owner", "x-write": "admin" },
    "status": { "enum": ["active", "suspended", "deleted"],
                "x-visibility": "owner", "x-write": "admin" }
  }
}
```

The `owner` field of the Ownable trait is a Ref to `urn:sys:trait:principal`, so organizations, teams, and service accounts can own features. A user owns their own user feature. `owner` has `x-write: action:transfer`; it can only change through the built-in `transfer` action. `roles` can only be written by admins, so no principal can raise its own privileges.

### 8.2 Credential store

The credential store is a separate repository port (`access.CredentialStore`) with its own tables. It is never routed through the feature repositories, the change log, flows, or search.

| Credential | Storage |
| --- | --- |
| Password | Argon2id hash with per-user salt; parameters stored with the hash for future upgrades |
| External identity | Provider ID plus subject (`oidc:google`, `sub`); no provider tokens stored unless the app requests offline access |
| TOTP secret | Encrypted with the instance's data key (envelope encryption, key in KMS or a local key file) |
| API key | Shown once; stored as SHA-256 of the key; prefix kept for identification |
| Session | Random 256-bit ID, stored hashed; idle and absolute expiry |

### 8.3 Authentication

- **Password login**: `POST /auth/password`; rate limited per account and per IP; constant-time comparison; generic error for unknown user or wrong password.
- **OIDC**: authorization code flow with PKCE (`/auth/oidc/{provider}/start` and `/callback`). On first login, a user feature is created and linked; on later logins it is matched by provider and subject, never by email alone.
- **Sessions**: browsers get an `HttpOnly` session cookie. State-changing requests from browsers require a CSRF token header (double-submit), checked when the request carries a session cookie.
- **Bearer tokens**: service accounts and the CLI use API keys or short-lived access tokens obtained with them.
- **MFA**: TOTP as a second step, enforced for all users or per role.

The result of authentication is a `Principal` value placed in the request context. Nothing downstream reads cookies or headers.

```go
type Principal struct {
    ID      kernel.ID
    Kind    PrincipalKind
    Roles   []string
    Groups  []kernel.ID  // transitive memberships, from the relationship index
}
```

### 8.4 Bootstrapping

`fw init` (or the first server start with an empty store) creates, in one transaction: the system principal, the app's root feature owned by it, the system feature types, and an initial admin user whose credentials come from the CLI prompt or `FW_BOOTSTRAP_ADMIN_*` environment variables. The system principal cannot log in and has no credentials. Bootstrapping is guarded by a unique row, so when several replicas start against an empty store at once, exactly one transaction succeeds and the others load the bootstrapped state.

### 8.5 Authorization

The Content and Query contexts depend on this port; the Access context implements it.

```go
type Authorizer interface {
    // Check decides one operation on one feature (or on a parent, for create).
    Check(ctx context.Context, p Principal, op Operation, f FeatureRef) (Decision, error)
    // Audience returns the visibility tier the principal gets for this feature.
    Audience(ctx context.Context, p Principal, f FeatureRef) (schema.Audience, error)
    // Scope returns a predicate restricting a query to features p may read.
    Scope(ctx context.Context, p Principal, t schema.TypeID) (query.Predicate, error)
}
```

Policies are written per type (or trait) in the manifest and compiled with expr. Every operation not allowed by a policy is denied.

```json
{
  "type": "urn:app:feature:task",
  "read":   "hasRole(feature, 'viewer')",
  "create": "hasRole(parent, 'editor')",
  "update": "hasRole(feature, 'editor') || principal.id == feature.assignee",
  "delete": "hasRole(feature, 'admin') || isOwner(feature)",
  "actions": {
    "complete": "principal.id == feature.assignee",
    "reopen":   "hasRole(feature, 'admin')"
  }
}
```

The expression environment provides `principal`, `feature` (the storage view, before the change), `patch` (for updates), `parent` (for create and move), and functions `isOwner(f)`, `hasRole(f, role)`, `inGroup(id)`, and `now()`. Policies that can be compiled into a predicate (those using only `isOwner`, `hasRole`, and comparisons on indexed fields) are used by `Scope`; the compiler warns when a `read` policy cannot be turned into a predicate, because queries on that type then fall back to post-filtering with the row limit.

### 8.6 Grants and the relationship index

Sharing is modeled with a system type `urn:sys:feature:grant` holding `{subject, object, role}`: a principal (often a team) gets a role on a feature and its subtree. Built-in roles are `viewer`, `editor`, and `admin`, each including the one before; apps may declare more in the manifest.

The relationship index is a projection maintained by a `worker` through jobs (section 2.5) and stored in the repository, so every replica reads the same index. It stores tuples:

- `(principal, member, group)` from membership changes, with transitive closure precomputed per principal;
- `(principal, role, feature)` from grants;
- `(principal, owner, feature)` from owner fields.

`hasRole(f, r)` is true when the principal, or any group in `principal.Groups`, holds role `r` or higher on `f` or any of `f`'s ancestors. With the `ancestors` column (section 5.3), this is one indexed lookup. `Scope` produces a predicate of the form "owner in S, or any ancestor in G", where S and G come from the index. Index updates are applied in the same transaction as the triggering write when both live in the same repository; otherwise they are applied by an update\_relationships job enqueued in the same transaction, and lag by the job latency, typically well under a second.

### 8.7 Audience resolution

The tier used for projection is decided per request, in this order: `system` for flows explicitly running as system; `admin` for principals with the app `admin` role or the `admin` grant on the feature; `owner` when `isOwner` is true; otherwise `public`. The decision is cached per request.

### 8.8 Principal lifecycle

- **Suspension** sets `status: suspended`, revokes sessions and tokens, and closes streams.
- **Deletion** requires every feature owned by the principal to be transferred or deleted first. The `delete` use case returns the list of blocking features; an admin action `transferAll(to)` resolves them in batches.
- **Erasure** removes the user feature, its credentials, its personal fields in history and events (events store a redaction marker), and its search documents. Features that merely reference the user keep a dangling reference, shown by clients as a deleted principal.

## 9. Change log and field streaming

Every committed write enqueues a `publish_events` job carrying its events, in the same transaction (section 2.5). A worker publishes them to the change log, Valkey Streams by default. Gateway nodes match events to subscriptions, project them per subscriber, and deliver them over SSE or WebSocket with resumable cursors. Streaming is not a special field type: any field can be made subscribable with `x-stream`. The change log exists for real-time fan-out only; search indexing, aggregates, the relationship index, event-triggered flows, and effects get their own jobs at commit, so they never depend on the change log's retention or delivery.

### 9.1 Event model

```go
package change // internal/change/domain

type Event struct {
    Seq        string              // change log position, assigned on publish
    Kind       EventKind           // Created, Updated, Moved, Deleted, Purged, Ephemeral
    FeatureID  kernel.ID
    TypeID     schema.TypeID
    ParentID   *kernel.ID
    Version    kernel.Version      // after the change; consumers order by this
    Changed    []string            // JSON Pointer paths that changed
    Before     map[string]kernel.Value // previous values of Changed paths
    After      map[string]kernel.Value // full storage document after the change
    Actor      kernel.ID
    Causation  CausationChain      // flow runs that led to this write (section 10.9)
    At         time.Time
}
```

Events always carry the full storage view. Filtering by audience happens at delivery, so the log is independent of who listens. `After` is the full document so that gateways can evaluate query subscriptions without reading the repository; `Before` lets them detect a feature leaving a query result.

### 9.2 Publishing

- Each commit enqueues one `publish_events` job in the `events` queue with the batch's events, split into several jobs when the event data exceeds 1 MiB.
- A worker appends the events to the change log. River starts jobs within milliseconds of commit through database notifications; the target is commit-to-publish under 100 ms at p95.
- Delivery is at least once, and events for one feature may arrive out of order, because different replicas can publish consecutive commits concurrently. Publishers do not enforce order.
- Consumers MUST be version-aware: they keep the last version applied per feature, drop events at or below it, and when versions skip, use the full `After` document instead of only the `Changed` paths. Gateways apply this per subscription (section 9.6).
- River keeps completed `publish_events` jobs for the change log retention period, then deletes them with its own cleanup.

### 9.3 Change log port

```go
type ChangeLog interface {
    Publish(ctx context.Context, events []Event) error
    // Subscribe delivers events matching the filter, starting after the cursor.
    Subscribe(ctx context.Context, f Filter, from Cursor) (Stream, error)
    // Oldest returns the earliest cursor still retained.
    Oldest(ctx context.Context) (Cursor, error)
}
```

Adapters: Valkey Streams (the production default; events are appended to 64 streams partitioned by feature ID, and gateways read the partitions holding their subscribed features with `XREAD`; Valkey is chosen over Redis for its BSD licence), PostgreSQL `LISTEN/NOTIFY` with a replay table (the lightweight profile for small instances without Valkey), NATS JetStream (an optional adapter for high event volume), and in-memory (development and tests; refused in the replicated topology). Streams are trimmed by age; default retention is 24 hours, configurable. Valkey replicates asynchronously, so a failover can lose the newest events: gateways detect the failover and send `reset` to their clients, which refetch snapshots (section 9.11).

### 9.4 Subscription targets

| Target | Delivers | Notes |
| --- | --- | --- |
| `feature` | Changes to one feature, optionally limited to listed paths | The common case for streamed fields |
| `children` | Features entering, changing, or leaving the child list of a parent, optionally of one type | Powers live lists |
| `query` | Features entering, changing, or leaving a query result | Restricted: one type, a scope, and `eq`/`in` predicates on indexed fields; no sort-dependent windows |

A subscription is authorized at creation with the `read` policy, and its audience view is computed then. The requested paths are intersected with the readable paths of that view.

### 9.5 Subscribing without gaps

1. The gateway subscribes to the change log for the target and buffers incoming events.
2. It reads the snapshot through the Content application service, which returns each feature with its version.
3. It drops buffered events whose version is at or below the snapshot version for that feature, then delivers the snapshot followed by the remaining events.

The client receives one `snapshot` message and then `delta` messages, with no gap and no duplicate.

### 9.6 Per-subscriber projection

For each event, the gateway intersects `Changed` with the subscription's readable and requested paths. If nothing remains, the event is not delivered to that subscriber. Otherwise the delivered `delta` contains only those paths and their new values from `After`. When the event's version skips past the last version delivered for that feature, the delta instead carries every readable path from After, so nothing changed in a lost or reordered event is missed (section 9.2). Projection uses the same projector as HTTP responses.

Because filtered events create legitimate gaps in feature versions, each subscription has its own sequence number `sseq`, incremented per delivered message. Clients detect loss with `sseq`, never with the feature version.

### 9.7 Stream modes

| Mode | Semantics | Slow subscriber |
| --- | --- | --- |
| `state` | Only the latest value matters (progress, status, price) | Pending values for the same path are coalesced; delivery throttled to `maxRate` per second |
| `append` | Every value matters (chat, log lines) | Nothing is dropped; overflow closes the subscription with a resume cursor |
| ephemeral (`x-persist: false`) | Values never stored (typing indicators, cursors, sensor ticks) | Like `state`; the last value is kept in the shared last-value store (section 14.10) for new subscribers |

Ephemeral writes go through `POST /ephemeral/{type}/{id}` or a WebSocket `publish` message. They are authorized with the type's `update` policy, validated against the field's schema, and published straight to the change log as `Ephemeral` events, skipping the repository. Large append streams SHOULD be modeled as child features rather than an ever-growing List field.

### 9.8 Backpressure

Each connection has one writer goroutine and a bounded queue (default 256 messages). The dispatcher never blocks on a client.

```go
func (s *subscription) offer(d Delivery) {
    select {
    case s.out <- d:
    default:
        if d.Mode == ModeState {
            s.mu.Lock()
            for p, v := range d.Values { s.pending[p] = v } // keep only the latest
            s.mu.Unlock()
            s.signalFlush()
        } else {
            s.closeOverflowed() // sends reset with a resume cursor, then closes
        }
    }
}
```

### 9.9 Permission changes

Every commit that changes the relationship index also publishes `access-changed` events through its `publish_events` job, naming the affected principals and features. Gateways re-evaluate matching subscriptions: if read access is gone, they send `error` with `forbidden` and close the subscription; if the audience tier changed, they recompute the view and send a fresh `snapshot`. Suspending a principal closes all of its connections.

### 9.10 Transports and protocol

- **SSE** (`GET /v1/stream?sub=<encoded subscription>`): one subscription per connection; the event ID is the resume cursor, so browser reconnects with `Last-Event-ID` resume automatically. Heartbeat comment every 25 seconds.
- **WebSocket** (`/v1/ws`): many subscriptions multiplexed over one connection. Messages are JSON objects with `t` (type) and `sid` (subscription ID).

```json
{ "t": "subscribe", "sid": "s1", "target": "feature", "id": "0192f4c1-...",
  "paths": ["/progress", "/status"], "from": "c:8812:41" }
{ "t": "snapshot", "sid": "s1", "sseq": 1, "cursor": "c:8813:0",
  "features": [{ "id": "0192f4c1-...", "version": 41, "data": { "progress": 40 } }] }
{ "t": "delta", "sid": "s1", "sseq": 2, "cursor": "c:8820:0",
  "id": "0192f4c1-...", "version": 42, "set": { "/progress": 55 }, "unset": [] }
{ "t": "leave", "sid": "s2", "sseq": 7, "id": "0192f4c9-..." }
{ "t": "reset", "sid": "s1", "reason": "cursor_expired" }
{ "t": "error", "sid": "s1", "code": "forbidden" }
{ "t": "reconnect", "afterMs": 1500 }
```

### 9.11 Resume

On reconnect the client sends its last cursor. If the cursor is within retention, the gateway replays from it, projected as usual. If it is older than `Oldest`, the gateway sends `reset` followed by a fresh `snapshot`. Clients treat both paths identically.

### 9.12 Gateway implementation notes

- Subscriptions are indexed in a sharded map keyed by feature ID (and by parent ID for `children`, by type for `query`), so dispatching an event is one lookup per key, not a scan.
- Each subscription's lifetime is bound to its connection's `context.Context`; closing the connection cancels every subscription and its change log consumer.
- Gateways read only the change log partitions that hold at least one locally subscribed feature, and stop reading a partition when its last subscription closes.
- Metrics per gateway: connections, subscriptions, delivery latency (commit to send), coalesced messages, overflow closes.

## 10. Flow engine

Flows add behavior that schemas and policies cannot express. A flow is a run-to-completion directed acyclic graph of typed nodes: it starts, executes, and finishes, with no waiting or pausing. Flows are started by API requests, events, schedules, or inbound webhooks. Writes and side effects produced by a flow are staged during execution and committed in one short transaction at the end; side effects are jobs, run after commit.

### 10.1 Principles

- **Run to completion.** No `delay`, `waitForEvent`, or human approval inside a flow. Processes that span time are modeled as feature state (section 10.7).
- **DAG only.** Cycles are rejected at deploy. Iteration uses the bounded `forEach` node.
- **Typed end to end.** Every node declares input and output JSON Schemas; every expression is type-checked against them at deploy.
- **Staged writes, one commit.** Nodes never write to a repository directly. They stage mutations and jobs; the engine commits them together.
- **No transaction during external calls.** The commit happens after all nodes have run.

### 10.2 Triggers

| Trigger | Started by | Runs in | Caller waits | Can reject the write |
| --- | --- | --- | --- | --- |
| `beforeWrite` | Create, update, delete, or move of a type | `api`, inside the write pipeline | Yes | Yes |
| `action` | `POST /actions/{type}/{id}/{action}` or SDK call | `api` | Yes, receives the flow output | Not applicable |
| `event` | A committed write matching the trigger filter | `worker` | No | No (write already committed) |
| `schedule` | Cron expression in UTC | `worker`, as a River periodic job | No | Not applicable |
| `webhook` | `POST /hooks/{name}` from an external system | `api` enqueues a job, `worker` runs it | No; endpoint returns `202` | Not applicable |

Event triggers filter by event kind, type or trait, and an optional expr condition, for example `changed('/status') && after.status == 'done'`. Webhook triggers verify an HMAC signature or shared secret declared in the manifest before enqueuing the run. Event triggers are matched in the write pipeline: at commit, the compiled trigger filters are evaluated against each event, and one run\_flow job is enqueued per matching flow in the same transaction, so a committed write never misses its flows.

### 10.3 Actions

An action is a flow with an `action` trigger. It declares an input schema, an output schema, a policy (section 8.5), and optionally the state-machine transition it performs. Actions are the only way to change fields with `x-write: action:<name>`. The SDK generates a typed method per action.

Built-in actions on every type: `transfer` (Ownable types; changes `owner`, requires `isOwner` or admin). The `transition` action is generated for every state machine and dispatches to the declared transitions.

### 10.4 Flow definition

```json
{
  "flow": "completeTask",
  "version": 3,
  "trigger": { "action": "complete", "on": "urn:app:feature:task" },
  "runAs": "trigger",
  "input":  { "type": "object", "properties": { "note": { "type": "string", "maxLength": 2000 } } },
  "output": { "type": "object", "properties": { "late": { "type": "boolean" } } },
  "timeoutMs": 10000,
  "nodes": {
    "project": { "node": "feature.get", "in": { "id": "feature.parentId" } },
    "late":    { "node": "expr", "in": { "expr": "feature.dueAt != nil && now() > feature.dueAt" } },
    "setDone": { "node": "feature.update",
                 "in": { "id": "feature.id", "set": { "/status": "'done'" } } },
    "comment": { "node": "feature.create", "when": "input.note != ''",
                 "in": { "type": "'comment'", "parent": "feature.id",
                         "data": { "/body": "input.note" } } },
    "notify":  { "node": "email.send", "when": "nodes.late.out",
                 "in": { "to": "nodes.project.out.ownerEmail",
                         "template": "'late-task'",
                         "vars": { "title": "feature.title" } } }
  },
  "edges": [["project", "notify"], ["late", "notify"], ["late", "setDone"]],
  "result": { "late": "nodes.late.out" }
}
```

Every `in` value is an expr expression evaluated against the run context: `trigger`, `feature` (the trigger feature, storage view), `input` (action input), `event` (event triggers), `principal`, `nodes.<name>.out`, and `env` (app configuration). String literals are quoted inside the expression. `when` skips a node (and nodes depending only on it) when false. Nodes without edges between them may run concurrently.

### 10.5 Node kinds and catalog

Every node type has a kind that controls where it may appear and how the engine treats it.

| Kind | Meaning | Examples |
| --- | --- | --- |
| Pure | Deterministic computation, no I/O | `expr`, `template`, `jsonSchema.validate` |
| DataRead | Reads features through Content application services | `feature.get`, `feature.query`, `feature.children` |
| DataWrite | Stages mutations into the run's write set | `feature.create`, `feature.update`, `feature.delete`, `feature.move` |
| Effect | Stages a job, run after commit | `email.send`, `webhook.call`, `emit` |
| External | Calls outside the system during execution and uses the result | `http.request` (GET, HEAD, or idempotent calls), `llm.complete` |
| Code | Runs user code through the code-node protocol | `python` |
| Control | Shapes execution | `if`, `switch`, `forEach`, `subflow`, `reject`, `fail` |

Rules: `beforeWrite` flows may contain only Pure, DataRead, DataWrite (same repository as the triggering write), and Control nodes, so writes stay fast and never depend on external services. All other triggers may use every kind. `forEach` takes a list and a subgraph, runs at most `maxItems` iterations (default 100, hard maximum 1,000), and outputs a list. `subflow` calls another flow inline; subflow depth is limited to 4. `reject` aborts a `beforeWrite` or `action` flow with a declared error code and message.

### 10.6 Execution phases

1. **Prepare.** Resolve the flow version, build the run context, start the deadline timer (`context.WithTimeout`).
2. **Execute.** Run nodes in topological order, concurrently where independent. DataWrite nodes add mutations to the write set; DataRead nodes read through the write set first, so a run sees its own staged writes. Effect nodes add jobs. External and Code nodes run with the remaining deadline.
3. **Commit.** Submit the write set, the triggering write (for `beforeWrite`), and the staged jobs as one batch with version checks. The batch is subject to all normal validation, write rules, and policies, evaluated as the run's principal.
4. **Respond.** For request triggers, project the result through the caller's view. For background triggers, record the run outcome.

On `version_conflict` at commit, background runs retry the whole flow from step 1 up to 3 times with jittered backoff; request-triggered runs return `409` to the caller. Because nothing was committed and no effect was delivered, a retried run has no duplicated side effects. External and Code nodes, however, may run again, so they must tolerate repetition (section 11.6).

### 10.7 Long-running processes as data

Anything that waits is a feature with a state machine, advanced by short flows. Example: an AI summary.

1. The `summarize` action creates a `summaryJob` child with `status: pending` and returns its ID immediately.
2. An event-triggered flow on `summaryJob` created runs `llm.complete` and updates `result` and `status: done` (or `failed`).
3. `result` has `x-stream: {mode: state}`, so the client watches it fill in. The flow may write partial results as ephemeral updates while the model streams.

Approvals, reminders, and expirations use the same pattern with `schedule` triggers querying for due features.

### 10.8 Expressions

The expression language everywhere (policies, computed fields, guards, flows) is [expr](https://github.com/expr-lang/expr). Programs are compiled once at deploy with `expr.Env` set to a typed environment derived from schemas, plus `expr.AsKind` or `expr.AsBool` where the result type is known. Type errors fail `validate`.

- Built-in functions added by the framework: `now()`, `duration(s)`, `changed(path)`, `isOwner(f)`, `hasRole(f, r)`, `inGroup(id)`, `uuid()` (flows only), `len`, string and math helpers.
- Limits: compiled program size, maximum 10,000 collection elements produced by one evaluation, and evaluation inside the run's deadline. Expressions cannot perform I/O.
- Decimal and Int64 values are exposed with exact semantics; mixing them with Float is a compile error.

### 10.9 Identity, causation, and recursion

- `runAs: trigger` (default) runs with the triggering principal. `runAs: system` runs with the system principal and requires the flow to be marked `privileged: true`, which `plan` highlights for review. Event and schedule flows default to the principal that caused the event, or the system principal for schedules.
- Every batch carries a `CausationChain`: the list of `(flowID, runID, featureID)` that led to it. It is copied into events and into the arguments of the run\_flow jobs enqueued at commit, so the runs they start inherit it.
- The engine refuses to start a run when the chain is longer than 8, or when the same flow already appears in the chain for the same feature. The refusal is recorded as a run with status `loop_stopped` and an alert metric.

### 10.10 Errors and retries

- External nodes may declare `retry: {max, backoff}` for retryable errors (timeouts, `5xx`, `429`), within the run deadline.
- Any node may declare `onError: "<node>"` to route its error to a handler node. Without a handler, the run fails.
- A failed request-triggered run returns the node's error code (or `flow_failed`) and commits nothing.
- A failed background run fails its run\_flow job: River retries it with exponential backoff up to 5 attempts, then discards it, and the run is recorded as `dead_letter`. Dead-lettered runs can be inspected and retried from the CLI and admin console.
- Effect delivery failures are handled by the Integration context with their own retries; they never fail the run that staged them.

### 10.11 Time budgets

| Trigger | Default budget | Maximum |
| --- | --- | --- |
| `beforeWrite` | 2 s | 5 s |
| `action` | 10 s | 30 s |
| `event`, `schedule`, `webhook` | 60 s | 15 min |

Background runs execute as `run_flow` jobs, and each job's River timeout is set from its flow's budget rather than River's default job timeout, so a long event-triggered flow is not cancelled early.

### 10.12 Flow runs

Each run is recorded as a `urn:sys:feature:flowRun` feature: flow ID and version, trigger, principal, causation chain, status (`succeeded`, `failed`, `rejected`, `dead_letter`, `loop_stopped`), start and end times, per-node timing, and the error if any. Node inputs and outputs are stored only when the flow sets `trace: true`, are visible only to admins, and are kept for 7 days by default because they may contain owner or private fields.

### 10.13 Engine ports

```go
package flow // internal/flow/domain

type NodeType interface {
    Name() string                     // "email.send"
    Kind() NodeKind
    InputSchema() *schema.CompiledType
    OutputSchema() *schema.CompiledType
    Execute(ctx context.Context, in Input, rt Runtime) (Output, error)
}

// Runtime is what a node may use. It deliberately exposes no repository.
type Runtime interface {
    Principal() access.Principal
    Read() FeatureReader        // DataRead nodes; reads through the write set
    Stage() WriteSet            // DataWrite nodes
    Effects() EffectStager      // Effect nodes
    HTTP() OutboundHTTP         // External nodes; egress policy enforced
    Code() CodeRunner           // Code nodes
    LLM() LLMProvider           // llm.complete
    Log() RunLogger
}

type NodeRegistry interface {
    Register(NodeType) error
    Lookup(name string) (NodeType, bool)
}
```

Built-in nodes are registered through the same `NodeRegistry` as any future extension, so there is no privileged path for them.

### 10.14 Deploy-time validation

`validate` rejects a flow when any of these hold: the graph has a cycle; a node type is unknown; an expression fails to compile or type-check against its environment; an `in` value does not match the node's input schema; a node kind is not allowed for the trigger; `forEach` lacks `maxItems` above the default; a `DataWrite` in a `beforeWrite` flow targets another repository; `runAs: system` without `privileged: true`; or the result does not match the declared output schema.

### 10.15 Versioning

Each deploy creates a new flow version. Because runs complete within their budget, versioning only needs a drain: a run uses the version current at its start, and the old version is retired once its in-flight runs finish or time out.

## 11. Code nodes and the LLM node

Code nodes run user code inside flows through a language-neutral protocol; Python is the first runtime. Code receives typed JSON input, returns typed JSON output, and has no direct access to storage, credentials, or the network unless granted. A built-in `llm.complete` node covers the common AI case without code.

### 11.1 Code-node protocol

The engine talks to runner processes over gRPC on a Unix domain socket (or TCP for remote runners). The protocol is defined in `api/proto/codenode/v1/codenode.proto`.

| Message | Direction | Content |
| --- | --- | --- |
| `Load` | engine to runner | Node ID and code bundle hash; runner prepares or reuses a worker |
| `Execute` | engine to runner | Run ID, node ID, idempotency key (`runID:nodeID:attempt-independent`), deadline, input JSON |
| `Callback` | runner to engine | A host call: `feature.get`, `feature.query`, `llm.complete`, `log`; executed by the engine as the run's principal and through the run's write set |
| `CallbackResult` | engine to runner | Result or error of a callback |
| `Result` | runner to engine | Output JSON, or an error with code and message |
| `Cancel` | engine to runner | Deadline passed or run aborted |

The engine validates `Result` output against the node's output schema before any downstream node sees it. Callbacks are read-only: code cannot stage writes or effects directly; it returns data, and downstream DataWrite or Effect nodes act on it. This keeps every write visible in the flow graph.

### 11.2 Python runtime

```python
# nodes/classify.py
def run(input: dict, ctx) -> dict:
    task = ctx.get_feature(input["taskId"])          # projected for the run's principal
    result = ctx.llm(model="default", prompt=f"Classify: {task['title']}",
                     schema={"type": "object",
                             "properties": {"label": {"type": "string"}}})
    ctx.log("classified", label=result["label"])
    return {"label": result["label"], "score": 0.9}
```

The `ctx` object provides `get_feature`, `query`, `llm`, `log`, `idempotency_key`, and `remaining_ms`. The runtime is a small Python package (`fwnode`) vendored into every worker image; it implements the protocol and imports the user module once per worker.

```json
"classify": {
  "node": "python",
  "in": { "taskId": "feature.id" },
  "code": "nodes/classify.py",
  "requirements": "nodes/requirements.lock",
  "inputSchema":  { "type": "object", "properties": { "taskId": { "type": "string" } }, "required": ["taskId"] },
  "outputSchema": { "type": "object", "properties": { "label": { "type": "string" }, "score": { "type": "number" } }, "required": ["label"] },
  "limits": { "timeoutMs": 15000, "memoryMb": 512, "cpu": 1, "network": ["api.example-ai.com:443"] }
}
```

### 11.3 Isolation

The isolation level is a deployment setting (`runner.isolation`) and MUST match the trust model.

| Level | Mechanism | When |
| --- | --- | --- |
| `process` | Separate OS process, rlimits, read-only code directory, private temp directory | Development only (\`fw dev\`), or a single-replica SQLite deployment where code is written only by the app's own developers |
| `container` | One container per worker; seccomp profile; read-only root; no capabilities | Default for production |
| `microvm` | gVisor or Firecracker per worker | Required when several tenants' code runs on shared infrastructure |

At every level: no database credentials or app secrets in the environment; outbound network denied except the node's `network` allowlist, enforced by the runner's egress proxy, which also blocks private and link-local address ranges; CPU, memory, wall time, open files, and output size are limited; the runner kills the worker on `Cancel` or deadline.

### 11.4 Worker pools

- Workers are kept warm per (node, code bundle hash), with configurable minimum and maximum counts, because importing heavy libraries can take seconds.
- Workers are recycled after 500 executions or 30 minutes, and immediately after any execution that raised an unhandled exception, to limit state leaking between runs.
- The `runner` role exposes pool metrics: warm, busy, queue length, cold-start time.

### 11.5 Dependencies

Requirements are a lock file with hashes (`requirements.lock`). During `apply`, the CLI asks the runner to build an environment with `uv` (or a container image layer), keyed by the lock file hash and cached. Installing packages at run time is not supported. The manifest MAY restrict allowed packages with an allowlist in `app.json`.

### 11.6 Repetition and idempotency

Code nodes can run more than once for the same logical step: a background flow may retry after a commit conflict or a failure, and a worker may crash mid-execution. Code MUST tolerate repetition. `ctx.idempotency_key` is stable across retries of the same run and node, and SHOULD be passed to any external API that supports idempotency keys.

### 11.7 The llm.complete node

```json
"summarize": {
  "node": "llm.complete",
  "in": {
    "model": "'default'",
    "system": "'You summarize project updates in three sentences.'",
    "prompt": "feature.body",
    "maxTokens": "600",
    "streamTo": "{ 'feature': feature.id, 'path': '/summaryDraft' }"
  },
  "outputSchema": { "type": "object", "properties": { "summary": { "type": "string" } }, "required": ["summary"] },
  "retry": { "max": 2, "backoff": "exponential" }
}
```

- The node's `outputSchema` is sent to the provider as the required structured output format, and the response is validated before downstream nodes run. A response that fails validation counts as a retryable error.
- `streamTo` (optional) writes partial output to an ephemeral or `state`-mode field during generation, using the ephemeral path (section 9.7), so clients see text appear live.
- Model names in manifests are aliases (`default`, `fast`, `large`) mapped to provider models in environment configuration, so apps do not hard-code vendor model IDs.
- Provider keys are app secrets held by the Integration context and never exposed to code nodes; code reaches models only through `ctx.llm`.
- Every call records model, input and output token counts, latency, and cost estimate, and is subject to the instance's rate limits and monthly budget.

```go
package llm // internal/runtime/domain/llm

type Provider interface {
    Complete(ctx context.Context, req Request) (Response, error)
    Stream(ctx context.Context, req Request, onDelta func(Delta) error) (Response, error)
}

type Request struct {
    Model        string          // resolved provider model ID
    System       string
    Messages     []Message
    OutputSchema json.RawMessage // structured output contract
    MaxTokens    int
    Temperature  *float64
}
```

Adapters are injected by configuration: an Anthropic adapter, an OpenAI-compatible adapter (also covering local servers that implement that API), and a deterministic fake for tests and `dev`.

## 12. App manifest, CLI, and deployment

An application is a directory of JSON files kept in version control. The CLI (`fw`) validates it, shows a plan of changes against a running environment, applies it, runs tests, and generates client code. Editing definitions directly in production is not supported outside `dev`.

### 12.1 Manifest layout

```
tracker/
  app.json                 # name, version, repositories, auth, roles, settings
  types/
    project.json           # FeatureTypes (section 4)
    task.json
  traits/
    taggable.json
  policies/
    project.json           # read/create/update/delete/action rules (section 8.5)
    task.json
  flows/
    complete-task.json     # flows and actions (section 10)
    notify-late.json
  nodes/
    classify.py            # code-node sources (section 11)
    requirements.lock
  templates/
    email/late-task.mjml   # email templates used by email.send
  migrations/
    0003-task-estimate-decimal.json
  seed/
    dev.json               # features created by `fw seed` in dev
  tests/
    task-permissions.test.json
  env/
    dev.json  staging.json  prod.json
```

Larger apps MAY group files into modules: `modules/<name>/` with its own `types/`, `traits/`, `policies/`, `flows/`, and `nodes/`. A module's type IDs carry its name (`urn:app:crm:feature:contact/v1`), so names cannot collide across modules. Modules share one set of principals, one feature tree, and one deployment; they organize the manifest and are not an isolation boundary. Policies and flows may reference types in other modules.

### 12.2 app.json

```json
{
  "app": "tracker",
  "version": "1.4.0",
  "framework": ">=1.0 <2.0",
  "repositories": { "default": "${env.repositories.default}" },
  "auth": {
    "password": { "enabled": true, "minLength": 12 },
    "oidc": [ { "provider": "google", "clientId": "${env.oidc.google.clientId}",
                "clientSecret": "${secret:oidc-google}" } ],
    "mfa": { "requiredForRoles": ["admin"] }
  },
  "roles": { "viewer": [], "editor": ["viewer"], "admin": ["editor"], "billing": ["viewer"] },
  "limits": { "requestsPerMinute": 600, "maxDocumentBytes": 262144 },
  "llm": { "aliases": { "default": "${env.llm.default}" }, "monthlyBudgetUsd": 200 },
  "python": { "allowedPackages": ["httpx", "pydantic", "numpy"] }
}
```

`${env.<path>}` reads the selected environment file. `${secret:<name>}` references a secret stored in the server's secret store; secret values never appear in the manifest, plans, logs, or API responses.

### 12.3 CLI commands

| Command | Purpose |
| --- | --- |
| `fw init <name>` | Scaffold a manifest with an example type, policy, and test |
| `fw dev` | Run all roles locally with the SQLite repository and River, the in-memory change log, and the fake LLM, so jobs and schedules behave as in production; hot reload on file changes; seed data applied |
| `fw validate` | Profile check, compile types, type-check every expression and flow, verify references between files |
| `fw plan --env prod` | Diff against the environment; list breaking changes per view, migrations, index builds, privileged flows, in-flight runs affected |
| `fw apply --env prod` | Execute a plan (requires the plan hash from `plan` unless `--auto-approve`) |
| `fw test` | Run manifest tests against an in-memory instance |
| `fw codegen ts --out ./src/gen` | Generate TypeScript types and the typed client for each audience |
| `fw migrate status\|run\|pause` | Inspect and control data migrations |
| `fw seed --env dev` | Load seed data |
| `fw runs list\|show\|retry` | Inspect flow runs and retry dead-lettered runs |
| `fw export` / `fw import` | Move all features and definitions between environments or backends (NDJSON) |
| `fw login`, `fw token create` | Authenticate the CLI; create service-account API keys |
| `fw tail` | Stream change events for debugging (admin only, projected) |

The CLI talks to the server only through the public API with an admin service account, so everything it does is authorized and audited like any other client.

### 12.4 Plan and apply

`plan` sends the compiled manifest to the server, which compares it with the deployed version and returns: type changes with per-view compatibility (section 4.8), required and supplied migrations, indexes to build or drop, policy changes, flow versions created and in-flight runs that will drain, privileged flows added or changed, and new public fields (flagged as potential data exposure). The plan has a hash; `apply` refuses if the deployed state changed since the plan.

`apply` runs in this order: build code-node environments; register new type versions (expand); enqueue index builds and migration batches as jobs; activate new policies and flow versions; once migrations finish, retire old type versions (contract). Each deploy is recorded with the manifest hash, actor, and plan.

### 12.5 Data migrations

Migrations use an expand, migrate, contract sequence so the API keeps serving during the change.

```json
{
  "id": "0003-task-estimate-decimal",
  "type": "urn:app:feature:task",
  "from": 1,
  "to": 2,
  "set": {
    "/estimate": "string(feature.estimateHours)",
    "/estimateHours": "nil"
  },
  "batchSize": 500
}
```

- Each feature records the type version it was written with. During migration, reads of features still at the old version are upcast in memory by the same `set` expressions, so clients always see the new shape.
- Each batch is a migrate\_batch job, unique per migration and batch, that applies the changes with version checks. River retries failed batches, so a migration is resumable, and its progress is the share of completed batch jobs. Re-running a finished batch is a no-op.
- Migrations that need logic beyond expressions may name a flow instead of `set`; that flow runs as system and must be `privileged`.
- Rollback of an app version re-applies the previous manifest. Data migrations are not reversed automatically; a reverse migration must be written explicitly.

### 12.6 Manifest tests

```json
{
  "name": "only the assignee can complete a task",
  "given": {
    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user" } },
    "features": [
      { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
      { "ref": "t", "type": "task", "parent": "p", "owner": "ana",
        "data": { "title": "T", "status": "in_progress", "assignee": "ben" } }
    ],
    "grants": [ { "subject": "ben", "object": "p", "role": "editor" } ]
  },
  "steps": [
    { "as": "ana", "action": "complete", "on": "t", "expect": { "error": "forbidden" } },
    { "as": "ben", "action": "complete", "on": "t",
      "expect": { "ok": true, "feature": { "status": "done" },
                  "effects": [ { "node": "email.send", "count": 0 } ] } }
  ]
}
```

Tests run against the memory repository with the fake LLM; Effect nodes are recorded, not delivered; External nodes use fixtures declared in the test or fail. Principal names in tests are local labels for fixtures only.

## 13. Browser SDK and admin console

The browser SDK is a framework-independent TypeScript core with thin bindings for UI frameworks. Types and typed methods are generated from the manifest's public views, so private fields do not exist on the client. The admin console is a generated web app built on the same SDK.

### 13.1 Packages and code generation

| Package | Contents |
| --- | --- |
| `@fw/client` | Transport, auth, cache, live queries, mutations, streaming; no UI framework dependency |
| `@fw/react` | Hooks: `useFeature`, `useQuery`, `useChildren`, `useAction`, `useSession` |
| `@fw/vue`, `@fw/svelte` | Equivalent composables and stores (after React) |
| generated `./gen` | Types per type and audience, the typed client, action methods, enum and state-machine constants |

```ts
// generated by `fw codegen ts`
export interface Task {
  readonly id: FeatureId<"task">;
  readonly version: number;
  title: string;
  status: "todo" | "in_progress" | "done";
  assignee: FeatureId<"principal"> | null;
  estimate?: Decimal;      // string-backed decimal type
  dueAt?: Date;
  progress?: number;
  readonly isLate: boolean;
}

export const client = createClient<Schema>({ baseUrl: "/" });

const task = await client.task.get(id);
await client.task.update(task, { title: "New title" });   // sends If-Match: task.version
await client.task.actions.complete(task, { note: "Shipped" });
```

Codegen also emits a JSON Schema validator for each create and update view so forms can validate locally with the same rules as the server.

### 13.2 Runtime behavior

- **Auth.** `client.auth` handles password login, OIDC redirects, logout, and session refresh. Browser sessions use the `HttpOnly` cookie; the SDK never stores tokens in `localStorage`. It attaches the CSRF header automatically and broadcasts logout across tabs with `BroadcastChannel`.
- **Transport.** One WebSocket per client for all subscriptions, falling back to SSE per subscription when WebSockets are blocked. Automatic reconnect with backoff and resume from the last cursor; `reset` triggers a silent refetch.
- **Normalized cache.** Features are cached by ID. Every query result is a list of IDs plus cursor, so an update to one feature updates every view that contains it.
- **Live queries.** `useQuery` and `useChildren` subscribe to the matching `query` or `children` target (section 9.4). Results update as features enter, change, or leave, with no refetch. Queries the gateway cannot serve live (for example, sorted windows) refetch on relevant events instead, which the SDK decides from the query shape.
- **Optimistic updates.** Mutations apply to the cache immediately, tagged as pending. On success the server version replaces them; on `version_conflict` or validation errors they roll back and the error is surfaced with the server's current value.
- **Errors.** Every rejected promise is a typed `FwError` with the problem-details `code`, so UIs can branch on `forbidden`, `version_conflict`, or `validation_failed` with field paths.

```tsx
function TaskProgress({ id }: { id: FeatureId<"task"> }) {
  const { data: task } = useFeature(client.task, id, { paths: ["/progress", "/status"] });
  if (!task) return null;
  return <progress value={task.progress ?? 0} max={100} />;   // updates live
}
```

Offline support is out of scope for version 1, but the cache keeps pending mutations as an ordered log so a persistent queue can be added later.

### 13.3 Admin console

The admin console is a single-page app served by the `api` role at `/admin`, generated at run time from the compiled types and the caller's views (an admin sees admin views; a regular user, if allowed in, sees only what their views allow). It provides:

- Browse the feature tree, list and filter by type, and open any feature with a form generated from its update view, including references (searchable pickers), enums, state-machine transition buttons, and live-updating streamed fields.
- Principals, teams, and grants management; suspending and deleting principals with the ownership-transfer workflow (section 8.8).
- Flow runs: list, filter by status, inspect timing and traces, retry dead-lettered runs. Background jobs: the River UI is embedded at /admin/jobs for admins, to inspect queues and retry discarded jobs.
- Deploy history, current plan, migration progress, index build status.
- Usage: requests, storage, streaming connections, LLM tokens and cost.

The console uses only the public API and SDK, so it serves as a continuous end-to-end test of the platform. Generating end-user UI from schemas is out of scope.

## 14. Operational concerns

Each instance runs exactly one app for one client (section 14.6). The limits, network rules, observability, file handling, and delivery guarantees below are part of version 1, not later hardening.

### 14.1 Limits

All limits have server-wide defaults, may be lowered in `app.json`, and may only be raised by the operator.

| Limit | Default |
| --- | --- |
| Request body | 1 MiB (uploads use signed URLs, section 14.4) |
| Feature document size | 256 KiB |
| Object nesting depth in a document | 8 |
| List field items | 1,000 (schema may set lower) |
| Transaction operations | 100 |
| Query predicates / depth | 20 / 4 |
| Query page size | 500 |
| Requests per principal | 600 per minute |
| Unauthenticated requests per IP | 60 per minute |
| Login attempts | 10 per account per 15 minutes, then exponential delay |
| WebSocket subscriptions per connection | 200 |
| Streaming connections per principal | 20 |
| Ephemeral writes per feature | 30 per second |

Limits are enforced in HTTP middleware (sizes, rates) and in application services (domain limits), so they apply equally to the API, the CLI, and flows calling application services.

### 14.2 Outbound network safety

Every outbound connection made on behalf of an app (`http.request`, `webhook.call`, code-node network access, OIDC discovery) goes through one egress component. It resolves DNS itself and refuses private, loopback, link-local, and cloud metadata addresses, re-checks after redirects (maximum 3), enforces domain allowlists when configured, applies timeouts, and caps response size. Without this, anyone who can deploy a flow could probe the server's internal network.

### 14.3 Observability

- **Tracing.** OpenTelemetry from the first commit. The trace context of a request is stored in job arguments and events, so a trace follows a write through event publishing, gateway delivery, event-triggered flows, effect delivery, and code nodes.
- **Metrics.** RED metrics per endpoint; commit-to-delivery latency for streaming; job latency and backlog per queue; flow runs by status; code-node cold starts; LLM tokens and cost; search index lag; migration progress.
- **Logs.** Structured JSON via `log/slog`. Field values are never logged by default; when debug logging of documents is enabled, only public-view fields are written.
- **Audit log.** Written in the commit transaction (one audit row per mutation, alongside its jobs) plus authentication events, so it stays complete even though the change log is not durable; kept for a configurable period (default 90 days), queryable by admins.

### 14.4 Files and blob storage

- A `BlobStore` port with adapters for S3-compatible storage and the local filesystem (dev).
- Uploads: the client requests `POST /uploads` with the target type, field, size, and content type; the server checks the update policy and schema limits (`x-maxBytes`, `x-contentTypes` on Blob fields), then returns a signed upload URL valid for 15 minutes. After upload, the client commits the returned blob ID to the field in a normal update.
- Downloads: reading a Blob field returns a short-lived signed URL, generated only for principals who can read the field.
- Unreferenced blobs are deleted by a cleanup\_blobs periodic job after 24 hours. Image thumbnails are generated by an event-triggered system flow when the field declares `x-thumbnails`.

### 14.5 Effect delivery

Effects are jobs (`deliver_email`, `deliver_webhook`) inserted in the same transaction as the writes that staged them. The Integration context implements their workers.

- **Email**: `EmailSender` port with SMTP and API-provider adapters; templates rendered from `templates/` with the staged variables; sender identity and daily quota from configuration.
- **Webhooks**: payload signed with HMAC-SHA256 (`X-Fw-Signature` with timestamp to prevent replay); `X-Fw-Delivery` ID for receiver idempotency.
- **Retries**: River's retry policy, configured for exponential backoff with jitter over up to 24 hours; jobs that exhaust their attempts are discarded and can be retried from the admin console.
- **Ordering**: effects run as independent jobs in no guaranteed order; a flow that needs one effect after another emits an event and lets a second flow stage the next effect.

### 14.6 Single-tenant model

Each instance runs exactly one app for one client. A second client, or a separate application with its own user base, gets its own instance with its own database, change log, blob storage, and secrets.

Nothing is keyed by app or tenant: there are no `app_id` columns, no app segment in URLs or change log subjects, and no cross-app isolation tests. Limits, quotas, the LLM budget, the data key, and code-node pools are configured per instance. Organizations and teams inside the client's app are modeled with principals and grants (section 8.6). Larger apps are organized with manifest modules (section 12.1), which share one set of principals. Data moves between instances with `fw export` and `fw import` (section 14.8).

### 14.7 Secrets and encryption

App secrets (OIDC client secrets, provider API keys, webhook signing keys) are stored encrypted with envelope encryption: an instance data key encrypted by a master key held in a KMS or, for self-hosting, a key file outside the database. Secrets are referenced by name in manifests and resolved only inside the Integration and Access contexts. TLS is required for all external traffic; the server refuses to start in production mode without it unless explicitly behind a trusted proxy.

### 14.8 Backup and export

Backups are the responsibility of each repository backend (for PostgreSQL, standard point-in-time recovery). Independently, `fw export` produces a backend-neutral NDJSON archive of definitions and features, which `fw import` loads into any backend; this is also the supported way to move between databases.

### 14.9 Server configuration

One YAML file plus environment variable overrides (`FW_` prefix), loaded and validated at startup into a typed `config.Config`. It selects roles, the topology (section 14.10), repository adapters and connection settings, River queue concurrency, change log adapter, search adapter, blob store, email sender, LLM providers, runner isolation, limits, and observability exporters. Invalid configuration stops startup with an error naming the key.

### 14.10 Cloud-native deployment

The system is cloud native. Every role MUST run correctly as multiple replicas, any replica may be killed at any time, and no correctness may depend on which replica serves a request. SQLite deployments (section 5.3) are the only single-replica exception.

**State.** The `api`, `gateway`, `worker`, and `runner` roles keep no durable local state. All state lives in shared services:

| State | Production service | Refused in the replicated topology |
| --- | --- | --- |
| Features, jobs, credentials, sessions, idempotency keys | PostgreSQL | Memory, SQLite |
| Change log | Valkey Streams, or in the lightweight profile PostgreSQL `LISTEN/NOTIFY` | Memory |
| Search index | PostgreSQL full-text search or Meilisearch | Bleve |
| Blobs | S3-compatible storage | Local filesystem |
| Rate-limit counters | Valkey (via a `RateLimiter` port) | Local counters |
| Secrets | KMS or Kubernetes Secrets | Local key file |
| Last values of ephemeral fields | Valkey, or a PostgreSQL table in the lightweight profile | Gateway memory |

A replica cannot count its peers, so the server never decides this by detecting other replicas. Instead the configuration declares a topology: `replicated` (the default in production mode, and what the Helm chart sets) or `single` (for `fw dev` and single-replica SQLite deployments). At startup, the `replicated` topology refuses every adapter in the right-hand column with an error naming it. The `single` topology allows them; there, the SQLite file lock (section 5.3) stops an accidental second replica from starting.

**No leader election in framework code.** Every role is identical across its replicas, and no replica refers to another. The two needs that once called for a leader are met by shared services: scheduled flows are River periodic jobs, which River enqueues exactly once through its own leadership in the database, and event ordering is tolerated by version-aware consumers (section 9.2). There is no `leader.Elector` port.

**Work distribution.** All background work is River jobs (section 2.5), claimed from the database by any `worker` replica. River rescues jobs whose replica crashed and runs them again elsewhere, which is why every job worker is idempotent.

**Caches.** Per-replica caches (compiled types, policy programs, relationship-index lookups) are only optimizations. They are invalidated by change log events (`type-registered`, `access-changed`) and have a maximum age of 60 seconds as a safety net.

**No correctness in replica memory.** Anything another replica may need lives in a shared service: last values of ephemeral fields (table above); OIDC `state` values and PKCE verifiers (an encrypted, short-lived cookie, not server memory); idempotency keys and upload intents (repository); the relationship index (repository tables); work claims (River job rows); cursor-signing and session secrets (instance secrets). Per-replica memory may hold only caches, subscription registries, coalescing buffers, open connections, and warm code-node workers. Losing any of these on restart costs performance, never correctness, and the multi-replica end-to-end tests (milestone 10) verify it by killing replicas mid-operation.

**Streaming.** Gateways need no sticky sessions: a client can resume on any replica with its cursor. Load balancers must allow long-lived WebSocket and SSE connections with an idle timeout above the 25-second heartbeat. Before shutting down, a gateway sends `{"t": "reconnect"}` to its clients with a random delay hint, spreading reconnects across the remaining replicas.

**Health probes.** Every role serves three probes on an internal port (default `:9090`, shared with `/metrics`), never on the public listener.

| Probe | Endpoint | Passes when | Kubernetes defaults |
| --- | --- | --- | --- |
| Startup | `/startupz` | Configuration validated; framework tables at the expected version; current manifest loaded and compiled; role startup done (gateway: change log connected; runner: minimum warm workers started) | period 5 s, failure threshold 60 |
| Liveness | `/livez` | The process is responsive: an internal watchdog goroutine ticked within the last 10 s and the probe handler answers. It never checks dependencies, so an outage of a database or broker cannot cause restart loops | period 10 s, failure threshold 3 |
| Readiness | `/readyz` | The role's required dependencies answer within 1 s and the replica is not shutting down | period 5 s, failure threshold 2 |

Readiness requirements per role:

| Role | Required for readiness |
| --- | --- |
| `api` | Repository and credential store respond; manifest loaded |
| `gateway` | Change log connected; repository responds (for snapshots) |
| `worker` | River client running; repository and change log respond |
| `runner` | Each configured pool can start or reuse a worker |

`/readyz` returns `200` or `503` with a JSON body listing each check with its status and latency. Optional dependencies (search index, email provider, LLM providers) are reported as `degraded` but never fail readiness, so an outage of one feature does not take the whole API out of rotation. Check results are cached for 2 s so probe traffic never loads the dependencies. The probe handlers live in `platform/health`, and each context registers its checks through a `health.Registry` at the composition root.

**Shutdown.** On `SIGTERM` a replica fails readiness immediately, waits 5 s for the load balancer to stop routing to it, stops accepting new work, then finishes in-flight requests and flow runs within 25 s. Gateways send `reconnect` to their clients; unfinished work is redelivered to other replicas. The Helm chart sets `terminationGracePeriodSeconds` to 35.

**Deploys.** `apply` stores the new manifest as active and publishes a `manifest-activated` event. Each replica loads the active manifest from the database when it receives the event, checks for it every 30 s as a fallback, and reports not ready until it has loaded it. Old type versions are retired by a `retire_type_version` job scheduled once the migrations finish and at least 5 minutes after activation, longer than the cache maximum age, so no replica still serves them. `apply` never needs to know how many replicas exist.

**Packaging.** One OCI image contains the server binary; the role is chosen by flags or environment. The project ships a Helm chart with one Deployment per role, a separate Deployment for runner pools, a PodDisruptionBudget per role, and horizontal autoscaling on CPU for `api` and `worker` and on open connections for `gateway`. Configuration comes from environment variables and ConfigMaps; secrets from Kubernetes Secrets or a KMS.

## 15. Go implementation and Domain-Driven Design rules

The implementation MUST follow Domain-Driven Design. Each bounded context from section 2.2 is a Go package tree with four layers (domain, application, infrastructure, interfaces), dependencies point inward only, and contexts communicate only through published contracts. This keeps each context independently understandable and testable, lets teams or agents work on contexts in parallel, and allows a context to be extracted into its own service later without rewriting its domain.

### 15.1 Repository layout

```
/
  go.mod
  cmd/
    fwd/                  # server binary; composition root (wiring.go)
    fw/                   # CLI binary
  internal/
    kernel/               # shared kernel: ID, Kind, Value, Version, errors, Clock
    schema/
      api/                # published contracts: TypeRegistry, CompiledType DTOs
      domain/             # FeatureType, Trait, Compiler, View, CompatibilityChecker
      application/        # RegisterType, PlanTypeChange use cases
      infrastructure/     # type storage adapter, jsonschema adapter
      interfaces/         # HTTP handlers for /schema
    content/
      api/  domain/  application/  infrastructure/{memory,postgres}/  interfaces/http/
      repotest/           # repository conformance suite
    access/   query/   change/   flow/   runtime/   integration/   manifest/
      (same four layers + api/ in each)
  platform/               # cross-cutting technical code, no domain logic
    config/  httpserver/  observability/  jobs/  health/  egress/
  api/proto/codenode/v1/  # code-node protocol
  sdk/ts/                 # @fw/client, @fw/react
  runner/python/          # fwnode runtime package and worker
  admin/                  # admin console source
  test/e2e/               # black-box tests against a running server
  docs/adr/               # architecture decision records
```

### 15.2 Layer rules

| Layer | Contains | May import | Must not import |
| --- | --- | --- | --- |
| `domain` | Aggregates, entities, value objects, domain events, domain services, repository and port interfaces | Standard library, `kernel`, own context's `domain` | Any other layer; other contexts; drivers; HTTP; `platform` |
| `application` | Use cases (application services), transaction orchestration, DTOs, ports for external services | Own `domain`, `kernel`, other contexts' `api` packages | Own or other `infrastructure`; `interfaces`; drivers |
| `infrastructure` | Adapters implementing ports: databases, brokers, providers | Own `domain` and `application`, `kernel`, `platform`, drivers | `interfaces`; other contexts' internals |
| `interfaces` | HTTP handlers, WebSocket handlers, gRPC servers, CLI command handlers, request/response mapping | Own `application`, `kernel`, `platform` | `domain` internals beyond DTOs; `infrastructure` |
| `api` | The context's published contracts for other contexts: interfaces and DTOs only | `kernel` | Everything else |

These rules MUST be enforced in CI with `depguard` (through `golangci-lint`) or `go-arch-lint`. A pull request that violates them fails the build.

### 15.3 Building blocks per context

| Context | Aggregates | Key value objects | Domain services |
| --- | --- | --- | --- |
| Schema | FeatureType, Trait | TypeID, FieldDef, View, ViewKey, Visibility | Compiler, CompatibilityChecker |
| Content | Feature | Patch, Mutation, Batch, Ancestry | Projector, WritePipeline rules |
| Access | Principal (identity data), Grant, Session | Credential, Decision, Audience, Role | PolicyEvaluator, RelationshipIndex |
| Query | (none; stateless) | Plan, Predicate, Cursor, SortKey | Planner, CostEstimator |
| Change | Subscription | Event, Cursor, Delivery, StreamSpec | Matcher, Coalescer |
| Flow | Flow, FlowRun | NodeSpec, Edge, WriteSet, CausationChain | GraphValidator, Executor |
| Runtime | CodeNode, WorkerPool | CodeBundle, Limits, ModelAlias | ExpressionCompiler |
| Integration | None; deliveries are River jobs | EmailMessage, WebhookCall, BlobRef | TemplateRenderer, WebhookSigner |
| Manifest | AppVersion, Migration | ManifestHash, Plan | Differ |

Rules for the building blocks:

- Aggregates keep fields unexported and change state only through methods that enforce invariants. They never perform I/O.
- One repository per aggregate, defined in `domain`, implemented in `infrastructure`.
- Aggregates record domain events in memory; repositories persist them atomically with the aggregate (as jobs, section 2.5). Application services never publish events directly.
- Value objects are immutable and validated at construction (`NewTypeID(s) (TypeID, error)`); invalid values cannot exist.
- Application services are thin: load aggregates, call domain methods, commit through the repository, map results. Business rules belong in the domain.
- Domain events are internal to a context. Events published to other contexts (integration events) are defined in the context's `api` package and are versioned.

### 15.4 Communication between contexts

- Synchronous: call another context's `api` interface, implemented by an adapter in that context and injected at the composition root.
- Asynchronous: consume integration events from the change log. Consumers translate them into their own model in their `infrastructure` layer (anti-corruption layer).
- Contexts never share database tables. When contexts share one PostgreSQL database for convenience, each owns its tables, prefixed by context (`content_features`, `access_grants`), and no context queries another's tables.

### 15.5 Dependency injection

Wiring is manual constructor injection in one composition root, `cmd/fwd/wiring.go`. No global variables, no `init()` side effects, no service locator.

```go
func buildContent(cfg config.Config, deps Shared) (*contentapp.Service, error) {
    repos := content.NewRepositoryRegistry()
    for name, rc := range cfg.Repositories {
        repo, err := contentinfra.Open(rc, deps.Clock, deps.IDs) // selects memory|postgres|...
        if err != nil {
            return nil, fmt.Errorf("repository %q: %w", name, err)
        }
        repos.Register(name, repo)
    }
    return contentapp.NewService(contentapp.Deps{
        Repos:      repos,
        Types:      deps.TypeRegistry,   // schema/api
        Authorizer: deps.Authorizer,     // access/api
        Flows:      deps.BeforeWrite,    // flow/api
        Clock:      deps.Clock,
        Tracer:     deps.Tracer,
    }), nil
}
```

Roles from section 2.1 decide which parts of the graph are built. Cycles between contexts are resolved by depending on interfaces in `api` packages and wiring the implementation afterwards, never by importing each other.

### 15.6 Coding conventions

- Go version: the current stable release at project start, pinned in `go.mod` with a `toolchain` line.
- `context.Context` is the first parameter of every function that does I/O or may block.
- Errors: domain errors are typed values in `kernel/errors` with stable codes; wrap with `%w`; map to HTTP codes only in `interfaces`. No `panic` outside programmer errors at startup.
- Time comes from `kernel.Clock`; IDs from `kernel.IDGenerator`. Tests use fakes.
- Logging with `log/slog`, tracing with OpenTelemetry, both received through constructors.
- Concurrency: every goroutine has an owner that stops it through context cancellation; use `errgroup` for fan-out; no goroutine is started from a domain package.
- Generics are used for typed collections (`Page[T]`) and helpers, not for the value model.
- Public identifiers in `api` packages carry doc comments.

### 15.7 Testing strategy

| Level | Scope | Tools |
| --- | --- | --- |
| Domain unit tests | Aggregates, value objects, compiler, planner, policy evaluation; no I/O | Standard `testing`, table-driven |
| Application tests | Use cases with memory adapters | Memory repository, memory change log, fake clock and LLM |
| Conformance suites | Every adapter of a port: repositories, change logs, search indexes, blob stores | Shared suites per port |
| Infrastructure tests | Real databases and brokers | `testcontainers-go` |
| End-to-end | The binary with a sample manifest, through HTTP, WebSocket, CLI, and SDK | `test/e2e`, the tracker example app |
| Fuzz tests | Schema compiler, query parser, merge-patch application, expression environment | Go native fuzzing |

All tests run with `-race` in CI. Domain packages target 90% statement coverage; the number matters less than every invariant in this document having a test that names it.

### 15.8 Libraries

The implementing agent SHOULD verify that each library is maintained and compatible at project start, and record the choice in an ADR.

| Need | Choice |
| --- | --- |
| JSON Schema 2020-12 validation | `github.com/santhosh-tekuri/jsonschema` (v6) |
| Expressions | `github.com/expr-lang/expr` |
| PostgreSQL | `github.com/jackc/pgx` (v5) |
| Change log | `github.com/valkey-io/valkey-go` (Valkey Streams; a NATS adapter is optional) |
| Full-text search (embedded) | `github.com/blevesearch/bleve` |
| HTTP routing | Standard library `net/http` `ServeMux` patterns |
| WebSocket | `github.com/coder/websocket` |
| CLI | `github.com/spf13/cobra` |
| UUIDv7 | `github.com/google/uuid` |
| Decimal | `github.com/cockroachdb/apd` (v3) |
| Observability | `go.opentelemetry.io/otel` |
| Code-node protocol | `google.golang.org/grpc` |
| Test containers | `github.com/testcontainers/testcontainers-go` |
| Background jobs and scheduling | `github.com/riverqueue/river` with riverpgxv5, riversqlite, and riverui |

### 15.9 Instructions for the implementing agent

1. Treat this document as the source of truth. Do not add features, endpoints, or keywords that it does not describe; propose them in an ADR instead.
2. Work milestone by milestone (section 16). Do not start a milestone before the previous one meets its acceptance criteria.
3. For each context, write the domain layer and its unit tests first, then application services with memory adapters, then infrastructure, then interfaces.
4. Write the conformance suite for a port before the second adapter of that port.
5. Where this document is ambiguous, choose the simpler option, record it in `docs/adr/NNNN-title.md` with context, decision, and consequences, and continue.
6. Each pull request is done when: it builds, `golangci-lint` and architecture rules pass, tests pass with `-race`, new behavior has tests that reference the section of this document they implement, and public `api` changes are documented.
7. Never weaken a security rule (projection, default deny, egress checks, limits) to make a test or feature work; raise it as an ADR instead.

## 16. Roadmap and open questions

Implementation proceeds in ten milestones. Each ends with something runnable, and the tracker example app (projects, tasks, comments, teams) grows with each milestone as the end-to-end acceptance test. A milestone is complete only when its acceptance criteria pass in CI.

### 16.1 Milestones

1. **Kernel and Schema.**
   - Delivers: shared kernel; profile check; `x-` vocabulary; compiler; traits; views; computed-field and state-machine compilation; compatibility checker.
   - Done when: the tracker types compile; every rejected keyword is refused with its path; views for every audience match golden files; the compatibility checker classifies every row of the table in section 4.8; fuzzing the compiler for 10 minutes finds no panics.
2. **Content with the memory repository.**
   - Delivers: Feature aggregate; write pipeline without flows; transactions with temporary references; memory repository; the repository conformance suite.
   - Done when: the memory repository passes the conformance suite, including 100-goroutine concurrent version tests.
3. **Access, generic API, and query on memory.**
   - Delivers: principals, password login, sessions, CSRF, API keys; policies; grants; relationship index; audience resolution; projector; HTTP API from section 6; query planner from section 7.3; health probes (section 14.10).
   - Done when: the tracker's permission matrix (each role against each operation on each type) passes through HTTP; no test can obtain a private field through read, query, error, or transaction responses.
4. **PostgreSQL.**
   - Delivers: PostgreSQL repository with the layout in section 5.3, expression indexes, ancestry, River migrations and transactional job insertion; then the SQLite repository (section 5.3).
   - Done when: it passes the same conformance suite as memory; queries on indexed fields use indexes (checked with `EXPLAIN` in tests); a 1-million-feature dataset answers indexed queries under 50 ms at p95 on reference hardware; the SQLite repository passes the same suite except the multi-replica tests.
5. **Manifest and CLI.**
   - Delivers: `init`, `dev`, `validate`, `plan`, `apply`, `test`, `seed`, `export`, `import`; deploy records; data migrations.
   - Done when: the tracker is defined entirely as a manifest; `plan` reports a breaking change correctly; a migration of 100,000 features completes while the API keeps serving.
6. **Change log and streaming.**
   - Delivers: event publishing jobs; memory, Valkey, and PostgreSQL change logs; gateway with SSE and WebSocket; feature, children, and query subscriptions; stream modes; resume; permission-change handling.
   - Done when: across a forced gateway restart, clients see every change exactly once after version-based deduplication; a slow client in `state` mode receives coalesced values without slowing others; revoking a grant closes the affected subscription within 2 seconds.
7. **TypeScript SDK.**
   - Delivers: codegen; `@fw/client`; `@fw/react`; normalized cache; live queries; optimistic updates.
   - Done when: a minimal tracker UI built only on the SDK shows live updates across two browsers, and a version conflict rolls back the optimistic change.
8. **Flows, actions, and effects.**
   - Delivers: flow engine; all five triggers; node catalog except Code and `llm.complete`; actions and state machines; causation limits; flow runs; email and webhook delivery.
   - Done when: every validation rule in section 10.14 has a failing-manifest test; a commit conflict retries without duplicating an email; a flow loop is stopped and recorded as `loop_stopped`.
9. **Search, aggregates, and admin console.**
   - Delivers: search index port with PostgreSQL full-text search (production) and Bleve (development); per-tier indexing; `x-aggregate`; the admin console.
   - Done when: public search never matches owner or private text; aggregates stay correct under concurrent child changes; the admin console can perform every tracker operation.
10. **Code nodes, LLM, files, and hardening.**
    - Delivers: Python runner with `process` and `container` isolation; `llm.complete` with Anthropic, OpenAI-compatible, and fake adapters; blob storage; OIDC; MFA; full limits and egress checks; observability dashboards; OCI image and Helm chart (section 14.10).
    - Done when: a code node cannot reach a private address or read features it was not given in tests; the AI summary pattern from section 10.7 streams into a client; a security review checklist derived from sections 8 and 14 passes; the end-to-end suite passes against at least two replicas of every role while replicas are killed at random.

### 16.2 Decisions

The project owner's decisions are recorded in the table and override any conflicting default elsewhere in this document. New questions that come up during implementation are recorded as ADRs (section 15.9) and added here once decided.

| Question | Decision |
| --- | --- |
| Deployment target | Cloud native: every role MUST support multiple replicas (section 14.10). Code-node isolation defaults to `container`; `process` is for development only. |
| Field-level grants | Not needed. Built-in roles plus app-defined roles. |
| Sorted, windowed query subscriptions | No. The SDK refetches on relevant events. |
| Change log retention | 24 hours by default, configurable. |
| Second backend after PostgreSQL | SQLite (section 5.3), for development, embedded, and single-replica use. |
| Code from different customers on shared infrastructure | No. `microvm` isolation is not required; `container` remains the default (section 11.3). |
| External nodes in `beforeWrite` flows | No. `beforeWrite` flows stay limited to Pure, DataRead, DataWrite, and Control nodes (section 10.5). |
| WebAssembly code-node runtime | No. Python is the only code-node runtime in version 1. |
| Licensing and distribution | No licence for now. The code is not published externally. |
| Apps per instance | One app per instance, one instance per client. Multi-app support is removed (section 14.6); larger apps use manifest modules (section 12.1). |
| Coordination between replicas | No leader election in framework code; all roles are identical across replicas (section 14.10). |
| Background jobs and scheduling | River (section 2.5) replaces the outbox, the relay, the scheduler, and custom retry and dead-letter code. |
| Change log and shared state | Valkey by default; a PostgreSQL-only lightweight profile; NATS as an optional adapter (section 9.3). |
