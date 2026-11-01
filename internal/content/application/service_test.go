package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	accessapi "organon/internal/access/api"
	changeapi "organon/internal/change/api"
	contentapi "organon/internal/content/api"
	"organon/internal/content/application"
	"organon/internal/content/domain"
	"organon/internal/content/infrastructure/memory"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	querydomain "organon/internal/query/domain"
	"organon/internal/runtime/domain/expression"
	schemaapi "organon/internal/schema/api"
	schemadomain "organon/internal/schema/domain"
	"organon/internal/schema/infrastructure/jsonschema"
)

// allowAll authorizes everything; the audience is owner for owners and
// public otherwise. Phase 3 replaces it with the Access context.
type allowAll struct{}

func (allowAll) Check(context.Context, accessapi.Principal, accessapi.CheckRequest) (accessapi.Decision, error) {
	return accessapi.Decision{Allowed: true}, nil
}
func (allowAll) Audience(_ context.Context, p accessapi.Principal, f *accessapi.FeatureRef) (schemaapi.Audience, error) {
	if p.IsSystem() {
		return schemaapi.AudSystem, nil
	}
	if f.OwnerID != nil && *f.OwnerID == p.ID {
		return schemaapi.AudOwner, nil
	}
	return schemaapi.AudPublic, nil
}
func (allowAll) Scope(context.Context, accessapi.Principal, *schemaapi.CompiledType) (queryapi.Predicate, bool, error) {
	return queryapi.True{}, true, nil
}
func (allowAll) Bindings(_ context.Context, p accessapi.Principal) map[string]any {
	return map[string]any{
		"isOwner": func(any) bool { return false },
		"hasRole": func(any, string) bool { return false },
		"inGroup": func(string) bool { return false },
	}
}

type jobLog struct {
	mu   sync.Mutex
	jobs []kernel.JobRequest
}

func (j *jobLog) Enqueue(_ context.Context, jobs []kernel.JobRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jobs = append(j.jobs, jobs...)
	return nil
}

type fixture struct {
	svc   *application.Service
	repo  *memory.Repository
	jobs  *jobLog
	clock *kernel.FakeClock
	ana   accessapi.Principal
	ben   accessapi.Principal
}

func setup(t *testing.T, extraTypes ...string) *fixture {
	t.Helper()
	files, _ := filepath.Glob("../../../examples/tracker/types/*.json")
	var srcs []schemadomain.Source
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, schemadomain.Source{File: f, Data: b})
	}
	for i, d := range extraTypes {
		srcs = append(srcs, schemadomain.Source{File: string(rune('x' + i)), Data: []byte(d)})
	}
	reg, probs := schemadomain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{}).Compile(schemadomain.Input{Types: srcs})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	jobs := &jobLog{}
	repo := memory.New(jobs)
	repos := domain.NewRepositoryRegistry()
	repos.Register("default", repo)
	clock := kernel.NewFakeClock(time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	svc := application.NewService(application.Deps{
		Repos: repos, Types: reg, Authorizer: allowAll{}, Clock: clock, IDs: kernel.UUIDv7Generator{},
		Planner: querydomain.NewPlanner([]byte("test-key")),
	})
	if err := svc.EnsureRoot(context.Background(), "tracker"); err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureRoot(context.Background(), "tracker"); err != nil {
		t.Fatalf("EnsureRoot must be idempotent: %v", err)
	}
	ids := kernel.UUIDv7Generator{}
	return &fixture{svc: svc, repo: repo, jobs: jobs, clock: clock,
		ana: accessapi.Principal{ID: ids.NewID(), Kind: accessapi.KindUser},
		ben: accessapi.Principal{ID: ids.NewID(), Kind: accessapi.KindUser}}
}

