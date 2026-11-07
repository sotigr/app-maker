// Package application holds the Access use cases: the Authorizer (spec
// 8.5), authentication (8.3), bootstrap (8.4), and principal resolution.
package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	accessapi "organon/internal/access/api"
	"organon/internal/access/domain"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// Authorizer implements accessapi.Authorizer with compiled policies and the
// relationship index. Every operation not allowed by a policy is denied.
type Authorizer struct {
	policies *domain.PolicySet
	roles    *domain.Roles
	index    domain.RelationshipIndex
	exprs    rt.Compiler
	clock    kernel.Clock

	mu         sync.Mutex
	principals map[string]rt.Program // compiled principal-only scope conditions

	parents RefLoader // loads a feature's parent for rules that read parent
}

// RefLoader reads a feature for the Authorizer without authorization.
type RefLoader func(ctx context.Context, id kernel.ID) (*accessapi.FeatureRef, error)

// SetParentLoader lets update, delete, read, and action rules read
// `parent` (gap #38): the parent is loaded when a deciding rule uses it.
func (a *Authorizer) SetParentLoader(l RefLoader) { a.parents = l }

var _ accessapi.Authorizer = (*Authorizer)(nil)

// NewAuthorizer returns an Authorizer.
func NewAuthorizer(policies *domain.PolicySet, roles *domain.Roles, index domain.RelationshipIndex, exprs rt.Compiler, clock kernel.Clock) *Authorizer {
	return &Authorizer{policies: policies, roles: roles, index: index, exprs: exprs, clock: clock, principals: map[string]rt.Program{}}
}

type cacheKey struct{}

type requestCache struct {
	mu      sync.Mutex
	grants  map[string][]domain.Grant
	parents map[kernel.ID]*accessapi.FeatureRef
}

// WithRequestCache returns a context that memoizes relationship lookups for
// one request (spec 8.7: decisions are cached per request).
func WithRequestCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, cacheKey{}, &requestCache{grants: map[string][]domain.Grant{}, parents: map[kernel.ID]*accessapi.FeatureRef{}})
}

// subjects returns the principal and its groups.
func subjects(p accessapi.Principal) []kernel.ID {
	return append([]kernel.ID{p.ID}, p.Groups...)
}

func (a *Authorizer) grants(ctx context.Context, p accessapi.Principal) ([]domain.Grant, error) {
	if p.ID.IsZero() {
		return nil, nil
	}
	c, _ := ctx.Value(cacheKey{}).(*requestCache)
	key := fmt.Sprint(subjects(p))
	if c != nil {
		c.mu.Lock()
		g, ok := c.grants[key]
		c.mu.Unlock()
		if ok {
			return g, nil
		}
	}
	g, err := a.index.Grants(ctx, subjects(p))
	if err != nil {
		return nil, err
	}
	if c != nil {
		c.mu.Lock()
		c.grants[key] = g
		c.mu.Unlock()
	}
	return g, nil
}

// isOwner reports whether p (or one of its groups) owns the feature data,
// or the feature is p's own principal feature.
func isOwner(p accessapi.Principal, f any) bool {
	m, _ := f.(map[string]any)
	if m == nil || p.ID.IsZero() {
		return false
	}
	if id, _ := m["id"].(string); id == p.ID.String() {
		return true
	}
	owner, _ := m["owner"].(string)
	if owner == "" {
		return false
	}
	for _, s := range subjects(p) {
		if s.String() == owner {
			return true
		}
	}
	return false
}

func lineage(f any) []string {
	m, _ := f.(map[string]any)
	if m == nil {
		return nil
	}
	var out []string
	if id, _ := m["id"].(string); id != "" {
		out = append(out, id)
	}
	if anc, ok := m[accessapi.AncestorsKey].([]string); ok {
		out = append(out, anc...)
	}
	return out
}

// hasRole: the principal holds role r (or a role including it) as an app
// role, or through a grant on f or an ancestor, directly or via a group.
func (a *Authorizer) hasRole(ctx context.Context, p accessapi.Principal, f any, r string) (bool, error) {
	for _, held := range p.Roles {
		if a.roles.Satisfies(held, r) {
			return true, nil
		}
	}
	lin := lineage(f)
	if len(lin) == 0 {
		return false, nil
	}
	gs, err := a.grants(ctx, p)
	if err != nil {
		return false, err
	}
	for _, g := range gs {
		if a.roles.Satisfies(g.Role, r) && slices.Contains(lin, g.Object.String()) {
			return true, nil
		}
	}
	return false, nil
}

