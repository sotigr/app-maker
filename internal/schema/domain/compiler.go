// Package domain implements the Schema context's domain: the JSON Schema
// profile, the x- vocabulary, the compiler, views, and the compatibility
// checker (spec 4).
package domain

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"sort"
	"strings"

	accessapi "organon/internal/access/api"
	rt "organon/internal/runtime/api"
	"organon/internal/schema/api"
)

//go:embed system/*.json
var systemFS embed.FS

// Source is one schema document.
type Source struct {
	File string
	Data []byte
}

// ValidatorFactory compiles a view's JSON Schema into a validator. It is
// implemented in infrastructure (jsonschema adapter).
type ValidatorFactory interface {
	New(id string, schema []byte) (api.Validator, error)
}

// Compiler compiles FeatureTypes and traits (spec 4.4).
type Compiler struct {
	exprs      rt.Compiler
	validators ValidatorFactory
}

// NewCompiler returns a compiler using the given expression compiler and
// validator factory.
func NewCompiler(exprs rt.Compiler, validators ValidatorFactory) *Compiler {
	return &Compiler{exprs: exprs, validators: validators}
}

// Trait is a compiled trait: a reusable set of properties.
type Trait struct {
	ID       api.TypeID
	File     string
	Props    map[string]any // resolved property schemas
	Required []string
}

// Input is a set of documents compiled together.
type Input struct {
	Traits []Source
	Types  []Source
}

type parsedDoc struct {
	file string
	id   api.TypeID
	doc  map[string]any
	sys  bool
}

// SystemSources returns the built-in system traits and types (spec 3.7, 4.7).
func SystemSources() (traits, types []Source) {
	entries, err := fs.ReadDir(systemFS, "system")
	if err != nil {
		panic(err) // embedded at build time
	}
	for _, e := range entries {
		data, err := systemFS.ReadFile("system/" + e.Name())
		if err != nil {
			panic(err)
		}
		src := Source{File: "sys:" + e.Name(), Data: data}
		if strings.HasPrefix(e.Name(), "trait-") {
			traits = append(traits, src)
		} else {
			types = append(types, src)
		}
	}
	return traits, types
}

// Compile compiles the system documents plus the input into a Registry.
// All problems found are returned together.
func (c *Compiler) Compile(in Input) (*Registry, Problems) {
	var probs Problems
	sysTraits, sysTypes := SystemSources()

	parse := func(srcs []Source, wantTrait, sys bool) []parsedDoc {
		var out []parsedDoc
		for _, s := range srcs {
			p, ok := parseDoc(s, wantTrait, sys, &probs)
			if ok {
				out = append(out, p)
			}
		}
		return out
	}
	traitDocs := append(parse(sysTraits, true, true), parse(in.Traits, true, false)...)
	typeDocs := append(parse(sysTypes, false, true), parse(in.Types, false, false)...)

	traits := map[api.TypeID]*Trait{}
	for _, d := range traitDocs {
		if _, dup := traits[d.id]; dup {
			probs = append(probs, Problem{File: d.file, Pointer: "/$id", Message: fmt.Sprintf("duplicate trait %s", d.id)})
			continue
		}
		props, _ := d.doc["properties"].(map[string]any)
		traits[d.id] = &Trait{ID: d.id, File: d.file, Props: props, Required: stringList(d.doc["required"])}
	}

	builders := map[api.TypeID]*typeBuilder{}
	names := map[string]api.TypeID{}
	var order []api.TypeID
	for _, d := range typeDocs {
		base := d.id.Base()
		if _, dup := builders[base]; dup {
			probs = append(probs, Problem{File: d.file, Pointer: "/$id", Message: fmt.Sprintf("duplicate type %s", base)})
			continue
		}
		name := shortName(d.id)
		if other, dup := names[name]; dup {
			probs = append(probs, Problem{File: d.file, Pointer: "/$id", Message: fmt.Sprintf("type name %q is already used by %s", name, other)})
			continue
		}
		names[name] = base
		merged, ok := mergeTraits(d, traits, &probs)
		if !ok {
			continue
		}
		b := newTypeBuilder(c, d.file, d.id, name, merged, &probs)
		b.buildFields()
		builders[base] = b
		order = append(order, base)
	}

	// Second pass: cross-type checks, expressions, views.
	reg := newRegistry(traits)
	for _, base := range order {
		reg.add(builders[base].ct)
	}
	for _, base := range order {
		b := builders[base]
		b.checkReferences(reg)
		b.compileExpressions(reg)
		b.buildViews()
	}
	if len(probs) > 0 {
		return nil, probs
	}
	reg.freeze()
	return reg, nil
}

func shortName(id api.TypeID) string {
	if m := id.Module(); m != "" {
		return m + "." + id.Name()
	}
	return id.Name()
}

