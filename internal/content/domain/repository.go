package domain

import (
	"context"

	flowapi "app-maker/internal/flow/api"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	schemaapi "app-maker/internal/schema/api"
)

// MutationKind is the kind of a mutation.
type MutationKind uint8

// Mutation kinds (spec 5.1).
const (
	MutCreate MutationKind = iota + 1
	MutUpdate
	MutMove
	MutDelete
	MutPurge
)

func (k MutationKind) String() string {
	return [...]string{"", "create", "update", "move", "delete", "purge"}[k]
}

// Mutation is one write in a batch. Feature is the state after the change;
// Feature.Version is the new version. Expected is the version the caller
// last saw (zero for create).
type Mutation struct {
	Kind     MutationKind
	Feature  Snapshot
	Expected kernel.Version
	// UniqueKey, when set, must be unique among the live children of the
	// feature's parent (x-uniqueName, spec 3.4).
	UniqueKey string
}

// Batch is committed atomically by one repository (spec 5.1, 5.2).
type Batch struct {
	Mutations []Mutation
	Events    []DomainEvent
	Jobs      []kernel.JobRequest
	Causation flowapi.CausationChain
}

// CommitResult reports each affected feature's new version.
type CommitResult struct {
	Versions map[kernel.ID]kernel.Version
}

// Capabilities declares what a repository can do (spec 5.1).
type Capabilities struct {
	Transactions      bool // multi-feature atomic Apply
	TransactionalJobs bool // jobs inserted in the commit transaction
	SecondaryIndexes  bool
	Sort              bool
	RangeFilters      bool
	FullText          bool
	SubtreeQueries    bool
	MaxBatchSize      int
}

// PageRequest asks for one page of children.
type PageRequest struct {
	Limit int
	After *queryapi.Cursor
	Types []schemaapi.TypeID // optional filter, versioned IDs
}

// Page is one page of results; Next is nil on the last page.
type Page[T any] struct {
	Items []T
	Next  *queryapi.Cursor
}

// FeatureRepository stores features (spec 5.1). Implementations live in
// infrastructure/<backend> and must pass the repotest conformance suite.
type FeatureRepository interface {
	// Get returns a live feature or kernel.ErrNotFound.
	Get(ctx context.Context, id kernel.ID) (*Feature, error)
	// GetMany returns the live features among ids, in no particular order.
	GetMany(ctx context.Context, ids []kernel.ID) ([]*Feature, error)
	// Children lists live children ordered by ID.
	Children(ctx context.Context, parent kernel.ID, page PageRequest) (Page[*Feature], error)
	// Descendants returns up to limit live descendants of id, in no
	// particular order. It fails with ErrConstraint when there are more.
	Descendants(ctx context.Context, id kernel.ID, limit int) ([]*Feature, error)
	// Query runs a plan.
	Query(ctx context.Context, q queryapi.Plan) (Page[*Feature], error)
	// Apply commits all mutations atomically, with a version check on each
	// update, move, delete, and purge, and inserts the batch's jobs in the
	// same transaction.
	Apply(ctx context.Context, batch Batch) (CommitResult, error)
	Capabilities() Capabilities
}

// MaxMoveSize bounds subtree moves and cascading deletes (spec 5.3).
const MaxMoveSize = 10_000
