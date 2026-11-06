// Package application holds the Content use cases (spec 6.4): CreateFeature,
// GetFeature, UpdateFeature, DeleteFeature, MoveFeature, ListChildren,
// ExecuteTransaction. HTTP handlers only translate to these.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	accessapi "organon/internal/access/api"
	changeapi "organon/internal/change/api"
	contentapi "organon/internal/content/api"
	"organon/internal/content/domain"
	flowapi "organon/internal/flow/api"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	schemaapi "organon/internal/schema/api"
)

// CommitHook plans extra jobs for a batch's events, enqueued in the same
// commit (run_flow for event triggers, index_features, recompute_aggregate,
// update_relationships; spec 2.5).
type CommitHook interface {
	Jobs(ctx context.Context, events []changeapi.Event, causation flowapi.CausationChain) ([]kernel.JobRequest, error)
}

// Limits are the domain limits enforced by the service (spec 14.1).
type Limits struct {
	MaxTransactionOps int // default 100
	MaxDocumentBytes  int // default 256 KiB
}

// Deps are the service's collaborators, injected at the composition root.
type Deps struct {
	Repos       *domain.RepositoryRegistry
	Types       schemaapi.TypeRegistry
	Authorizer  accessapi.Authorizer
	Clock       kernel.Clock
	IDs         kernel.IDGenerator
	Hooks       []CommitHook
	BeforeWrite BeforeWriteHook // optional (spec 10.2)
	Planner     queryapi.Planner
	// Search is the full-text index (optional, spec 7.5).
	Search queryapi.SearchIndex
	// Jobs enqueues jobs that accompany no write (effect-only flow runs).
	Jobs kernel.JobEnqueuer
	// Blobs verifies blob values and links downloads (optional, spec 14.4).
	Blobs  contentapi.Blobs
	Limits Limits
}

// Service implements the Content application use cases.
type Service struct {
	d Deps
	// migrations by base type and source version (spec 12.5).
	migrations    map[schemaapi.TypeID]map[int]*domain.Migration
	migrationFlow func(ctx context.Context, name string, feature kernel.ID) error
}

// NewService returns a Service.
func NewService(d Deps) *Service {
	if d.Limits.MaxTransactionOps == 0 {
		d.Limits.MaxTransactionOps = 100
	}
	if d.Limits.MaxDocumentBytes == 0 {
		d.Limits.MaxDocumentBytes = 256 << 10
	}
	return &Service{d: d}
}

// SetJobs sets the enqueuer for jobs without writes.
func (s *Service) SetJobs(j kernel.JobEnqueuer) { s.d.Jobs = j }

func (s *Service) enqueueJobs(ctx context.Context, jobs []kernel.JobRequest) error {
	if s.d.Jobs == nil {
		return errors.New("no job enqueuer configured")
	}
	return s.d.Jobs.Enqueue(ctx, jobs)
}

// AddHook appends a commit hook; used at wiring time to break cycles.
func (s *Service) AddHook(h CommitHook) { s.d.Hooks = append(s.d.Hooks, h) }

// SetBeforeWrite installs the before-write flow runner.
func (s *Service) SetBeforeWrite(h BeforeWriteHook) { s.d.BeforeWrite = h }

// SetBlobs installs blob storage (it depends on the service's system ports).
func (s *Service) SetBlobs(b contentapi.Blobs) { s.d.Blobs = b }

// Types returns the type registry.
func (s *Service) Types() schemaapi.TypeRegistry { return s.d.Types }

// OpError attaches the failing operation index to an error (spec 6.3).
type OpError struct {
	Index int
	Err   error
}

func (e *OpError) Error() string { return fmt.Sprintf("operation %d: %v", e.Index, e.Err) }
func (e *OpError) Unwrap() error { return e.Err }

// OperationIndex reports the failing operation for problem details.
func (e *OpError) OperationIndex() int { return e.Index }

func opErr(i int, err error) error {
	var ve *kernel.ValidationError
	if errors.As(err, &ve) {
		ve.Op = i
	}
	return &OpError{Index: i, Err: err}
}

