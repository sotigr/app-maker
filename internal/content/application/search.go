package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	accessapi "organon/internal/access/api"
	changeapi "organon/internal/change/api"
	contentapi "organon/internal/content/api"
	"organon/internal/content/domain"
	flowapi "organon/internal/flow/api"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// JobIndexFeatures updates the search index for one feature (spec 7.5;
// unique per feature so bursts coalesce in the in-process adapter).
const JobIndexFeatures = "index_features"

type indexArgs struct {
	ID kernel.ID `json:"id"`
}

func searchable(ct *schemaapi.CompiledType) bool {
	for _, f := range ct.TopFields() {
		if f.Searchable {
			return true
		}
	}
	return false
}

// IndexHook enqueues index_features jobs for commits touching types with
// x-searchable fields; search is eventually consistent (spec 7.5).
type IndexHook struct{ s *Service }

// IndexHook returns the commit hook for the search index.
func (s *Service) IndexHook() *IndexHook { return &IndexHook{s: s} }

// Jobs implements CommitHook.
func (h *IndexHook) Jobs(_ context.Context, events []changeapi.Event, _ flowapi.CausationChain) ([]kernel.JobRequest, error) {
	if h.s.d.Search == nil {
		return nil, nil
	}
	var jobs []kernel.JobRequest
	for _, ev := range events {
		ct, ok := h.s.d.Types.Get(ev.TypeID.Base())
		if !ok || !searchable(ct) {
			continue
		}
		args, _ := json.Marshal(indexArgs{ID: ev.FeatureID})
		jobs = append(jobs, kernel.JobRequest{Kind: JobIndexFeatures, Queue: kernel.QueueMaintenance, Args: args,
			UniqueKey: "idx:" + ev.FeatureID.String(), MaxAttempts: 10})
	}
	return jobs, nil
}

// HandleIndexFeatures indexes a feature once per visibility tier with that
// tier's searchable text only (spec 7.5); private text is never indexed.
func (s *Service) HandleIndexFeatures(ctx context.Context, raw json.RawMessage) error {
	var a indexArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	f, ct, err := s.load(ctx, a.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		return s.d.Search.Delete(ctx, []kernel.ID{a.ID})
	}
	if err != nil {
		return err
	}
	return s.indexFeature(ctx, f, ct)
}

// ReindexAll rebuilds the search index from the repositories, e.g. after a
// restart with the in-memory Bleve index (spec 7.5). It returns how many
// features were indexed.
func (s *Service) ReindexAll(ctx context.Context) (int, error) {
	if s.d.Search == nil {
		return 0, nil
	}
	n := 0
	for _, ct := range s.d.Types.All() {
		if !searchable(ct) {
			continue
		}
		repo, err := s.repoFor(ct)
		if err != nil {
			return n, err
		}
		var after *queryapi.Cursor
		for {
			page, err := repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{ct.ID}, Limit: queryapi.MaxLimit, After: after, Sort: []queryapi.SortKey{{}}})
			if err != nil {
				return n, err
			}
			for _, f := range page.Items {
				if err := s.indexFeature(ctx, f, ct); err != nil {
					return n, err
				}
				n++
			}
			if page.Next == nil {
				break
			}
			after = page.Next
		}
	}
	return n, nil
}

// indexFeature writes one feature's documents, one per visibility tier.
func (s *Service) indexFeature(ctx context.Context, f *domain.Feature, ct *schemaapi.CompiledType) error {
	scope := append(f.Ancestors(), f.ID())
	var owners []kernel.ID
	if o := f.OwnerID(); o != nil {
		owners = append(owners, *o)
	}
	if principalKinds[ct.Base()] != "" {
		owners = append(owners, f.ID()) // a principal owns its own feature
	}
	docs := []queryapi.SearchDoc{
		{ID: f.ID(), Type: ct.Base(), Tier: schemaapi.VisPublic, Owners: owners, Scope: scope, Text: map[string]string{}},
		{ID: f.ID(), Type: ct.Base(), Tier: schemaapi.VisOwner, Owners: owners, Scope: scope, Text: map[string]string{}},
	}
	for _, fd := range ct.TopFields() {
		if !fd.Searchable || fd.WriteOnly {
			continue
		}
		v, ok := f.Field(fd.Path)
		if !ok {
			continue
		}
		text, _ := kernel.Native(v).(string)
		switch fd.Visibility {
		case schemaapi.VisPublic:
			docs[0].Text[fd.Path] = text
		case schemaapi.VisOwner:
			docs[1].Text[fd.Path] = text
		}
	}
	return s.d.Search.Upsert(ctx, docs)
}

