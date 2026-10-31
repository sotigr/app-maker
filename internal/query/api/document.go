package api

import (
	"context"

	schemaapi "app-maker/internal/schema/api"
)

// Document is a parsed query document (spec 7.1). Values are still raw
// JSON; the planner types them against the schema.
type Document struct {
	Type   string    `json:"type"`
	Scope  DocScope  `json:"scope"`
	Where  *Node     `json:"where,omitempty"`
	Sort   []DocSort `json:"sort,omitempty"`
	Select []string  `json:"select,omitempty"`
	Expand []string  `json:"expand,omitempty"`
	Limit  int       `json:"limit,omitempty"`
	Cursor string    `json:"cursor,omitempty"`
}

// DocScope is the optional tree scope.
type DocScope struct {
	ChildrenOf    string `json:"childrenOf,omitempty"`
	DescendantsOf string `json:"descendantsOf,omitempty"`
}

// DocSort is one sort key.
type DocSort struct {
	Field string `json:"field"`
	Dir   string `json:"dir,omitempty"` // asc (default) or desc
}

// Node is a where-clause node: either a composite (and/or/not) or a leaf
// comparison.
type Node struct {
	And   []*Node `json:"and,omitempty"`
	Or    []*Node `json:"or,omitempty"`
	Not   *Node   `json:"not,omitempty"`
	Field string  `json:"field,omitempty"`
	Op    Op      `json:"op,omitempty"`
	Value any     `json:"value,omitempty"`
}

// RepoCaps are the repository capabilities that affect planning.
type RepoCaps struct {
	RangeFilters     bool
	Sort             bool
	SubtreeQueries   bool
	SecondaryIndexes bool
}

// PlanRequest is everything the planner needs (spec 7.3).
type PlanRequest struct {
	Doc   *Document
	Types []*schemaapi.CompiledType
	// Audience selects the query view the fields are checked against.
	Audience schemaapi.Audience
	// Scope is the caller's permission predicate.
	Scope      Predicate
	ScopeExact bool
	Caps       RepoCaps
	// Count returns the number of live features of the types, for the
	// cost estimate; nil means unknown.
	Count func(ctx context.Context) (int, error)
}

// Planner turns query documents into plans and signs cursors.
type Planner interface {
	Parse(raw []byte) (*Document, error)
	Plan(ctx context.Context, req PlanRequest) (Plan, error)
	// EncodeCursor signs a cursor bound to the query (or list) identity.
	EncodeCursor(c *Cursor, binding string) string
	// DecodeCursor verifies and decodes a cursor for the same binding.
	DecodeCursor(s, binding string) (*Cursor, error)
	// Binding identifies a query for cursor binding.
	Binding(d *Document) string
}

// ScanLimit is the size below which unindexed queries are allowed (spec 7.3).
const ScanLimit = 10_000

// Query limits (spec 7.2, 14.1).
const (
	MaxPredicates = 20
	MaxDepth      = 4
	MaxInValues   = 100
	MaxExpand     = 100
)
