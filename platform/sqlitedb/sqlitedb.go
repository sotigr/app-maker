// Package sqlitedb opens the SQLite database shared by the SQLite
// adapters (features, credentials, jobs). SQLite allows one writer on one
// machine, so the file is locked exclusively and a second replica refuses
// to start against it (spec 5.3).
package sqlitedb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	_ "modernc.org/sqlite" // database/sql driver "sqlite", pure Go
)

// DB is an open database plus its replica lock.
type DB struct {
	*sql.DB
	Path string
	lock *os.File
}

// Open opens path (":memory:" for an in-memory database) with WAL,
// immediate transactions, and a single connection.
func Open(path string) (*DB, error) {
	var lock *os.File
	dsn := "file::memory:?cache=shared&_pragma=foreign_keys(1)&_txlock=immediate"
	if path != ":memory:" {
		if dir := filepath.Dir(path); dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("sqlite database %s is in use by another process: SQLite deployments run exactly one replica", path)
		}
		lock = f
		dsn = "file:" + path + "?" + strings.Join([]string{
			"_pragma=journal_mode(WAL)", "_pragma=busy_timeout(5000)", "_pragma=foreign_keys(1)",
			"_pragma=synchronous(NORMAL)", "_txlock=immediate",
		}, "&")
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer at a time; River and the repositories share the pool, so
	// nothing may use the pool while a transaction is open.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &DB{DB: db, Path: path, lock: lock}, nil
}

// Close closes the database and releases the replica lock.
func (d *DB) Close() error {
	err := d.DB.Close()
	if d.lock != nil {
		_ = syscall.Flock(int(d.lock.Fd()), syscall.LOCK_UN)
		_ = d.lock.Close()
	}
	return err
}