// SearchItem is one search result: the projected feature and highlights
// limited to fields the caller can read.
type SearchItem struct {
	Feature    contentapi.FeatureView `json:"feature"`
	Highlights map[string]string      `json:"highlights,omitempty"`
}

// SearchResult is GET /v1/search's body. The total is approximate because
// hits that fail the re-check are dropped (spec 7.5).
type SearchResult struct {
	Items            []SearchItem `json:"items"`
	ApproximateTotal int          `json:"approximateTotal"`
}

// Search runs a full-text query (spec 7.5): the index filters by tier and
// owners, then every hit is re-checked against the current authorizer and
// projected before return.
func (s *Service) Search(ctx context.Context, p accessapi.Principal, text, typeName string, limit, offset int) (SearchResult, error) {
	if s.d.Search == nil {
		return SearchResult{}, &kernel.UnsupportedError{Capability: "FullText"}
	}
	if strings.TrimSpace(text) == "" {
		return SearchResult{}, kernel.NewError(kernel.CodeInvalidQuery, "q is required")
	}
	q := queryapi.SearchQuery{Text: text, Limit: limit, Offset: offset}
	if typeName != "" {
		ct, ok := s.d.Types.ByName(typeName)
		if !ok {
			return SearchResult{}, kernel.NewError(kernel.CodeInvalidQuery, "unknown type %q", typeName)
		}
		q.Types = []schemaapi.TypeID{ct.Base()}
	}
	switch {
	case p.IsSystem() || p.HasRole("admin"):
		q.AllOwners = true
	case !p.ID.IsZero():
		q.Owners = append([]kernel.ID{p.ID}, p.Groups...)
	}
	res, err := s.d.Search.Search(ctx, q)
	if err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{Items: []SearchItem{}, ApproximateTotal: res.Total}
	seen := map[kernel.ID]bool{}
	for _, h := range res.Hits {
		if seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		f, ct, err := s.load(ctx, h.ID)
		if err != nil {
			out.ApproximateTotal--
			continue
		}
		aud, err := s.canRead(ctx, p, s.Ref(f, ct))
		if err != nil {
			out.ApproximateTotal-- // dropped by the re-check
			continue
		}
		view := s.link(ct, domain.Project(ct, f.Snapshot(), aud, nil))
		hl := map[string]string{}
		readable := ct.View(aud, schemaapi.DirRead).Readable
		words := strings.Fields(strings.ToLower(text))
		for _, fd := range ct.TopFields() {
			if !fd.Searchable || !readable[fd.Path] {
				continue
			}
			v, _ := f.Field(fd.Path)
			str, _ := kernel.Native(v).(string)
			if frag := highlight(str, words); frag != "" {
				hl[fd.Path] = frag
			}
		}
		out.Items = append(out.Items, SearchItem{Feature: view, Highlights: hl})
	}
	return out, nil
}

// highlight returns a fragment around the first matched word, marked <mark>.
func highlight(text string, words []string) string {
	lower := strings.ToLower(text)
	for _, w := range words {
		i := strings.Index(lower, w)
		if i < 0 {
			continue
		}
		start, end := max(0, i-40), min(len(text), i+len(w)+40)
		return text[start:i] + "<mark>" + text[i:i+len(w)] + "</mark>" + text[i+len(w):end]
	}
	return ""
}
