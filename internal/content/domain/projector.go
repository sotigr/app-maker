package domain

import (
	"strings"

	contentapi "app-maker/internal/content/api"
	"app-maker/internal/kernel"
	schemaapi "app-maker/internal/schema/api"
)

// Project projects a snapshot through the read view of audience a (spec
// 4.6). Only readable top-level fields are emitted, in wire format. When
// sel is non-empty, the output is limited to those paths (spec 7.1 select);
// unreadable selections are silently dropped.
func Project(ct *schemaapi.CompiledType, s Snapshot, a schemaapi.Audience, sel []string) contentapi.FeatureView {
	view := ct.View(a, schemaapi.DirRead)
	var allow map[string]bool
	if len(sel) > 0 {
		allow = map[string]bool{}
		for _, p := range sel {
			allow["/"+strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)[0]] = true
		}
	}
	data := make(map[string]any, len(s.Fields))
	for path, v := range s.Fields {
		if !view.Readable[path] || (allow != nil && !allow[path]) {
			continue
		}
		f, ok := ct.Field(path)
		if !ok {
			continue
		}
		data[path[1:]] = f.Type.Wire(v)
	}
	out := contentapi.FeatureView{
		ID: s.ID.String(), Type: ct.Name, Version: s.Version,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, Data: data,
	}
	if s.ParentID != nil {
		p := s.ParentID.String()
		out.Parent = &p
	}
	return out
}

// ProjectFields returns the wire values of the given paths readable by
// audience a; used for event deltas (spec 9.6).
func ProjectFields(ct *schemaapi.CompiledType, fields map[string]kernel.Value, a schemaapi.Audience) map[string]any {
	view := ct.View(a, schemaapi.DirRead)
	out := map[string]any{}
	for path, v := range fields {
		if !view.Readable[path] {
			continue
		}
		if f, ok := ct.Field(path); ok {
			out[path[1:]] = f.Type.Wire(v)
		}
	}
	return out
}

// WireDocument returns the full storage document in wire form, used for
// storage validation and change events.
func WireDocument(ct *schemaapi.CompiledType, fields map[string]kernel.Value) map[string]any {
	out := make(map[string]any, len(fields))
	for path, v := range fields {
		if f, ok := ct.Field(path); ok && f.Persist {
			out[path[1:]] = f.Type.Wire(v)
		}
	}
	return out
}

// ExprDocument returns the document as expressions see it: native
// values plus "id", "parentId", and "version".
func ExprDocument(s Snapshot) map[string]any {
	out := make(map[string]any, len(s.Fields)+3)
	for path, v := range s.Fields {
		out[path[1:]] = kernel.Native(v)
	}
	out["id"] = s.ID.String()
	if s.ParentID != nil {
		out["parentId"] = s.ParentID.String()
	} else {
		out["parentId"] = ""
	}
	out["version"] = int64(s.Version)
	return out
}
