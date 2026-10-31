// Package memory is the reference FeatureRepository (spec 5.3): features
// in maps guarded by a RWMutex, every capability except FullText, and jobs
// handed to the in-process adapter after each commit.
package memory

import (
	"context"
	"slices"
	"sort"
	"sync"

	"app-maker/internal/content/domain"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	schemaapi "app-maker/internal/schema/api"
)

// MaxBatchSize is the largest batch accepted.
const MaxBatchSize = 1000

type record struct {
	snap      domain.Snapshot
	uniqueKey string
}

// Repository is the memory FeatureRepository.
type Repository struct {
	mu       sync.RWMutex
	features map[kernel.ID]*record
	children map[kernel.ID]map[kernel.ID]struct{}
	jobs     kernel.JobEnqueuer
}

var _ domain.FeatureRepository = (*Repository)(nil)

// New returns an empty repository. jobs receives each committed batch's
// jobs after commit; it may be nil.
func New(jobs kernel.JobEnqueuer) *Repository {
	return &Repository{features: map[kernel.ID]*record{}, children: map[kernel.ID]map[kernel.ID]struct{}{}, jobs: jobs}
}

// Capabilities implements domain.FeatureRepository.
func (r *Repository) Capabilities() domain.Capabilities {
	return domain.Capabilities{
		Transactions: true, TransactionalJobs: true, SecondaryIndexes: true, Sort: true,
		RangeFilters: true, FullText: false, SubtreeQueries: true, MaxBatchSize: MaxBatchSize,
	}
}

// Get implements domain.FeatureRepository.
func (r *Repository) Get(_ context.Context, id kernel.ID) (*domain.Feature, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.features[id]
	if !ok || rec.snap.DeletedAt != nil {
		return nil, kernel.ErrNotFound
	}
	return domain.Rehydrate(rec.snap), nil
}

// GetMany implements domain.FeatureRepository.
func (r *Repository) GetMany(_ context.Context, ids []kernel.ID) ([]*domain.Feature, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.Feature, 0, len(ids))
	seen := map[kernel.ID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if rec, ok := r.features[id]; ok && rec.snap.DeletedAt == nil {
			out = append(out, domain.Rehydrate(rec.snap))
		}
	}
	return out, nil
}

// Children implements domain.FeatureRepository.
func (r *Repository) Children(_ context.Context, parent kernel.ID, page domain.PageRequest) (domain.Page[*domain.Feature], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var snaps []domain.Snapshot
	for id := range r.children[parent] {
		rec := r.features[id]
		if rec.snap.DeletedAt != nil || !typeMatches(rec.snap, page.Types) {
			continue
		}
		snaps = append(snaps, rec.snap)
	}
	sort.Slice(snaps, func(i, j int) bool { return lessID(snaps[i].ID, snaps[j].ID) })
	return paginate(snaps, nil, page.After, limitOf(page.Limit)), nil
}

// Descendants implements domain.FeatureRepository.
func (r *Repository) Descendants(_ context.Context, id kernel.ID, limit int) ([]*domain.Feature, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.Feature
	queue := []kernel.ID{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for c := range r.children[cur] {
			rec := r.features[c]
			if rec.snap.DeletedAt != nil {
				continue
			}
			if len(out) >= limit {
				return nil, &kernel.ConstraintError{Path: "", Code: "subtree_too_large", ID: id}
			}
			out = append(out, domain.Rehydrate(rec.snap))
			queue = append(queue, c)
		}
	}
	return out, nil
}

// Count returns the number of live features of the types (cost estimate).
func (r *Repository) Count(_ context.Context, types []schemaapi.TypeID) (int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, rec := range r.features {
		if rec.snap.DeletedAt == nil && typeMatches(rec.snap, types) {
			n++
		}
	}
	return n, nil
}

// Query implements domain.FeatureRepository by scanning; the reference
// semantics are queryapi.Match.
func (r *Repository) Query(_ context.Context, q queryapi.Plan) (domain.Page[*domain.Feature], error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var snaps []domain.Snapshot
	for _, rec := range r.features {
		s := rec.snap
		if (s.DeletedAt != nil && !q.IncludeDeleted) || !typeMatches(s, q.Types) {
			continue
		}
		switch q.Scope.Kind {
		case queryapi.ScopeChildrenOf:
			if s.ParentID == nil || *s.ParentID != q.Scope.Of {
				continue
			}
		case queryapi.ScopeDescendants:
			if !slices.Contains(s.Ancestors, q.Scope.Of) {
				continue
			}
		}
		if !queryapi.Match(q.Where, s.AsDoc()) {
			continue
		}
		snaps = append(snaps, s)
	}
	sort.SliceStable(snaps, func(i, j int) bool { return compareSnaps(snaps[i], snaps[j], q.Sort) < 0 })
	return paginate(snaps, q.Sort, q.After, limitOf(q.Limit)), nil
}

