package application

import (
	"context"

	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	"app-maker/internal/content/domain"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	schemaapi "app-maker/internal/schema/api"
)

// System adapts the service to the content/api system ports.
type System struct{ s *Service }

// SystemPorts returns the trusted reader/writer adapters.
func (s *Service) SystemPorts() *System { return &System{s: s} }

var (
	_ contentapi.SystemReader = (*System)(nil)
	_ contentapi.SystemWriter = (*System)(nil)
)

func stored(f *domain.Feature) contentapi.StoredFeature {
	snap := f.Snapshot()
	return contentapi.StoredFeature{
		ID: f.ID(), Type: f.TypeID(), ParentID: f.ParentID(), OwnerID: f.OwnerID(),
		Ancestors: f.Ancestors(), Version: f.Version(), Data: domain.ExprDocument(snap),
	}
}

// Get implements contentapi.SystemReader.
func (x *System) Get(ctx context.Context, id kernel.ID) (contentapi.StoredFeature, error) {
	f, _, err := x.s.load(ctx, id)
	if err != nil {
		return contentapi.StoredFeature{}, err
	}
	return stored(f), nil
}

// Find implements contentapi.SystemReader.
func (x *System) Find(ctx context.Context, t schemaapi.TypeID, where queryapi.Predicate, limit int) ([]contentapi.StoredFeature, error) {
	types := x.s.d.Types.Resolve(t)
	if len(types) == 0 {
		return nil, nil
	}
	repo, err := x.s.repoFor(types[0])
	if err != nil {
		return nil, err
	}
	ids := make([]schemaapi.TypeID, len(types))
	for i, ct := range types {
		ids[i] = ct.ID
	}
	var out []contentapi.StoredFeature
	var after *queryapi.Cursor
	for len(out) < limit {
		page, err := repo.Query(ctx, queryapi.Plan{Types: ids, Where: where, Limit: min(queryapi.MaxLimit, limit-len(out)), After: after,
			Sort: []queryapi.SortKey{{Path: ""}}})
		if err != nil {
			return nil, err
		}
		for _, f := range page.Items {
			out = append(out, stored(f))
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	return out, nil
}

// Create implements contentapi.SystemWriter.
func (x *System) Create(ctx context.Context, typeName string, parent *kernel.ID, data map[string]any, owner *kernel.ID) (kernel.ID, error) {
	op := Op{Op: OpCreate, Type: typeName, Ref: "$new", Data: data}
	if parent != nil {
		op.Parent = parent.String()
	}
	res, err := x.s.Execute(ctx, WriteContext{Principal: accessapi.System(), Owner: owner, Fixture: true}, []Op{op})
	if err != nil {
		return kernel.NilID, err
	}
	return kernel.ParseID(res.Refs["$new"])
}

// Update implements contentapi.SystemWriter.
func (x *System) Update(ctx context.Context, typeName string, id kernel.ID, version kernel.Version, data map[string]any) error {
	_, err := x.s.Execute(ctx, WriteContext{Principal: accessapi.System(), Fixture: true},
		[]Op{{Op: OpUpdate, Type: typeName, ID: id.String(), Version: version, Data: data}})
	return err
}
