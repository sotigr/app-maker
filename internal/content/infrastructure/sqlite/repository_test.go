package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"app-maker/internal/content/infrastructure/sqlite"
	"app-maker/internal/content/repotest"
	"app-maker/internal/kernel"
	"app-maker/platform/jobs"
	"app-maker/platform/sqlitedb"
)

// Milestone 4: the SQLite repository passes the conformance suite except
// the multi-replica tests (spec 5.3).
func TestConformance(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Harness {
		ctx := context.Background()
		db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "test.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		rj, err := jobs.NewRiverSQLite(ctx, db.DB, jobs.NewRegistry(), jobs.RiverConfig{InsertOnly: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		repo, err := sqlite.New(ctx, db.DB, rj)
		if err != nil {
			t.Fatal(err)
		}
		return repotest.Harness{Repo: repo, SingleReplica: true, Jobs: func() []kernel.JobRequest {
			js, err := rj.Committed(ctx)
			if err != nil {
				t.Fatal(err)
			}
			return js
		}}
	})
}

// Spec 5.3: a second replica cannot open the same database file.
func TestExclusiveLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := sqlitedb.Open(path); err == nil {
		t.Fatal("second open of a locked database succeeded")
	}
}