// typeByName resolves a short type name.
func (s *Service) typeByName(name string) (*schemaapi.CompiledType, error) {
	ct, ok := s.d.Types.ByName(name)
	if !ok {
		return nil, kernel.NewError(kernel.CodeNotFound, "unknown type %q", name)
	}
	return ct, nil
}

// typeOf returns the compiled type of a stored feature.
func (s *Service) typeOf(f *domain.Feature) (*schemaapi.CompiledType, error) {
	ct, ok := s.d.Types.Get(f.TypeID())
	if !ok {
		// Features written with an older version are read with the
		// current one (upcast during migrations, spec 12.5).
		ct, ok = s.d.Types.Get(f.TypeID().Base())
	}
	if !ok {
		return nil, fmt.Errorf("type %s is not registered", f.TypeID())
	}
	s.upcast(f, ct)
	return ct, nil
}

func (s *Service) repoFor(ct *schemaapi.CompiledType) (domain.FeatureRepository, error) {
	return s.d.Repos.Get(ct.Repository)
}

// Ref builds the Authorizer's view of a feature.
func (s *Service) Ref(f *domain.Feature, ct *schemaapi.CompiledType) *accessapi.FeatureRef {
	return RefOf(f.Snapshot(), ct)
}

// RefOf builds the Authorizer's view of a snapshot.
func RefOf(snap domain.Snapshot, ct *schemaapi.CompiledType) *accessapi.FeatureRef {
	data := domain.ExprDocument(snap)
	anc := make([]string, len(snap.Ancestors))
	for i, a := range snap.Ancestors {
		anc[i] = a.String()
	}
	data[accessapi.AncestorsKey] = anc
	return &accessapi.FeatureRef{
		ID: snap.ID, Type: ct, ParentID: snap.ParentID, OwnerID: snap.OwnerID,
		Ancestors: snap.Ancestors, Data: data,
	}
}