func (f *fixture) exec(t *testing.T, p accessapi.Principal, ops ...application.Op) application.TxResult {
	t.Helper()
	res, err := f.svc.Execute(context.Background(), application.WriteContext{Principal: p}, ops)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// Spec 6.3: transactions with temporary references.
func TestTransactionWithTemporaryRefs(t *testing.T) {
	f := setup(t)
	res := f.exec(t, f.ana,
		application.Op{Op: application.OpCreate, Type: "project", Ref: "$p", Data: map[string]any{"title": "Launch", "slug": "launch"}},
		application.Op{Op: application.OpCreate, Type: "task", Parent: "$p", Ref: "$t1", Data: map[string]any{"title": "Write copy", "estimate": "3.5"}},
	)
	if len(res.Refs) != 2 || len(res.Features) != 2 {
		t.Fatalf("result: %+v", res)
	}
	task := res.Features[1]
	if task.Parent == nil || *task.Parent != res.Refs["$p"] || task.Version != 1 {
		t.Fatalf("task: %+v", task)
	}
	if task.Data["status"] != "todo" || task.Data["estimate"] != "3.5" || task.Data["owner"] != f.ana.ID.String() {
		t.Fatalf("defaults/owner: %+v", task.Data)
	}
	if _, ok := task.Data["createdAt"]; !ok {
		t.Fatal("timestamped trait not maintained")
	}
	if task.Data["isLate"] != false {
		t.Fatalf("computed isLate: %v", task.Data["isLate"])
	}
	// publish_events job enqueued with the commit (spec 2.5, 9.2).
	var publish []kernel.JobRequest
	for _, j := range f.jobs.jobs {
		if j.Kind == changeapi.JobPublishEvents {
			publish = append(publish, j)
		}
	}
	var args changeapi.PublishEventsArgs
	if err := json.Unmarshal(publish[len(publish)-1].Args, &args); err != nil || len(args.Events) != 2 {
		t.Fatalf("publish_events: %v %+v", err, args)
	}
	if args.Events[1].Kind != changeapi.EventCreated || args.Events[1].After["title"] != "Write copy" {
		t.Fatalf("event: %+v", args.Events[1])
	}
}

func TestTransactionErrorsIdentifyOperation(t *testing.T) {
	f := setup(t)
	_, err := f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana}, []application.Op{
		{Op: application.OpCreate, Type: "project", Data: map[string]any{"title": "ok"}},
		{Op: application.OpCreate, Type: "task", Parent: "$p", Data: map[string]any{"title": "x"}},
	})
	var oe *application.OpError
	if !errors.As(err, &oe) || oe.Index != 1 {
		t.Fatalf("want op 1 error, got %v", err)
	}
	_, err = f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana}, []application.Op{
		{Op: application.OpCreate, Type: "project", Data: map[string]any{"title": "ok"}},
		{Op: application.OpCreate, Type: "project", Data: map[string]any{"title": strings.Repeat("x", 201)}},
	})
	var ve *kernel.ValidationError
	if !errors.As(err, &ve) || ve.Op != 1 || ve.Errors[0].Path != "/title" || ve.Errors[0].Code != "maxLength" {
		t.Fatalf("validation: %v", err)
	}
	// Nothing from the failed transaction was written.
	page, _ := f.repo.Query(context.Background(), queryapi.Plan{Types: []schemaapi.TypeID{"urn:app:feature:project/v1"}, Limit: 10})
	if len(page.Items) != 0 {
		t.Fatalf("partial write: %d projects", len(page.Items))
	}
}

func TestUpdateRulesAndConflicts(t *testing.T) {
	f := setup(t)
	res := f.exec(t, f.ana,
		application.Op{Op: application.OpCreate, Type: "project", Ref: "$p", Data: map[string]any{"title": "P"}},
		application.Op{Op: application.OpCreate, Type: "task", Parent: "$p", Ref: "$t", Data: map[string]any{"title": "T", "dueAt": "2026-10-01T00:00:00Z"}},
	)
	tid := res.Refs["$t"]
	if res.Features[1].Data["isLate"] != true {
		t.Fatal("isLate should be true for a past due date")
	}
	upd := func(p accessapi.Principal, v kernel.Version, data map[string]any) (application.TxResult, error) {
		return f.svc.Execute(context.Background(), application.WriteContext{Principal: p}, []application.Op{{Op: application.OpUpdate, Type: "task", ID: tid, Version: v, Data: data}})
	}
	r, err := upd(f.ana, 1, map[string]any{"title": "T2", "dueAt": nil})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Features[0]
	if got.Version != 2 || got.Data["title"] != "T2" || got.Data["isLate"] != false {
		t.Fatalf("after update: %+v", got)
	}
	if _, ok := got.Data["dueAt"]; ok {
		t.Fatal("merge patch null must remove the field")
	}
	if _, err := upd(f.ana, 1, map[string]any{"title": "stale"}); !errors.Is(err, kernel.ErrVersionConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if _, err := upd(f.ana, 0, map[string]any{"title": "x"}); kernel.CodeOf(err) != kernel.CodeVersionRequired {
		t.Fatalf("missing version: %v", err)
	}
	// x-write action:transition fields cannot change through generic updates (spec 6.3).
	_, err = upd(f.ana, 2, map[string]any{"status": "in_progress"})
	var ve *kernel.ValidationError
	if !errors.As(err, &ve) || ve.Errors[0].Code != "not_writable" {
		t.Fatalf("action field: %v", err)
	}
	// Private fields look unknown to non-system callers (spec 4.6).
	_, err = upd(f.ana, 2, map[string]any{"internalScore": 3})
	if !errors.As(err, &ve) || ve.Errors[0].Code != "additionalProperties" {
		t.Fatalf("private field: %v", err)
	}
	// An action unlocks its transition (spec 10.3, 4.10).
	r, err = f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana, Action: "start"},
		[]application.Op{{Op: application.OpUpdate, Type: "task", ID: tid, Version: 2, Data: map[string]any{"status": "in_progress"}}})
	if err != nil || r.Features[0].Data["status"] != "in_progress" {
		t.Fatalf("start: %v", err)
	}
	_, err = f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana, Action: "start"},
		[]application.Op{{Op: application.OpUpdate, Type: "task", ID: tid, Version: 3, Data: map[string]any{"status": "done"}}})
	if kernel.CodeOf(err) != kernel.CodeInvalidTransition {
		t.Fatalf("invalid transition: %v", err)
	}
	// Guard: only the assignee may complete.
	_, err = f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana, Action: "complete"},
		[]application.Op{{Op: application.OpUpdate, Type: "task", ID: tid, Version: 3, Data: map[string]any{"status": "done"}}})
	if kernel.CodeOf(err) != kernel.CodePreconditionFailed {
		t.Fatalf("guard: %v", err)
	}
}

