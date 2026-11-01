package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	manifestdomain "organon/internal/manifest/domain"
	manifestfs "organon/internal/manifest/infrastructure/fs"
	schemadomain "organon/internal/schema/domain"
	"organon/platform/httpserver"
)

// TypeDiff is one type's change in a plan.
type TypeDiff struct {
	Type    string                `json:"type"`
	Status  string                `json:"status"` // added, removed, changed
	Changes []schemadomain.Change `json:"changes,omitempty"`
}

// NamedDiff is a changed policy or flow.
type NamedDiff struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Privileged bool   `json:"privileged,omitempty"`
}

// Plan is the result of organon plan (spec 12.4).
type Plan struct {
	Hash       string      `json:"hash"`
	From       string      `json:"from"`
	To         string      `json:"to"`
	Types      []TypeDiff  `json:"types"`
	Policies   []NamedDiff `json:"policies"`
	Flows      []NamedDiff `json:"flows"`
	Privileged []string    `json:"privilegedFlows"`
	Exposures  []string    `json:"newPublicFields"`
	Indexes    []string    `json:"indexBuilds"`
	Migrations []string    `json:"migrations"`
	Breaking   bool        `json:"breaking"`
}

// MakePlan compares the deployed and the new compiled manifest.
func MakePlan(cur, next *Compiled) Plan {
	p := Plan{From: cur.Manifest.Hash, To: next.Manifest.Hash, Types: []TypeDiff{}, Policies: []NamedDiff{}, Flows: []NamedDiff{},
		Privileged: []string{}, Exposures: []string{}, Indexes: []string{}, Migrations: []string{}}
	sum := sha256.Sum256([]byte(cur.Manifest.Hash + "|" + next.Manifest.Hash))
	p.Hash = hex.EncodeToString(sum[:])[:16]
	for _, ct := range next.Types.All() {
		if ct.ID.IsSystem() {
			continue
		}
		old, ok := cur.Types.Get(ct.Base())
		if !ok {
			p.Types = append(p.Types, TypeDiff{Type: string(ct.ID), Status: "added"})
			continue
		}
		changes := schemadomain.CheckCompatibility(old, ct)
		if len(changes) == 0 {
			continue
		}
		p.Types = append(p.Types, TypeDiff{Type: string(ct.ID), Status: "changed", Changes: changes})
		for _, c := range changes {
			if c.Breaking() {
				p.Breaking = true
			}
			switch c.Kind {
			case schemadomain.ChangeVisibilityWidened:
				p.Exposures = append(p.Exposures, string(ct.Base())+c.Path)
			case schemadomain.ChangeAddIndex:
				p.Indexes = append(p.Indexes, string(ct.Base())+c.Path)
			}
		}
	}
	for _, ct := range cur.Types.All() {
		if _, ok := next.Types.Get(ct.Base()); !ok && !ct.ID.IsSystem() {
			p.Types = append(p.Types, TypeDiff{Type: string(ct.ID), Status: "removed"})
			p.Breaking = true
		}
	}
	p.Policies = diffFiles(cur.Manifest.Policies, next.Manifest.Policies)
	old := map[string][]byte{}
	for _, f := range cur.Flows.All() {
		b, _ := json.Marshal(f.Def)
		old[f.Name] = b
	}
	for _, f := range next.Flows.All() {
		b, _ := json.Marshal(f.Def)
		status := ""
		if prev, ok := old[f.Name]; !ok {
			status = "added"
		} else if !bytes.Equal(prev, b) {
			status = "changed"
		}
		delete(old, f.Name)
		if status == "" {
			continue
		}
		p.Flows = append(p.Flows, NamedDiff{Name: f.Name, Status: status, Privileged: f.Def.Privileged})
		if f.Def.Privileged {
			p.Privileged = append(p.Privileged, f.Name)
		}
	}
	for name := range old {
		p.Flows = append(p.Flows, NamedDiff{Name: name, Status: "removed"})
	}
	sort.Slice(p.Flows, func(i, j int) bool { return p.Flows[i].Name < p.Flows[j].Name })
	for _, m := range next.Manifest.Migrations {
		p.Migrations = append(p.Migrations, m.Path)
	}
	return p
}

type manifestFile = manifestdomain.File

func diffFiles(a, b []manifestFile) []NamedDiff {
	old := map[string][]byte{}
	for _, f := range a {
		old[f.Path] = f.Data
	}
	out := []NamedDiff{}
	for _, f := range b {
		prev, ok := old[f.Path]
		switch {
		case !ok:
			out = append(out, NamedDiff{Name: f.Path, Status: "added"})
		case !bytes.Equal(prev, f.Data):
			out = append(out, NamedDiff{Name: f.Path, Status: "changed"})
		}
		delete(old, f.Path)
	}
	for name := range old {
		out = append(out, NamedDiff{Name: name, Status: "removed"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DeployRecord is one deploy (spec 12.4: manifest hash, actor, plan).
type DeployRecord struct {
	Hash  string            `json:"hash"`
	Actor string            `json:"actor"`
	At    time.Time         `json:"at"`
	Plan  *Plan             `json:"plan,omitempty"`
	Files map[string][]byte `json:"-"`
}

// DeployStore keeps deploy records and the active manifest bundle.
type DeployStore interface {
	Active(ctx context.Context) (*DeployRecord, error)
	Save(ctx context.Context, r DeployRecord) error
	List(ctx context.Context) ([]DeployRecord, error)
}

type memoryDeploys struct {
	mu   sync.Mutex
	recs []DeployRecord
}

func (m *memoryDeploys) Active(context.Context) (*DeployRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.recs) == 0 {
		return nil, nil
	}
	r := m.recs[len(m.recs)-1]
	return &r, nil
}

func (m *memoryDeploys) Save(_ context.Context, r DeployRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, r)
	return nil
}

func (m *memoryDeploys) List(context.Context) ([]DeployRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]DeployRecord(nil), m.recs...), nil
}

