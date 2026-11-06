package api

import (
	"encoding/json"
	"slices"
	"time"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

// FieldDef is one row of a compiled type's field table (spec 4.5).
// Paths are JSON Pointers; nested object properties have their own rows.
type FieldDef struct {
	Path        string
	Kind        kernel.Kind
	Type        *ValueType
	Nullable    bool
	Required    bool
	Default     any // wire JSON default, or nil
	Indexed     bool
	Searchable  bool
	Immutable   bool
	ReadOnly    bool
	WriteOnly   bool
	Visibility  Visibility
	Write       WriteRule
	RefTarget   TypeID
	OnDelete    OnDelete
	Stream      *StreamSpec // nil if not subscribable
	Persist     bool
	Computed    string // x-computed source, or ""
	Title       string
	Description string
	Blob        *BlobSpec
	// Top is true for top-level properties (path "/name").
	Top bool
}

// BlobSpec holds Blob field limits (spec 14.4).
type BlobSpec struct {
	MaxBytes     int64
	ContentTypes []string
	Thumbnails   []int
}

// ComputedField is a compiled x-computed expression.
type ComputedField struct {
	Path    string
	Program rt.Program
	Type    *ValueType
}

// ShareDef is a compiled x-shareable (ADR 0020).
type ShareDef struct {
	// Actions link holders may run on the shared feature.
	Actions []string
	// MaxDays bounds a link's lifetime (default 30, at most 365).
	MaxDays int
}

// AggregateDef is a compiled x-aggregate (spec 4.2).
type AggregateDef struct {
	Path  string
	Op    string // count, sum, min, max, avg
	Of    TypeID
	Field string // path on the child for sum/min/max/avg
	Where string
	// WhereProgram is evaluated with env {feature: child storage view}.
	WhereProgram rt.Program
}

// StateMachine is a compiled x-stateMachine (spec 4.10).
type StateMachine struct {
	Path        string
	Initial     string
	Transitions []Transition
}

// Transition is one allowed state change.
type Transition struct {
	From, To, Action string
	Guard            string
	// GuardProgram is evaluated with env {principal, feature, input}.
	GuardProgram rt.Program
}

// Find returns the transition for action from state from.
func (sm *StateMachine) Find(from, action string) (*Transition, bool) {
	for i := range sm.Transitions {
		t := &sm.Transitions[i]
		if t.From == from && t.Action == action {
			return t, true
		}
	}
	return nil, false
}

// Allowed reports whether from -> to is a declared transition.
func (sm *StateMachine) Allowed(from, to string) (*Transition, bool) {
	for i := range sm.Transitions {
		t := &sm.Transitions[i]
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return nil, false
}

// Validator validates wire JSON documents against a view's JSON Schema.
type Validator interface {
	Validate(doc any) []kernel.FieldError
}

// View is a projection of a compiled type for one audience and direction
// (spec 4.6). Paths in the sets are top-level JSON Pointers.
type View struct {
	Key        ViewKey
	Readable   map[string]bool
	Writable   map[string]bool
	Queryable  map[string]bool
	JSONSchema []byte
	Validator  Validator
}

// CompiledType is the internal representation of a FeatureType (spec 4.5).
// It is immutable after compilation and safe for concurrent use.
type CompiledType struct {
	ID            TypeID
	Name          string
	Version       int
	Title         string
	Traits        []TypeID
	Fields        []FieldDef // ordered by path
	Children      []TypeID   // x-allowedChildren as declared (types or traits, base IDs)
	UniqueName    string     // path, or empty
	Repository    string
	Retention     time.Duration
	Views         map[ViewKey]*View
	Storage       *View // all persisted fields, with required
	Computed      []ComputedField
	Aggregates    []AggregateDef
	StateMachines map[string]*StateMachine
	// Sequences are the x-sequence fields (ADR 0021).
	Sequences []SequenceDef
	// Shareable is x-shareable: the type offers the built-in share action
	// (ADR 0020); nil otherwise.
	Shareable *ShareDef
	// Root is the value type of the whole document.
	Root *ValueType
	// Source is the trait-merged, $ref-resolved schema document.
	Source json.RawMessage
}

// Base returns the unversioned type ID.
func (c *CompiledType) Base() TypeID { return c.ID.Base() }

// Field returns the field definition at path.
func (c *CompiledType) Field(path string) (*FieldDef, bool) {
	i, ok := slices.BinarySearchFunc(c.Fields, path, func(f FieldDef, p string) int {
		switch {
		case f.Path < p:
			return -1
		case f.Path > p:
			return 1
		}
		return 0
	})
	if !ok {
		return nil, false
	}
	return &c.Fields[i], true
}

// TopFields returns the top-level field definitions.
func (c *CompiledType) TopFields() []*FieldDef {
	var out []*FieldDef
	for i := range c.Fields {
		if c.Fields[i].Top {
			out = append(out, &c.Fields[i])
		}
	}
	return out
}

// View returns the view for audience and direction.
func (c *CompiledType) View(a Audience, d Direction) *View {
	return c.Views[ViewKey{Audience: a, Direction: d}]
}

// HasTrait reports whether the type includes trait.
func (c *CompiledType) HasTrait(trait TypeID) bool {
	return slices.Contains(c.Traits, trait.Base())
}

// Is reports whether the type is id (any version) or includes trait id.
func (c *CompiledType) Is(id TypeID) bool {
	if id.IsTrait() {
		return c.HasTrait(id)
	}
	return c.Base() == id.Base()
}

// AllowsChild reports whether a feature of type child may be a child of
// this type: the child's base ID or one of its traits is listed.
func (c *CompiledType) AllowsChild(child *CompiledType) bool {
	for _, a := range c.Children {
		if child.Is(a) {
			return true
		}
	}
	return false
}

// ExprType returns the expression type of the storage document, used as
// the type of `feature` in expressions. Includes the "id" and "parentId"
// pseudo-fields.
func (c *CompiledType) ExprType() *rt.Type {
	t := c.Root.ExprType()
	fields := make(map[string]*rt.Type, len(t.Fields)+2)
	for k, v := range t.Fields {
		fields[k] = v
	}
	fields["id"] = rt.String
	fields["parentId"] = rt.String
	fields["version"] = rt.Int
	return rt.MapOf(fields)
}

// TypeRegistry gives read access to compiled types (spec 2.3).
type TypeRegistry interface {
	// Get returns the current version for a base ID, or the exact version
	// for a versioned ID.
	Get(id TypeID) (*CompiledType, bool)
	// ByName resolves a short name ("task", or "crm.contact" for modules)
	// to the current version.
	ByName(name string) (*CompiledType, bool)
	// Resolve returns the current types matching a type or trait ID.
	Resolve(id TypeID) []*CompiledType
	// All returns the current version of every type.
	All() []*CompiledType
}
