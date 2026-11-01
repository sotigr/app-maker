package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/content/domain"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	rt "app-maker/internal/runtime/api"
	schemaapi "app-maker/internal/schema/api"
)

// JobMigrateBatch migrates one page of a type's features (spec 12.5).
const JobMigrateBatch = "migrate_batch"

// SetMigrations installs the compiled migrations; flow runs the flow of a
// flow-based migration as the system for one feature.
func (s *Service) SetMigrations(ms []*domain.Migration, flow func(ctx context.Context, name string, feature kernel.ID) error) {
	s.migrations = map[schemaapi.TypeID]map[int]*domain.Migration{}
	for _, m := range ms {
		if s.migrations[m.Type] == nil {
			s.migrations[m.Type] = map[int]*domain.Migration{}
		}
		s.migrations[m.Type][m.From] = m
	}
	s.migrationFlow = flow
}

func (s *Service) migrationFrom(base schemaapi.TypeID, v int) *domain.Migration {
	return s.migrations[base][v]
}

// upcast applies expression migrations in memory to a feature written with
// an older type version (spec 12.5: clients always see the new shape). It
// stops at a flow-based step, and leaves the feature unchanged when an
// expression fails.
func (s *Service) upcast(f *domain.Feature, ct *schemaapi.CompiledType) {
	v := f.TypeID().Version()
	if v == 0 || v >= ct.Version {
		return
	}
	fields := f.Fields()
	doc := domain.ExprDocument(f.Snapshot())
	start := v
	for v < ct.Version {
		m := s.migrationFrom(ct.Base(), v)
		if m == nil || m.Set == nil {
			break
		}
		vars := map[string]any{"feature": doc, rt.FnNow: s.d.Clock.Now, rt.FnUUID: func() string { return s.d.IDs.NewID().String() }}
		paths := make([]string, 0, len(m.Set))
		for p := range m.Set {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		vals := map[string]kernel.Value{}
		for _, p := range paths {
			prog := m.Set[p]
			if prog == nil {
				vals[p] = nil
				continue
			}
			out, err := prog.Eval(vars)
			if err != nil {
				return
			}
			if out == nil {
				vals[p] = nil
				continue
			}
			fd, _ := ct.Field(p)
			nv, err := fd.Type.Coerce(p, out)
			if err != nil {
				return
			}
			vals[p] = nv
		}
		for p, nv := range vals {
			if nv == nil {
				delete(fields, p)
				delete(doc, p[1:])
				continue
			}
			fields[p] = nv
			doc[p[1:]] = kernel.Native(nv)
		}
		v = m.To
	}
	if v != start {
		f.Upcast(ct.Base().WithVersion(v), fields)
	}
}

type migrateArgs struct {
	Type   schemaapi.TypeID `json:"type"`
	After  *kernel.ID       `json:"after,omitempty"`
	Record kernel.ID        `json:"record"`
}

// StartMigrations starts a migrate_batch chain for every type with
// migrations up to its current version, unless one already finished
// (spec 12.5). It runs when a worker starts, so after every deploy.
func (s *Service) StartMigrations(ctx context.Context) error {
	sys := s.SystemPorts()
	var jobs []kernel.JobRequest
	bases := make([]schemaapi.TypeID, 0, len(s.migrations))
	for base := range s.migrations {
		bases = append(bases, base)
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i] < bases[j] })
	for _, base := range bases {
		ct, ok := s.d.Types.Get(base)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s@%d", base, ct.Version)
		found, err := sys.Find(ctx, schemaapi.TypeMigration, queryapi.Compare{Path: "/key", Op: queryapi.OpEq, Value: kernel.String(key)}, 1)
		if err != nil {
			return err
		}
		var rec kernel.ID
		switch {
		case len(found) == 1 && found[0].Data["state"] == "done":
			continue
		case len(found) == 1:
			rec = found[0].ID // resume: batches already done are no-ops
		default:
			var ids []any
			for v := 1; v < ct.Version; v++ {
				if m := s.migrationFrom(base, v); m != nil {
					ids = append(ids, m.ID)
				}
			}
			rec, err = sys.Create(ctx, "migration", nil, map[string]any{"key": key, "target": string(base), "version": ct.Version,
				"migrations": ids, "state": "running", "processed": 0, "migrated": 0, "failed": 0}, nil)
			if err != nil {
				return err
			}
		}
		args, _ := json.Marshal(migrateArgs{Type: base, Record: rec})
		jobs = append(jobs, kernel.JobRequest{Kind: JobMigrateBatch, Queue: kernel.QueueMaintenance, Args: args,
			UniqueKey: "migrate:" + key + ":start", MaxAttempts: 25})
	}
	if len(jobs) == 0 {
		return nil
	}
	return s.d.Jobs.Enqueue(ctx, jobs)
}

