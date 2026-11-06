// Package sqlite is the SQLite FeatureRepository (spec 5.3): the
// PostgreSQL layout with JSON TEXT documents, a feature_ancestors table in
// place of the ancestors array, expression indexes for x-indexed fields,
// and jobs inserted with River in the commit transaction.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"organon/internal/content/domain"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// MaxBatchSize is the largest batch accepted.
const MaxBatchSize = 1000

// TxJobInserter inserts jobs inside the commit transaction (River).
type TxJobInserter interface {
	InsertTx(ctx context.Context, tx *sql.Tx, jobs []kernel.JobRequest) error
}

// Repository is the SQLite FeatureRepository. Tables are prefixed with the
// owning context (spec 15.4).
type Repository struct {
	db   *sql.DB
	jobs TxJobInserter
	// indexes maps type base + "\x00" + field to its expression index.
	indexes map[string]string
}

var (
	_ domain.FeatureRepository = (*Repository)(nil)
	_ domain.AuditStore        = (*Repository)(nil)
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS content_features (
  id         TEXT PRIMARY KEY,
  type_id    TEXT NOT NULL,
  type_base  TEXT NOT NULL,
  parent_id  TEXT,
  owner_id   TEXT,
  version    INTEGER NOT NULL,
  data       TEXT NOT NULL,
  kinds      TEXT NOT NULL,
  unique_key TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  deleted_at TEXT
);
CREATE INDEX IF NOT EXISTS content_features_children ON content_features (parent_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS content_features_type ON content_features (type_base) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS content_features_owner ON content_features (owner_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS content_features_unique_name ON content_features (parent_id, unique_key)
  WHERE deleted_at IS NULL AND unique_key IS NOT NULL;
CREATE TABLE IF NOT EXISTS content_feature_ancestors (
  feature_id  TEXT NOT NULL,
  ancestor_id TEXT NOT NULL,
  depth       INTEGER NOT NULL,
  PRIMARY KEY (feature_id, ancestor_id)
);
CREATE INDEX IF NOT EXISTS content_feature_ancestors_anc ON content_feature_ancestors (ancestor_id);
CREATE TABLE IF NOT EXISTS content_audit (
  id     TEXT PRIMARY KEY,
  at     TEXT NOT NULL,
  actor  TEXT NOT NULL,
  action TEXT NOT NULL,
  target TEXT,
  record TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS content_audit_at ON content_audit (at);
CREATE TABLE IF NOT EXISTS content_sequences (
  series TEXT PRIMARY KEY,
  last   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS content_audit_actor ON content_audit (actor, id);
CREATE INDEX IF NOT EXISTS content_audit_target ON content_audit (target, id);
`

// New creates the tables if needed. jobs may be nil (no job insertion).
func New(ctx context.Context, db *sql.DB, jobs TxJobInserter) (*Repository, error) {
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return nil, fmt.Errorf("content schema: %w", err)
	}
	return &Repository{db: db, jobs: jobs, indexes: map[string]string{}}, nil
}

// EnsureIndexes builds an expression index for every x-indexed scalar
// field of every type (spec 5.3).
func (r *Repository) EnsureIndexes(ctx context.Context, types []*schemaapi.CompiledType) error {
	for _, ct := range types {
		for _, f := range ct.TopFields() {
			if !f.Indexed || !f.Kind.IsScalar() || !safeName.MatchString(f.Path[1:]) {
				continue
			}
			// (value, id) serves both filters and ORDER BY value, id.
			name := "content_ix_" + sanitize(string(ct.Base())) + "_" + f.Path[1:]
			stmt := fmt.Sprintf(`CREATE INDEX IF NOT EXISTS "%s" ON content_features (json_extract(data, '$."%s"'), id) WHERE type_base = '%s' AND deleted_at IS NULL`,
				name, f.Path[1:], strings.ReplaceAll(string(ct.Base()), "'", "''"))
			if _, err := r.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("index %s: %w", name, err)
			}
			r.indexes[string(ct.Base())+"\x00"+f.Path] = name
		}
	}
	return nil
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}

// Capabilities implements domain.FeatureRepository.
func (r *Repository) Capabilities() domain.Capabilities {
	return domain.Capabilities{Transactions: true, TransactionalJobs: r.jobs != nil, SecondaryIndexes: true, Sort: true,
		RangeFilters: true, FullText: false, SubtreeQueries: true, MaxBatchSize: MaxBatchSize}
}

const selectCols = `f.id, f.type_id, f.parent_id, f.owner_id, f.version, f.data, f.kinds, f.created_at, f.updated_at, f.deleted_at`

type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
}

// load runs a SELECT of selectCols and attaches ancestors.
func (r *Repository) load(ctx context.Context, q querier, query string, args ...any) ([]domain.Snapshot, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite query: %w", err)
	}
	var out []domain.Snapshot
	for rows.Next() {
		var s domain.Snapshot
		var id, typeID, created, updated string
		var parent, owner, deleted sql.NullString
		var data, kinds []byte
		if err := rows.Scan(&id, &typeID, &parent, &owner, &s.Version, &data, &kinds, &created, &updated, &deleted); err != nil {
			_ = rows.Close()
			return nil, err
		}
		s.ID = kernel.MustParseID(id)
		s.TypeID = schemaapi.TypeID(typeID)
		if parent.Valid {
			p := kernel.MustParseID(parent.String)
			s.ParentID = &p
		}
		if owner.Valid {
			o := kernel.MustParseID(owner.String)
			s.OwnerID = &o
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		if deleted.Valid {
			t, _ := time.Parse(time.RFC3339Nano, deleted.String)
			s.DeletedAt = &t
		}
		if s.Fields, err = decodeFields(data, kinds); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("feature %s: %w", id, err)
		}
		out = append(out, s)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := r.attachAncestors(ctx, q, out); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) attachAncestors(ctx context.Context, q querier, snaps []domain.Snapshot) error {
	if len(snaps) == 0 {
		return nil
	}
	idx := map[string]int{}
	ph := make([]string, len(snaps))
	args := make([]any, len(snaps))
	for i, s := range snaps {
		idx[s.ID.String()] = i
		ph[i] = "?"
		args[i] = s.ID.String()
	}
	rows, err := q.QueryContext(ctx, `SELECT feature_id, ancestor_id FROM content_feature_ancestors WHERE feature_id IN (`+
		strings.Join(ph, ",")+`) ORDER BY feature_id, depth`, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f, a string
		if err := rows.Scan(&f, &a); err != nil {
			return err
		}
		i := idx[f]
		snaps[i].Ancestors = append(snaps[i].Ancestors, kernel.MustParseID(a))
	}
	return rows.Err()
}

func rehydrate(snaps []domain.Snapshot) []*domain.Feature {
	out := make([]*domain.Feature, len(snaps))
	for i, s := range snaps {
		out[i] = domain.Rehydrate(s)
	}
	return out
}

// Get implements domain.FeatureRepository.
func (r *Repository) Get(ctx context.Context, id kernel.ID) (*domain.Feature, error) {
	snaps, err := r.load(ctx, r.db, `SELECT `+selectCols+` FROM content_features f WHERE f.id = ? AND f.deleted_at IS NULL`, id.String())
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, kernel.ErrNotFound
	}
	return domain.Rehydrate(snaps[0]), nil
}

// GetMany implements domain.FeatureRepository.
func (r *Repository) GetMany(ctx context.Context, ids []kernel.ID) ([]*domain.Feature, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		ph[i], args[i] = "?", id.String()
	}
	snaps, err := r.load(ctx, r.db, `SELECT `+selectCols+` FROM content_features f WHERE f.deleted_at IS NULL AND f.id IN (`+strings.Join(ph, ",")+`)`, args...)
	return rehydrate(snaps), err
}

func typeFilter(b *builder, types []schemaapi.TypeID) string {
	if len(types) == 0 {
		return "1=1"
	}
	if len(types) == 1 {
		// Inlined so partial expression indexes apply; likely() keeps the
		// planner from preferring the generic type index over them.
		return "likely(f.type_base = '" + strings.ReplaceAll(string(types[0].Base()), "'", "''") + "')"
	}
	ph := make([]string, len(types))
	for i, t := range types {
		ph[i] = b.arg(string(t.Base()))
	}
	return "f.type_base IN (" + strings.Join(ph, ",") + ")"
}

func limitOf(n int) int {
	switch {
	case n <= 0:
		return queryapi.DefaultLimit
	case n > queryapi.MaxLimit:
		return queryapi.MaxLimit
	}
	return n
}

func (r *Repository) page(ctx context.Context, b *builder, from, where, order string, limit int, keys []queryapi.SortKey) (domain.Page[*domain.Feature], error) {
	query := `SELECT ` + selectCols + ` FROM ` + from + ` WHERE ` + where + ` ORDER BY ` + order + fmt.Sprintf(` LIMIT %d`, limit+1)
	snaps, err := r.load(ctx, r.db, query, b.args...)
	if err != nil {
		return domain.Page[*domain.Feature]{}, err
	}
	p := domain.Page[*domain.Feature]{}
	if len(snaps) > limit {
		last := snaps[limit-1]
		p.Next = &queryapi.Cursor{Values: domain.SortValues(last, keys), ID: last.ID}
		snaps = snaps[:limit]
	}
	p.Items = rehydrate(snaps)
	return p, nil
}

// Children implements domain.FeatureRepository.
func (r *Repository) Children(ctx context.Context, parent kernel.ID, req domain.PageRequest) (domain.Page[*domain.Feature], error) {
	b := &builder{}
	where := "f.deleted_at IS NULL AND f.parent_id = " + b.arg(parent.String()) + " AND " + typeFilter(b, req.Types)
	if req.After != nil {
		where += " AND f.id > " + b.arg(req.After.ID.String())
	}
	return r.page(ctx, b, "content_features f", where, "f.id ASC", limitOf(req.Limit), nil)
}

// Descendants implements domain.FeatureRepository.
func (r *Repository) Descendants(ctx context.Context, id kernel.ID, limit int) ([]*domain.Feature, error) {
	snaps, err := r.load(ctx, r.db, `SELECT `+selectCols+` FROM content_features f JOIN content_feature_ancestors a ON a.feature_id = f.id
		WHERE a.ancestor_id = ? AND f.deleted_at IS NULL LIMIT ?`, id.String(), limit+1)
	if err != nil {
		return nil, err
	}
	if len(snaps) > limit {
		return nil, &kernel.ConstraintError{Code: "subtree_too_large", ID: id}
	}
	return rehydrate(snaps), nil
}

// Count returns the number of live features of the types (cost estimate).
func (r *Repository) Count(ctx context.Context, types []schemaapi.TypeID) (int, error) {
	b := &builder{}
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM content_features f WHERE f.deleted_at IS NULL AND `+typeFilter(b, types), b.args...).Scan(&n)
	return n, err
}

// Query implements domain.FeatureRepository.
func (r *Repository) Query(ctx context.Context, q queryapi.Plan) (domain.Page[*domain.Feature], error) {
	b, from, where, order, err := r.compile(q)
	if err != nil {
		return domain.Page[*domain.Feature]{}, err
	}
	return r.page(ctx, b, from, where, order, limitOf(q.Limit), q.Sort)
}

func (r *Repository) compile(q queryapi.Plan) (b *builder, from, where, order string, err error) {
	b = &builder{}
	conds := []string{typeFilter(b, q.Types)}
	if !q.IncludeDeleted {
		conds = append(conds, "f.deleted_at IS NULL")
	}
	switch q.Scope.Kind {
	case queryapi.ScopeChildrenOf:
		conds = append(conds, "f.parent_id = "+b.arg(q.Scope.Of.String()))
	case queryapi.ScopeDescendants:
		conds = append(conds, "EXISTS (SELECT 1 FROM content_feature_ancestors a WHERE a.feature_id = f.id AND a.ancestor_id = "+b.arg(q.Scope.Of.String())+")")
	}
	w, err := b.pred(q.Where)
	if err != nil {
		return nil, "", "", "", err
	}
	conds = append(conds, "("+w+")")
	if q.After != nil {
		conds = append(conds, "("+b.after(q.Sort, q.After)+")")
	}
	order = b.orderBy(q.Sort)
	from = "content_features f"
	// Sorting by an indexed field walks its index instead of sorting.
	if !q.IncludeDeleted && len(q.Types) == 1 && len(q.Sort) > 1 && q.Sort[0].Path != "" {
		if ix, ok := r.indexes[string(q.Types[0].Base())+"\x00"+q.Sort[0].Path]; ok {
			from = `content_features f INDEXED BY "` + ix + `"`
		}
	}
	return b, from, strings.Join(conds, " AND "), order, nil
}

// Explain returns SQLite's query plan for a query plan (spec milestone 4:
// indexed queries are checked with EXPLAIN in tests).
func (r *Repository) Explain(ctx context.Context, q queryapi.Plan) ([]string, error) {
	b, from, where, order, err := r.compile(q)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT f.id FROM `+from+` WHERE `+where+` ORDER BY `+order+` LIMIT 50`, b.args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return nil, err
		}
		out = append(out, detail)
	}
	return out, rows.Err()
}

type current struct {
	version kernel.Version
	deleted bool
	typeID  string
	parent  sql.NullString
}

// Apply implements domain.FeatureRepository (spec 5.2): one IMMEDIATE
// transaction with version checks on every mutation, then River jobs.
func (r *Repository) Apply(ctx context.Context, b domain.Batch) (domain.CommitResult, error) {
	if len(b.Mutations) > MaxBatchSize {
		return domain.CommitResult{}, &kernel.UnsupportedError{Capability: "MaxBatchSize"}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.CommitResult{}, err
	}
	if b.Finalize != nil {
		// Sequence numbers are drawn in this transaction (ADR 0021).
		if b, err = b.Finalize(func(series string) (int64, error) {
			var n int64
			err := tx.QueryRowContext(ctx, `INSERT INTO content_sequences (series, last) VALUES (?, 1)
				ON CONFLICT (series) DO UPDATE SET last = last + 1 RETURNING last`, series).Scan(&n)
			return n, err
		}); err != nil {
			_ = tx.Rollback()
			return domain.CommitResult{}, err
		}
	}
	res, err := r.apply(ctx, tx, b)
	if err == nil {
		err = insertAudit(ctx, tx, b.Audit)
	}
	if err != nil {
		_ = tx.Rollback()
		return domain.CommitResult{}, err
	}
	if r.jobs != nil {
		if err := r.jobs.InsertTx(ctx, tx, b.Jobs); err != nil {
			_ = tx.Rollback()
			return domain.CommitResult{}, fmt.Errorf("insert jobs: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return domain.CommitResult{}, err
	}
	return res, nil
}

func (r *Repository) apply(ctx context.Context, tx *sql.Tx, b domain.Batch) (domain.CommitResult, error) {
	res := domain.CommitResult{Versions: map[kernel.ID]kernel.Version{}}
	// Current versions of every existing feature in the batch.
	var ids []any
	var ph []string
	for _, m := range b.Mutations {
		ids = append(ids, m.Feature.ID.String())
		ph = append(ph, "?")
		if m.Feature.ParentID != nil {
			ids = append(ids, m.Feature.ParentID.String())
			ph = append(ph, "?")
		}
	}
	cur := map[kernel.ID]current{}
	if len(ids) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT id, version, deleted_at IS NOT NULL, type_id, parent_id FROM content_features WHERE id IN (`+strings.Join(ph, ",")+`)`, ids...)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var id string
			var c current
			if err := rows.Scan(&id, &c.version, &c.deleted, &c.typeID, &c.parent); err != nil {
				_ = rows.Close()
				return res, err
			}
			cur[kernel.MustParseID(id)] = c
		}
		if err := rows.Close(); err != nil {
			return res, err
		}
	}
	var conflicts []kernel.VersionConflict
	created := map[kernel.ID]bool{}
	for _, m := range b.Mutations {
		s := m.Feature
		c, exists := cur[s.ID]
		if m.Kind == domain.MutCreate {
			if exists || created[s.ID] {
				return res, &kernel.ConstraintError{Path: "/id", Code: "duplicate_id", ID: s.ID}
			}
			if s.Version != 1 {
				return res, &kernel.ConstraintError{Path: "/version", Code: "invalid_version", ID: s.ID}
			}
			created[s.ID] = true
			continue
		}
		if !exists || (m.Kind != domain.MutPurge && c.deleted) {
			return res, kernel.NewError(kernel.CodeNotFound, "feature %s not found", s.ID)
		}
		if c.version != m.Expected {
			conflicts = append(conflicts, kernel.VersionConflict{ID: s.ID, Current: c.version})
			continue
		}
		if cur := schemaapi.TypeID(c.typeID); s.TypeID.Base() != cur.Base() || s.TypeID.Version() < cur.Version() {
			// Only migrations move a feature, to a newer version (spec 12.5).
			return res, &kernel.ConstraintError{Path: "/type", Code: "type_changed", ID: s.ID}
		}
		if m.Kind != domain.MutPurge && s.Version != m.Expected+1 {
			return res, &kernel.ConstraintError{Path: "/version", Code: "invalid_version", ID: s.ID}
		}
	}
	if len(conflicts) > 0 {
		return res, &kernel.VersionConflictError{Conflicts: conflicts}
	}
	for _, m := range b.Mutations {
		if err := r.write(ctx, tx, m); err != nil {
			return res, err
		}
		res.Versions[m.Feature.ID] = m.Feature.Version
	}
	return res, nil
}

