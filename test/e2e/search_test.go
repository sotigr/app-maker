package e2e

import (
	"context"
	"strings"
	"testing"

	"organon/platform/config"
)

// Milestone 9: public search never matches owner or private text, and no
// search returns a feature the caller cannot read (spec 7.5).
func TestSearchTiers(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		// The admin owns project P; vera views it, sam sees nothing.
		// Aggregate recomputes may bump the project's version: retry conflicts.
		eventually(t, "project update", func() bool {
			r := w.admin.do("PATCH", "/v1/features/project/"+w.project,
				map[string]any{"description": "public zebra roadmap", "notes": "secret giraffe plan"},
				"If-Match", mustOK(t, w.admin.do("GET", "/v1/features/project/"+w.project, nil), 200).Header.Get("ETag"))
			if r.Status != 200 && r.Status != 409 {
				t.Fatalf("project update: %s", r.Raw)
			}
			return r.Status == 200
		})
		if _, err := w.s.rt.Content.Execute(context.Background(), systemFixture(), updateOp(w.task, w.taskVersionInt(t, w.task), map[string]any{"internalScore": 0.3})); err != nil {
			t.Fatal(err)
		}
		search := func(c *client, q string) resp {
			return mustOK(t, c.do("GET", "/v1/search?q="+q, nil), 200)
		}
		count := func(r resp) int { return len(r.Body["items"].([]any)) }
		eventually(t, "indexed", func() bool { return count(search(w.admin, "zebra")) == 1 })
		if r := search(w.viewer, "zebra"); count(r) != 1 || !strings.Contains(r.Raw, `\u003cmark\u003ezebra`) {
			t.Fatalf("viewer public search: %s", r.Raw)
		}
		if r := search(w.viewer, "giraffe"); count(r) != 0 || strings.Contains(r.Raw, "giraffe") {
			t.Fatalf("owner text leaked to a viewer: %s", r.Raw)
		}
		if r := search(w.admin, "giraffe"); count(r) != 1 {
			t.Fatalf("owner search: %s", r.Raw)
		}
		if r := search(w.stranger, "zebra"); count(r) != 0 {
			t.Fatalf("stranger sees a feature it cannot read: %s", r.Raw)
		}
		// Private fields are never indexed.
		if r := search(w.admin, "0.3"); strings.Contains(r.Raw, "internalScore") {
			t.Fatalf("private text searchable: %s", r.Raw)
		}
	})
}

// Spec 7.5: the in-memory index is rebuilt from SQLite after a restart.
func TestSearchReindexAfterRestart(t *testing.T) {
	dsn := t.TempDir() + "/app.db"
	useDB := func(c *config.Config) {
		c.Repositories = map[string]config.Repository{"default": {Driver: "sqlite", DSN: dsn}}
	}
	s1 := startCfg(t, "sqlite", nil, useDB)
	admin := s1.admin()
	mustOK(t, admin.create("project", "", map[string]any{"title": "Zebra migration"}), 201)
	eventually(t, "indexed", func() bool {
		return strings.Contains(admin.do("GET", "/v1/search?q=zebra", nil).Raw, "Zebra migration")
	})
	s1.rt.Close(context.Background())

	s2 := startCfg(t, "sqlite", nil, useDB)
	admin2 := s2.admin()
	eventually(t, "reindexed after restart", func() bool {
		return strings.Contains(admin2.do("GET", "/v1/search?q=zebra", nil).Raw, "Zebra migration")
	})
}
