package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// SQLIdempotency stores idempotent responses in a SQL table so they
// survive restarts and are shared by replicas (spec 6.1, 14.10).
type SQLIdempotency struct{ db *sql.DB }

// NewSQLIdempotency creates the table if needed.
func NewSQLIdempotency(ctx context.Context, db *sql.DB) (*SQLIdempotency, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS platform_idempotency (
		key TEXT PRIMARY KEY, status INTEGER NOT NULL, header TEXT NOT NULL, body BLOB NOT NULL, expires_at TEXT NOT NULL)`)
	if err != nil {
		return nil, err
	}
	return &SQLIdempotency{db: db}, nil
}

// Get implements IdempotencyStore.
func (s *SQLIdempotency) Get(ctx context.Context, key string) (StoredResponse, bool, error) {
	var r StoredResponse
	var header, expires string
	err := s.db.QueryRowContext(ctx, `SELECT status, header, body, expires_at FROM platform_idempotency WHERE key = ?`, key).
		Scan(&r.Status, &header, &r.Body, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	r.Expires, _ = time.Parse(time.RFC3339Nano, expires)
	if time.Now().After(r.Expires) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM platform_idempotency WHERE key = ?`, key)
		return r, false, nil
	}
	r.Header = http.Header{}
	_ = json.Unmarshal([]byte(header), &r.Header)
	return r, true, nil
}

// Put implements IdempotencyStore.
func (s *SQLIdempotency) Put(ctx context.Context, key string, r StoredResponse) error {
	h, _ := json.Marshal(r.Header)
	_, err := s.db.ExecContext(ctx, `INSERT INTO platform_idempotency (key, status, header, body, expires_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO NOTHING`, key, r.Status, string(h), r.Body, r.Expires.UTC().Format(time.RFC3339Nano))
	return err
}
