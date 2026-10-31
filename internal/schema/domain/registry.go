package domain

import (
	"sort"

	"app-maker/internal/schema/api"
)

// Registry is an immutable snapshot of compiled types and traits. It
// implements api.TypeRegistry.
type Registry struct {
	byBase map[api.TypeID]*api.CompiledType
	byName map[string]*api.CompiledType
	traits map[api.TypeID]*Trait
	all    []*api.CompiledType
}

var _ api.TypeRegistry = (*Registry)(nil)

func newRegistry(traits map[api.TypeID]*Trait) *Registry {
	return &Registry{
		byBase: map[api.TypeID]*api.CompiledType{},
		byName: map[string]*api.CompiledType{},
		traits: traits,
	}
}

func (r *Registry) add(ct *api.CompiledType) {
	r.byBase[ct.Base()] = ct
	r.byName[ct.Name] = ct
	r.all = append(r.all, ct)
}

func (r *Registry) freeze() {
	sort.Slice(r.all, func(i, j int) bool { return r.all[i].ID < r.all[j].ID })
}

func (r *Registry) isKnownTrait(id api.TypeID) bool {
	_, ok := r.traits[id.Base()]
	return id.IsTrait() && ok
}

// resolveAll returns every type matching a type or trait ID.
func (r *Registry) resolveAll(id api.TypeID) []*api.CompiledType {
	if id.IsTrait() {
		var out []*api.CompiledType
		for _, ct := range r.all {
			if ct.HasTrait(id) {
				out = append(out, ct)
			}
		}
		return out
	}
	if ct, ok := r.byBase[id.Base()]; ok {
		return []*api.CompiledType{ct}
	}
	return nil
}

// Get returns the type for a base or versioned ID. A versioned ID must
// match the registered version.
func (r *Registry) Get(id api.TypeID) (*api.CompiledType, bool) {
	ct, ok := r.byBase[id.Base()]
	if !ok {
		return nil, false
	}
	if v := id.Version(); v != 0 && v != ct.Version {
		return nil, false
	}
	return ct, true
}

// ByName resolves a short name.
func (r *Registry) ByName(name string) (*api.CompiledType, bool) {
	ct, ok := r.byName[name]
	return ct, ok
}

// Resolve returns the types matching a type or trait ID.
func (r *Registry) Resolve(id api.TypeID) []*api.CompiledType { return r.resolveAll(id) }

// All returns every type, ordered by ID.
func (r *Registry) All() []*api.CompiledType { return append([]*api.CompiledType(nil), r.all...) }

// Traits returns the trait IDs known to the registry.
func (r *Registry) Traits() []api.TypeID {
	out := make([]api.TypeID, 0, len(r.traits))
	for id := range r.traits {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
