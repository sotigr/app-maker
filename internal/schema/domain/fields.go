package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"organon/internal/kernel"
	"organon/internal/schema/api"
)

const (
	decimalPattern = `^-?[0-9]+(\.[0-9]+)?$`
	int64Pattern   = `^-?[0-9]{1,19}$`
)

type typeBuilder struct {
	c     *Compiler
	file  string
	doc   map[string]any
	probs *Problems
	ct    *api.CompiledType
	// wire holds the wire JSON Schema of each top-level field by path.
	wire      map[string]map[string]any
	aggregate map[string]map[string]any
	machines  map[string]map[string]any
}

func newTypeBuilder(c *Compiler, file string, id api.TypeID, name string, doc map[string]any, probs *Problems) *typeBuilder {
	title, _ := doc["title"].(string)
	src, _ := json.Marshal(doc)
	return &typeBuilder{
		c: c, file: file, doc: doc, probs: probs,
		ct: &api.CompiledType{
			ID: id, Name: name, Version: id.Version(), Title: title,
			Repository:    "default",
			Views:         map[api.ViewKey]*api.View{},
			StateMachines: map[string]*api.StateMachine{},
			Source:        src,
		},
		wire:      map[string]map[string]any{},
		aggregate: map[string]map[string]any{},
		machines:  map[string]map[string]any{},
	}
}

func (b *typeBuilder) errf(ptr, format string, args ...any) {
	*b.probs = append(*b.probs, Problem{File: b.file, Pointer: ptr, Message: fmt.Sprintf(format, args...)})
}

// buildFields runs compiler steps 3 and 5: kind inference and the field
// table, plus type-level vocabulary.
func (b *typeBuilder) buildFields() {
	ct := b.ct
	for _, t := range stringList(b.doc["x-traits"]) {
		ct.Traits = append(ct.Traits, api.TypeID(t))
	}
	for i, c := range stringList(b.doc["x-allowedChildren"]) {
		id, err := api.ParseTypeID(c)
		if err != nil {
			b.errf(fmt.Sprintf("/x-allowedChildren/%d", i), "%v", err)
			continue
		}
		ct.Children = append(ct.Children, id.Base())
	}
	if r, ok := b.doc["x-repository"]; ok {
		s, _ := r.(string)
		if s == "" {
			b.errf("/x-repository", "x-repository must be a non-empty string")
		}
		ct.Repository = s
	}
	if r, ok := b.doc["x-retention"]; ok {
		s, _ := r.(string)
		d, err := parseDuration(s)
		if err != nil {
			b.errf("/x-retention", "%v", err)
		}
		ct.Retention = d
	}

	props, _ := b.doc["properties"].(map[string]any)
	required := map[string]bool{}
	for _, r := range stringList(b.doc["required"]) {
		required[r] = true
		if _, ok := props[r]; !ok {
			b.errf("/required", "required property %q is not defined", r)
		}
	}
	root := &api.ValueType{Kind: kernel.KindObject}
	for _, name := range sortedKeys(props) {
		node, _ := props[name].(map[string]any)
		ptr := "/properties/" + escape(name)
		vt, fields := b.valueType(ptr, "/"+name, node)
		if vt == nil {
			continue
		}
		root.Props = append(root.Props, api.PropType{Name: name, Type: vt, Required: required[name]})
		fd := b.topField(ptr, name, node, vt, required[name])
		ct.Fields = append(ct.Fields, fd)
		for _, nf := range fields {
			nf.Visibility = fd.Visibility
			nf.Write = fd.Write
			ct.Fields = append(ct.Fields, nf)
		}
		b.wire["/"+name] = wireSchema(node, vt)
	}
	ct.Root = root
	sort.Slice(ct.Fields, func(i, j int) bool { return ct.Fields[i].Path < ct.Fields[j].Path })

	if u, ok := b.doc["x-uniqueName"]; ok {
		s, _ := u.(string)
		path := "/" + strings.TrimPrefix(s, "/")
		f, found := ct.Field(path)
		if !found || !f.Top || f.Kind != kernel.KindString {
			b.errf("/x-uniqueName", "x-uniqueName must name a top-level string field")
		} else {
			ct.UniqueName = path
		}
	}
}