func limitOf(n int) int {
	switch {
	case n <= 0:
		return queryapi.DefaultLimit
	case n > queryapi.MaxLimit:
		return queryapi.MaxLimit
	}
	return n
}

func typeMatches(s domain.Snapshot, types []schemaapi.TypeID) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if s.TypeID.Base() == t.Base() {
			return true
		}
	}
	return false
}

func lessID(a, b kernel.ID) bool { return slices.Compare(a[:], b[:]) < 0 }

// compareSnaps orders by the sort keys, then by ID.
func compareSnaps(a, b domain.Snapshot, keys []queryapi.SortKey) int {
	for _, k := range keys {
		if k.Path == "" {
			continue
		}
		av, _ := domain.ValueAt(a.Fields, k.Path)
		bv, _ := domain.ValueAt(b.Fields, k.Path)
		c, _ := kernel.Compare(orNull(av), orNull(bv))
		if k.Desc {
			c = -c
		}
		if c != 0 {
			return c
		}
	}
	c := slices.Compare(a.ID[:], b.ID[:])
	if len(keys) > 0 && keys[len(keys)-1].Path == "" && keys[len(keys)-1].Desc {
		c = -c
	}
	return c
}

func orNull(v kernel.Value) kernel.Value {
	if v == nil {
		return kernel.Null{}
	}
	return v
}

// paginate skips past the cursor and cuts one page.
func paginate(snaps []domain.Snapshot, keys []queryapi.SortKey, after *queryapi.Cursor, limit int) domain.Page[*domain.Feature] {
	start := 0
	if after != nil {
		cur := domain.Snapshot{ID: after.ID, Fields: map[string]kernel.Value{}}
		i := 0
		for _, k := range keys {
			if k.Path == "" {
				continue
			}
			if i < len(after.Values) {
				setAt(cur.Fields, k.Path, after.Values[i])
			}
			i++
		}
		start = sort.Search(len(snaps), func(i int) bool { return compareSnaps(snaps[i], cur, keys) > 0 })
	}
	end := min(start+limit, len(snaps))
	page := domain.Page[*domain.Feature]{}
	for _, s := range snaps[start:end] {
		page.Items = append(page.Items, domain.Rehydrate(s))
	}
	if end < len(snaps) && end > start {
		last := snaps[end-1]
		page.Next = &queryapi.Cursor{Values: domain.SortValues(last, keys), ID: last.ID}
	}
	return page
}

// setAt stores a sort value for a cursor; nested paths are flattened into
// an object chain.
func setAt(fields map[string]kernel.Value, path string, v kernel.Value) {
	parts := splitPath(path)
	if len(parts) == 1 {
		fields["/"+parts[0]] = v
		return
	}
	var build func(i int) kernel.Value
	build = func(i int) kernel.Value {
		if i == len(parts) {
			return v
		}
		return kernel.NewObject([]string{parts[i]}, map[string]kernel.Value{parts[i]: build(i + 1)})
	}
	fields["/"+parts[0]] = build(1)
}

// Apply implements domain.FeatureRepository (spec 5.2).
func (r *Repository) Apply(ctx context.Context, b domain.Batch) (domain.CommitResult, error) {
	if len(b.Mutations) > MaxBatchSize {
		return domain.CommitResult{}, &kernel.UnsupportedError{Capability: "MaxBatchSize"}
	}
	r.mu.Lock()
	result, err := r.apply(b)
	r.mu.Unlock()
	if err != nil {
		return domain.CommitResult{}, err
	}
	if r.jobs != nil && len(b.Jobs) > 0 {
		// Handed over after commit; the in-process adapter is not durable.
		_ = r.jobs.Enqueue(ctx, b.Jobs)
	}
	return result, nil
}

