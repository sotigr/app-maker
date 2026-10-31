package sqlite_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"app-maker/internal/content/infrastructure/sqlite"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	"app-maker/internal/runtime/domain/expression"
	schemaapi "app-maker/internal/schema/api"
	schemadomain "app-maker/internal/schema/domain"
	"app-maker/internal/schema/infrastructure/jsonschema"
	"app-maker/platform/sqlitedb"
)

// Milestone 4: queries on x-indexed fields use the expression indexes
// (checked with EXPLAIN QUERY PLAN).
func TestIndexedQueriesUseIndexes(t *testing.T) {
	ctx := context.Background()
	reg, probs := schemadomain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{}).Compile(schemadomain.Input{Types: []schemadomain.Source{{
		File: "task.json",
		Data: []byte(`{"$id":"urn:app:feature:task/v1","type":"object","properties":{
			"status":{"enum":["todo","done"],"x-indexed":true},
			"dueAt":{"type":"string","format":"date-time","x-indexed":true},
			"note":{"type":"string"}}}`),
	}}})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo, err := sqlite.New(ctx, db.DB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureIndexes(ctx, reg.All()); err != nil {
		t.Fatal(err)
	}
	task, _ := reg.ByName("task")
	plans := map[string]queryapi.Plan{
		"eq":    {Types: []schemaapi.TypeID{task.ID}, Where: queryapi.Compare{Path: "/status", Op: queryapi.OpEq, Value: kernel.Enum("todo")}, Sort: []queryapi.SortKey{{}}},
		"range": {Types: []schemaapi.TypeID{task.ID}, Where: queryapi.Compare{Path: "/dueAt", Op: queryapi.OpLt, Value: kernel.NewTime(time.Now())}, Sort: []queryapi.SortKey{{}}},
		"sort":  {Types: []schemaapi.TypeID{task.ID}, Sort: []queryapi.SortKey{{Path: "/dueAt"}, {}}},
		"sort desc after cursor": {Types: []schemaapi.TypeID{task.ID}, Sort: []queryapi.SortKey{{Path: "/dueAt", Desc: true}, {Desc: true}},
			After: &queryapi.Cursor{Values: []kernel.Value{kernel.NewTime(time.Now())}, ID: kernel.UUIDv7Generator{}.NewID()}},
	}
	for name, plan := range plans {
		got, err := repo.Explain(ctx, plan)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		joined := strings.Join(got, " | ")
		if !strings.Contains(joined, "content_ix_") {
			t.Errorf("%s does not use an expression index: %v", name, got)
		}
		if strings.HasPrefix(name, "sort") && strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("%s sorts instead of walking the index: %v", name, got)
		}
	}
}
