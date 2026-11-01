package domain

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"organon/internal/kernel"
	"organon/internal/schema/api"
)

// StorageImpact is the storage column of the compatibility table (spec 4.8).
type StorageImpact string

// Storage impacts.
const (
	StorageSafe           StorageImpact = "safe"
	StorageBackfill       StorageImpact = "safe_backfilled"
	StorageNeedsMigration StorageImpact = "needs_migration"
	StorageIndexBuild     StorageImpact = "index_build"
	StorageIndexDrop      StorageImpact = "index_drop"
)

// APIImpact is the public API views column of the compatibility table.
type APIImpact string

// API impacts.
const (
	APISafe              APIImpact = "safe"
	APIBreaking          APIImpact = "breaking"
	APIBreakingCreate    APIImpact = "breaking_for_create"
	APIBreakingStrict    APIImpact = "breaking_for_strict_readers"
	APIBreakingAudience  APIImpact = "breaking_for_audience"
	APIExposureForReview APIImpact = "flagged_data_exposure"
)

// ChangeKind names the kind of schema change.
type ChangeKind string

// Change kinds (rows of spec 4.8; a rename is reported as remove + add).
const (
	ChangeAddOptional        ChangeKind = "add_optional_field"
	ChangeAddRequired        ChangeKind = "add_required_field"
	ChangeAddRequiredDefault ChangeKind = "add_required_field_with_default"
	ChangeRemove             ChangeKind = "remove_field"
	ChangeWiden              ChangeKind = "widen_type"
	ChangeNarrow             ChangeKind = "narrow_type"
	ChangeVisibilityNarrowed ChangeKind = "visibility_narrowed"
	ChangeVisibilityWidened  ChangeKind = "visibility_widened"
	ChangeAddIndex           ChangeKind = "add_index"
	ChangeRemoveIndex        ChangeKind = "remove_index"
	ChangeOther              ChangeKind = "other"
)

// Change is one classified difference between two versions of a type.
type Change struct {
	Path      string         `json:"path"`
	Kind      ChangeKind     `json:"kind"`
	Storage   StorageImpact  `json:"storage"`
	API       APIImpact      `json:"api"`
	Audiences []api.Audience `json:"audiences,omitempty"` // for breaking_for_audience
	Detail    string         `json:"detail,omitempty"`
}

// Breaking reports whether applying the change needs a migration or breaks
// a published view (spec 4.8: apply refuses these without a migration or
// --allow-breaking).
func (c Change) Breaking() bool {
	return c.Storage == StorageNeedsMigration || c.API == APIBreaking || c.API == APIBreakingCreate ||
		c.API == APIBreakingAudience
}

// CheckCompatibility compares the previous and new compiled versions of a
// type field by field (spec 4.8).
func CheckCompatibility(prev, next *api.CompiledType) []Change {
	var out []Change
	for _, nf := range next.TopFields() {
		pf, existed := prev.Field(nf.Path)
		if !existed || !pf.Top {
			switch {
			case !nf.Required:
				out = append(out, Change{Path: nf.Path, Kind: ChangeAddOptional, Storage: StorageSafe, API: APISafe})
			case nf.Default != nil:
				out = append(out, Change{Path: nf.Path, Kind: ChangeAddRequiredDefault, Storage: StorageBackfill, API: APISafe})
			default:
				out = append(out, Change{Path: nf.Path, Kind: ChangeAddRequired, Storage: StorageNeedsMigration, API: APIBreakingCreate})
			}
			continue
		}
		name := nf.Path[1:]
		out = append(out, compareField(pf, nf, rawProperty(prev, name), rawProperty(next, name))...)
	}
	for _, pf := range prev.TopFields() {
		if _, ok := next.Field(pf.Path); !ok {
			out = append(out, Change{Path: pf.Path, Kind: ChangeRemove, Storage: StorageNeedsMigration, API: APIBreaking})
		}
	}
	return out
}

func compareField(p, n *api.FieldDef, praw, nraw map[string]any) []Change {
	var out []Change
	// Visibility (rows 8, 9).
	if n.Visibility != p.Visibility {
		var lost []api.Audience
		gained := false
		for _, a := range api.Audiences {
			before, after := a.CanRead(p.Visibility), a.CanRead(n.Visibility)
			if before && !after {
				lost = append(lost, a)
			}
			if !before && after {
				gained = true
			}
		}
		switch {
		case len(lost) > 0:
			out = append(out, Change{Path: n.Path, Kind: ChangeVisibilityNarrowed, Storage: StorageSafe, API: APIBreakingAudience, Audiences: lost,
				Detail: fmt.Sprintf("%s -> %s", p.Visibility, n.Visibility)})
		case gained:
			out = append(out, Change{Path: n.Path, Kind: ChangeVisibilityWidened, Storage: StorageSafe, API: APIExposureForReview,
				Detail: fmt.Sprintf("%s -> %s", p.Visibility, n.Visibility)})
		}
	}
	// Index (row 10).
	if n.Indexed && !p.Indexed {
		out = append(out, Change{Path: n.Path, Kind: ChangeAddIndex, Storage: StorageIndexBuild, API: APISafe})
	}
	if !n.Indexed && p.Indexed {
		out = append(out, Change{Path: n.Path, Kind: ChangeRemoveIndex, Storage: StorageIndexDrop, API: APIBreaking,
			Detail: "field is no longer filterable or sortable"})
	}
	// Type (rows 6, 7).
	widened, narrowed, detail := compareTypes(p, n, praw, nraw)
	switch {
	case narrowed:
		out = append(out, Change{Path: n.Path, Kind: ChangeNarrow, Storage: StorageNeedsMigration, API: APIBreaking, Detail: detail})
	case widened:
		out = append(out, Change{Path: n.Path, Kind: ChangeWiden, Storage: StorageSafe, API: APIBreakingStrict, Detail: detail})
	}
	return out
}

