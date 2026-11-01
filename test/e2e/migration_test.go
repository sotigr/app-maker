package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	manifestdomain "app-maker/internal/manifest/domain"
	"app-maker/platform/config"
)

// taskV3 turns the tracker's task type into version 3: progress is
// dropped and priority is new, with a migration from version 2.
func taskV3(t *testing.T) func(m *manifestdomain.Manifest) {
	return func(m *manifestdomain.Manifest) {
		for i, f := range m.Types {
			if f.Path != "types/task.json" {
				continue
			}
			var doc map[string]any
			if err := json.Unmarshal(f.Data, &doc); err != nil {
				t.Fatal(err)
			}
			doc["$id"] = "urn:app:feature:task/v3"
			props := doc["properties"].(map[string]any)
			delete(props, "progress")
			props["priority"] = map[string]any{"enum": []any{"high", "normal"}, "x-indexed": true}
			m.Types[i].Data, _ = json.Marshal(doc)
		}
		m.Migrations = append(m.Migrations, manifestdomain.File{Path: "migrations/0001-task-priority.json", Data: []byte(`{
			"id": "0001-task-priority", "type": "urn:app:feature:task", "from": 2, "to": 3, "batchSize": 2,
			"set": { "/priority": "feature.title contains 'urgent' ? 'high' : 'normal'", "/progress": "nil" }
		}`)})
	}
}

// Spec 12.5: after a deploy with a new type version, reads upcast old
// features at once and migrate_batch jobs rewrite them in batches.
func TestDataMigration(t *testing.T) {
	dsn := t.TempDir() + "/app.db"
	useDB := func(c *config.Config) {
		c.Repositories = map[string]config.Repository{"default": {Driver: "sqlite", DSN: dsn}}
	}
	s1 := startCfg(t, "sqlite", nil, useDB)
	admin := s1.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	var ids []string
	for _, title := range []string{"urgent fix", "write docs", "urgent deploy", "tidy up", "plan"} {
		ids = append(ids, mustOK(t, admin.create("task", p, map[string]any{"title": title, "progress": 10}), 201).str("id"))
	}
	s1.rt.Close(context.Background())

	s2 := startFull(t, "sqlite", nil, taskV3(t), useDB)
	admin = s2.admin()
	// Clients always see the new shape.
	got := mustOK(t, admin.do("GET", "/v1/features/task/"+ids[0], nil), 200)
	data := got.Body["data"].(map[string]any)
	if data["priority"] != "high" || data["progress"] != nil {
		t.Fatalf("upcast read: %s", got.Raw)
	}
	eventually(t, "migration finished", func() bool {
		r := admin.do("POST", "/v1/query", map[string]any{"type": "migration"})
		return strings.Contains(r.Raw, `"state":"done"`) && strings.Contains(r.Raw, `"migrated":5`)
	})
	// Stored features now match the index-backed query too.
	q := mustOK(t, admin.do("POST", "/v1/query", map[string]any{"type": "task", "where": map[string]any{"field": "/priority", "op": "eq", "value": "high"}}), 200)
	if n := len(q.Body["items"].([]any)); n != 2 {
		t.Fatalf("high-priority tasks after migration: %d %s", n, q.Raw)
	}
	audit := mustOK(t, admin.do("GET", "/v1/audit?action=migrated", nil), 200)
	if n := len(audit.Body["items"].([]any)); n != 5 {
		t.Fatalf("migrated audit rows: %d", n)
	}
	// Writes keep working on migrated features.
	v := mustOK(t, admin.do("GET", "/v1/features/task/"+ids[1], nil), 200).Header.Get("ETag")
	mustOK(t, admin.do("PATCH", "/v1/features/task/"+ids[1], map[string]any{"priority": "high"}, "If-Match", v), 200)

	// A restart does not migrate again.
	s2.rt.Close(context.Background())
	s3 := startFull(t, "sqlite", nil, taskV3(t), useDB)
	r := s3.admin().do("POST", "/v1/query", map[string]any{"type": "migration"})
	if strings.Count(r.Raw, `"key"`) != 1 {
		t.Fatalf("migration records: %s", r.Raw)
	}
}