// valueType infers the kind of a schema node (spec 4.4 step 3) and returns
// its value type plus FieldDefs for nested object properties.
func (b *typeBuilder) valueType(ptr, path string, node map[string]any) (*api.ValueType, []api.FieldDef) {
	typ, nullable := readType(node)
	vt := &api.ValueType{Nullable: nullable}
	xkind, hasXKind := node["x-kind"].(string)
	_, hasEnum := node["enum"]
	constVal, hasConst := node["const"]
	format, _ := node["format"].(string)
	var nested []api.FieldDef

	switch {
	case hasXKind:
		k, ok := kernel.ParseKind(xkind)
		allowed := map[kernel.Kind][]string{
			kernel.KindInt64:   {"", "string", "integer"},
			kernel.KindDecimal: {"", "string"},
			kernel.KindTime:    {"", "string"},
			kernel.KindRef:     {"", "string"},
			kernel.KindBlob:    {"", "object"},
			kernel.KindJSON:    nil,
		}
		types, listed := allowed[k]
		if !ok || !listed {
			b.errf(ptr+"/x-kind", "x-kind must be one of int64, decimal, time, ref, blob, json")
			return nil, nil
		}
		if k != kernel.KindJSON && !contains(types, typ) {
			b.errf(ptr+"/x-kind", "x-kind %s conflicts with type %q", xkind, typ)
			return nil, nil
		}
		if hasEnum && k != kernel.KindJSON {
			b.errf(ptr+"/enum", "enum cannot be combined with x-kind %s", xkind)
			return nil, nil
		}
		if k == kernel.KindTime && format != "" && format != "date-time" {
			b.errf(ptr+"/format", "x-kind time requires format date-time")
		}
		vt.Kind = k
		vt.NumberWire = k == kernel.KindInt64 && typ == "integer"
		if k == kernel.KindRef {
			if r, ok := node["x-ref"]; ok {
				s, _ := r.(string)
				id, err := api.ParseTypeID(s)
				if err != nil {
					b.errf(ptr+"/x-ref", "%v", err)
				}
				vt.RefTarget = id.Base()
			}
		}
	case hasEnum:
		if typ != "" && typ != "string" {
			b.errf(ptr+"/enum", "enum values must be strings")
			return nil, nil
		}
		vals, _ := node["enum"].([]any)
		if len(vals) == 0 {
			b.errf(ptr+"/enum", "enum must not be empty")
			return nil, nil
		}
		for _, v := range vals {
			switch s := v.(type) {
			case string:
				vt.Enum = append(vt.Enum, s)
			case nil:
				vt.Nullable = true
			default:
				b.errf(ptr+"/enum", "enum values must be strings")
				return nil, nil
			}
		}
		vt.Kind = kernel.KindEnum
	case hasConst && (typ == "" || typ == "string"):
		s, ok := constVal.(string)
		if !ok {
			b.errf(ptr+"/const", "const without type must be a string")
			return nil, nil
		}
		vt.Kind = kernel.KindEnum
		vt.Enum = []string{s}
	default:
		switch typ {
		case "string":
			if format == "date-time" {
				vt.Kind = kernel.KindTime
			} else {
				vt.Kind = kernel.KindString
				vt.Format = format
			}
		case "integer":
			vt.Kind = kernel.KindInt64
			vt.NumberWire = true
		case "number":
			vt.Kind = kernel.KindFloat
		case "boolean":
			vt.Kind = kernel.KindBool
		case "array":
			items, ok := node["items"].(map[string]any)
			if !ok {
				b.errf(ptr, "array needs an items schema")
				return nil, nil
			}
			elem, sub := b.valueType(ptr+"/items", path+"/*", items)
			if elem == nil {
				return nil, nil
			}
			if elem.Kind == kernel.KindList {
				b.errf(ptr+"/items", "lists of lists are not supported")
				return nil, nil
			}
			vt.Kind = kernel.KindList
			vt.Elem = elem
			nested = append(nested, sub...)
		case "object", "":
			if one, ok := node["oneOf"].([]any); ok {
				vt.Kind = kernel.KindObject
				vt.Union = b.union(ptr, path, one)
				if vt.Union == nil {
					return nil, nil
				}
				break
			}
			if typ == "" {
				b.errf(ptr, "cannot infer the kind: add type, enum, or x-kind")
				return nil, nil
			}
			vt.Kind = kernel.KindObject
			props, _ := node["properties"].(map[string]any)
			req := map[string]bool{}
			for _, r := range stringList(node["required"]) {
				req[r] = true
			}
			for _, name := range sortedKeys(props) {
				pn, _ := props[name].(map[string]any)
				pv, sub := b.valueType(ptr+"/properties/"+escape(name), path+"/"+name, pn)
				if pv == nil {
					continue
				}
				vt.Props = append(vt.Props, api.PropType{Name: name, Type: pv, Required: req[name]})
				nested = append(nested, api.FieldDef{
					Path: path + "/" + name, Kind: pv.Kind, Type: pv, Nullable: pv.Nullable,
					Required: req[name], RefTarget: pv.RefTarget, Persist: true,
				})
				nested = append(nested, sub...)
			}
		}
	}
	if d, ok := node["default"]; ok {
		if _, err := vt.Coerce(path, d); err != nil {
			b.errf(ptr+"/default", "default does not match the field type: %v", err)
		}
	}
	return vt, nested
}

