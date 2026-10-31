package api

import (
	"context"

	accessapi "app-maker/internal/access/api"
	flowapi "app-maker/internal/flow/api"
	"app-maker/internal/kernel"
)

// WriteOp is one staged write of a flow (spec 10.6), committed through the
// normal write pipeline. IDs and parents may be "$" references to
// features created earlier in the same commit.
type WriteOp struct {
	Op      string         `json:"op"` // create, update, delete, move
	Type    string         `json:"type"`
	Ref     string         `json:"ref,omitempty"`
	ID      string         `json:"id,omitempty"`
	Parent  string         `json:"parent,omitempty"`
	Version kernel.Version `json:"version,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// CommitRequest commits a flow's write set and staged jobs as one batch,
// subject to all validation, write rules, and policies, evaluated as the
// run's principal (spec 10.6 step 3).
type CommitRequest struct {
	Principal accessapi.Principal
	Action    string // unlocks x-write: action:<name> fields
	Causation flowapi.CausationChain
	Ops       []WriteOp
	Jobs      []kernel.JobRequest
}

// CommitResult maps references to IDs and reports new versions.
type CommitResult struct {
	Refs     map[string]string
	Versions map[string]kernel.Version
	Features []FeatureView
}

// FlowFeature is a feature as a flow sees it: the fields readable by the
// run's principal, in expression form (time.Time, int64, Decimal, ...),
// plus "id", "parentId", "version", and "type".
type FlowFeature map[string]any

// FlowAccess is what the Flow context may do with features. Every read and
// write goes through Content's application services as the run's
// principal; flows never touch repositories (spec 2.3).
type FlowAccess interface {
	// Read returns the feature projected for p, or not found.
	Read(ctx context.Context, p accessapi.Principal, id kernel.ID) (FlowFeature, error)
	// Query runs a query document for p.
	Query(ctx context.Context, p accessapi.Principal, doc []byte) ([]FlowFeature, error)
	// Children lists readable children.
	Children(ctx context.Context, p accessapi.Principal, parent kernel.ID, typeName string, limit int) ([]FlowFeature, error)
	// Storage returns the full storage view of a feature (the `feature`
	// variable of a run, spec 10.4); no authorization.
	Storage(ctx context.Context, id kernel.ID) (FlowFeature, error)
	// Commit runs staged writes and jobs through the write pipeline.
	Commit(ctx context.Context, req CommitRequest) (CommitResult, error)
	// AuthorizeAction checks the action policy for p on a feature it can
	// read (spec 8.5 actions) and returns the feature's storage view.
	AuthorizeAction(ctx context.Context, p accessapi.Principal, id kernel.ID, action string, input map[string]any) (FlowFeature, error)
}
