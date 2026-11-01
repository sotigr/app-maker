// Package repotest is the repository conformance suite (spec 5.5). Every
// FeatureRepository implementation must pass Run before it is mergeable.
package repotest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"app-maker/internal/content/domain"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	schemaapi "app-maker/internal/schema/api"
)

// Harness is what a backend provides to the suite.
type Harness struct {
	Repo domain.FeatureRepository
	// Jobs returns every job committed so far, in commit order.
	Jobs func() []kernel.JobRequest
	// SingleReplica skips multi-replica tests (SQLite, spec 5.3).
	SingleReplica bool
}

// Factory builds a fresh, empty backend for one test.
type Factory func(t *testing.T) Harness

// Test type IDs. Repositories do not interpret types beyond their ID.
const (
	TypeRoot  schemaapi.TypeID = "urn:sys:feature:root"
	TypeA     schemaapi.TypeID = "urn:app:feature:a/v1"
	TypeB     schemaapi.TypeID = "urn:app:feature:b/v1"
	TypeOther schemaapi.TypeID = "urn:app:feature:other/v1"
)

var (
	ids   = kernel.UUIDv7Generator{}
	epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// Run runs the whole suite against the factory.
func Run(t *testing.T, f Factory) {
	tests := []struct {
		name string
		fn   func(*testing.T, Harness)
	}{
		{"CRUD", testCRUD},
		{"VersionConflict", testVersionConflict},
		{"BatchAtomicity", testBatchAtomicity},
		{"TransactionalJobs", testTransactionalJobs},
		{"SoftDeleteAndPurge", testSoftDeleteAndPurge},
		{"ChildrenPaging", testChildrenPaging},
		{"SubtreeAndMove", testSubtreeAndMove},
		{"QueryOperators", testQueryOperators},
		{"SortEachKind", testSortEachKind},
		{"UniqueName", testUniqueName},
		{"ConcurrentWriters", testConcurrentWriters},
		{"Unsupported", testUnsupported},
		{"AuditLog", testAuditLog},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, f(t)) })
	}
}

func snap(id kernel.ID, typ schemaapi.TypeID, parent *kernel.ID, version kernel.Version, fields map[string]kernel.Value) domain.Snapshot {
	if fields == nil {
		fields = map[string]kernel.Value{}
	}
	return domain.Snapshot{ID: id, TypeID: typ, ParentID: parent, Version: version, Fields: fields, CreatedAt: epoch, UpdatedAt: epoch}
}

func create(t *testing.T, h Harness, typ schemaapi.TypeID, parent *kernel.ID, fields map[string]kernel.Value) domain.Snapshot {
	t.Helper()
	s := snap(ids.NewID(), typ, parent, 1, fields)
	if _, err := h.Repo.Apply(context.Background(), domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: s}}}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return s
}

func root(t *testing.T, h Harness) kernel.ID {
	t.Helper()
	return create(t, h, TypeRoot, nil, nil).ID
}

func update(s domain.Snapshot, fields map[string]kernel.Value) domain.Mutation {
	n := s.Clone()
	for k, v := range fields {
		n.Fields[k] = v
	}
	n.Version = s.Version + 1
	return domain.Mutation{Kind: domain.MutUpdate, Feature: n, Expected: s.Version}
}

func mustGet(t *testing.T, h Harness, id kernel.ID) *domain.Feature {
	t.Helper()
	f, err := h.Repo.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return f
}

func testCRUD(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	a := create(t, h, TypeA, &r, map[string]kernel.Value{"/title": kernel.String("one"), "/n": kernel.Int64(1)})
	got := mustGet(t, h, a.ID)
	if got.Version() != 1 || got.TypeID() != TypeA || *got.ParentID() != r {
		t.Fatalf("read back: %+v", got.Snapshot())
	}
	if v, _ := got.Field("/title"); v != kernel.String("one") {
		t.Fatalf("title = %v", v)
	}
	if anc := got.Ancestors(); len(anc) != 1 || anc[0] != r {
		t.Fatalf("ancestors = %v", anc)
	}
	res, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{update(a, map[string]kernel.Value{"/title": kernel.String("two")})}})
	if err != nil || res.Versions[a.ID] != 2 {
		t.Fatalf("update: %v %v", res, err)
	}
	if v, _ := mustGet(t, h, a.ID).Field("/title"); v != kernel.String("two") {
		t.Fatalf("updated title = %v", v)
	}
	many, err := h.Repo.GetMany(ctx, []kernel.ID{a.ID, r, ids.NewID()})
	if err != nil || len(many) != 2 {
		t.Fatalf("GetMany: %d %v", len(many), err)
	}
	if _, err := h.Repo.Get(ctx, ids.NewID()); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("missing get: %v", err)
	}
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: a}}}); !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("duplicate create: %v", err)
	}
	orphan := snap(ids.NewID(), TypeA, ptr(ids.NewID()), 1, nil)
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: orphan}}}); !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("create under missing parent: %v", err)
	}
}

