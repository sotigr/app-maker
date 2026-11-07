// Package domain implements the Query context's domain services: the
// document parser, the planner with cost estimation, and signed cursors
// (spec 7, 6.5).
package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// Planner implements queryapi.Planner.
type Planner struct {
	cursors *CursorCodec
}

// NewPlanner returns a planner signing cursors with key (an instance
// secret shared by all replicas, spec 6.5).
func NewPlanner(key []byte) *Planner { return &Planner{cursors: NewCursorCodec(key)} }

var _ queryapi.Planner = (*Planner)(nil)

func invalid(format string, args ...any) error {
	return kernel.NewError(kernel.CodeInvalidQuery, format, args...)
}

// Parse decodes and checks the shape of a query document (spec 7.3 step 1).
func (p *Planner) Parse(raw []byte) (*queryapi.Document, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var d queryapi.Document
	if err := dec.Decode(&d); err != nil {
		return nil, invalid("malformed query: %v", err)
	}
	if d.Type == "" {
		return nil, invalid("type is required")
	}
	if d.Scope.ChildrenOf != "" && d.Scope.DescendantsOf != "" {
		return nil, invalid("scope must be one of childrenOf or descendantsOf")
	}
	leaves := 0
	if err := checkNode(d.Where, 1, &leaves); err != nil {
		return nil, err
	}
	for _, s := range d.Sort {
		if s.Dir != "" && s.Dir != "asc" && s.Dir != "desc" {
			return nil, invalid("sort dir must be asc or desc")
		}
	}
	if d.Limit < 0 || d.Limit > queryapi.MaxLimit {
		return nil, invalid("limit must be between 1 and %d", queryapi.MaxLimit)
	}
	return &d, nil
}

var knownOps = map[queryapi.Op]bool{
	queryapi.OpEq: true, queryapi.OpNe: true, queryapi.OpLt: true, queryapi.OpLte: true, queryapi.OpGt: true,
	queryapi.OpGte: true, queryapi.OpIn: true, queryapi.OpNin: true, queryapi.OpPrefix: true,
	queryapi.OpIContains: true, queryapi.OpIPrefix: true, queryapi.OpContains: true, queryapi.OpExists: true,
}

func checkNode(n *queryapi.Node, depth int, leaves *int) error {
	if n == nil {
		return nil
	}
	if depth > queryapi.MaxDepth {
		return invalid("where nesting exceeds depth %d", queryapi.MaxDepth)
	}
	kinds := 0
	if n.And != nil {
		kinds++
	}
	if n.Or != nil {
		kinds++
	}
	if n.Not != nil {
		kinds++
	}
	if n.Field != "" || n.Op != "" {
		kinds++
	}
	if kinds != 1 {
		return invalid("each where node is exactly one of and, or, not, or a comparison")
	}
	for _, c := range append(append([]*queryapi.Node{}, n.And...), n.Or...) {
		if err := checkNode(c, depth+1, leaves); err != nil {
			return err
		}
	}
	if n.Not != nil {
		return checkNode(n.Not, depth+1, leaves)
	}
	if n.Field != "" || n.Op != "" {
		*leaves++
		if *leaves > queryapi.MaxPredicates {
			return invalid("at most %d predicates", queryapi.MaxPredicates)
		}
		if !knownOps[n.Op] {
			return invalid("unknown operator %q", n.Op)
		}
		if !strings.HasPrefix(n.Field, "/") {
			return invalid("field must be a JSON Pointer like /status")
		}
		if n.Op == queryapi.OpIn || n.Op == queryapi.OpNin {
			l, ok := n.Value.([]any)
			if !ok {
				return invalid("%s needs an array value", n.Op)
			}
			if len(l) > queryapi.MaxInValues {
				return invalid("%s accepts at most %d values", n.Op, queryapi.MaxInValues)
			}
		}
	}
	return nil
}

