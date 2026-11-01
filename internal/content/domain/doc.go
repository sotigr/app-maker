package domain

import (
	"strings"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
)

// AsDoc adapts a snapshot to the query matcher.
func (s Snapshot) AsDoc() queryapi.Doc { return snapDoc{s} }

type snapDoc struct{ s Snapshot }

func (d snapDoc) FeatureID() kernel.ID     { return d.s.ID }
func (d snapDoc) Owner() *kernel.ID        { return d.s.OwnerID }
func (d snapDoc) AncestorIDs() []kernel.ID { return d.s.Ancestors }
func (d snapDoc) Value(path string) (kernel.Value, bool) {
	return ValueAt(d.s.Fields, path)
}

// ValueAt resolves a JSON Pointer against top-level fields, navigating
// nested objects ("/address/city").
func ValueAt(fields map[string]kernel.Value, path string) (kernel.Value, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	v, ok := fields["/"+parts[0]]
	for _, p := range parts[1:] {
		if !ok {
			return nil, false
		}
		o, isObj := v.(kernel.Object)
		if !isObj {
			return nil, false
		}
		v, ok = o.Get(p)
	}
	return v, ok
}

// SortValues extracts the sort key values of a snapshot for cursors.
func SortValues(s Snapshot, keys []queryapi.SortKey) []kernel.Value {
	out := make([]kernel.Value, 0, len(keys))
	for _, k := range keys {
		if k.Path == "" {
			continue
		}
		v, ok := ValueAt(s.Fields, k.Path)
		if !ok {
			v = kernel.Null{}
		}
		out = append(out, v)
	}
	return out
}
