package domain_test

import (
	"errors"
	"testing"
	"time"

	"app-maker/internal/content/domain"
	"app-maker/internal/kernel"
	"app-maker/internal/runtime/domain/expression"
	schemaapi "app-maker/internal/schema/api"
	schemadomain "app-maker/internal/schema/domain"
	"app-maker/internal/schema/infrastructure/jsonschema"
)

var (
	ids = kernel.UUIDv7Generator{}
	t0  = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

func registry(t *testing.T) *schemadomain.Registry {
	t.Helper()
	docs := []string{
		`{"$id":"urn:app:feature:folder/v1","type":"object","x-allowedChildren":["urn:app:feature:folder","urn:app:feature:doc"],"properties":{"name":{"type":"string"}}}`,
		`{"$id":"urn:app:feature:doc/v1","type":"object","x-traits":["urn:sys:trait:ownable"],"properties":{"title":{"type":"string"}}}`,
	}
	var srcs []schemadomain.Source
	for i, d := range docs {
		srcs = append(srcs, schemadomain.Source{File: string(rune('a' + i)), Data: []byte(d)})
	}
	reg, probs := schemadomain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{}).Compile(schemadomain.Input{Types: srcs})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return reg
}

func typ(t *testing.T, reg *schemadomain.Registry, name string) *schemaapi.CompiledType {
	ct, ok := reg.ByName(name)
	if !ok {
		t.Fatalf("no type %s", name)
	}
	return ct
}

func TestNewFeatureEnforcesAllowedChildren(t *testing.T) {
	reg := registry(t)
	folder, doc, root := typ(t, reg, "folder"), typ(t, reg, "doc"), typ(t, reg, "root")
	rootInfo := &domain.ParentInfo{ID: ids.NewID(), Type: root}
	f, err := domain.NewFeature(ids.NewID(), folder, rootInfo, nil, nil, t0, ids.NewID())
	if err != nil {
		t.Fatalf("root accepts any type: %v", err)
	}
	d, err := domain.NewFeature(ids.NewID(), doc, &domain.ParentInfo{ID: f.ID(), Type: folder, Ancestors: f.Ancestors()}, nil, nil, t0, ids.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.NewFeature(ids.NewID(), folder, &domain.ParentInfo{ID: d.ID(), Type: doc}, nil, nil, t0, ids.NewID()); !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("doc does not allow children: %v", err)
	}
	if _, err := domain.NewFeature(ids.NewID(), folder, nil, nil, nil, t0, ids.NewID()); err == nil {
		t.Fatal("non-root feature without parent accepted")
	}
	ev := d.DrainEvents()
	if len(ev) != 1 || ev[0].Kind != domain.EventCreated || ev[0].Version != 1 {
		t.Fatalf("created event: %+v", ev)
	}
	if anc := d.Ancestors(); len(anc) != 2 || anc[0] != rootInfo.ID || anc[1] != f.ID() {
		t.Fatalf("ancestors: %v", anc)
	}
}

// Spec 3.1: a feature cannot be its own ancestor; version increases by one.
func TestMoveAndVersion(t *testing.T) {
	reg := registry(t)
	folder, root := typ(t, reg, "folder"), typ(t, reg, "root")
	rootInfo := &domain.ParentInfo{ID: ids.NewID(), Type: root}
	a, _ := domain.NewFeature(ids.NewID(), folder, rootInfo, nil, nil, t0, ids.NewID())
	stored := domain.Rehydrate(a.Snapshot())
	b, _ := domain.NewFeature(ids.NewID(), folder, &domain.ParentInfo{ID: a.ID(), Type: folder, Ancestors: a.Ancestors()}, nil, nil, t0, ids.NewID())
	err := stored.MoveTo(domain.ParentInfo{ID: b.ID(), Type: folder, Ancestors: b.Ancestors()}, folder, t0, ids.NewID())
	if !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("move under own descendant: %v", err)
	}
	if err := stored.MoveTo(domain.ParentInfo{ID: stored.ID(), Type: folder}, folder, t0, ids.NewID()); !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("move under itself: %v", err)
	}
	if stored.Version() != 1 || stored.NextVersion() != 1 {
		t.Fatal("unchanged feature must keep its version")
	}
	changed, err := stored.Apply(domain.Patch{"/name": kernel.String("x")}, t0, ids.NewID())
	if err != nil || len(changed) != 1 || stored.NextVersion() != 2 {
		t.Fatalf("apply: %v %v next=%d", changed, err, stored.NextVersion())
	}
	if changed, _ := stored.Apply(domain.Patch{"/name": kernel.String("x")}, t0, ids.NewID()); len(changed) != 0 {
		t.Fatal("no-op patch reported changes")
	}
	if stored.Snapshot().TypeID != folder.ID {
		t.Fatal("type changed")
	}
}

func TestOwnerSyncAndDelete(t *testing.T) {
	reg := registry(t)
	doc, root := typ(t, reg, "doc"), typ(t, reg, "root")
	owner := ids.NewID()
	d, _ := domain.NewFeature(ids.NewID(), doc, &domain.ParentInfo{ID: ids.NewID(), Type: root}, nil,
		map[string]kernel.Value{"/owner": kernel.Ref{ID: owner}}, t0, ids.NewID())
	if o := d.OwnerID(); o == nil || *o != owner {
		t.Fatalf("owner = %v", o)
	}
	if err := d.MarkDeleted(t0, ids.NewID()); err != nil || !d.IsDeleted() {
		t.Fatal(err)
	}
	if _, err := d.Apply(domain.Patch{"/title": kernel.String("x")}, t0, ids.NewID()); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("update of deleted: %v", err)
	}
}
