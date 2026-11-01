package application

import (
	"context"
	"encoding/json"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/content/domain"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// unaudited types are bookkeeping written by the system that would only
// echo other audit rows.
var unaudited = map[schemaapi.TypeID]bool{schemaapi.TypeFlowRun: true, schemaapi.TypeBlob: true}

// auditRecords turns a batch's events into audit rows (spec 14.3): who did
// what to which feature, never the values.
func (s *Service) auditRecords(wc WriteContext, events []domain.DomainEvent) []kernel.AuditRecord {
	var run string
	if n := len(wc.Causation); n > 0 {
		run = wc.Causation[n-1].RunID.String()
	}
	var out []kernel.AuditRecord
	for _, ev := range events {
		if unaudited[ev.TypeID.Base()] {
			continue
		}
		target := ev.FeatureID
		out = append(out, kernel.AuditRecord{ID: s.d.IDs.NewID(), At: ev.At, Actor: wc.Principal.ID, ActorKind: string(wc.Principal.Kind),
			Action: string(ev.Kind), Target: &target, Type: string(ev.TypeID.Base()), Version: ev.Version, Changed: ev.Changed, Run: run})
	}
	return out
}

// auditStore is the default repository's audit log.
func (s *Service) auditStore() (domain.AuditStore, error) {
	repo, err := s.d.Repos.Get("default")
	if err != nil {
		return nil, err
	}
	store, ok := repo.(domain.AuditStore)
	if !ok {
		return nil, &kernel.UnsupportedError{Capability: "AuditLog"}
	}
	return store, nil
}

// AppendAudit records events outside a feature commit (authentication).
func (s *Service) AppendAudit(ctx context.Context, recs []kernel.AuditRecord) error {
	store, err := s.auditStore()
	if err != nil {
		return err
	}
	for i := range recs {
		if recs[i].ID.IsZero() {
			recs[i].ID = s.d.IDs.NewID()
		}
		if recs[i].At.IsZero() {
			recs[i].At = s.d.Clock.Now()
		}
	}
	return store.AppendAudit(ctx, recs)
}

// MaxAuditPage bounds one audit query.
const MaxAuditPage = 500

// Audit lists audit records, newest first; admins only (spec 14.3).
func (s *Service) Audit(ctx context.Context, p accessapi.Principal, f kernel.AuditFilter) ([]kernel.AuditRecord, error) {
	if !p.IsSystem() && !p.HasRole("admin") {
		return nil, kernel.ErrForbidden
	}
	if f.Limit <= 0 || f.Limit > MaxAuditPage {
		f.Limit = 100
	}
	store, err := s.auditStore()
	if err != nil {
		return nil, err
	}
	out, err := store.Audit(ctx, f)
	if out == nil {
		out = []kernel.AuditRecord{}
	}
	return out, err
}

// JobExpireAudit deletes audit records past retention (daily).
const JobExpireAudit = "expire_audit"

// ExpireAudit returns the expire_audit handler for a retention period.
func (s *Service) ExpireAudit(retention time.Duration) func(ctx context.Context, _ json.RawMessage) error {
	return func(ctx context.Context, _ json.RawMessage) error {
		store, err := s.auditStore()
		if err != nil {
			return err
		}
		_, err = store.PurgeAudit(ctx, s.d.Clock.Now().Add(-retention))
		return err
	}
}
