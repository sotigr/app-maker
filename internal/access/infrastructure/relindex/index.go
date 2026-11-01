// Package relindex implements the relationship index (spec 8.6) on top of
// the grant and team features themselves, read through indexed queries
// (ADR 0006). Every replica reads the same repository, so it is consistent
// across replicas without a separate projection.
package relindex

import (
	"context"

	"organon/internal/access/domain"
	contentapi "organon/internal/content/api"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// MaxGroupDepth bounds transitive membership resolution.
const MaxGroupDepth = 8

// MaxRows bounds grant and membership lookups per principal.
const MaxRows = 10_000

// Index reads grants and memberships from features.
type Index struct {
	reader contentapi.SystemReader
}

// New returns an index over the reader.
func New(reader contentapi.SystemReader) *Index { return &Index{reader: reader} }

var _ domain.RelationshipIndex = (*Index)(nil)

// Groups returns teams whose members include the principal, transitively.
func (x *Index) Groups(ctx context.Context, principal kernel.ID) ([]kernel.ID, error) {
	seen := map[kernel.ID]bool{principal: true}
	frontier := []kernel.ID{principal}
	var out []kernel.ID
	for depth := 0; depth < MaxGroupDepth && len(frontier) > 0; depth++ {
		var next []kernel.ID
		for _, m := range frontier {
			teams, err := x.reader.Find(ctx, schemaapi.TypeTeam,
				queryapi.Compare{Path: "/members", Op: queryapi.OpContains, Value: kernel.Ref{ID: m}}, MaxRows)
			if err != nil {
				return nil, err
			}
			for _, t := range teams {
				if !seen[t.ID] {
					seen[t.ID] = true
					out = append(out, t.ID)
					next = append(next, t.ID)
				}
			}
		}
		frontier = next
	}
	return out, nil
}

// Grants returns every grant held by the subjects.
func (x *Index) Grants(ctx context.Context, subjects []kernel.ID) ([]domain.Grant, error) {
	if len(subjects) == 0 {
		return nil, nil
	}
	list := kernel.List{Elem: kernel.KindRef}
	for _, s := range subjects {
		list.Items = append(list.Items, kernel.Ref{ID: s})
	}
	rows, err := x.reader.Find(ctx, schemaapi.TypeGrant,
		queryapi.Compare{Path: "/subject", Op: queryapi.OpIn, Value: list}, MaxRows)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Grant, 0, len(rows))
	for _, r := range rows {
		subj, err1 := kernel.ParseID(str(r.Data["subject"]))
		obj, err2 := kernel.ParseID(str(r.Data["object"]))
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, domain.Grant{Subject: subj, Object: obj, Role: str(r.Data["role"])})
	}
	return out, nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