// Bindings implements accessapi.Authorizer.
func (a *Authorizer) Bindings(ctx context.Context, p accessapi.Principal) map[string]any {
	return map[string]any{
		rt.FnIsOwner: func(f any) bool { return isOwner(p, f) },
		rt.FnHasRole: func(f any, r string) bool {
			ok, err := a.hasRole(ctx, p, f, r)
			return err == nil && ok
		},
		rt.FnInGroup: func(id string) bool {
			for _, g := range p.Groups {
				if g.String() == id {
					return true
				}
			}
			return false
		},
		rt.FnNow: a.clock.Now,
	}
}

func superuser(p accessapi.Principal) bool { return p.IsSystem() || p.HasRole("admin") }

// Check implements accessapi.Authorizer.
func (a *Authorizer) Check(ctx context.Context, p accessapi.Principal, req accessapi.CheckRequest) (accessapi.Decision, error) {
	if superuser(p) {
		return accessapi.Decision{Allowed: true, Reason: "superuser"}, nil
	}
	if p.IsLink() {
		return linkCheck(p, req), nil
	}
	ct := req.Type
	if ct == nil && req.Feature != nil {
		ct = req.Feature.Type
	}
	if ct == nil {
		return accessapi.Decision{}, fmt.Errorf("authorization request without a type")
	}
	if req.Op == accessapi.OpMove && !a.defines(ct, accessapi.OpMove) {
		// Without a move policy, a move needs update on the feature and
		// create under the new parent.
		up, err := a.Check(ctx, p, accessapi.CheckRequest{Op: accessapi.OpUpdate, Type: ct, Feature: req.Feature})
		if err != nil || !up.Allowed {
			return up, err
		}
		return a.Check(ctx, p, accessapi.CheckRequest{Op: accessapi.OpCreate, Type: ct, Parent: req.Parent})
	}
	deciding := a.policies.Deciding(ct, req.Op)
	parent := req.Parent
	if parent == nil && req.Feature != nil && req.Feature.ParentID != nil && usesParent(deciding, req.Op) {
		var err error
		if parent, err = a.parent(ctx, *req.Feature.ParentID); err != nil {
			return accessapi.Decision{}, err
		}
	}
	vars := a.Bindings(ctx, p)
	vars["principal"] = p.ExprValue()
	vars["feature"] = dataOf(req.Feature)
	vars["parent"] = dataOf(parent)
	vars["patch"] = orEmpty(req.Patch)
	vars["input"] = orEmpty(req.Input)
	for _, pol := range deciding {
		prog, ok := pol.Progs[req.Op]
		if !ok {
			continue
		}
		out, err := prog.Eval(vars)
		if err != nil {
			// A failing policy denies; it never allows.
			continue
		}
		if out == true {
			return accessapi.Decision{Allowed: true, Reason: string(pol.Target)}, nil
		}
	}
	return accessapi.Decision{Allowed: false, Reason: "no policy allows " + string(req.Op)}, nil
}

// linkCheck answers for share-link holders without consulting app
// policies (ADR 0020): read the shared feature and its subtree, and run
// the link's actions on the shared feature itself. The writes of such an
// action (its transition, or its flow's writes) may update the shared
// feature and create features in its subtree.
func linkCheck(p accessapi.Principal, req accessapi.CheckRequest) accessapi.Decision {
	inAction := req.InAction != "" && slices.Contains(p.Link.Actions, req.InAction)
	if req.Op == accessapi.OpCreate && inAction && req.Parent != nil && slices.Contains(req.Parent.Lineage(), p.Link.Object) {
		return accessapi.Decision{Allowed: true, Reason: "share link action"}
	}
	f := req.Feature
	if f == nil {
		return accessapi.Decision{Reason: "not covered by the share link"}
	}
	switch {
	case req.Op == accessapi.OpUpdate && inAction && f.ID == p.Link.Object:
		return accessapi.Decision{Allowed: true, Reason: "share link action"}
	case req.Op == accessapi.OpRead && slices.Contains(f.Lineage(), p.Link.Object):
		return accessapi.Decision{Allowed: true, Reason: "share link"}
	case req.Op.Action() != "" && f.ID == p.Link.Object && slices.Contains(p.Link.Actions, req.Op.Action()):
		return accessapi.Decision{Allowed: true, Reason: "share link"}
	}
	return accessapi.Decision{Reason: "not covered by the share link"}
}