func ptr(id kernel.ID) *kernel.ID { return &id }

// Spec 3.5: stale versions fail with the current version.
func testVersionConflict(t *testing.T, h Harness) {
	r := root(t, h)
	a := create(t, h, TypeA, &r, nil)
	m := update(a, map[string]kernel.Value{"/title": kernel.String("x")})
	if _, err := h.Repo.Apply(context.Background(), domain.Batch{Mutations: []domain.Mutation{m}}); err != nil {
		t.Fatal(err)
	}
	_, err := h.Repo.Apply(context.Background(), domain.Batch{Mutations: []domain.Mutation{m}})
	var vc *kernel.VersionConflictError
	if !errors.As(err, &vc) || len(vc.Conflicts) != 1 || vc.Conflicts[0].Current != 2 {
		t.Fatalf("want conflict at version 2, got %v", err)
	}
}

// Spec 5.2: all or nothing; the error lists every conflicting feature.
func testBatchAtomicity(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	a := create(t, h, TypeA, &r, map[string]kernel.Value{"/title": kernel.String("a")})
	b := create(t, h, TypeA, &r, nil)
	c := create(t, h, TypeA, &r, nil)
	stale := func(s domain.Snapshot) domain.Mutation {
		m := update(s, nil)
		m.Expected = 9
		m.Feature.Version = 10
		return m
	}
	newOne := snap(ids.NewID(), TypeB, &r, 1, nil)
	_, err := h.Repo.Apply(ctx, domain.Batch{
		Mutations: []domain.Mutation{
			update(a, map[string]kernel.Value{"/title": kernel.String("changed")}),
			{Kind: domain.MutCreate, Feature: newOne},
			stale(b), stale(c),
		},
		Jobs: []kernel.JobRequest{{Kind: "publish_events", Queue: kernel.QueueEvents}},
	})
	var vc *kernel.VersionConflictError
	if !errors.As(err, &vc) || len(vc.Conflicts) != 2 {
		t.Fatalf("want 2 conflicts, got %v", err)
	}
	if v, _ := mustGet(t, h, a.ID).Field("/title"); v != kernel.String("a") {
		t.Fatal("partial write: update applied")
	}
	if _, err := h.Repo.Get(ctx, newOne.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("partial write: create applied")
	}
}

// Spec 5.2: jobs exist exactly when the commit succeeds.
func testTransactionalJobs(t *testing.T, h Harness) {
	if !h.Repo.Capabilities().TransactionalJobs {
		t.Skip("backend does not declare TransactionalJobs")
	}
	ctx := context.Background()
	r := root(t, h)
	before := len(h.Jobs())
	a := snap(ids.NewID(), TypeA, &r, 1, nil)
	job := kernel.JobRequest{Kind: "publish_events", Queue: kernel.QueueEvents, Args: []byte(fmt.Sprintf(`{"id":%q}`, a.ID))}
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: a}}, Jobs: []kernel.JobRequest{job}}); err != nil {
		t.Fatal(err)
	}
	if got := len(h.Jobs()) - before; got != 1 {
		t.Fatalf("committed batch inserted %d jobs, want 1", got)
	}
	m := update(a, nil)
	m.Expected = 5
	_, _ = h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{m}, Jobs: []kernel.JobRequest{job}})
	if got := len(h.Jobs()) - before; got != 1 {
		t.Fatalf("failed batch inserted jobs: %d", got)
	}
}