// parseDoc decodes, profile-checks and $ref-resolves one document.
func parseDoc(s Source, wantTrait, sys bool, probs *Problems) (parsedDoc, bool) {
	dec := json.NewDecoder(bytes.NewReader(s.Data))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		*probs = append(*probs, Problem{File: s.File, Message: "invalid JSON: " + err.Error()})
		return parsedDoc{}, false
	}
	doc, ok := raw.(map[string]any)
	if !ok {
		*probs = append(*probs, Problem{File: s.File, Message: "schema must be a JSON object"})
		return parsedDoc{}, false
	}
	idStr, _ := doc["$id"].(string)
	id, err := api.ParseTypeID(idStr)
	if err != nil {
		*probs = append(*probs, Problem{File: s.File, Pointer: "/$id", Message: err.Error()})
		return parsedDoc{}, false
	}
	if id.IsTrait() != wantTrait {
		what := "a feature type"
		if wantTrait {
			what = "a trait"
		}
		*probs = append(*probs, Problem{File: s.File, Pointer: "/$id", Message: fmt.Sprintf("%s must be %s", id, what)})
		return parsedDoc{}, false
	}
	if id.IsSystem() && !sys {
		*probs = append(*probs, Problem{File: s.File, Pointer: "/$id", Message: "the urn:sys: namespace is reserved"})
		return parsedDoc{}, false
	}
	if !wantTrait && id.Version() == 0 && !sys {
		*probs = append(*probs, Problem{File: s.File, Pointer: "/$id", Message: "feature type $id needs a version suffix /v<N>"})
		return parsedDoc{}, false
	}
	if t, ok := doc["type"]; ok && t != "object" {
		*probs = append(*probs, Problem{File: s.File, Pointer: "/type", Message: `document type must be "object"`})
	}
	before := len(*probs)
	pos := posTypeRoot
	if wantTrait {
		pos = posTraitRoot
	}
	var local Problems
	checkProfile(doc, "", pos, &local)
	for _, p := range local {
		p.File = s.File
		*probs = append(*probs, p)
	}
	defs, _ := doc["$defs"].(map[string]any)
	resolved, err := resolveRefs(doc, defs, nil)
	if err != nil {
		*probs = append(*probs, Problem{File: s.File, Message: err.Error()})
	}
	if len(*probs) > before {
		return parsedDoc{}, false
	}
	rm := resolved.(map[string]any)
	delete(rm, "$defs")
	return parsedDoc{file: s.File, id: id, doc: rm, sys: sys}, true
}

// resolveRefs inlines local $refs; recursive references are rejected
// (spec 4.1).
func resolveRefs(node any, defs map[string]any, stack []string) (any, error) {
	switch n := node.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/$defs/")
			for _, s := range stack {
				if s == name {
					return nil, fmt.Errorf("recursive $ref to #/$defs/%s", name)
				}
			}
			def, ok := defs[name]
			if !ok {
				return nil, fmt.Errorf("$ref to unknown definition #/$defs/%s", name)
			}
			target, err := resolveRefs(def, defs, append(stack, name))
			if err != nil {
				return nil, err
			}
			out := deepCopy(target).(map[string]any)
			for k, v := range n { // sibling keywords override the definition
				if k != "$ref" {
					rv, err := resolveRefs(v, defs, stack)
					if err != nil {
						return nil, err
					}
					out[k] = rv
				}
			}
			return out, nil
		}
		out := make(map[string]any, len(n))
		for k, v := range n {
			if k == "$defs" {
				out[k] = v
				continue
			}
			rv, err := resolveRefs(v, defs, stack)
			if err != nil {
				return nil, err
			}
			out[k] = rv
		}
		return out, nil
	case []any:
		out := make([]any, len(n))
		for i, v := range n {
			rv, err := resolveRefs(v, defs, stack)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	}
	return node, nil
}

// mergeTraits merges trait properties into the type document (spec 4.4
// step 2). A property defined by two sources must be identical.
func mergeTraits(d parsedDoc, traits map[api.TypeID]*Trait, probs *Problems) (map[string]any, bool) {
	doc := deepCopy(d.doc).(map[string]any)
	props, _ := doc["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	source := map[string]string{}
	for name := range props {
		source[name] = string(d.id)
	}
	required := stringList(doc["required"])
	ok := true
	traitIDs := stringList(doc["x-traits"])
	for i, tid := range traitIDs {
		tr, found := traits[api.TypeID(tid)]
		if !found {
			*probs = append(*probs, Problem{File: d.file, Pointer: fmt.Sprintf("/x-traits/%d", i), Message: fmt.Sprintf("unknown trait %s", tid)})
			ok = false
			continue
		}
		for _, name := range sortedKeys(tr.Props) {
			p := tr.Props[name]
			if existing, dup := props[name]; dup {
				if !reflect.DeepEqual(existing, p) {
					*probs = append(*probs, Problem{File: d.file, Pointer: "/properties/" + escape(name),
						Message: fmt.Sprintf("property %q is defined differently by %s and %s", name, source[name], tid)})
					ok = false
				}
				continue
			}
			props[name] = deepCopy(p)
			source[name] = tid
		}
		required = append(required, tr.Required...)
	}
	doc["properties"] = props
	if len(required) > 0 {
		sort.Strings(required)
		required = dedupe(required)
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		doc["required"] = req
	}
	return doc, ok
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = deepCopy(val)
		}
		return out
	}
	return v
}

func stringList(v any) []string {
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, x := range l {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func dedupe(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// guardEnv is the environment of state-machine guards and action policies.
func guardEnv(ct *api.CompiledType) rt.Env {
	return rt.Env{
		Vars: map[string]*rt.Type{
			"principal": accessapi.PrincipalExprType(),
			"feature":   ct.ExprType(),
			"input":     rt.OpenMap(),
		},
		Funcs: []string{rt.FnNow, rt.FnIsOwner, rt.FnHasRole, rt.FnInGroup},
	}
}