func (b *typeBuilder) union(ptr, path string, branches []any) *api.UnionType {
	u := &api.UnionType{Variants: map[string]*api.ValueType{}}
	for i, br := range branches {
		bm, _ := br.(map[string]any)
		props, _ := bm["properties"].(map[string]any)
		for _, name := range sortedKeys(props) {
			if pm, ok := props[name].(map[string]any); ok {
				if c, ok := pm["const"].(string); ok {
					u.Tag = name
					obj := deepCopy(bm).(map[string]any)
					obj["type"] = "object"
					vt, _ := b.valueType(fmt.Sprintf("%s/oneOf/%d", ptr, i), path, obj)
					if vt == nil {
						return nil
					}
					u.Variants[c] = vt
					break
				}
			}
		}
	}
	return u
}

// topField builds the FieldDef of a top-level property with its field-level
// vocabulary (spec 4.2).
func (b *typeBuilder) topField(ptr, name string, node map[string]any, vt *api.ValueType, required bool) api.FieldDef {
	f := api.FieldDef{
		Path: "/" + name, Kind: vt.Kind, Type: vt, Nullable: vt.Nullable, Required: required,
		Default: node["default"], RefTarget: vt.RefTarget, Persist: true, Top: true,
	}
	f.Title, _ = node["title"].(string)
	f.Description, _ = node["description"].(string)
	f.ReadOnly = node["readOnly"] == true
	f.WriteOnly = node["writeOnly"] == true
	f.Immutable = node["x-immutable"] == true
	scalarish := vt.Kind.IsScalar() || (vt.Kind == kernel.KindList && vt.Elem.Kind.IsScalar())

	if v, ok := node["x-indexed"]; ok {
		f.Indexed = v == true
		if f.Indexed && !scalarish {
			b.errf(ptr+"/x-indexed", "x-indexed is only allowed on scalar fields and lists of scalars")
		}
	}
	if v, ok := node["x-searchable"]; ok {
		f.Searchable = v == true
		if f.Searchable && vt.Kind != kernel.KindString {
			b.errf(ptr+"/x-searchable", "x-searchable is only allowed on string fields")
		}
	}
	if v, ok := node["x-visibility"]; ok {
		s, _ := v.(string)
		vis, ok := api.ParseVisibility(s)
		if !ok {
			b.errf(ptr+"/x-visibility", "x-visibility must be public, owner, private, or system")
		}
		f.Visibility = vis
	}
	if v, ok := node["x-write"]; ok {
		s, _ := v.(string)
		w, err := api.ParseWriteRule(s)
		if err != nil {
			b.errf(ptr+"/x-write", "%v", err)
		}
		f.Write = w
	}
	if f.Visibility == api.VisSystem && f.Write.Kind == api.WriteAny {
		f.Write = api.WriteRule{Kind: api.WriteSystem}
	}
	isRef := vt.Kind == kernel.KindRef || (vt.Kind == kernel.KindList && vt.Elem.Kind == kernel.KindRef)
	if _, ok := node["x-ref"]; ok && vt.Kind != kernel.KindRef {
		b.errf(ptr+"/x-ref", "x-ref requires x-kind ref")
	}
	if v, ok := node["x-onDelete"]; ok {
		s, _ := v.(string)
		od, ok := api.ParseOnDelete(s)
		if !ok {
			b.errf(ptr+"/x-onDelete", "x-onDelete must be restrict, setNull, or dangle")
		}
		if !isRef {
			b.errf(ptr+"/x-onDelete", "x-onDelete requires a ref field")
		}
		if od == api.OnDeleteSetNull && !vt.Nullable {
			b.errf(ptr+"/x-onDelete", "x-onDelete setNull requires a nullable field")
		}
		f.OnDelete = od
	}
	if v, ok := node["x-computed"]; ok {
		s, _ := v.(string)
		if s == "" {
			b.errf(ptr+"/x-computed", "x-computed must be an expression string")
		}
		f.Computed = s
		f.ReadOnly = true
		f.Write = api.WriteRule{Kind: api.WriteSystem}
	}
	if v, ok := node["x-aggregate"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			b.errf(ptr+"/x-aggregate", "x-aggregate must be an object")
		} else {
			b.aggregate[f.Path] = m
		}
		f.ReadOnly = true
		f.Write = api.WriteRule{Kind: api.WriteSystem}
	}
	if v, ok := node["x-stream"]; ok {
		m, ok := v.(map[string]any)
		if !ok {
			b.errf(ptr+"/x-stream", "x-stream must be an object")
		} else {
			spec := &api.StreamSpec{}
			switch m["mode"] {
			case nil, "state":
			case "append":
				spec.Mode = api.StreamAppend
			default:
				b.errf(ptr+"/x-stream/mode", "mode must be state or append")
			}
			if r, ok := m["maxRate"]; ok {
				n, err := toFloat(r)
				if err != nil || n <= 0 {
					b.errf(ptr+"/x-stream/maxRate", "maxRate must be a positive number")
				}
				spec.MaxRate = n
			}
			for k := range m {
				if k != "mode" && k != "maxRate" {
					b.errf(ptr+"/x-stream/"+escape(k), "unknown x-stream member")
				}
			}
			f.Stream = spec
		}
	}
	if v, ok := node["x-persist"]; ok {
		f.Persist = v != false
		if !f.Persist {
			if f.Stream == nil {
				f.Stream = &api.StreamSpec{Mode: api.StreamState}
			} else if f.Stream.Mode != api.StreamState {
				b.errf(ptr+"/x-persist", "ephemeral fields use state mode")
			}
			if required {
				b.errf(ptr+"/x-persist", "ephemeral fields cannot be required")
			}
		}
	}
	if v, ok := node["x-stateMachine"]; ok {
		m, ok := v.(map[string]any)
		if !ok || vt.Kind != kernel.KindEnum {
			b.errf(ptr+"/x-stateMachine", "x-stateMachine requires an enum field and an object value")
		} else {
			b.machines[f.Path] = m
			if f.Default == nil {
				if init, ok := m["initial"].(string); ok {
					f.Default = init
				}
			}
		}
	}
	blobKeys := []string{"x-maxBytes", "x-contentTypes", "x-thumbnails"}
	for _, k := range blobKeys {
		if _, ok := node[k]; ok && vt.Kind != kernel.KindBlob {
			b.errf(ptr+"/"+k, "%s requires a blob field", k)
		}
	}
	if vt.Kind == kernel.KindBlob {
		spec := &api.BlobSpec{ContentTypes: stringList(node["x-contentTypes"])}
		if mb, ok := node["x-maxBytes"]; ok {
			n, err := toFloat(mb)
			if err != nil || n <= 0 {
				b.errf(ptr+"/x-maxBytes", "x-maxBytes must be a positive integer")
			}
			spec.MaxBytes = int64(n)
		}
		if th, ok := node["x-thumbnails"].([]any); ok {
			for _, x := range th {
				n, _ := toFloat(x)
				spec.Thumbnails = append(spec.Thumbnails, int(n))
			}
		}
		f.Blob = spec
	}
	if f.ReadOnly && f.WriteOnly {
		b.errf(ptr, "a field cannot be both readOnly and writeOnly")
	}
	return f
}