// usesParent reports whether a rule for op may read `parent`.
func usesParent(pols []*domain.Policy, op accessapi.Operation) bool {
	for _, pol := range pols {
		if prog, ok := pol.Progs[op]; ok && strings.Contains(prog.Source(), "parent") {
			return true
		}
	}
	return false
}

// parent loads a feature's parent once per request.
func (a *Authorizer) parent(ctx context.Context, id kernel.ID) (*accessapi.FeatureRef, error) {
	if a.parents == nil {
		return nil, nil
	}
	c, _ := ctx.Value(cacheKey{}).(*requestCache)
	if c != nil {
		c.mu.Lock()
		ref, ok := c.parents[id]
		c.mu.Unlock()
		if ok {
			return ref, nil
		}
	}
	ref, err := a.parents(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		ref, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	if c != nil {
		c.mu.Lock()
		c.parents[id] = ref
		c.mu.Unlock()
	}
	return ref, nil
}

func (a *Authorizer) defines(ct *schemaapi.CompiledType, op accessapi.Operation) bool {
	for _, pol := range a.policies.For(ct) {
		if _, ok := pol.Progs[op]; ok {
			return true
		}
	}
	return false
}

func dataOf(f *accessapi.FeatureRef) map[string]any {
	if f == nil {
		return map[string]any{}
	}
	return f.Data
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Audience implements accessapi.Authorizer (spec 8.7): system, then admin
// (app role or admin grant on the feature), then owner, then public.
func (a *Authorizer) Audience(ctx context.Context, p accessapi.Principal, f *accessapi.FeatureRef) (schemaapi.Audience, error) {
	switch {
	case p.IsSystem():
		return schemaapi.AudSystem, nil
	case p.HasRole("admin"):
		return schemaapi.AudAdmin, nil
	case p.IsLink():
		return schemaapi.AudPublic, nil
	}
	if f != nil {
		ok, err := a.hasRole(ctx, p, f.Data, "admin")
		if err != nil {
			return 0, err
		}
		if ok {
			return schemaapi.AudAdmin, nil
		}
		if isOwner(p, f.Data) {
			return schemaapi.AudOwner, nil
		}
	}
	return memberOrPublic(p), nil
}

// memberOrPublic is the audience of callers without owner or admin access:
// member when signed in, public for anonymous callers and share links.
func memberOrPublic(p accessapi.Principal) schemaapi.Audience {
	if p.IsAnonymous() || p.IsLink() {
		return schemaapi.AudPublic
	}
	return schemaapi.AudMember
}

// Scope implements accessapi.Authorizer: the read policies of the type,
// translated into a predicate (spec 8.5, 8.6).
func (a *Authorizer) Scope(ctx context.Context, p accessapi.Principal, ct *schemaapi.CompiledType) (queryapi.Predicate, bool, error) {
	if superuser(p) {
		return queryapi.True{}, true, nil
	}
	if p.IsLink() {
		return queryapi.InScope{Field: queryapi.ScopeAncestor, IDs: []kernel.ID{p.Link.Object}}, true, nil
	}
	var terms []queryapi.Predicate
	for _, pol := range a.policies.Deciding(ct, accessapi.OpRead) {
		src, ok := pol.Sources[accessapi.OpRead]
		if !ok {
			continue
		}
		se, ok := domain.TranslateRead(src)
		if !ok {
			return queryapi.True{}, false, nil
		}
		pred, exact, err := a.resolve(ctx, p, ct, se)
		if err != nil {
			return nil, false, err
		}
		if !exact {
			return queryapi.True{}, false, nil
		}
		terms = append(terms, pred)
	}
	if len(terms) == 0 {
		return queryapi.False{}, true, nil
	}
	return simplifyOr(terms), true, nil
}

func (a *Authorizer) resolve(ctx context.Context, p accessapi.Principal, ct *schemaapi.CompiledType, se domain.ScopeExpr) (queryapi.Predicate, bool, error) {
	switch x := se.(type) {
	case domain.ScConst:
		if x.Value {
			return queryapi.True{}, true, nil
		}
		return queryapi.False{}, true, nil
	case domain.ScOwner:
		if p.ID.IsZero() {
			return queryapi.False{}, true, nil
		}
		return queryapi.Or{Terms: []queryapi.Predicate{
			queryapi.InScope{Field: queryapi.ScopeOwner, IDs: subjects(p)},
			queryapi.InScope{Field: queryapi.ScopeSelf, IDs: []kernel.ID{p.ID}},
		}}, true, nil
	case domain.ScRole:
		for _, held := range p.Roles {
			if a.roles.Satisfies(held, x.Role) {
				return queryapi.True{}, true, nil
			}
		}
		gs, err := a.grants(ctx, p)
		if err != nil {
			return nil, false, err
		}
		var objs []kernel.ID
		for _, g := range gs {
			if a.roles.Satisfies(g.Role, x.Role) {
				objs = append(objs, g.Object)
			}
		}
		if len(objs) == 0 {
			return queryapi.False{}, true, nil
		}
		return queryapi.InScope{Field: queryapi.ScopeAncestor, IDs: objs}, true, nil
	case domain.ScPrincipal:
		prog, err := a.principalProgram(x.Source)
		if err != nil {
			return nil, false, nil
		}
		vars := a.Bindings(ctx, p)
		vars["principal"] = p.ExprValue()
		out, err := prog.Eval(vars)
		if err != nil || out != true {
			return queryapi.False{}, true, nil
		}
		return queryapi.True{}, true, nil
	case domain.ScField:
		f, ok := ct.Field(x.Path)
		if !ok || !f.Top || !f.Indexed {
			return nil, false, nil
		}
		var v kernel.Value = kernel.Null{}
		if x.PrincipalID {
			v = kernel.Ref{ID: p.ID}
			if f.Kind != kernel.KindRef {
				v = kernel.String(p.ID.String())
			}
		} else if x.Literal != nil {
			cv, err := f.Type.Coerce(x.Path, x.Literal)
			if err != nil {
				return queryapi.False{}, true, nil
			}
			v = cv
		}
		op := queryapi.OpEq
		if x.Negate {
			op = queryapi.OpNe
		}
		return queryapi.Compare{Path: x.Path, Op: op, Value: v}, true, nil
	case domain.ScAnd, domain.ScOr:
		var parts []domain.ScopeExpr
		if and, isAnd := x.(domain.ScAnd); isAnd {
			parts = and.Terms
		} else {
			parts = x.(domain.ScOr).Terms
		}
		var preds []queryapi.Predicate
		for _, t := range parts {
			pr, exact, err := a.resolve(ctx, p, ct, t)
			if err != nil || !exact {
				return nil, exact, err
			}
			preds = append(preds, pr)
		}
		if _, isAnd := x.(domain.ScAnd); isAnd {
			return simplifyAnd(preds), true, nil
		}
		return simplifyOr(preds), true, nil
	}
	return nil, false, nil
}

func (a *Authorizer) principalProgram(src string) (rt.Program, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if p, ok := a.principals[src]; ok {
		return p, nil
	}
	p, err := a.exprs.Compile(src, domain.Env(rt.OpenMap()), rt.Bool, false)
	if err != nil {
		return nil, err
	}
	a.principals[src] = p
	return p, nil
}

func simplifyOr(ts []queryapi.Predicate) queryapi.Predicate {
	var keep []queryapi.Predicate
	for _, t := range ts {
		switch t.(type) {
		case queryapi.True:
			return queryapi.True{}
		case queryapi.False:
			continue
		}
		keep = append(keep, t)
	}
	switch len(keep) {
	case 0:
		return queryapi.False{}
	case 1:
		return keep[0]
	}
	return queryapi.Or{Terms: keep}
}

func simplifyAnd(ts []queryapi.Predicate) queryapi.Predicate {
	var keep []queryapi.Predicate
	for _, t := range ts {
		switch t.(type) {
		case queryapi.False:
			return queryapi.False{}
		case queryapi.True:
			continue
		}
		keep = append(keep, t)
	}
	switch len(keep) {
	case 0:
		return queryapi.True{}
	case 1:
		return keep[0]
	}
	return queryapi.And{Terms: keep}
}
