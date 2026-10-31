package application

import (
	"context"
	"errors"

	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	"app-maker/internal/content/domain"
	"app-maker/internal/kernel"
	schemaapi "app-maker/internal/schema/api"
)

// EnsureRoot creates the app's root feature, owned by the system principal,
// unless it exists (spec 8.4). When several replicas race, exactly one
// create succeeds and the others observe the duplicate.
func (s *Service) EnsureRoot(ctx context.Context, appName string) error {
	ct, ok := s.d.Types.Get(schemaapi.TypeRoot)
	if !ok {
		return errors.New("root type is not registered")
	}
	repo, err := s.repoFor(ct)
	if err != nil {
		return err
	}
	if _, err := repo.Get(ctx, contentapi.RootID); err == nil {
		return nil
	} else if !errors.Is(err, kernel.ErrNotFound) {
		return err
	}
	now := s.d.Clock.Now()
	sys := accessapi.SystemPrincipalID
	fields := map[string]kernel.Value{
		"/owner": kernel.Ref{ID: sys, Target: string(schemaapi.TraitPrincipal)},
	}
	if appName != "" {
		fields["/name"] = kernel.String(appName)
	}
	f, err := domain.NewFeature(contentapi.RootID, ct, nil, &sys, fields, now, sys)
	if err != nil {
		return err
	}
	events := s.toChangeEvents(f.DrainEvents(), nil)
	jobs, err := publishJobs(events)
	if err != nil {
		return err
	}
	_, err = repo.Apply(ctx, domain.Batch{
		Mutations: []domain.Mutation{{Kind: domain.MutCreate, Feature: f.Snapshot()}},
		Jobs:      jobs,
	})
	var ce *kernel.ConstraintError
	if errors.As(err, &ce) && ce.Code == "duplicate_id" {
		return nil
	}
	return err
}