// Spec 3.6: soft-deleted features disappear from reads and queries; purge
// hard-deletes.
func testSoftDeleteAndPurge(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	a := create(t, h, TypeA, &r, nil)
	del := a.Clone()
	at := epoch.Add(time.Hour)
	del.DeletedAt = &at
	del.Version = 2
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutDelete, Feature: del, Expected: 1}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Repo.Get(ctx, a.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("deleted feature readable: %v", err)
	}
	page, _ := h.Repo.Children(ctx, r, domain.PageRequest{})
	if len(page.Items) != 0 {
		t.Fatal("deleted feature listed")
	}
	q, _ := h.Repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{TypeA}, Limit: 10})
	if len(q.Items) != 0 {
		t.Fatal("deleted feature queried")
	}
	q, _ = h.Repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{TypeA}, Limit: 10, IncludeDeleted: true})
	if len(q.Items) != 1 || !q.Items[0].IsDeleted() {
		t.Fatal("IncludeDeleted query must return the deleted feature")
	}
	purge := del.Clone()
	purge.Version = 3
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutPurge, Feature: purge, Expected: 2}}}); err != nil {
		t.Fatal(err)
	}
	q, _ = h.Repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{TypeA}, Limit: 10, IncludeDeleted: true})
	if len(q.Items) != 0 {
		t.Fatal("purged feature still stored")
	}
}

// Spec 6.5: cursor paging returns every child exactly once, in ID order.
func testChildrenPaging(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	want := map[kernel.ID]bool{}
	for i := 0; i < 120; i++ {
		typ := TypeA
		if i%3 == 0 {
			typ = TypeB
		}
		want[create(t, h, typ, &r, nil).ID] = typ == TypeA
	}
	seen := map[kernel.ID]bool{}
	var after *queryapi.Cursor
	var last kernel.ID
	pages := 0
	for {
		p, err := h.Repo.Children(ctx, r, domain.PageRequest{Limit: 50, After: after})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, f := range p.Items {
			if seen[f.ID()] {
				t.Fatalf("duplicate %s", f.ID())
			}
			if !last.IsZero() && string(f.ID().String()) <= last.String() {
				t.Fatal("children not ordered by ID")
			}
			last = f.ID()
			seen[f.ID()] = true
		}
		if p.Next == nil {
			break
		}
		after = p.Next
	}
	if len(seen) != 120 || pages != 3 {
		t.Fatalf("saw %d children in %d pages", len(seen), pages)
	}
	p, _ := h.Repo.Children(ctx, r, domain.PageRequest{Limit: 500, Types: []schemaapi.TypeID{TypeA}})
	if len(p.Items) != 80 {
		t.Fatalf("type filter: %d", len(p.Items))
	}
}

// Spec 5.3: subtree queries and moves rewrite descendants' ancestry.
func testSubtreeAndMove(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	p1 := create(t, h, TypeA, &r, nil)
	p2 := create(t, h, TypeA, &r, nil)
	c := create(t, h, TypeB, &p1.ID, nil)
	g := create(t, h, TypeB, &c.ID, nil)
	desc, err := h.Repo.Query(ctx, queryapi.Plan{Scope: queryapi.Scope{Kind: queryapi.ScopeDescendants, Of: p1.ID}, Limit: 10})
	if err != nil || len(desc.Items) != 2 {
		t.Fatalf("descendants of p1: %d %v", len(desc.Items), err)
	}
	ds, err := h.Repo.Descendants(ctx, r, 100)
	if err != nil || len(ds) != 4 {
		t.Fatalf("Descendants: %d %v", len(ds), err)
	}
	if _, err := h.Repo.Descendants(ctx, r, 2); !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("Descendants over limit: %v", err)
	}
	mv := c.Clone()
	mv.ParentID = &p2.ID
	mv.Version = 2
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutMove, Feature: mv, Expected: 1}}}); err != nil {
		t.Fatal(err)
	}
	if anc := mustGet(t, h, g.ID).Ancestors(); len(anc) != 3 || anc[1] != p2.ID || anc[2] != c.ID {
		t.Fatalf("grandchild ancestors after move: %v", anc)
	}
	desc, _ = h.Repo.Query(ctx, queryapi.Plan{Scope: queryapi.Scope{Kind: queryapi.ScopeDescendants, Of: p1.ID}, Limit: 10})
	if len(desc.Items) != 0 {
		t.Fatal("moved subtree still under old parent")
	}
	children, _ := h.Repo.Query(ctx, queryapi.Plan{Scope: queryapi.Scope{Kind: queryapi.ScopeChildrenOf, Of: p2.ID}, Limit: 10})
	if len(children.Items) != 1 {
		t.Fatal("childrenOf after move")
	}
	cyc := mustGet(t, h, p2.ID).Snapshot()
	cyc.ParentID = &g.ID
	cyc.Version = 2
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutMove, Feature: cyc, Expected: 1}}}); !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("cycle not rejected: %v", err)
	}
}