// compareTypes classifies kind and constraint differences.
func compareTypes(p, n *api.FieldDef, praw, nraw map[string]any) (widened, narrowed bool, detail string) {
	if p.Kind != n.Kind {
		switch {
		case p.Kind == kernel.KindInt64 && (n.Kind == kernel.KindDecimal):
			return true, false, "int64 -> decimal"
		case p.Kind == kernel.KindEnum && n.Kind == kernel.KindString:
			return true, false, "enum -> string"
		}
		return false, true, fmt.Sprintf("%s -> %s", p.Kind, n.Kind)
	}
	if p.Required != n.Required {
		if n.Required {
			if n.Default == nil {
				narrowed, detail = true, "field became required"
			}
		} else {
			widened, detail = true, "field became optional"
		}
	}
	if p.Nullable != n.Nullable {
		if n.Nullable {
			widened, detail = true, "field became nullable"
		} else {
			narrowed, detail = true, "field became non-nullable"
		}
	}
	if p.Kind == kernel.KindEnum {
		for _, v := range p.Type.Enum {
			if !slices.Contains(n.Type.Enum, v) {
				return false, true, fmt.Sprintf("enum value %q removed", v)
			}
		}
		if len(n.Type.Enum) > len(p.Type.Enum) {
			widened, detail = true, "enum values added"
		}
	}
	if narrowed {
		return false, true, detail
	}
	for _, cmp := range []func() (bool, bool, string){
		func() (bool, bool, string) { return compareConstraints(p.Type, n.Type) },
		func() (bool, bool, string) { return compareConstraintsRaw(praw, nraw) },
	} {
		w, nw, d := cmp()
		if nw {
			return false, true, d
		}
		if w {
			widened, detail = true, d
		}
	}
	return widened, false, detail
}

// compareConstraints compares the numeric and string constraints recorded
// in each field's source schema.
func compareConstraints(p, n *api.ValueType) (widened, narrowed bool, detail string) {
	if p.Format != n.Format {
		if n.Format == "" {
			return true, false, "format removed"
		}
		return false, true, "format " + n.Format + " added"
	}
	if p.Kind == kernel.KindObject && n.Kind == kernel.KindObject && !reflect.DeepEqual(propNames(p), propNames(n)) {
		return false, true, "nested properties changed"
	}
	if p.Kind == kernel.KindList && n.Kind == kernel.KindList && p.Elem.Kind != n.Elem.Kind {
		return false, true, "list element kind changed"
	}
	return false, false, ""
}

func propNames(t *api.ValueType) []string {
	out := make([]string, len(t.Props))
	for i, p := range t.Props {
		out[i] = p.Name
	}
	return out
}

// compareConstraintsRaw compares min/max style constraints of two raw
// property schemas. Tighter bounds narrow; looser bounds widen.
func compareConstraintsRaw(prev, next map[string]any) (widened, narrowed bool, detail string) {
	lower := []string{"minimum", "exclusiveMinimum", "minLength", "minItems"}
	upper := []string{"maximum", "exclusiveMaximum", "maxLength", "maxItems"}
	num := func(m map[string]any, k string) (float64, bool) {
		v, ok := m[k]
		if !ok {
			return 0, false
		}
		f, err := toFloat(v)
		return f, err == nil
	}
	for _, k := range lower {
		pv, pok := num(prev, k)
		nv, nok := num(next, k)
		switch {
		case !pok && nok, pok && nok && nv > pv:
			return false, true, k + " tightened"
		case pok && !nok, pok && nok && nv < pv:
			widened, detail = true, k+" relaxed"
		}
	}
	for _, k := range upper {
		pv, pok := num(prev, k)
		nv, nok := num(next, k)
		switch {
		case !pok && nok, pok && nok && nv < pv:
			return false, true, k + " tightened"
		case pok && !nok, pok && nok && nv > pv:
			widened, detail = true, k+" relaxed"
		}
	}
	pp, _ := prev["pattern"].(string)
	np, _ := next["pattern"].(string)
	if pp != np {
		if np == "" {
			widened, detail = true, "pattern removed"
		} else {
			return false, true, "pattern changed"
		}
	}
	return widened, narrowed, detail
}

// rawProperty returns the source schema of a top-level property.
func rawProperty(ct *api.CompiledType, name string) map[string]any {
	var doc map[string]any
	if err := json.Unmarshal(ct.Source, &doc); err != nil {
		return nil
	}
	props, _ := doc["properties"].(map[string]any)
	m, _ := props[name].(map[string]any)
	return m
}
