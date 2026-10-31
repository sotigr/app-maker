package application_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	flowapp "app-maker/internal/flow/application"
	"app-maker/internal/flow/domain"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	"app-maker/internal/runtime/domain/expression"
	schemaapi "app-maker/internal/schema/api"
	schemadomain "app-maker/internal/schema/domain"
	"app-maker/internal/schema/infrastructure/jsonschema"
)

// fakeContent fails the first commit with a version conflict.
type fakeContent struct {
	commits   int
	committed []contentapi.CommitRequest
}

func (f *fakeContent) Read(context.Context, accessapi.Principal, kernel.ID) (contentapi.FlowFeature, error) {
	return contentapi.FlowFeature{"id": "x", "version": int64(1), "type": "thing"}, nil
}
func (f *fakeContent) Query(context.Context, accessapi.Principal, []byte) ([]contentapi.FlowFeature, error) {
	return nil, nil
}
func (f *fakeContent) Children(context.Context, accessapi.Principal, kernel.ID, string, int) ([]contentapi.FlowFeature, error) {
	return nil, nil
}
func (f *fakeContent) Storage(_ context.Context, id kernel.ID) (contentapi.FlowFeature, error) {
	return contentapi.FlowFeature{"id": id.String(), "version": int64(1), "type": "thing", "n": int64(1)}, nil
}
func (f *fakeContent) AuthorizeAction(context.Context, accessapi.Principal, kernel.ID, string, map[string]any) (contentapi.FlowFeature, error) {
	return nil, nil
}
func (f *fakeContent) Commit(_ context.Context, req contentapi.CommitRequest) (contentapi.CommitResult, error) {
	f.commits++
	if f.commits == 1 {
		return contentapi.CommitResult{}, &kernel.VersionConflictError{Conflicts: []kernel.VersionConflict{{Current: 2}}}
	}
	f.committed = append(f.committed, req)
	return contentapi.CommitResult{}, nil
}

type allow struct{}

func (allow) Check(context.Context, accessapi.Principal, accessapi.CheckRequest) (accessapi.Decision, error) {
	return accessapi.Decision{Allowed: true}, nil
}
func (allow) Audience(context.Context, accessapi.Principal, *accessapi.FeatureRef) (schemaapi.Audience, error) {
	return schemaapi.AudSystem, nil
}
func (allow) Scope(context.Context, accessapi.Principal, *schemaapi.CompiledType) (queryapi.Predicate, bool, error) {
	return queryapi.True{}, true, nil
}
func (allow) Bindings(context.Context, accessapi.Principal) map[string]any {
	return map[string]any{"isOwner": func(any) bool { return true }, "hasRole": func(any, string) bool { return true },
		"inGroup": func(string) bool { return false }}
}

type records struct{ runs []flowapp.RunRecord }

func (r *records) Record(_ context.Context, rec flowapp.RunRecord) error {
	r.runs = append(r.runs, rec)
	return nil
}

// Milestone 8: a commit conflict retries the run without duplicating its
// email (spec 10.6: nothing was committed, no effect delivered).
func TestConflictRetryDoesNotDuplicateEffects(t *testing.T) {
	reg, probs := schemadomain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{}).Compile(schemadomain.Input{Types: []schemadomain.Source{{
		File: "thing.json", Data: []byte(`{"$id":"urn:app:feature:thing/v1","type":"object","properties":{"n":{"type":"integer"}}}`)}}})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	nodes := domain.NewNodeRegistry()
	for _, n := range flowapp.Builtins() {
		_ = nodes.Register(n)
	}
	flows, fprobs := domain.Compile(domain.CompileDeps{Types: reg, Exprs: expression.NewCompiler(), Nodes: nodes, Validators: jsonschema.Factory{}},
		[]domain.Source{{File: "f.json", Data: []byte(`{"flow":"bump","trigger":{"event":["updated"],"on":"urn:app:feature:thing"},
			"nodes":{"inc":{"node":"feature.update","in":{"id":"feature.id","set":{"/n":"feature.n + 1"}}},
			         "mail":{"node":"email.send","in":{"to":"'ops@example.com'","template":"'bumped'"}}}}`)}})
	if len(fprobs) > 0 {
		t.Fatal(fprobs)
	}
	content := &fakeContent{}
	rec := &records{}
	e := flowapp.NewEngine(flowapp.Deps{Flows: flows, Content: content, Authorizer: allow{}, Types: reg,
		Clock: kernel.NewFakeClock(time.Now()), IDs: kernel.UUIDv7Generator{}, Recorder: rec})
	f, _ := flows.Get("bump")
	id := kernel.UUIDv7Generator{}.NewID()
	if _, err := e.Run(context.Background(), flowapp.Request{Flow: f, Principal: accessapi.System(), FeatureID: &id, Background: true}); err != nil {
		t.Fatal(err)
	}
	if content.commits != 2 || len(content.committed) != 1 {
		t.Fatalf("commits: %d attempts, %d committed", content.commits, len(content.committed))
	}
	jobs := content.committed[0].Jobs
	if len(jobs) != 1 || jobs[0].Kind != flowapp.JobDeliverEmail {
		t.Fatalf("committed jobs: %+v", jobs)
	}
	var args map[string]any
	_ = json.Unmarshal(jobs[0].Args, &args)
	if args["delivery"] == "" {
		t.Fatal("email needs a stable delivery key")
	}
	if len(rec.runs) != 1 || rec.runs[0].Status != "succeeded" {
		t.Fatalf("run records: %+v", rec.runs)
	}
	// The run's causation link travels with the commit (spec 10.9).
	if c := content.committed[0].Causation; len(c) != 1 || c[0].FlowID != "bump" || c[0].FeatureID != id {
		t.Fatalf("causation: %+v", c)
	}
}