// Spec 7.2: every operator the repository claims.
func testQueryOperators(t *testing.T, h Harness) {
	ctx := context.Background()
	caps := h.Repo.Capabilities()
	r := root(t, h)
	owner := ids.NewID()
	mk := func(title string, n int64, tags []string, nullable kernel.Value) domain.Snapshot {
		items := make([]kernel.Value, len(tags))
		for i, tg := range tags {
			items[i] = kernel.String(tg)
		}
		fields := map[string]kernel.Value{
			"/title": kernel.String(title), "/n": kernel.Int64(n),
			"/tags": kernel.List{Elem: kernel.KindString, Items: items},
			"/addr": kernel.NewObject([]string{"city"}, map[string]kernel.Value{"city": kernel.String(title + "ville")}),
		}
		if nullable != nil {
			fields["/opt"] = nullable
		}
		s := snap(ids.NewID(), TypeA, &r, 1, fields)
		s.OwnerID = &owner
		if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: s}}}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	mk("alpha", 1, []string{"x"}, kernel.String("set"))
	mk("beta", 2, []string{"x", "y"}, kernel.Null{})
	mk("gamma", 3, nil, nil)
	create(t, h, TypeB, &r, map[string]kernel.Value{"/title": kernel.String("alpha")})
	str := func(s ...string) kernel.List {
		l := kernel.List{Elem: kernel.KindString}
		for _, x := range s {
			l.Items = append(l.Items, kernel.String(x))
		}
		return l
	}
	cases := []struct {
		name  string
		where queryapi.Predicate
		want  int
		needs bool
	}{
		{"eq", queryapi.Compare{Path: "/title", Op: queryapi.OpEq, Value: kernel.String("beta")}, 1, true},
		{"ne", queryapi.Compare{Path: "/title", Op: queryapi.OpNe, Value: kernel.String("beta")}, 2, true},
		{"eq null", queryapi.Compare{Path: "/opt", Op: queryapi.OpEq, Value: kernel.Null{}}, 2, true},
		{"lt", queryapi.Compare{Path: "/n", Op: queryapi.OpLt, Value: kernel.Int64(2)}, 1, caps.RangeFilters},
		{"lte", queryapi.Compare{Path: "/n", Op: queryapi.OpLte, Value: kernel.Int64(2)}, 2, caps.RangeFilters},
		{"gt", queryapi.Compare{Path: "/n", Op: queryapi.OpGt, Value: kernel.Int64(2)}, 1, caps.RangeFilters},
		{"gte", queryapi.Compare{Path: "/title", Op: queryapi.OpGte, Value: kernel.String("beta")}, 2, caps.RangeFilters},
		{"in", queryapi.Compare{Path: "/title", Op: queryapi.OpIn, Value: str("alpha", "gamma")}, 2, true},
		{"nin", queryapi.Compare{Path: "/title", Op: queryapi.OpNin, Value: str("alpha", "gamma")}, 1, true},
		{"prefix", queryapi.Compare{Path: "/title", Op: queryapi.OpPrefix, Value: kernel.String("al")}, 1, true},
		{"contains", queryapi.Compare{Path: "/tags", Op: queryapi.OpContains, Value: kernel.String("x")}, 2, true},
		{"exists", queryapi.Compare{Path: "/opt", Op: queryapi.OpExists}, 1, true},
		{"nested", queryapi.Compare{Path: "/addr/city", Op: queryapi.OpEq, Value: kernel.String("gammaville")}, 1, true},
		{"and", queryapi.And{Terms: []queryapi.Predicate{
			queryapi.Compare{Path: "/tags", Op: queryapi.OpContains, Value: kernel.String("x")},
			queryapi.Compare{Path: "/title", Op: queryapi.OpEq, Value: kernel.String("alpha")}}}, 1, true},
		{"or", queryapi.Or{Terms: []queryapi.Predicate{
			queryapi.Compare{Path: "/title", Op: queryapi.OpEq, Value: kernel.String("gamma")},
			queryapi.Compare{Path: "/title", Op: queryapi.OpEq, Value: kernel.String("alpha")}}}, 2, true},
		{"not", queryapi.Not{Term: queryapi.Compare{Path: "/title", Op: queryapi.OpEq, Value: kernel.String("alpha")}}, 2, true},
		{"scope owner", queryapi.InScope{Field: queryapi.ScopeOwner, IDs: []kernel.ID{owner}}, 3, true},
		{"scope ancestor", queryapi.InScope{Field: queryapi.ScopeAncestor, IDs: []kernel.ID{r}}, 3, true},
		{"scope none", queryapi.InScope{Field: queryapi.ScopeAncestor, IDs: []kernel.ID{ids.NewID()}}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := h.Repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{TypeA}, Where: c.where, Limit: 100})
			if !c.needs {
				if !errors.Is(err, kernel.ErrUnsupported) {
					t.Fatalf("undeclared capability must return ErrUnsupported, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Items) != c.want {
				t.Fatalf("got %d results, want %d", len(p.Items), c.want)
			}
		})
	}
}