func (r *Repository) apply(b domain.Batch) (domain.CommitResult, error) {
	staged := map[kernel.ID]*record{}
	get := func(id kernel.ID) (*record, bool) {
		if rec, ok := staged[id]; ok {
			return rec, rec != nil
		}
		rec, ok := r.features[id]
		return rec, ok
	}
	live := func(id kernel.ID) bool {
		rec, ok := get(id)
		return ok && rec.snap.DeletedAt == nil
	}
	var conflicts []kernel.VersionConflict
	var moved []kernel.ID
	result := domain.CommitResult{Versions: map[kernel.ID]kernel.Version{}}
	for _, m := range b.Mutations {
		s := m.Feature.Clone()
		switch m.Kind {
		case domain.MutCreate:
			if _, exists := get(s.ID); exists {
				return result, &kernel.ConstraintError{Path: "/id", Code: "duplicate_id", ID: s.ID}
			}
			if s.ParentID != nil && !live(*s.ParentID) {
				return result, &kernel.ConstraintError{Path: "/parent", Code: "parent_not_found", ID: s.ID}
			}
			if s.Version != 1 {
				return result, &kernel.ConstraintError{Path: "/version", Code: "invalid_version", ID: s.ID}
			}
			s.Ancestors = r.ancestorsOf(s.ParentID, get)
		default:
			cur, ok := get(s.ID)
			if !ok || (m.Kind != domain.MutPurge && cur.snap.DeletedAt != nil) {
				return result, kernel.NewError(kernel.CodeNotFound, "feature %s not found", s.ID)
			}
			if cur.snap.Version != m.Expected {
				conflicts = append(conflicts, kernel.VersionConflict{ID: s.ID, Current: cur.snap.Version})
				continue
			}
			if s.TypeID != cur.snap.TypeID {
				return result, &kernel.ConstraintError{Path: "/type", Code: "type_changed", ID: s.ID}
			}
			if m.Kind == domain.MutPurge {
				staged[s.ID] = nil
				result.Versions[s.ID] = s.Version
				continue
			}
			if s.Version != m.Expected+1 {
				return result, &kernel.ConstraintError{Path: "/version", Code: "invalid_version", ID: s.ID}
			}
			if m.Kind == domain.MutMove {
				if s.ParentID == nil || !live(*s.ParentID) {
					return result, &kernel.ConstraintError{Path: "/parent", Code: "parent_not_found", ID: s.ID}
				}
				anc := r.ancestorsOf(s.ParentID, get)
				if *s.ParentID == s.ID || slices.Contains(anc, s.ID) {
					return result, &kernel.ConstraintError{Path: "/parent", Code: "cycle", ID: s.ID}
				}
				s.Ancestors = anc
				moved = append(moved, s.ID)
			} else {
				s.ParentID, s.Ancestors = cur.snap.ParentID, cur.snap.Ancestors
			}
		}
		staged[s.ID] = &record{snap: s, uniqueKey: m.UniqueKey}
		result.Versions[s.ID] = s.Version
	}
	if len(conflicts) > 0 {
		return domain.CommitResult{}, &kernel.VersionConflictError{Conflicts: conflicts}
	}
	if err := r.checkUnique(staged); err != nil {
		return domain.CommitResult{}, err
	}
	// Commit.
	for id, rec := range staged {
		old, existed := r.features[id]
		if existed && old.snap.ParentID != nil {
			delete(r.children[*old.snap.ParentID], id)
		}
		if rec == nil {
			delete(r.features, id)
			delete(r.children, id)
			continue
		}
		if rec.uniqueKey == "" && existed {
			rec.uniqueKey = old.uniqueKey
		}
		r.features[id] = rec
		if rec.snap.ParentID != nil {
			p := *rec.snap.ParentID
			if r.children[p] == nil {
				r.children[p] = map[kernel.ID]struct{}{}
			}
			r.children[p][id] = struct{}{}
		}
	}
	for _, id := range moved {
		r.rewriteAncestors(id)
	}
	return result, nil
}

func (r *Repository) ancestorsOf(parent *kernel.ID, get func(kernel.ID) (*record, bool)) []kernel.ID {
	if parent == nil {
		return nil
	}
	rec, ok := get(*parent)
	if !ok {
		return []kernel.ID{*parent}
	}
	return append(slices.Clone(rec.snap.Ancestors), *parent)
}

// rewriteAncestors updates the materialized path of a moved subtree.
func (r *Repository) rewriteAncestors(id kernel.ID) {
	base := append(slices.Clone(r.features[id].snap.Ancestors), id)
	for c := range r.children[id] {
		rec := r.features[c]
		rec.snap.Ancestors = slices.Clone(base)
		r.rewriteAncestors(c)
	}
}

// checkUnique enforces x-uniqueName among live siblings (spec 3.4).
func (r *Repository) checkUnique(staged map[kernel.ID]*record) error {
	for id, rec := range staged {
		if rec == nil || rec.uniqueKey == "" || rec.snap.DeletedAt != nil || rec.snap.ParentID == nil {
			continue
		}
		parent := *rec.snap.ParentID
		siblings := map[kernel.ID]*record{}
		for c := range r.children[parent] {
			siblings[c] = r.features[c]
		}
		for sid, srec := range staged {
			if srec != nil && srec.snap.ParentID != nil && *srec.snap.ParentID == parent {
				siblings[sid] = srec
			} else {
				delete(siblings, sid) // moved away or purged in this batch
			}
		}
		for sid, srec := range siblings {
			if sid != id && srec.snap.DeletedAt == nil && srec.uniqueKey == rec.uniqueKey {
				return &kernel.ConstraintError{Path: "/uniqueName", Code: "duplicate_name", ID: id}
			}
		}
	}
	return nil
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p[1:] {
		if c == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}