func nullable(id *kernel.ID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func (r *Repository) ancestorsOf(ctx context.Context, tx *sql.Tx, parent *kernel.ID) ([]kernel.ID, error) {
	if parent == nil {
		return nil, nil
	}
	var deleted bool
	err := tx.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM content_features WHERE id = ?`, parent.String()).Scan(&deleted)
	if errors.Is(err, sql.ErrNoRows) || deleted {
		return nil, &kernel.ConstraintError{Path: "/parent", Code: "parent_not_found"}
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT ancestor_id FROM content_feature_ancestors WHERE feature_id = ? ORDER BY depth`, parent.String())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []kernel.ID
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, kernel.MustParseID(a))
	}
	return append(out, *parent), rows.Err()
}

func (r *Repository) setAncestors(ctx context.Context, tx *sql.Tx, id kernel.ID, anc []kernel.ID) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_feature_ancestors WHERE feature_id = ?`, id.String()); err != nil {
		return err
	}
	for i, a := range anc {
		if _, err := tx.ExecContext(ctx, `INSERT INTO content_feature_ancestors (feature_id, ancestor_id, depth) VALUES (?, ?, ?)`, id.String(), a.String(), i); err != nil {
			return err
		}
	}
	return nil
}

func uniqueErr(err error, id kernel.ID) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		if strings.Contains(err.Error(), "unique_key") {
			return &kernel.ConstraintError{Path: "/uniqueName", Code: "duplicate_name", ID: id}
		}
		return &kernel.ConstraintError{Path: "/id", Code: "duplicate_id", ID: id}
	}
	return err
}

func (r *Repository) write(ctx context.Context, tx *sql.Tx, m domain.Mutation) error {
	s := m.Feature
	id := s.ID.String()
	var deleted any
	if s.DeletedAt != nil {
		deleted = ts(*s.DeletedAt)
	}
	switch m.Kind {
	case domain.MutPurge:
		if _, err := tx.ExecContext(ctx, `DELETE FROM content_features WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM content_feature_ancestors WHERE feature_id = ?`, id)
		return err
	case domain.MutCreate:
		anc, err := r.ancestorsOf(ctx, tx, s.ParentID)
		if err != nil {
			var ce *kernel.ConstraintError
			if errors.As(err, &ce) {
				ce.ID = s.ID
			}
			return err
		}
		data, kinds, err := encodeFields(s.Fields)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO content_features (id, type_id, type_base, parent_id, owner_id, version, data, kinds, unique_key, created_at, updated_at, deleted_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, string(s.TypeID), string(s.TypeID.Base()), nullable(s.ParentID), nullable(s.OwnerID),
			s.Version, string(data), string(kinds), nullString(m.UniqueKey), ts(s.CreatedAt), ts(s.UpdatedAt), deleted)
		if err != nil {
			return uniqueErr(err, s.ID)
		}
		return r.setAncestors(ctx, tx, s.ID, anc)
	}
	data, kinds, err := encodeFields(s.Fields)
	if err != nil {
		return err
	}
	uniq := nullString(m.UniqueKey)
	if m.Kind == domain.MutDelete {
		_, err = tx.ExecContext(ctx, `UPDATE content_features SET version = ?, data = ?, kinds = ?, owner_id = ?, updated_at = ?, deleted_at = ?
			WHERE id = ? AND version = ?`, s.Version, string(data), string(kinds), nullable(s.OwnerID), ts(s.UpdatedAt), deleted, id, m.Expected)
		return err
	}
	if m.Kind == domain.MutMove {
		if s.ParentID == nil {
			return &kernel.ConstraintError{Path: "/parent", Code: "parent_not_found", ID: s.ID}
		}
		anc, err := r.ancestorsOf(ctx, tx, s.ParentID)
		if err != nil {
			return err
		}
		if *s.ParentID == s.ID || slices.Contains(anc, s.ID) {
			return &kernel.ConstraintError{Path: "/parent", Code: "cycle", ID: s.ID}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE content_features SET parent_id = ?, version = ?, data = ?, kinds = ?, owner_id = ?, unique_key = ?, updated_at = ?
			WHERE id = ? AND version = ?`, s.ParentID.String(), s.Version, string(data), string(kinds), nullable(s.OwnerID), uniq, ts(s.UpdatedAt), id, m.Expected); err != nil {
			return uniqueErr(err, s.ID)
		}
		return r.rewriteSubtree(ctx, tx, s.ID, anc)
	}
	_, err = tx.ExecContext(ctx, `UPDATE content_features SET type_id = ?, version = ?, data = ?, kinds = ?, owner_id = ?, unique_key = ?, updated_at = ?
		WHERE id = ? AND version = ?`, string(s.TypeID), s.Version, string(data), string(kinds), nullable(s.OwnerID), uniq, ts(s.UpdatedAt), id, m.Expected)
	return uniqueErr(err, s.ID)
}