// load reads one live feature and its type.
func (s *Service) load(ctx context.Context, id kernel.ID) (*domain.Feature, *schemaapi.CompiledType, error) {
	for _, name := range s.d.Repos.Names() {
		repo, _ := s.d.Repos.Get(name)
		f, err := repo.Get(ctx, id)
		if errors.Is(err, kernel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		ct, err := s.typeOf(f)
		if err != nil {
			return nil, nil, err
		}
		return f, ct, nil
	}
	return nil, nil, kernel.ErrNotFound
}

// Load reads a feature for internal callers (flows run as their principal
// and must still go through Read); it performs no authorization.
func (s *Service) Load(ctx context.Context, id kernel.ID) (*domain.Feature, *schemaapi.CompiledType, error) {
	return s.load(ctx, id)
}

// canRead checks the read policy; denial is reported as not found.
func (s *Service) canRead(ctx context.Context, p accessapi.Principal, ref *accessapi.FeatureRef) (schemaapi.Audience, error) {
	d, err := s.d.Authorizer.Check(ctx, p, accessapi.CheckRequest{Op: accessapi.OpRead, Type: ref.Type, Feature: ref})
	if err != nil {
		return 0, err
	}
	if !d.Allowed {
		return 0, kernel.ErrNotFound // never distinguishes missing and hidden (spec 6.6)
	}
	return s.d.Authorizer.Audience(ctx, p, ref)
}

// GetFeature reads one feature projected through the caller's read view.
// typeName may be empty to accept any type.
func (s *Service) GetFeature(ctx context.Context, p accessapi.Principal, typeName string, id kernel.ID) (contentapi.FeatureView, error) {
	f, ct, err := s.load(ctx, id)
	if err != nil {
		return contentapi.FeatureView{}, err
	}
	if typeName != "" && ct.Name != typeName {
		return contentapi.FeatureView{}, kernel.ErrNotFound
	}
	aud, err := s.canRead(ctx, p, s.Ref(f, ct))
	if err != nil {
		return contentapi.FeatureView{}, err
	}
	return s.link(ct, domain.Project(ct, f.Snapshot(), aud, nil)), nil
}

// ChildPage is a page of projected children.
type ChildPage struct {
	Items []contentapi.FeatureView `json:"items"`
	Next  string                   `json:"next,omitempty"`
}

// ListChildren lists the readable children of a feature (spec 6.2). The
// cursor is opaque and signed.
func (s *Service) ListChildren(ctx context.Context, p accessapi.Principal, parent kernel.ID, typeName, cursor string, limit int) (ChildPage, error) {
	pf, pct, err := s.load(ctx, parent)
	if err != nil {
		return ChildPage{}, err
	}
	if _, err := s.canRead(ctx, p, s.Ref(pf, pct)); err != nil {
		return ChildPage{}, err
	}
	binding := "children:" + parent.String() + ":" + typeName
	after, err := s.d.Planner.DecodeCursor(cursor, binding)
	if err != nil {
		return ChildPage{}, err
	}
	req := domain.PageRequest{Limit: limit, After: after}
	if typeName != "" {
		ct, err := s.typeByName(typeName)
		if err != nil {
			return ChildPage{}, err
		}
		req.Types = []schemaapi.TypeID{ct.ID}
	}
	repo, err := s.repoFor(pct)
	if err != nil {
		return ChildPage{}, err
	}
	page, err := repo.Children(ctx, parent, req)
	if err != nil {
		return ChildPage{}, err
	}
	out := ChildPage{Items: []contentapi.FeatureView{}, Next: s.d.Planner.EncodeCursor(page.Next, binding)}
	for _, f := range page.Items {
		ct, err := s.typeOf(f)
		if err != nil {
			return ChildPage{}, err
		}
		aud, err := s.canRead(ctx, p, s.Ref(f, ct))
		if errors.Is(err, kernel.ErrNotFound) {
			continue // post-filtered: pages may be short
		}
		if err != nil {
			return ChildPage{}, err
		}
		out.Items = append(out.Items, s.link(ct, domain.Project(ct, f.Snapshot(), aud, nil)))
	}
	return out, nil
}

// toChangeEvents converts domain events to the published event form.
func (s *Service) toChangeEvents(events []domain.DomainEvent, causation flowapi.CausationChain) []changeapi.Event {
	out := make([]changeapi.Event, 0, len(events))
	for _, e := range events {
		ct, ok := s.d.Types.Get(e.TypeID)
		if !ok {
			ct, ok = s.d.Types.Get(e.TypeID.Base())
		}
		if !ok {
			continue
		}
		ev := changeapi.Event{
			Kind: changeapi.EventKind(e.Kind), FeatureID: e.FeatureID, TypeID: e.TypeID, ParentID: e.ParentID,
			OwnerID: e.OwnerID, Ancestors: e.Ancestors, Version: e.Version, Changed: e.Changed,
			After: domain.WireDocument(ct, e.After), Actor: e.Actor, Causation: causation, At: e.At, FromParent: e.FromParent,
		}
		if len(e.Before) > 0 {
			ev.Before = map[string]any{}
			for path, v := range e.Before {
				if f, ok := ct.Field(path); ok {
					ev.Before[path[1:]] = f.Type.Wire(v)
				}
			}
		}
		out = append(out, ev)
	}
	return out
}

// publishJobs splits events into publish_events jobs of at most 1 MiB.
func publishJobs(events []changeapi.Event) ([]kernel.JobRequest, error) {
	var jobs []kernel.JobRequest
	var cur []changeapi.Event
	size := 0
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		b, err := json.Marshal(changeapi.PublishEventsArgs{Events: cur})
		if err != nil {
			return err
		}
		jobs = append(jobs, kernel.JobRequest{Kind: changeapi.JobPublishEvents, Queue: kernel.QueueEvents, Args: b, MaxAttempts: 25})
		cur, size = nil, 0
		return nil
	}
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if size+len(b) > changeapi.MaxPublishBytes && len(cur) > 0 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		cur = append(cur, e)
		size += len(b)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return jobs, nil
}
