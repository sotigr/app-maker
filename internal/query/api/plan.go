// Package api is the Query context's published language: the query plan
// and predicates handed to repositories (spec 7.3).
package api

import (
	"app-maker/internal/kernel"
	schemaapi "app-maker/internal/schema/api"
)

// Op is a comparison operator (spec 7.2).
type Op string

// Operators.
const (
	OpEq       Op = "eq"
	OpNe       Op = "ne"
	OpLt       Op = "lt"
	OpLte      Op = "lte"
	OpGt       Op = "gt"
	OpGte      Op = "gte"
	OpIn       Op = "in"
	OpNin      Op = "nin"
	OpPrefix   Op = "prefix"
	OpContains Op = "contains"
	OpExists   Op = "exists"
)

// IsRange reports whether op needs the RangeFilters capability.
func (o Op) IsRange() bool { return o == OpLt || o == OpLte || o == OpGt || o == OpGte }

// Predicate is a node of the where-clause AST.
type Predicate interface{ isPredicate() }

// Compare compares the field at Path with Value. For in/nin, Value is a
// kernel.List. For exists, Value is ignored.
type Compare struct {
	Path  string
	Op    Op
	Value kernel.Value
}

// And is a conjunction.
type And struct{ Terms []Predicate }

// Or is a disjunction.
type Or struct{ Terms []Predicate }

// Not negates a predicate.
type Not struct{ Term Predicate }

// InScope is the permission predicate (spec 7.3, 8.5): the feature's ID,
// owner, or any ancestor is in IDs, depending on Field.
type InScope struct {
	Field ScopeField
	IDs   []kernel.ID
}

// ScopeField selects what InScope matches.
type ScopeField string

// Scope fields.
const (
	ScopeOwner    ScopeField = "owner"    // owner_id in IDs
	ScopeAncestor ScopeField = "ancestor" // the feature itself or an ancestor in IDs
	ScopeSelf     ScopeField = "self"     // the feature's own ID in IDs
)

// True matches everything; False matches nothing.
type (
	True  struct{}
	False struct{}
)

func (Compare) isPredicate() {}
func (And) isPredicate()     {}
func (Or) isPredicate()      {}
func (Not) isPredicate()     {}
func (InScope) isPredicate() {}
func (True) isPredicate()    {}
func (False) isPredicate()   {}

// ScopeKind restricts a query to part of the tree.
type ScopeKind string

// Scope kinds (spec 7.1).
const (
	ScopeAll         ScopeKind = ""
	ScopeChildrenOf  ScopeKind = "childrenOf"
	ScopeDescendants ScopeKind = "descendantsOf"
)

// Scope is the tree scope of a query.
type Scope struct {
	Kind ScopeKind
	Of   kernel.ID
}

// SortKey orders results. Path "" with Dir asc means by ID.
type SortKey struct {
	Path string
	Desc bool
}

// Cursor is the decoded position after the last returned item: its sort
// key values and ID (spec 6.5). Signing happens in the Query context.
type Cursor struct {
	Values []kernel.Value
	ID     kernel.ID
}

// Plan is a validated query ready for a repository (spec 7.3).
type Plan struct {
	Types  []schemaapi.TypeID // concrete versioned type IDs
	Scope  Scope
	Where  Predicate // includes the permission predicate; nil = all
	Sort   []SortKey // always ends with ID for stable cursors
	Select []string
	Limit  int
	After  *Cursor
	// IncludeDeleted is used by maintenance jobs only.
	IncludeDeleted bool
}

// Default and maximum page sizes (spec 6.5).
const (
	DefaultLimit = 50
	MaxLimit     = 500
)