// rewriteSubtree rewrites the materialized path of a moved feature and all
// of its descendants in the same transaction (spec 5.3).
func (r *Repository) rewriteSubtree(ctx context.Context, tx *sql.Tx, id kernel.ID, anc []kernel.ID) error {
	rows, err := tx.QueryContext(ctx, `SELECT feature_id, depth FROM content_feature_ancestors WHERE ancestor_id = ?`, id.String())
	if err != nil {
		return err
	}
	type desc struct {
		id    string
		depth int
	}
	var ds []desc
	for rows.Next() {
		var d desc
		if err := rows.Scan(&d.id, &d.depth); err != nil {
			_ = rows.Close()
			return err
		}
		ds = append(ds, d)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ds) > domain.MaxMoveSize {
		return &kernel.ConstraintError{Code: "subtree_too_large", ID: id}
	}
	if err := r.setAncestors(ctx, tx, id, anc); err != nil {
		return err
	}
	base := append(slices.Clone(anc), id)
	for _, d := range ds {
		// Keep the part of the path below the moved feature.
		rows, err := tx.QueryContext(ctx, `SELECT ancestor_id FROM content_feature_ancestors WHERE feature_id = ? AND depth > ? ORDER BY depth`, d.id, d.depth)
		if err != nil {
			return err
		}
		tail := []kernel.ID{}
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				_ = rows.Close()
				return err
			}
			tail = append(tail, kernel.MustParseID(a))
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := r.setAncestors(ctx, tx, kernel.MustParseID(d.id), append(slices.Clone(base), tail...)); err != nil {
			return err
		}
	}
	return nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertAudit(ctx context.Context, db execer, recs []kernel.AuditRecord) error {
	for _, a := range recs {
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		var target any
		if a.Target != nil {
			target = a.Target.String()
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO content_audit (id, at, actor, action, target, record) VALUES (?, ?, ?, ?, ?, ?)`,
			a.ID.String(), a.At.UTC().Format(time.RFC3339Nano), a.Actor.String(), a.Action, target, string(raw)); err != nil {
			return fmt.Errorf("audit: %w", err)
		}
	}
	return nil
}

// AppendAudit implements domain.AuditStore.
func (r *Repository) AppendAudit(ctx context.Context, recs []kernel.AuditRecord) error {
	return insertAudit(ctx, r.db, recs)
}

// Audit implements domain.AuditStore.
func (r *Repository) Audit(ctx context.Context, f kernel.AuditFilter) ([]kernel.AuditRecord, error) {
	where, args := []string{"1=1"}, []any{}
	if f.Actor != nil {
		where, args = append(where, "actor = ?"), append(args, f.Actor.String())
	}
	if f.Target != nil {
		where, args = append(where, "target = ?"), append(args, f.Target.String())
	}
	if f.Action != "" {
		where, args = append(where, "action = ?"), append(args, f.Action)
	}
	if !f.Since.IsZero() {
		where, args = append(where, "at >= ?"), append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	if f.Before != nil {
		where, args = append(where, "id < ?"), append(args, f.Before.String())
	}
	rows, err := r.db.QueryContext(ctx, `SELECT record FROM content_audit WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`,
		append(args, f.Limit)...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []kernel.AuditRecord
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var a kernel.AuditRecord
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PurgeAudit implements domain.AuditStore.
func (r *Repository) PurgeAudit(ctx context.Context, before time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM content_audit WHERE at < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