// Plan validates the document against the caller's query view, merges the
// permission scope, checks capabilities, and estimates cost (spec 7.3).
func (p *Planner) Plan(ctx context.Context, req queryapi.PlanRequest) (queryapi.Plan, error) {
	d := req.Doc
	if len(req.Types) == 0 {
		return queryapi.Plan{}, invalid("unknown type %q", d.Type)
	}
	plan := queryapi.Plan{Limit: d.Limit}
	if plan.Limit == 0 {
		plan.Limit = queryapi.DefaultLimit
	}
	for _, ct := range req.Types {
		plan.Types = append(plan.Types, ct.ID)
	}
	field := func(path string, query bool) (*schemaapi.FieldDef, error) {
		top := "/" + strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
		var def *schemaapi.FieldDef
		for _, ct := range req.Types {
			v := ct.View(req.Audience, schemaapi.DirQuery)
			ok := v.Queryable[top]
			if !query {
				ok = v.Readable[top]
			}
			f, found := ct.Field(path)
			if !ok || !found {
				// Unreadable fields are reported as unknown, not forbidden.
				return nil, invalid("unknown field %s", path)
			}
			if def != nil && def.Kind != f.Kind {
				return nil, invalid("field %s has different kinds across types", path)
			}
			def = f
		}
		return def, nil
	}
	var where queryapi.Predicate = queryapi.True{}
	indexed := false
	if d.Where != nil {
		w, ix, err := p.predicate(d.Where, field, req.Caps)
		if err != nil {
			return queryapi.Plan{}, err
		}
		where, indexed = w, ix
	}
	switch {
	case d.Scope.ChildrenOf != "":
		id, err := kernel.ParseID(d.Scope.ChildrenOf)
		if err != nil {
			return queryapi.Plan{}, invalid("invalid scope id")
		}
		plan.Scope = queryapi.Scope{Kind: queryapi.ScopeChildrenOf, Of: id}
		indexed = true
	case d.Scope.DescendantsOf != "":
		id, err := kernel.ParseID(d.Scope.DescendantsOf)
		if err != nil {
			return queryapi.Plan{}, invalid("invalid scope id")
		}
		if !req.Caps.SubtreeQueries {
			return queryapi.Plan{}, &kernel.UnsupportedError{Capability: "SubtreeQueries"}
		}
		plan.Scope = queryapi.Scope{Kind: queryapi.ScopeDescendants, Of: id}
		indexed = true
	}
	for _, s := range d.Sort {
		if !req.Caps.Sort {
			return queryapi.Plan{}, &kernel.UnsupportedError{Capability: "Sort"}
		}
		f, err := field(s.Field, true)
		if err != nil {
			return queryapi.Plan{}, err
		}
		if !f.Kind.IsScalar() {
			return queryapi.Plan{}, invalid("cannot sort by %s", s.Field)
		}
		plan.Sort = append(plan.Sort, queryapi.SortKey{Path: s.Field, Desc: s.Dir == "desc"})
	}
	// The ID breaks ties in the direction of the first key, so an index on
	// (value, id) serves the whole order.
	idDesc := len(plan.Sort) > 0 && plan.Sort[0].Desc
	plan.Sort = append(plan.Sort, queryapi.SortKey{Path: "", Desc: idDesc})
	for _, sel := range d.Select {
		if _, err := field(sel, false); err != nil {
			return queryapi.Plan{}, err
		}
		plan.Select = append(plan.Select, sel)
	}
	// Cost (spec 7.3 step 6).
	if !indexed && req.Count != nil {
		n, err := req.Count(ctx)
		if err != nil {
			return queryapi.Plan{}, err
		}
		if n >= queryapi.ScanLimit {
			return queryapi.Plan{}, kernel.NewError(kernel.CodeQueryTooExpensive, "add an indexed predicate or a scope")
		}
	}
	// Permission scope is a mandatory predicate (spec 7.3 step 4).
	plan.Where = and(where, req.Scope)
	if d.Cursor != "" {
		c, err := p.cursors.Decode(d.Cursor, p.Binding(d))
		if err != nil {
			return queryapi.Plan{}, err
		}
		plan.After = c
	}
	return plan, nil
}

func and(a, b queryapi.Predicate) queryapi.Predicate {
	switch {
	case b == nil:
		return a
	case isTrue(a):
		return b
	case isTrue(b):
		return a
	}
	return queryapi.And{Terms: []queryapi.Predicate{a, b}}
}

func isTrue(p queryapi.Predicate) bool {
	_, ok := p.(queryapi.True)
	return ok
}

type fieldFn func(path string, query bool) (*schemaapi.FieldDef, error)