// Spec 5.5: index-backed sorting of each kind, with cursor paging.
func testSortEachKind(t *testing.T, h Harness) {
	ctx := context.Background()
	if !h.Repo.Capabilities().Sort {
		t.Skip("no Sort capability")
	}
	r := root(t, h)
	n := 12
	for i := 0; i < n; i++ {
		j := (i * 7) % n // shuffled insert order
		create(t, h, TypeA, &r, map[string]kernel.Value{
			"/s":   kernel.String(fmt.Sprintf("s%02d", j)),
			"/i":   kernel.Int64(int64(j*100 - 500)),
			"/f":   kernel.Float(float64(j) / 3),
			"/d":   kernel.DecimalValue{D: kernel.MustDecimal(fmt.Sprintf("%d.5", j*10))},
			"/t":   kernel.NewTime(epoch.Add(time.Duration(j) * time.Hour)),
			"/dup": kernel.Int64(int64(j % 2)),
		})
	}
	for _, path := range []string{"/s", "/i", "/f", "/d", "/t", "/dup"} {
		for _, desc := range []bool{false, true} {
			keys := []queryapi.SortKey{{Path: path, Desc: desc}, {Path: ""}}
			var got []domain.Snapshot
			var after *queryapi.Cursor
			for {
				p, err := h.Repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{TypeA}, Sort: keys, Limit: 5, After: after})
				if err != nil {
					t.Fatal(err)
				}
				for _, f := range p.Items {
					got = append(got, f.Snapshot())
				}
				if p.Next == nil {
					break
				}
				after = p.Next
			}
			if len(got) != n {
				t.Fatalf("%s desc=%v: %d results", path, desc, len(got))
			}
			for i := 1; i < n; i++ {
				a, _ := domain.ValueAt(got[i-1].Fields, path)
				b, _ := domain.ValueAt(got[i].Fields, path)
				c, ok := kernel.Compare(a, b)
				if !ok || (!desc && c > 0) || (desc && c < 0) {
					t.Fatalf("%s desc=%v: out of order at %d: %v then %v", path, desc, i, a, b)
				}
				if c == 0 && got[i-1].ID.String() >= got[i].ID.String() {
					t.Fatalf("%s: ties must be ordered by ID", path)
				}
			}
		}
	}
}

// Spec 3.4: x-uniqueName is unique among live siblings.
func testUniqueName(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	a := snap(ids.NewID(), TypeA, &r, 1, nil)
	b := snap(ids.NewID(), TypeA, &r, 1, nil)
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: a, UniqueKey: "a\x00launch"}}}); err != nil {
		t.Fatal(err)
	}
	_, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: b, UniqueKey: "a\x00launch"}}})
	if !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("duplicate name accepted: %v", err)
	}
	other := create(t, h, TypeA, &r, nil)
	c := snap(ids.NewID(), TypeA, &other.ID, 1, nil)
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: c, UniqueKey: "a\x00launch"}}}); err != nil {
		t.Fatalf("same name under another parent: %v", err)
	}
	del := a.Clone()
	at := epoch
	del.DeletedAt = &at
	del.Version = 2
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutDelete, Feature: del, Expected: 1, UniqueKey: "a\x00launch"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: b, UniqueKey: "a\x00launch"}}}); err != nil {
		t.Fatalf("name of deleted sibling must be reusable: %v", err)
	}
}

// Spec 5.5: 100 goroutines updating one feature produce exactly one
// success per version.
func testConcurrentWriters(t *testing.T, h Harness) {
	ctx := context.Background()
	r := root(t, h)
	a := create(t, h, TypeA, &r, map[string]kernel.Value{"/n": kernel.Int64(0)})
	const writers = 100
	var mu sync.Mutex
	successes := map[kernel.Version]int{}
	var wg sync.WaitGroup
	var conflicts atomic.Int64
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for {
				cur, err := h.Repo.Get(ctx, a.ID)
				if err != nil {
					t.Error(err)
					return
				}
				m := update(cur.Snapshot(), map[string]kernel.Value{"/n": kernel.Int64(int64(i))})
				res, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{m}})
				if err == nil {
					mu.Lock()
					successes[res.Versions[a.ID]]++
					mu.Unlock()
					return
				}
				if !errors.Is(err, kernel.ErrVersionConflict) {
					t.Error(err)
					return
				}
				conflicts.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if len(successes) != writers {
		t.Fatalf("%d distinct versions for %d writers", len(successes), writers)
	}
	for v, n := range successes {
		if n != 1 {
			t.Fatalf("version %d committed %d times", v, n)
		}
	}
	if got := mustGet(t, h, a.ID).Version(); got != writers+1 {
		t.Fatalf("final version %d", got)
	}
}

