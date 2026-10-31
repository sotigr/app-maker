// Package bleve is the embedded SearchIndex adapter (spec 7.5): for
// development and single-replica deployments only.
package bleve

import (
	"context"
	"fmt"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	schemaapi "app-maker/internal/schema/api"
)

// Index wraps a Bleve index. Each document is "<feature id>:<tier>".
type Index struct{ idx bleve.Index }

var _ queryapi.SearchIndex = (*Index)(nil)

func newMapping() mapping.IndexMapping {
	m := bleve.NewIndexMapping()
	kw := bleve.NewKeywordFieldMapping()
	doc := bleve.NewDocumentMapping()
	for _, f := range []string{"type", "tier", "owners", "scope", "feature"} {
		doc.AddFieldMappingsAt(f, kw)
	}
	m.DefaultMapping = doc
	return m
}

// NewMemory returns an in-memory index.
func NewMemory() (*Index, error) {
	idx, err := bleve.NewMemOnly(newMapping())
	if err != nil {
		return nil, err
	}
	return &Index{idx: idx}, nil
}

// Close closes the index.
func (x *Index) Close() error { return x.idx.Close() }

func docID(id kernel.ID, tier schemaapi.Visibility) string { return id.String() + ":" + tier.String() }

// Upsert implements queryapi.SearchIndex.
func (x *Index) Upsert(_ context.Context, docs []queryapi.SearchDoc) error {
	b := x.idx.NewBatch()
	for _, d := range docs {
		owners := make([]string, len(d.Owners))
		for i, o := range d.Owners {
			owners[i] = o.String()
		}
		scope := make([]string, len(d.Scope))
		for i, s := range d.Scope {
			scope[i] = s.String()
		}
		body := map[string]any{"type": string(d.Type), "tier": d.Tier.String(), "owners": owners, "scope": scope, "feature": d.ID.String()}
		var all []string
		for path, text := range d.Text {
			body["t_"+strings.TrimPrefix(path, "/")] = text
			all = append(all, text)
		}
		body["all"] = strings.Join(all, "\n")
		if len(d.Text) == 0 {
			b.Delete(docID(d.ID, d.Tier))
			continue
		}
		if err := b.Index(docID(d.ID, d.Tier), body); err != nil {
			return err
		}
	}
	return x.idx.Batch(b)
}

// Delete implements queryapi.SearchIndex.
func (x *Index) Delete(_ context.Context, ids []kernel.ID) error {
	b := x.idx.NewBatch()
	for _, id := range ids {
		for _, t := range []schemaapi.Visibility{schemaapi.VisPublic, schemaapi.VisOwner, schemaapi.VisPrivate} {
			b.Delete(docID(id, t))
		}
	}
	return x.idx.Batch(b)
}

func terms(field string, values []string) query.Query {
	var qs []query.Query
	for _, v := range values {
		q := bleve.NewTermQuery(v)
		q.SetField(field)
		qs = append(qs, q)
	}
	return bleve.NewDisjunctionQuery(qs...)
}

// Search implements queryapi.SearchIndex. Owner-tier documents match only
// for their owners (or everyone allowed AllOwners); text matches only the
// tier's own fields.
func (x *Index) Search(_ context.Context, q queryapi.SearchQuery) (queryapi.SearchResult, error) {
	text := bleve.NewMatchQuery(q.Text)
	text.SetField("all")
	public := bleve.NewTermQuery(schemaapi.VisPublic.String())
	public.SetField("tier")
	tiers := []query.Query{public}
	ownerTier := bleve.NewTermQuery(schemaapi.VisOwner.String())
	ownerTier.SetField("tier")
	switch {
	case q.AllOwners:
		tiers = append(tiers, ownerTier)
	case len(q.Owners) > 0:
		ids := make([]string, len(q.Owners))
		for i, o := range q.Owners {
			ids[i] = o.String()
		}
		tiers = append(tiers, bleve.NewConjunctionQuery(ownerTier, terms("owners", ids)))
	}
	must := []query.Query{text, bleve.NewDisjunctionQuery(tiers...)}
	if len(q.Types) > 0 {
		ts := make([]string, len(q.Types))
		for i, t := range q.Types {
			ts[i] = string(t.Base())
		}
		must = append(must, terms("type", ts))
	}
	limit := q.Limit
	if limit <= 0 || limit > queryapi.MaxLimit {
		limit = queryapi.DefaultLimit
	}
	req := bleve.NewSearchRequestOptions(bleve.NewConjunctionQuery(must...), limit, q.Offset, false)
	req.Highlight = bleve.NewHighlight()
	req.Fields = []string{"feature", "tier"}
	res, err := x.idx.Search(req)
	if err != nil {
		return queryapi.SearchResult{}, fmt.Errorf("search: %w", err)
	}
	out := queryapi.SearchResult{Total: int(res.Total)}
	for _, h := range res.Hits {
		id, tier, ok := strings.Cut(h.ID, ":")
		if !ok {
			continue
		}
		fid, err := kernel.ParseID(id)
		if err != nil {
			continue
		}
		vis, _ := schemaapi.ParseVisibility(tier)
		hl := map[string][]string{}
		for field, frags := range h.Fragments {
			if p, ok := strings.CutPrefix(field, "t_"); ok {
				hl["/"+p] = frags
			}
		}
		out.Hits = append(out.Hits, queryapi.SearchHit{ID: fid, Tier: vis, Highlights: hl})
	}
	return out, nil
}
