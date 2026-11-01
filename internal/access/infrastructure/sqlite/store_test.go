package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"app-maker/internal/access/infrastructure/storetest"
	"app-maker/platform/sqlitedb"
)

func TestStoreConformance(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(context.Background(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	storetest.Run(t, s)
}
