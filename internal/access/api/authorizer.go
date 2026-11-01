package api

import (
	"context"
	"strings"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// Operation is an operation checked by the Authorizer (spec 8.5).
type Operation string

// Operations. Actions are "action:<name>".
const (
	OpRead   Operation = "read"
	OpCreate Operation = "create"
	OpUpdate Operation = "update"
	OpDelete Operation = "delete"
	OpMove   Operation = "move"
)

// ActionOp returns the operation of a named action.
func ActionOp(name string) Operation { return Operation("action:" + name) }

// Action returns the action name of an action operation, or "".
func (o Operation) Action() string {
	name, _ := strings.CutPrefix(string(o), "action:")
	if name == string(o) {
		return ""
	}
	return name
}

// FeatureRef is what the Authorizer needs to know about a feature.
type FeatureRef struct {
	ID        kernel.ID
	Type      *schemaapi.CompiledType
	ParentID  *kernel.ID
	OwnerID   *kernel.ID
	Ancestors []kernel.ID // root first, excluding the feature itself
	// Data is the storage document in expression form (kernel.Native
	// values) including "id", "parentId", "version", and the hidden key
	// AncestorsKey (not addressable from expressions) used by hasRole.
	Data map[string]any
}

// AncestorsKey holds a feature's ancestor IDs ([]string, root first) in
// FeatureRef.Data. The "$" prefix makes it unreachable from expressions.
const AncestorsKey = "$ancestors"

// Lineage returns the feature's ID followed by its ancestors, nearest first.
func (f *FeatureRef) Lineage() []kernel.ID {
	out := make([]kernel.ID, 0, len(f.Ancestors)+1)
	if !f.ID.IsZero() {
		out = append(out, f.ID)
	}
	for i := len(f.Ancestors) - 1; i >= 0; i-- {
		out = append(out, f.Ancestors[i])
	}
	return out
}

// CheckRequest is one authorization question.
type CheckRequest struct {
	Op Operation
	// Type is the type of the feature acted on (for create, the new
	// feature's type).
	Type    *schemaapi.CompiledType
	Feature *FeatureRef    // the feature (before the change); nil for create
	Parent  *FeatureRef    // the parent for create; the new parent for move
	Patch   map[string]any // the merge patch for update, expression form
	Input   map[string]any // action input
}

// Decision is the Authorizer's answer.
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer decides operations, audiences, and query scopes (spec 8.5).
// Content and Query depend on it; Access implements it.
type Authorizer interface {
	// Check decides one operation on one feature (or on a parent, for create).
	Check(ctx context.Context, p Principal, req CheckRequest) (Decision, error)
	// Audience returns the visibility tier the principal gets for this feature.
	Audience(ctx context.Context, p Principal, f *FeatureRef) (schemaapi.Audience, error)
	// Scope returns a predicate restricting a query on type t to features p
	// may read. Exact reports whether the predicate is the whole read
	// policy; when false, results must also be post-filtered with Check.
	Scope(ctx context.Context, p Principal, t *schemaapi.CompiledType) (pred queryapi.Predicate, exact bool, err error)
	// Bindings returns the implementations of the access built-ins
	// (isOwner, hasRole, inGroup) bound to p, for expressions evaluated
	// outside policies: state-machine guards and flows (spec 10.8).
	Bindings(ctx context.Context, p Principal) map[string]any
}