// sqlDeploys stores deploys in manifest_deploys (Manifest context table).
type sqlDeploys struct{ db *sql.DB }

func newSQLDeploys(ctx context.Context, db *sql.DB) (*sqlDeploys, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS manifest_deploys (
		seq INTEGER PRIMARY KEY AUTOINCREMENT, hash TEXT NOT NULL, actor TEXT NOT NULL, at TEXT NOT NULL,
		plan TEXT, files TEXT NOT NULL)`)
	return &sqlDeploys{db: db}, err
}

func (s *sqlDeploys) Active(ctx context.Context) (*DeployRecord, error) {
	var r DeployRecord
	var at string
	var plan sql.NullString
	var files []byte
	err := s.db.QueryRowContext(ctx, `SELECT hash, actor, at, plan, files FROM manifest_deploys ORDER BY seq DESC LIMIT 1`).
		Scan(&r.Hash, &r.Actor, &at, &plan, &files)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.At, _ = time.Parse(time.RFC3339Nano, at)
	if plan.Valid {
		r.Plan = &Plan{}
		_ = json.Unmarshal([]byte(plan.String), r.Plan)
	}
	if err := json.Unmarshal(files, &r.Files); err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *sqlDeploys) Save(ctx context.Context, r DeployRecord) error {
	files, _ := json.Marshal(r.Files)
	var plan any
	if r.Plan != nil {
		b, _ := json.Marshal(r.Plan)
		plan = string(b)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO manifest_deploys (hash, actor, at, plan, files) VALUES (?, ?, ?, ?, ?)`,
		r.Hash, r.Actor, r.At.UTC().Format(time.RFC3339Nano), plan, files)
	return err
}

func (s *sqlDeploys) List(ctx context.Context) ([]DeployRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hash, actor, at FROM manifest_deploys ORDER BY seq DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []DeployRecord
	for rows.Next() {
		var r DeployRecord
		var at string
		if err := rows.Scan(&r.Hash, &r.Actor, &at); err != nil {
			return nil, err
		}
		r.At, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Deployer serves /v1/deploy: plan, apply, and deploy history (spec 12.4).
// Activation is provided by the process (organond), which swaps the runtime.
type Deployer struct {
	mu       sync.Mutex
	Env      string
	Current  func() *Runtime
	Activate func(ctx context.Context, c *Compiled) error
}

func (d *Deployer) register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/deploy/plan", d.plan)
	mux.HandleFunc("POST /v1/deploy/apply", d.apply)
	mux.HandleFunc("GET /v1/deploy", d.list)
}

type bundle struct {
	Files         map[string]string `json:"files"`
	PlanHash      string            `json:"planHash,omitempty"`
	AllowBreaking bool              `json:"allowBreaking,omitempty"`
}

func adminOnly(w http.ResponseWriter, r *http.Request) bool {
	p := accessapi.PrincipalFrom(r.Context())
	if p.IsSystem() || p.HasRole("admin") {
		return true
	}
	if p.IsAnonymous() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
	} else {
		httpserver.WriteError(w, r, kernel.ErrForbidden)
	}
	return false
}

func (d *Deployer) compileBundle(b bundle) (*Compiled, error) {
	files := make(map[string][]byte, len(b.Files))
	for k, v := range b.Files {
		files[k] = []byte(v)
	}
	m, err := manifestfs.LoadFiles("bundle", files, d.Env)
	if err != nil {
		return nil, kernel.NewError(kernel.CodeValidationFailed, "%v", err)
	}
	c, err := Compile(m)
	if err != nil {
		var ve *ValidationError
		if errors.As(err, &ve) {
			fe := make([]kernel.FieldError, 0, len(ve.Problems))
			for _, p := range ve.Problems {
				if !p.Warning {
					fe = append(fe, kernel.FieldError{Path: p.File + "#" + p.Pointer, Code: "manifest", Message: p.Message})
				}
			}
			return nil, kernel.NewValidationError(fe...)
		}
		return nil, err
	}
	return c, nil
}

func (d *Deployer) plan(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	var b bundle
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	next, err := d.compileBundle(b)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, MakePlan(d.Current().App, next))
}

func (d *Deployer) apply(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	var b bundle
	if err := httpserver.DecodeJSON(r, &b); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	next, err := d.compileBundle(b)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	cur := d.Current()
	plan := MakePlan(cur.App, next)
	if b.PlanHash != plan.Hash {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeConflict, "the deployed state changed since the plan (or no plan hash was given); run plan again"))
		return
	}
	if plan.Breaking && !b.AllowBreaking && len(plan.Migrations) == 0 {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeValidationFailed, "the plan has breaking changes; add a migration or pass allowBreaking"))
		return
	}
	actor := accessapi.PrincipalFrom(r.Context()).ID.String()
	rec := DeployRecord{Hash: next.Manifest.Hash, Actor: actor, At: time.Now().UTC(), Plan: &plan, Files: next.Manifest.Files}
	if err := cur.Deploys.Save(r.Context(), rec); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if err := d.Activate(context.WithoutCancel(r.Context()), next); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"activated": next.Manifest.Hash, "plan": plan})
}

func (d *Deployer) list(w http.ResponseWriter, r *http.Request) {
	if !adminOnly(w, r) {
		return
	}
	recs, err := d.Current().Deploys.List(r.Context())
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"deploys": recs})
}
