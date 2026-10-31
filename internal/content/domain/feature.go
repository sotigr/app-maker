// Package domain is the Content context's domain: the Feature aggregate,
// patches, mutations, batches, the repository port, and the projector
// (spec 3, 5, 6).
package domain

import (
	"maps"
	"slices"
	"sort"
	"time"

	"app-maker/internal/kernel"
	schemaapi "app-maker/internal/schema/api"
)

// Feature is the central aggregate (spec 3.1). Fields are keyed by
// top-level JSON Pointer ("/title"); nested objects are Object values.
type Feature struct {
	id        kernel.ID
	typeID    schemaapi.TypeID
	parentID  *kernel.ID
	ownerID   *kernel.ID
	ancestors []kernel.ID // root first; maintained by the repository
	version   kernel.Version
	fields    map[string]kernel.Value
	createdAt time.Time
	updatedAt time.Time
	deletedAt *time.Time
	pending   []DomainEvent
	touched   bool
}

// Snapshot is the persistent state of a feature, used by repositories.
type Snapshot struct {
	ID        kernel.ID
	TypeID    schemaapi.TypeID
	ParentID  *kernel.ID
	OwnerID   *kernel.ID
	Ancestors []kernel.ID
	Version   kernel.Version
	Fields    map[string]kernel.Value
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// Clone returns a deep copy (values are immutable and shared).
func (s Snapshot) Clone() Snapshot {
	s.ParentID = cloneID(s.ParentID)
	s.OwnerID = cloneID(s.OwnerID)
	s.Ancestors = slices.Clone(s.Ancestors)
	s.Fields = maps.Clone(s.Fields)
	if s.DeletedAt != nil {
		d := *s.DeletedAt
		s.DeletedAt = &d
	}
	return s
}

func cloneID(id *kernel.ID) *kernel.ID {
	if id == nil {
		return nil
	}
	c := *id
	return &c
}

// Rehydrate rebuilds an aggregate from stored state.
func Rehydrate(s Snapshot) *Feature {
	s = s.Clone()
	if s.Fields == nil {
		s.Fields = map[string]kernel.Value{}
	}
	return &Feature{
		id: s.ID, typeID: s.TypeID, parentID: s.ParentID, ownerID: s.OwnerID, ancestors: s.Ancestors,
		version: s.Version, fields: s.Fields, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt, deletedAt: s.DeletedAt,
	}
}

// Snapshot returns the current state. After a change, Version is the
// version the change will commit as.
func (f *Feature) Snapshot() Snapshot {
	return Snapshot{
		ID: f.id, TypeID: f.typeID, ParentID: f.parentID, OwnerID: f.ownerID, Ancestors: f.ancestors,
		Version: f.NextVersion(), Fields: f.fields, CreatedAt: f.createdAt, UpdatedAt: f.updatedAt, DeletedAt: f.deletedAt,
	}.Clone()
}

// ParentInfo describes a (prospective) parent for invariant checks: its ID,
// compiled type, and ancestry. The parent check uses a type lookup.
type ParentInfo struct {
	ID        kernel.ID
	Type      *schemaapi.CompiledType
	Ancestors []kernel.ID
}

// NewFeature creates a feature (spec 3.1). fields must already be
// validated against the storage view by the caller.
func NewFeature(id kernel.ID, ct *schemaapi.CompiledType, parent *ParentInfo, owner *kernel.ID,
	fields map[string]kernel.Value, at time.Time, actor kernel.ID) (*Feature, error) {
	if ct.Base() == schemaapi.TypeRoot {
		if parent != nil {
			return nil, kernel.NewError(kernel.CodeConstraint, "the root feature has no parent")
		}
	} else {
		if parent == nil {
			return nil, kernel.NewError(kernel.CodeValidationFailed, "parent is required")
		}
		if err := checkChildAllowed(parent.Type, ct); err != nil {
			return nil, err
		}
	}
	f := &Feature{
		id: id, typeID: ct.ID, ownerID: cloneID(owner), fields: maps.Clone(fields),
		createdAt: at, updatedAt: at, touched: true,
	}
	if f.fields == nil {
		f.fields = map[string]kernel.Value{}
	}
	if parent != nil {
		pid := parent.ID
		f.parentID = &pid
		f.ancestors = append(slices.Clone(parent.Ancestors), parent.ID)
	}
	f.syncOwner()
	changed := sortedPaths(f.fields)
	f.record(EventCreated, changed, nil, at, actor)
	return f, nil
}

// checkChildAllowed enforces x-allowedChildren (spec 3.1). The root
// feature accepts any non-root type, and grants may sit under any feature:
// a grant is a child of the object it grants a role on (spec 8.6).
func checkChildAllowed(parent, child *schemaapi.CompiledType) error {
	if parent.Base() == schemaapi.TypeRoot || child.Base() == schemaapi.TypeGrant || parent.AllowsChild(child) {
		return nil
	}
	return kernel.NewError(kernel.CodeConstraint, "%s is not an allowed child of %s", child.Base(), parent.Base())
}

// ID returns the feature's ID.
func (f *Feature) ID() kernel.ID { return f.id }

// TypeID returns the versioned type ID; it never changes.
func (f *Feature) TypeID() schemaapi.TypeID { return f.typeID }

// ParentID returns the parent ID, or nil for the root feature.
func (f *Feature) ParentID() *kernel.ID { return cloneID(f.parentID) }

// OwnerID returns the owner, or nil.
func (f *Feature) OwnerID() *kernel.ID { return cloneID(f.ownerID) }

// Ancestors returns the ancestor IDs, root first.
func (f *Feature) Ancestors() []kernel.ID { return slices.Clone(f.ancestors) }

// Version returns the committed version the feature was loaded at.
func (f *Feature) Version() kernel.Version { return f.version }

// NextVersion is the version the pending change commits as.
func (f *Feature) NextVersion() kernel.Version {
	if f.touched {
		return f.version + 1
	}
	return f.version
}

// Changed reports whether the aggregate has uncommitted changes.
func (f *Feature) Changed() bool { return f.touched }

// Field returns a field value.
func (f *Feature) Field(path string) (kernel.Value, bool) {
	v, ok := f.fields[path]
	return v, ok
}

// Fields returns a copy of the field map.
func (f *Feature) Fields() map[string]kernel.Value { return maps.Clone(f.fields) }

// CreatedAt returns the creation time.
func (f *Feature) CreatedAt() time.Time { return f.createdAt }

// UpdatedAt returns the last update time.
func (f *Feature) UpdatedAt() time.Time { return f.updatedAt }

// DeletedAt returns the soft-delete time, or nil.
func (f *Feature) DeletedAt() *time.Time { return f.deletedAt }

// IsDeleted reports whether the feature is soft-deleted.
func (f *Feature) IsDeleted() bool { return f.deletedAt != nil }

// Patch maps top-level paths to new values; a nil value removes the field
// (JSON Merge Patch semantics after merging nested objects, spec 4.9).
type Patch map[string]kernel.Value

// Apply applies a patch already checked against write rules and returns
// the paths that changed. An empty result means nothing changed and no
// event is recorded.
func (f *Feature) Apply(p Patch, at time.Time, actor kernel.ID) ([]string, error) {
	if f.deletedAt != nil {
		return nil, kernel.ErrNotFound
	}
	before := map[string]kernel.Value{}
	var changed []string
	for _, path := range sortedPaths(p) {
		nv := p[path]
		ov, had := f.fields[path]
		if nv == nil {
			if !had {
				continue
			}
			delete(f.fields, path)
		} else {
			if had && kernel.Equal(ov, nv) {
				continue
			}
			f.fields[path] = nv
		}
		if had {
			before[path] = ov
		} else {
			before[path] = kernel.Null{}
		}
		changed = append(changed, path)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	f.updatedAt = at
	f.syncOwner()
	f.touched = true
	f.record(EventUpdated, changed, before, at, actor)
	return changed, nil
}

// syncOwner keeps ownerID in step with the Ownable trait's /owner field.
func (f *Feature) syncOwner() {
	if v, ok := f.fields["/owner"]; ok {
		if r, ok := v.(kernel.Ref); ok {
			id := r.ID
			f.ownerID = &id
			return
		}
		f.ownerID = nil
	}
}

// MoveTo changes the parent (spec 3.1): the new parent's type must allow
// this type, and a feature cannot become its own ancestor.
func (f *Feature) MoveTo(parent ParentInfo, ct *schemaapi.CompiledType, at time.Time, actor kernel.ID) error {
	if f.deletedAt != nil {
		return kernel.ErrNotFound
	}
	if f.parentID == nil {
		return kernel.NewError(kernel.CodeConstraint, "the root feature cannot be moved")
	}
	if parent.ID == f.id || slices.Contains(parent.Ancestors, f.id) {
		return kernel.NewError(kernel.CodeConstraint, "a feature cannot be its own ancestor")
	}
	if err := checkChildAllowed(parent.Type, ct); err != nil {
		return err
	}
	if *f.parentID == parent.ID {
		return nil
	}
	pid := parent.ID
	f.parentID = &pid
	f.ancestors = append(slices.Clone(parent.Ancestors), parent.ID)
	f.updatedAt = at
	f.touched = true
	f.record(EventMoved, nil, nil, at, actor)
	return nil
}

// MarkDeleted soft-deletes the feature (spec 3.6).
func (f *Feature) MarkDeleted(at time.Time, actor kernel.ID) error {
	if f.deletedAt != nil {
		return kernel.ErrNotFound
	}
	if f.parentID == nil {
		return kernel.NewError(kernel.CodeConstraint, "the root feature cannot be deleted")
	}
	t := at
	f.deletedAt = &t
	f.updatedAt = at
	f.touched = true
	f.record(EventDeleted, nil, nil, at, actor)
	return nil
}

// MarkPurged records a hard delete of a soft-deleted feature.
func (f *Feature) MarkPurged(at time.Time, actor kernel.ID) {
	f.touched = true
	f.record(EventPurged, nil, nil, at, actor)
}

// DrainEvents returns and clears the recorded domain events.
func (f *Feature) DrainEvents() []DomainEvent {
	out := f.pending
	f.pending = nil
	return out
}

func (f *Feature) record(kind EventKind, changed []string, before map[string]kernel.Value, at time.Time, actor kernel.ID) {
	f.pending = append(f.pending, DomainEvent{
		Kind: kind, FeatureID: f.id, TypeID: f.typeID, ParentID: cloneID(f.parentID), OwnerID: cloneID(f.ownerID),
		Ancestors: slices.Clone(f.ancestors), Version: f.version + 1, Changed: changed, Before: before,
		After: maps.Clone(f.fields), Actor: actor, At: at,
	})
}

func sortedPaths[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EventKind is the kind of a domain event.
type EventKind string

// Domain event kinds (spec 9.1).
const (
	EventCreated EventKind = "created"
	EventUpdated EventKind = "updated"
	EventMoved   EventKind = "moved"
	EventDeleted EventKind = "deleted"
	EventPurged  EventKind = "purged"
)

// DomainEvent records one change of a feature, with the full storage
// document after the change.
type DomainEvent struct {
	Kind      EventKind
	FeatureID kernel.ID
	TypeID    schemaapi.TypeID
	ParentID  *kernel.ID
	OwnerID   *kernel.ID
	Ancestors []kernel.ID
	Version   kernel.Version
	Changed   []string
	Before    map[string]kernel.Value
	After     map[string]kernel.Value
	Actor     kernel.ID
	At        time.Time
}