// Spec 5.2, 5.5: undeclared capabilities return ErrUnsupported.
func testUnsupported(t *testing.T, h Harness) {
	ctx := context.Background()
	caps := h.Repo.Capabilities()
	if !caps.Transactions {
		r := root(t, h)
		_, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{
			{Kind: domain.MutCreate, Feature: snap(ids.NewID(), TypeA, &r, 1, nil)},
			{Kind: domain.MutCreate, Feature: snap(ids.NewID(), TypeA, &r, 1, nil)},
		}})
		if !errors.Is(err, kernel.ErrUnsupported) {
			t.Fatalf("multi-mutation batch without Transactions: %v", err)
		}
	}
	if caps.MaxBatchSize > 0 {
		r := root(t, h)
		muts := make([]domain.Mutation, caps.MaxBatchSize+1)
		for i := range muts {
			muts[i] = domain.Mutation{Kind: domain.MutCreate, Feature: snap(ids.NewID(), TypeA, &r, 1, nil)}
		}
		if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: muts}); !errors.Is(err, kernel.ErrUnsupported) {
			t.Fatalf("batch above MaxBatchSize: %v", err)
		}
	}
}

// Spec 14.3: audit rows are written in the commit transaction.
func testAuditLog(t *testing.T, h Harness) {
	ctx := context.Background()
	store, ok := h.Repo.(domain.AuditStore)
	if !ok {
		t.Skip("no audit store")
	}
	r := root(t, h)
	a := create(t, h, TypeA, &r, nil)
	actor := ids.NewID()
	rec := func(action string, target kernel.ID, at time.Time) kernel.AuditRecord {
		return kernel.AuditRecord{ID: ids.NewID(), At: at, Actor: actor, Action: action, Target: &target, Type: string(TypeA)}
	}
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{update(a, map[string]kernel.Value{"/title": kernel.String("x")})},
		Audit: []kernel.AuditRecord{rec("updated", a.ID, epoch.Add(time.Hour))}}); err != nil {
		t.Fatal(err)
	}
	// A failed commit writes no audit rows.
	stale := update(a, nil)
	stale.Expected, stale.Feature.Version = 9, 10
	if _, err := h.Repo.Apply(ctx, domain.Batch{Mutations: []domain.Mutation{stale},
		Audit: []kernel.AuditRecord{rec("updated", a.ID, epoch.Add(2*time.Hour))}}); err == nil {
		t.Fatal("stale update applied")
	}
	if err := store.AppendAudit(ctx, []kernel.AuditRecord{{ID: ids.NewID(), At: epoch.Add(3 * time.Hour), Actor: actor, Action: "login"}}); err != nil {
		t.Fatal(err)
	}
	all, err := store.Audit(ctx, kernel.AuditFilter{Limit: 10})
	if err != nil || len(all) != 2 || all[0].Action != "login" || all[1].Action != "updated" || *all[1].Target != a.ID {
		t.Fatalf("audit: %+v %v", all, err)
	}
	byTarget, _ := store.Audit(ctx, kernel.AuditFilter{Target: &a.ID, Limit: 10})
	byAction, _ := store.Audit(ctx, kernel.AuditFilter{Action: "login", Limit: 10})
	older, _ := store.Audit(ctx, kernel.AuditFilter{Before: &all[0].ID, Limit: 10})
	if len(byTarget) != 1 || len(byAction) != 1 || len(older) != 1 || older[0].ID != all[1].ID {
		t.Fatalf("filters: %d %d %d", len(byTarget), len(byAction), len(older))
	}
	n, err := store.PurgeAudit(ctx, epoch.Add(150*time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if left, _ := store.Audit(ctx, kernel.AuditFilter{Limit: 10}); len(left) != 1 || left[0].Action != "login" {
		t.Fatalf("after purge: %+v", left)
	}
}
