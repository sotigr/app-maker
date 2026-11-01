package api

import (
	"context"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// StoredFeature is a feature as the system sees it: the full storage
// document in expression form. It never leaves the server; it is used by
// Access (principals, grants, memberships) and Flow internals.
type StoredFeature struct {
	ID        kernel.ID
	Type      schemaapi.TypeID
	ParentID  *kernel.ID
	OwnerID   *kernel.ID
	Ancestors []kernel.ID
	Version   kernel.Version
	Data      map[string]any
}

// SystemReader reads features without authorization. Only trusted
// framework code (the Access context, flow internals) may use it.
type SystemReader interface {
	Get(ctx context.Context, id kernel.ID) (StoredFeature, error)
	// Find returns up to limit live features of type t matching where.
	Find(ctx context.Context, t schemaapi.TypeID, where queryapi.Predicate, limit int) ([]StoredFeature, error)
}

// SystemWriter creates and updates features as the system principal
// through the full write pipeline (validation, events, jobs).
type SystemWriter interface {
	// Create creates a feature of the named type under parent (nil = root)
	// and returns its ID. owner applies to Ownable types.
	Create(ctx context.Context, typeName string, parent *kernel.ID, data map[string]any, owner *kernel.ID) (kernel.ID, error)
	// Update merge-patches a feature at the given version.
	Update(ctx context.Context, typeName string, id kernel.ID, version kernel.Version, data map[string]any) error
}
