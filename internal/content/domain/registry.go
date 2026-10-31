package domain

import (
	"fmt"
	"sort"
)

// RepositoryRegistry maps repository names to implementations (spec 5.4).
type RepositoryRegistry struct {
	repos map[string]FeatureRepository
}

// NewRepositoryRegistry returns an empty registry.
func NewRepositoryRegistry() *RepositoryRegistry {
	return &RepositoryRegistry{repos: map[string]FeatureRepository{}}
}

// Register adds a repository under name.
func (r *RepositoryRegistry) Register(name string, repo FeatureRepository) {
	r.repos[name] = repo
}

// Get returns the repository named name.
func (r *RepositoryRegistry) Get(name string) (FeatureRepository, error) {
	repo, ok := r.repos[name]
	if !ok {
		return nil, fmt.Errorf("repository %q is not configured", name)
	}
	return repo, nil
}

// Names returns the registered names, sorted.
func (r *RepositoryRegistry) Names() []string {
	out := make([]string, 0, len(r.repos))
	for n := range r.repos {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