// predicate types a where node; indexed reports whether the node is
// index-backed (every queryable field is x-indexed).
func (p *Planner) predicate(n *queryapi.Node, field fieldFn, caps queryapi.RepoCaps) (queryapi.Predicate, bool, error) {
	switch {
	case n.And != nil:
		var terms []queryapi.Predicate
		indexed := false
		for _, c := range n.And {
			t, ix, err := p.predicate(c, field, caps)
			if err != nil {
				return nil, false, err
			}
			terms = append(terms, t)
			indexed = indexed || ix
		}
		return queryapi.And{Terms: terms}, indexed, nil
	case n.Or != nil:
		var terms []queryapi.Predicate
		indexed := len(n.Or) > 0
		for _, c := range n.Or {
			t, ix, err := p.predicate(c, field, caps)
			if err != nil {
				return nil, false, err
			}
			terms = append(terms, t)
			indexed = indexed && ix
		}
		return queryapi.Or{Terms: terms}, indexed, nil
	case n.Not != nil:
		t, _, err := p.predicate(n.Not, field, caps)
		return queryapi.Not{Term: t}, false, err
	}
	f, err := field(n.Field, true)
	if err != nil {
		return nil, false, err
	}
	if n.Op.IsRange() && !caps.RangeFilters {
		return nil, false, &kernel.UnsupportedError{Capability: "RangeFilters"}
	}
	c := queryapi.Compare{Path: n.Field, Op: n.Op}
	vt := f.Type
	switch n.Op {
	case queryapi.OpExists:
		return c, f.Indexed, nil
	case queryapi.OpLt, queryapi.OpLte, queryapi.OpGt, queryapi.OpGte:
		if !f.Kind.IsOrdered() {
			return nil, false, invalid("%s does not apply to %s field %s", n.Op, f.Kind, n.Field)
		}
	case queryapi.OpPrefix, queryapi.OpIContains, queryapi.OpIPrefix:
		if f.Kind != kernel.KindString {
			return nil, false, invalid("%s applies to string fields", n.Op)
		}
	case queryapi.OpContains:
		if f.Kind != kernel.KindList || !f.Type.Elem.Kind.IsScalar() {
			return nil, false, invalid("contains applies to lists of scalars")
		}
		vt = f.Type.Elem
	case queryapi.OpEq, queryapi.OpNe, queryapi.OpIn, queryapi.OpNin:
		if !f.Kind.IsScalar() {
			return nil, false, invalid("%s does not apply to %s field %s", n.Op, f.Kind, n.Field)
		}
	}
	if n.Op == queryapi.OpIn || n.Op == queryapi.OpNin {
		list := kernel.List{Elem: f.Kind}
		for _, raw := range n.Value.([]any) {
			v, err := coerce(vt, n.Field, raw, false)
			if err != nil {
				return nil, false, err
			}
			list.Items = append(list.Items, v)
		}
		c.Value = list
		return c, f.Indexed, nil
	}
	v, err := coerce(vt, n.Field, n.Value, n.Op == queryapi.OpEq || n.Op == queryapi.OpNe)
	if err != nil {
		return nil, false, err
	}
	c.Value = v
	return c, f.Indexed && n.Op != queryapi.OpNe, nil
}

func coerce(vt *schemaapi.ValueType, path string, raw any, allowNull bool) (kernel.Value, error) {
	if raw == nil {
		if allowNull {
			return kernel.Null{}, nil
		}
		return nil, invalid("null is not allowed here (%s)", path)
	}
	t := *vt
	t.Nullable = false
	if t.Kind == kernel.KindEnum {
		t.Kind = kernel.KindString // filtering by an unknown enum value is valid
	}
	v, err := t.Coerce(path, raw)
	if err != nil {
		return nil, invalid("value for %s: %v", path, err)
	}
	return v, nil
}

// Binding identifies a query for cursor binding: the same cursor cannot
// be replayed against a different query.
func (p *Planner) Binding(d *queryapi.Document) string {
	b, _ := json.Marshal(struct {
		T string
		S queryapi.DocScope
		W *queryapi.Node
		O []queryapi.DocSort
	}{d.Type, d.Scope, d.Where, d.Sort})
	return string(b)
}

// EncodeCursor implements queryapi.Planner.
func (p *Planner) EncodeCursor(c *queryapi.Cursor, binding string) string {
	if c == nil {
		return ""
	}
	return p.cursors.Encode(c, binding)
}

// DecodeCursor implements queryapi.Planner.
func (p *Planner) DecodeCursor(s, binding string) (*queryapi.Cursor, error) {
	if s == "" {
		return nil, nil
	}
	return p.cursors.Decode(s, binding)
}