// HandleMigrateBatch migrates one page of features, records progress, and
// enqueues the next page. Re-running a page is a no-op for features that
// are already at the current version.
func (s *Service) HandleMigrateBatch(ctx context.Context, raw json.RawMessage) error {
	var a migrateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	ct, ok := s.d.Types.Get(a.Type)
	if !ok {
		return nil
	}
	repo, err := s.repoFor(ct)
	if err != nil {
		return err
	}
	batch := domain.DefaultMigrationBatch
	for v := 1; v < ct.Version; v++ {
		if m := s.migrationFrom(ct.Base(), v); m != nil && m.BatchSize < batch {
			batch = m.BatchSize
		}
	}
	plan := queryapi.Plan{Types: []schemaapi.TypeID{ct.ID}, Limit: batch, Sort: []queryapi.SortKey{{}}}
	if a.After != nil {
		plan.After = &queryapi.Cursor{ID: *a.After}
	}
	page, err := repo.Query(ctx, plan)
	if err != nil {
		return err
	}
	var migrated, failed int
	var errs []any
	for _, f := range page.Items {
		changed, err := s.migrateOne(ctx, repo, f.ID())
		switch {
		case err != nil:
			failed++
			if len(errs) < 20 {
				errs = append(errs, map[string]any{"feature": f.ID().String(), "error": err.Error()})
			}
		case changed:
			migrated++
		}
	}
	if err := s.recordMigration(ctx, a.Record, len(page.Items), migrated, failed, errs, page.Next == nil); err != nil {
		return err
	}
	if page.Next == nil {
		return nil
	}
	next := migrateArgs{Type: a.Type, After: &page.Next.ID, Record: a.Record}
	args, _ := json.Marshal(next)
	return s.d.Jobs.Enqueue(ctx, []kernel.JobRequest{{Kind: JobMigrateBatch, Queue: kernel.QueueMaintenance, Args: args,
		UniqueKey: fmt.Sprintf("migrate:%s:%s", a.Type, page.Next.ID), MaxAttempts: 25}})
}

// migrateOne brings one feature to the current type version: expression
// steps through upcast, flow steps by running their flow as the system.
func (s *Service) migrateOne(ctx context.Context, repo domain.FeatureRepository, id kernel.ID) (bool, error) {
	changed := false
	for attempt := 0; attempt < 10; attempt++ {
		f, ct, err := s.load(ctx, id) // upcasts expression steps
		if errors.Is(err, kernel.ErrNotFound) {
			return changed, nil
		}
		if err != nil {
			return changed, err
		}
		v := f.TypeID().Version()
		if f.Upcasted() {
			if err := s.persistMigrated(ctx, repo, f, ct); err != nil {
				if errors.Is(err, kernel.ErrVersionConflict) {
					continue
				}
				return changed, err
			}
			changed = true
			continue
		}
		if v == 0 || v >= ct.Version {
			return changed, nil
		}
		m := s.migrationFrom(ct.Base(), v)
		if m == nil {
			return changed, fmt.Errorf("no migration from version %d of %s", v, ct.Name)
		}
		if m.Flow == "" {
			return changed, fmt.Errorf("migration %s could not compute the new values", m.ID)
		}
		if s.migrationFlow == nil {
			return changed, errors.New("flow migrations are not available")
		}
		if err := s.migrationFlow(ctx, m.Flow, id); err != nil {
			return changed, fmt.Errorf("migration flow %s: %w", m.Flow, err)
		}
		f, ct, err = s.load(ctx, id)
		if err != nil {
			return changed, err
		}
		if f.TypeID().Version() == m.From {
			f.Upcast(ct.Base().WithVersion(m.To), f.Fields())
			if err := s.persistMigrated(ctx, repo, f, ct); err != nil && !errors.Is(err, kernel.ErrVersionConflict) {
				return changed, err
			}
		}
		changed = true
	}
	return changed, errors.New("too many concurrent changes")
}

// persistMigrated writes an upcast feature with a version check, without
// events (shape changes are not domain changes) but with an audit row.
func (s *Service) persistMigrated(ctx context.Context, repo domain.FeatureRepository, f *domain.Feature, ct *schemaapi.CompiledType) error {
	if f.TypeID().Version() == ct.Version {
		if err := s.validateStorage(ct, schemaapi.AudSystem, f.Fields()); err != nil {
			return err
		}
	}
	snap := f.Snapshot()
	expected := snap.Version
	snap.Version++
	target := snap.ID
	_, err := repo.Apply(ctx, domain.Batch{
		Mutations: []domain.Mutation{{Kind: domain.MutUpdate, Feature: snap, Expected: expected, UniqueKey: uniqueKey(ct, snap.Fields)}},
		Audit: []kernel.AuditRecord{{ID: s.d.IDs.NewID(), At: s.d.Clock.Now(), Actor: accessapi.SystemPrincipalID, ActorKind: string(accessapi.KindSystem),
			Action: "migrated", Target: &target, Type: string(snap.TypeID), Version: snap.Version}},
	})
	return err
}

func (s *Service) recordMigration(ctx context.Context, rec kernel.ID, processed, migrated, failed int, errs []any, done bool) error {
	sys := s.SystemPorts()
	for attempt := 0; attempt < 5; attempt++ {
		cur, err := sys.Get(ctx, rec)
		if errors.Is(err, kernel.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		num := func(k string) int64 { n, _ := cur.Data[k].(int64); return n }
		patch := map[string]any{"processed": num("processed") + int64(processed), "migrated": num("migrated") + int64(migrated),
			"failed": num("failed") + int64(failed)}
		if len(errs) > 0 {
			patch["errors"] = errs
		}
		if done {
			patch["state"] = "done"
			patch["finishedAt"] = s.d.Clock.Now().UTC().Format(time.RFC3339Nano)
		}
		err = sys.Update(ctx, "migration", rec, cur.Version, patch)
		if !errors.Is(err, kernel.ErrVersionConflict) {
			return err
		}
	}
	return kernel.ErrVersionConflict
}