// Spec 3.6: deleting a parent soft-deletes its children in one transaction.
func TestDeleteCascadesAndRestricts(t *testing.T) {
	f := setup(t, `{"$id":"urn:app:feature:pin/v1","type":"object","properties":{
		"task":{"type":"string","x-kind":"ref","x-ref":"urn:app:feature:task","x-onDelete":"restrict"}}}`)
	res := f.exec(t, f.ana,
		application.Op{Op: application.OpCreate, Type: "project", Ref: "$p", Data: map[string]any{"title": "P"}},
		application.Op{Op: application.OpCreate, Type: "task", Parent: "$p", Ref: "$t", Data: map[string]any{"title": "T"}},
		application.Op{Op: application.OpCreate, Type: "comment", Parent: "$t", Ref: "$c", Data: map[string]any{"body": "hi"}},
	)
	pin := f.exec(t, f.ana, application.Op{Op: application.OpCreate, Type: "pin", Data: map[string]any{"task": res.Refs["$t"]}})
	_, err := f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana},
		[]application.Op{{Op: application.OpDelete, Type: "project", ID: res.Refs["$p"], Version: 1}})
	if !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("restrict reference must block delete: %v", err)
	}
	f.exec(t, f.ana, application.Op{Op: application.OpDelete, Type: "pin", ID: pin.Features[0].ID, Version: 1})
	f.exec(t, f.ana, application.Op{Op: application.OpDelete, Type: "project", ID: res.Refs["$p"], Version: 1})
	for _, ref := range []string{"$p", "$t", "$c"} {
		if _, err := f.svc.GetFeature(context.Background(), f.ana, "", kernel.MustParseID(res.Refs[ref])); !errors.Is(err, kernel.ErrNotFound) {
			t.Fatalf("%s still readable: %v", ref, err)
		}
	}
}

func TestMoveAndChildren(t *testing.T) {
	f := setup(t)
	res := f.exec(t, f.ana,
		application.Op{Op: application.OpCreate, Type: "project", Ref: "$a", Data: map[string]any{"title": "A", "slug": "a"}},
		application.Op{Op: application.OpCreate, Type: "project", Ref: "$b", Data: map[string]any{"title": "B", "slug": "b"}},
		application.Op{Op: application.OpCreate, Type: "task", Parent: "$a", Ref: "$t", Data: map[string]any{"title": "T"}},
	)
	f.exec(t, f.ana, application.Op{Op: application.OpMove, Type: "task", ID: res.Refs["$t"], Version: 1, Parent: res.Refs["$b"]})
	page, err := f.svc.ListChildren(context.Background(), f.ana, kernel.MustParseID(res.Refs["$b"]), "", "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != res.Refs["$t"] {
		t.Fatalf("children of b: %+v %v", page, err)
	}
	// x-allowedChildren: a comment cannot live under a project.
	_, err = f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana},
		[]application.Op{{Op: application.OpCreate, Type: "comment", Parent: res.Refs["$a"], Data: map[string]any{"body": "x"}}})
	if !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("allowed children: %v", err)
	}
	// x-uniqueName among siblings.
	_, err = f.svc.Execute(context.Background(), application.WriteContext{Principal: f.ana},
		[]application.Op{{Op: application.OpCreate, Type: "project", Data: map[string]any{"title": "A2", "slug": "a"}}})
	if !errors.Is(err, kernel.ErrConstraint) {
		t.Fatalf("unique name: %v", err)
	}
}

func TestProjectionHidesPrivateFields(t *testing.T) {
	f := setup(t)
	sys := accessapi.System()
	res, err := f.svc.Execute(context.Background(), application.WriteContext{Principal: sys, Owner: &f.ana.ID, Fixture: true},
		[]application.Op{{Op: application.OpCreate, Type: "project", Ref: "$p", Data: map[string]any{"title": "P"}},
			{Op: application.OpCreate, Type: "task", Parent: "$p", Ref: "$t", Data: map[string]any{"title": "T", "internalScore": 0.7}}})
	if err != nil {
		t.Fatal(err)
	}
	id := kernel.MustParseID(res.Refs["$t"])
	asSystem, _ := f.svc.GetFeature(context.Background(), sys, "task", id)
	if asSystem.Data["internalScore"] == nil {
		t.Fatal("system must see private fields")
	}
	for _, p := range []accessapi.Principal{f.ana, f.ben} {
		v, err := f.svc.GetFeature(context.Background(), p, "task", id)
		if err != nil {
			t.Fatal(err)
		}
		if _, leaked := v.Data["internalScore"]; leaked {
			t.Fatal("private field leaked")
		}
	}
	if _, err := f.svc.GetFeature(context.Background(), f.ana, "project", id); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatal("type mismatch must be not found")
	}
	if contentapi.RootID.IsZero() {
		t.Fatal("root id")
	}
}
