package application

import (
	"context"
	"errors"
	"reflect"
	"strings"

	accessapi "organon/internal/access/api"
	contentapi "organon/internal/content/api"
	"organon/internal/content/domain"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// Counter is an optional repository capability used by the cost estimate.
type Counter interface {
	Count(ctx context.Context, types []schemaapi.TypeID) (int, error)
}

// QueryResult is one page of a query (spec 7.1). Expanded holds the
// features referenced by the expanded paths, keyed by ID, each projected
// through the caller's read view for its own type.
type QueryResult struct {
	Items    []contentapi.FeatureView          `json:"items"`
	Next     string                            `json:"next,omitempty"`
	Expanded map[string]contentapi.FeatureView `json:"expanded,omitempty"`
}

// queryAudience is the query view used to check fields: callers may only
// filter on fields every matching feature would show them (spec 7).
func queryAudience(p accessapi.Principal) schemaapi.Audience {
	switch {
	case p.IsSystem():
		return schemaapi.AudSystem
	case p.HasRole("admin"):
		return schemaapi.AudAdmin
	}
	return schemaapi.AudPublic
}

// RunQuery runs a query document for the caller (spec 6.4 RunQuery, 7).
func (s *Service) RunQuery(ctx context.Context, p accessapi.Principal, raw []byte) (QueryResult, error) {
	if s.d.Planner == nil {
		return QueryResult{}, errors.New("query planner not configured")
	}
	doc, err := s.d.Planner.Parse(raw)
	if err != nil {
		return QueryResult{}, err
	}
	var types []*schemaapi.CompiledType
	if strings.HasPrefix(doc.Type, "urn:") {
		id, err := schemaapi.ParseTypeID(doc.Type)
		if err != nil {
			return QueryResult{}, kernel.NewError(kernel.CodeInvalidQuery, "%v", err)
		}
		types = s.d.Types.Resolve(id)
	} else if ct, ok := s.d.Types.ByName(doc.Type); ok {
		types = []*schemaapi.CompiledType{ct}
	}
	if len(types) == 0 {
		return QueryResult{}, kernel.NewError(kernel.CodeInvalidQuery, "unknown type %q", doc.Type)
	}
	for _, ct := range types[1:] {
		if ct.Repository != types[0].Repository {
			return QueryResult{}, kernel.ErrCrossRepository
		}
	}
	repo, err := s.repoFor(types[0])
	if err != nil {
		return QueryResult{}, err
	}
	// Permission scope (spec 7.3 step 4).
	var scope queryapi.Predicate
	exact := true
	for i, ct := range types {
		pred, ex, err := s.d.Authorizer.Scope(ctx, p, ct)
		if err != nil {
			return QueryResult{}, err
		}
		if i == 0 {
			scope = pred
		} else if !reflect.DeepEqual(pred, scope) {
			scope, ex = queryapi.True{}, false
		}
		exact = exact && ex
	}
	if !exact {
		scope = queryapi.True{}
	}
	caps := repo.Capabilities()
	req := queryapi.PlanRequest{
		Doc: doc, Types: types, Audience: queryAudience(p), Scope: scope, ScopeExact: exact,
		Caps: queryapi.RepoCaps{RangeFilters: caps.RangeFilters, Sort: caps.Sort, SubtreeQueries: caps.SubtreeQueries, SecondaryIndexes: caps.SecondaryIndexes},
	}
	if c, ok := repo.(Counter); ok {
		ids := make([]schemaapi.TypeID, len(types))
		for i, ct := range types {
			ids[i] = ct.ID
		}
		req.Count = func(ctx context.Context) (int, error) { return c.Count(ctx, ids) }
	}
	plan, err := s.d.Planner.Plan(ctx, req)
	if err != nil {
		return QueryResult{}, err
	}
	page, err := repo.Query(ctx, plan)
	if err != nil {
		return QueryResult{}, err
	}
	out := QueryResult{Items: []contentapi.FeatureView{}, Next: s.d.Planner.EncodeCursor(page.Next, s.d.Planner.Binding(doc))}
	var refs []kernel.ID
	for _, f := range page.Items {
		ct, err := s.typeOf(f)
		if err != nil {
			return QueryResult{}, err
		}
		ref := s.Ref(f, ct)
		var aud schemaapi.Audience
		if exact {
			aud, err = s.d.Authorizer.Audience(ctx, p, ref)
		} else {
			aud, err = s.canRead(ctx, p, ref) // post-filter (spec 8.5)
			if errors.Is(err, kernel.ErrNotFound) {
				continue
			}
		}
		if err != nil {
			return QueryResult{}, err
		}
		snap := f.Snapshot()
		out.Items = append(out.Items, s.link(ct, domain.Project(ct, snap, aud, plan.Select)))
		readable := ct.View(aud, schemaapi.DirRead).Readable
		for _, path := range doc.Expand {
			if !readable[path] {
				continue
			}
			if v, ok := snap.Fields[path]; ok {
				if r, ok := v.(kernel.Ref); ok {
					refs = append(refs, r.ID)
				}
			}
		}
	}
	if len(doc.Expand) > 0 {
		out.Expanded, err = s.expand(ctx, p, repo, refs)
		if err != nil {
			return QueryResult{}, err
		}
	}
	return out, nil
}

// expand loads up to MaxExpand referenced features the caller may read.
func (s *Service) expand(ctx context.Context, p accessapi.Principal, repo domain.FeatureRepository, ids []kernel.ID) (map[string]contentapi.FeatureView, error) {
	if len(ids) > queryapi.MaxExpand {
		ids = ids[:queryapi.MaxExpand]
	}
	out := map[string]contentapi.FeatureView{}
	fs, err := repo.GetMany(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		ct, err := s.typeOf(f)
		if err != nil {
			continue
		}
		aud, err := s.canRead(ctx, p, s.Ref(f, ct))
		if err != nil {
			continue
		}
		out[f.ID().String()] = s.link(ct, domain.Project(ct, f.Snapshot(), aud, nil))
	}
	return out, nil
}