// wireSchema returns the published JSON Schema of a field: the authored
// schema without the x- vocabulary, with wire formats made explicit.
func wireSchema(node map[string]any, vt *api.ValueType) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"title", "description", "default", "readOnly", "writeOnly"} {
		if v, ok := node[k]; ok {
			out[k] = v
		}
	}
	typeOf := func(t string) any {
		if vt.Nullable {
			return []any{t, "null"}
		}
		return t
	}
	copyKeys := func(keys ...string) {
		for _, k := range keys {
			if v, ok := node[k]; ok {
				out[k] = v
			}
		}
	}
	switch vt.Kind {
	case kernel.KindString:
		out["type"] = typeOf("string")
		copyKeys("minLength", "maxLength", "pattern", "format", "const")
	case kernel.KindEnum:
		vals := make([]any, 0, len(vt.Enum)+1)
		for _, e := range vt.Enum {
			vals = append(vals, e)
		}
		if vt.Nullable {
			vals = append(vals, nil)
		}
		out["enum"] = vals
	case kernel.KindInt64:
		if vt.NumberWire {
			out["type"] = typeOf("integer")
			copyKeys("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum")
		} else {
			out["type"] = typeOf("string")
			out["pattern"] = int64Pattern
		}
	case kernel.KindFloat:
		out["type"] = typeOf("number")
		copyKeys("minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum")
	case kernel.KindDecimal:
		out["type"] = typeOf("string")
		out["pattern"] = decimalPattern
	case kernel.KindBool:
		out["type"] = typeOf("boolean")
	case kernel.KindTime:
		out["type"] = typeOf("string")
		out["format"] = "date-time"
	case kernel.KindRef:
		out["type"] = typeOf("string")
		out["format"] = "uuid"
	case kernel.KindBlob:
		out["type"] = typeOf("object")
		out["properties"] = map[string]any{
			"id":          map[string]any{"type": "string"},
			"size":        map[string]any{"type": "integer", "minimum": 0},
			"contentType": map[string]any{"type": "string"},
		}
		out["required"] = []any{"id", "size", "contentType"}
		out["additionalProperties"] = false
	case kernel.KindJSON:
		// freeform
	case kernel.KindList:
		out["type"] = typeOf("array")
		copyKeys("minItems", "maxItems", "uniqueItems")
		items, _ := node["items"].(map[string]any)
		out["items"] = wireSchema(items, vt.Elem)
	case kernel.KindObject:
		if vt.Union != nil {
			branches, _ := node["oneOf"].([]any)
			var outBranches []any
			for _, br := range branches {
				bm, _ := br.(map[string]any)
				props, _ := bm["properties"].(map[string]any)
				for _, name := range sortedKeys(props) {
					if pm, ok := props[name].(map[string]any); ok {
						if c, ok := pm["const"].(string); ok {
							outBranches = append(outBranches, objectWire(bm, vt.Union.Variants[c], false))
							break
						}
					}
				}
			}
			out["oneOf"] = outBranches
			if vt.Nullable {
				out["oneOf"] = append(outBranches, map[string]any{"type": "null"})
			}
			break
		}
		for k, v := range objectWire(node, vt, vt.Nullable) {
			out[k] = v
		}
	}
	return out
}

func objectWire(node map[string]any, vt *api.ValueType, nullable bool) map[string]any {
	out := map[string]any{"type": "object", "additionalProperties": false}
	if nullable {
		out["type"] = []any{"object", "null"}
	}
	props, _ := node["properties"].(map[string]any)
	wp := map[string]any{}
	for _, p := range vt.Props {
		pn, _ := props[p.Name].(map[string]any)
		wp[p.Name] = wireSchema(pn, p.Type)
	}
	out["properties"] = wp
	if req, ok := node["required"].([]any); ok && len(req) > 0 {
		out["required"] = req
	}
	return out
}

func readType(node map[string]any) (typ string, nullable bool) {
	switch t := node["type"].(type) {
	case string:
		return t, false
	case []any:
		if len(t) == 2 {
			s, _ := t[0].(string)
			return s, t[1] == "null"
		}
	}
	return "", false
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case json.Number:
		return n.Float64()
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	}
	return 0, fmt.Errorf("not a number")
}

// parseDuration accepts Go durations plus a "d" (days) suffix.
func parseDuration(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}
