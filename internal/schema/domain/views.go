package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
	"organon/internal/schema/api"
)

// checkReferences validates x-ref targets, x-allowedChildren, and the
// repository rules of spec 5.4.
func (b *typeBuilder) checkReferences(reg *Registry) {
	ct := b.ct
	for i, c := range ct.Children {
		targets := reg.resolveAll(c)
		if len(targets) == 0 && !reg.isKnownTrait(c) {
			b.errf(fmt.Sprintf("/x-allowedChildren/%d", i), "unknown type or trait %s", c)
			continue
		}
		for _, t := range targets {
			if t.Repository != ct.Repository {
				b.errf(fmt.Sprintf("/x-allowedChildren/%d", i),
					"child type %s is stored in repository %q but the parent is in %q; children must share the parent's repository", t.ID, t.Repository, ct.Repository)
			}
		}
	}
	for _, f := range ct.Fields {
		target := f.RefTarget
		onDelete := f.OnDelete
		if f.Kind == kernel.KindList && f.Type.Elem.Kind == kernel.KindRef {
			target = f.Type.Elem.RefTarget
		}
		if target == "" {
			continue
		}
		ptr := "/properties/" + escape(f.Path[1:]) + "/x-ref"
		targets := reg.resolveAll(target)
		if len(targets) == 0 && !reg.isKnownTrait(target) {
			b.errf(ptr, "unknown ref target %s", target)
			continue
		}
		for _, t := range targets {
			if t.Repository != ct.Repository && onDelete != api.OnDeleteDangle {
				b.errf(ptr, "references across repositories must use x-onDelete: dangle")
				break
			}
		}
	}
}

// compileExpressions compiles x-computed, state-machine guards, and
// x-aggregate filters (spec 4.4 step 4).
func (b *typeBuilder) compileExpressions(reg *Registry) {
	ct := b.ct
	featureEnv := ComputedEnv(ct)
	for _, f := range ct.TopFields() {
		if f.Computed == "" {
			continue
		}
		p, err := b.c.exprs.Compile(f.Computed, featureEnv, f.Type.ExprType(), !f.Required || f.Nullable)
		if err != nil {
			b.errf("/properties/"+escape(f.Path[1:])+"/x-computed", "%v", err)
			continue
		}
		ct.Computed = append(ct.Computed, api.ComputedField{Path: f.Path, Program: p, Type: f.Type})
	}

	for _, path := range sortedKeysMap(b.sequences) {
		b.sequence(path, b.sequences[path], featureEnv)
	}

	for _, path := range sortedKeysMap(b.machines) {
		m := b.machines[path]
		ptr := "/properties/" + escape(path[1:]) + "/x-stateMachine"
		f, _ := ct.Field(path)
		sm := &api.StateMachine{Path: path}
		sm.Initial, _ = m["initial"].(string)
		if !contains(f.Type.Enum, sm.Initial) {
			b.errf(ptr+"/initial", "initial state %q is not in the enum", sm.Initial)
		}
		trs, _ := m["transitions"].([]any)
		if len(trs) == 0 {
			b.errf(ptr+"/transitions", "a state machine needs at least one transition")
		}
		seen := map[string]bool{}
		for i, tr := range trs {
			tm, _ := tr.(map[string]any)
			tp := fmt.Sprintf("%s/transitions/%d", ptr, i)
			t := api.Transition{}
			t.From, _ = tm["from"].(string)
			t.To, _ = tm["to"].(string)
			t.Action, _ = tm["action"].(string)
			t.Guard, _ = tm["guard"].(string)
			for _, s := range []string{t.From, t.To} {
				if !contains(f.Type.Enum, s) {
					b.errf(tp, "state %q is not in the enum", s)
				}
			}
			if t.Action == "" {
				b.errf(tp+"/action", "transition needs an action name")
			}
			key := t.From + "\x00" + t.Action
			if seen[key] {
				b.errf(tp, "duplicate transition from %q for action %q", t.From, t.Action)
			}
			seen[key] = true
			if t.Guard != "" {
				p, err := b.c.exprs.Compile(t.Guard, guardEnv(ct), rt.Bool, false)
				if err != nil {
					b.errf(tp+"/guard", "%v", err)
				} else {
					t.GuardProgram = p
				}
			}
			sm.Transitions = append(sm.Transitions, t)
		}
		for k := range m {
			if k != "initial" && k != "transitions" {
				b.errf(ptr+"/"+escape(k), "unknown x-stateMachine member")
			}
		}
		ct.StateMachines[path] = sm
	}

	for _, path := range sortedKeysMap(b.aggregate) {
		m := b.aggregate[path]
		ptr := "/properties/" + escape(path[1:]) + "/x-aggregate"
		f, _ := ct.Field(path)
		a := api.AggregateDef{Path: path}
		a.Op, _ = m["op"].(string)
		of, _ := m["of"].(string)
		a.Where, _ = m["where"].(string)
		a.Field, _ = m["field"].(string)
		ofID, err := api.ParseTypeID(of)
		if err != nil {
			b.errf(ptr+"/of", "%v", err)
			continue
		}
		a.Of = ofID.Base()
		children := reg.resolveAll(a.Of)
		if len(children) != 1 || ofID.IsTrait() {
			b.errf(ptr+"/of", "x-aggregate of must name one feature type")
			continue
		}
		child := children[0]
		if !ct.AllowsChild(child) {
			b.errf(ptr+"/of", "%s is not an allowed child of %s", a.Of, ct.ID)
		}
		switch a.Op {
		case "count":
			if f.Kind != kernel.KindInt64 {
				b.errf(ptr, "count aggregates need an integer field")
			}
		case "sum", "avg", "min", "max":
			cf, ok := child.Field(a.Field)
			if !ok || !cf.Top {
				b.errf(ptr+"/field", "field %q is not a top-level field of %s", a.Field, child.ID)
			} else if !cf.Kind.IsOrdered() || cf.Kind == kernel.KindString && a.Op != "min" && a.Op != "max" {
				b.errf(ptr+"/field", "cannot %s a %s field", a.Op, cf.Kind)
			} else if avgToFloat := a.Op == "avg" && f.Kind == kernel.KindFloat; cf.Kind != f.Kind && !avgToFloat {
				b.errf(ptr, "aggregate field kind %s does not match %s", f.Kind, cf.Kind)
			}
		default:
			b.errf(ptr+"/op", "op must be count, sum, avg, min, or max")
		}
		if a.Where != "" {
			env := rt.Env{Vars: child.Root.ExprType().Fields, Funcs: []string{rt.FnNow}}
			p, err := b.c.exprs.Compile(a.Where, env, rt.Bool, false)
			if err != nil {
				b.errf(ptr+"/where", "%v", err)
			} else {
				a.WhereProgram = p
			}
		}
		ct.Aggregates = append(ct.Aggregates, a)
	}
}

// ComputedEnv is the environment of x-computed expressions: every
// top-level field as a variable (as in the spec 4.3 example) plus
// `feature`, the whole storage document.
func ComputedEnv(ct *api.CompiledType) rt.Env {
	vars := map[string]*rt.Type{}
	for name, t := range ct.Root.ExprType().Fields {
		vars[name] = t
	}
	vars["feature"] = ct.ExprType()
	return rt.Env{Vars: vars, Funcs: []string{rt.FnNow}}
}

// buildViews derives one view per (audience, direction) plus the storage
// view, each with a compiled validator (spec 4.4 step 6, 4.6).
func (b *typeBuilder) buildViews() {
	ct := b.ct
	top := ct.TopFields()
	cache := map[string]api.Validator{}
	compile := func(key string, props map[string]bool, required []string, stripRO bool) *api.View {
		doc := map[string]any{
			"$schema":              "https://json-schema.org/draft/2020-12/schema",
			"$id":                  string(ct.ID) + "/views/" + key,
			"type":                 "object",
			"additionalProperties": false,
		}
		if ct.Title != "" {
			doc["title"] = ct.Title
		}
		wp := map[string]any{}
		for p := range props {
			w := b.wire[p]
			if stripRO {
				w = without(w, "readOnly", "writeOnly", "default")
			}
			wp[p[1:]] = w
		}
		doc["properties"] = wp
		if len(required) > 0 {
			sort.Strings(required)
			doc["required"] = required
		}
		delete(doc, "$id")
		body, err := json.Marshal(doc) // cache key: identical schemas share a validator
		if err != nil {
			b.errf("", "view %s: %v", key, err)
			return &api.View{}
		}
		doc["$id"] = string(ct.ID) + "/views/" + key
		raw, _ := json.Marshal(doc)
		v, ok := cache[string(body)]
		if !ok {
			v, err = b.c.validators.New(string(ct.ID)+"#"+key, raw)
			if err != nil {
				b.errf("", "view %s: %v", key, err)
				return &api.View{}
			}
			cache[string(body)] = v
		}
		return &api.View{JSONSchema: raw, Validator: v}
	}

	for _, a := range api.Audiences {
		readable := map[string]bool{}
		writable := map[string]bool{}
		updatable := map[string]bool{}
		queryable := map[string]bool{}
		var readReq, createReq []string
		for _, f := range top {
			canSee := a.CanRead(f.Visibility)
			if canSee && !f.WriteOnly {
				readable[f.Path] = true
				if f.Required && f.Persist {
					readReq = append(readReq, f.Path[1:])
				}
				if f.Indexed {
					queryable[f.Path] = true
				}
			}
			if canSee && f.Persist && !f.ReadOnly && f.Write.Allows(a) {
				writable[f.Path] = true
				if !f.Immutable {
					updatable[f.Path] = true
				}
				if f.Required && f.Default == nil {
					createReq = append(createReq, f.Path[1:])
				}
			}
		}
		k := func(d api.Direction) string { return api.ViewKey{Audience: a, Direction: d}.String() }
		read := compile(k(api.DirRead), readable, readReq, false)
		read.Readable = readable
		create := compile(k(api.DirCreate), writable, createReq, true)
		create.Writable = writable
		create.Readable = readable
		update := compile(k(api.DirUpdate), updatable, nil, true)
		update.Writable = updatable
		update.Readable = readable
		query := compile(k(api.DirQuery), queryable, nil, false)
		query.Queryable = queryable
		query.Readable = readable
		event := compile(k(api.DirEvent), readable, nil, false)
		event.Readable = readable
		for d, v := range map[api.Direction]*api.View{api.DirRead: read, api.DirCreate: create, api.DirUpdate: update, api.DirQuery: query, api.DirEvent: event} {
			v.Key = api.ViewKey{Audience: a, Direction: d}
			ct.Views[v.Key] = v
		}
	}

	stored := map[string]bool{}
	var storedReq []string
	for _, f := range top {
		if f.Persist {
			stored[f.Path] = true
			if f.Required {
				storedReq = append(storedReq, f.Path[1:])
			}
		}
	}
	ct.Storage = compile("storage", stored, storedReq, false)
	ct.Storage.Readable = stored
	ct.Storage.Writable = stored
}

func without(m map[string]any, keys ...string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

func sortedKeysMap[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sequence compiles one x-sequence (ADR 0021). The series expression sees
// the feature's fields like x-computed; on names an action, checked
// against the app's flows by app.Compile.
func (b *typeBuilder) sequence(path string, m map[string]any, env rt.Env) {
	ptr := "/properties/" + escape(path[1:]) + "/x-sequence"
	f, _ := b.ct.Field(path)
	d := api.SequenceDef{Path: path, SeriesConst: b.ct.Name + path}
	for k, v := range m {
		switch k {
		case "format":
			d.Format, _ = v.(string)
			if err := api.CheckSequenceFormat(d.Format); err != nil {
				b.errf(ptr+"/format", "%v", err)
			}
		case "series":
			s, _ := v.(string)
			p, err := b.c.exprs.Compile(s, env, rt.String, false)
			if err != nil {
				b.errf(ptr+"/series", "%v", err)
				continue
			}
			d.Series = p
		case "on":
			s, _ := v.(string)
			name, isAction := strings.CutPrefix(s, "action:")
			switch {
			case s == "create":
			case isAction && name != "":
				d.On = name
			default:
				b.errf(ptr+"/on", "on is create (default) or action:<name>")
			}
		default:
			b.errf(ptr+"/"+escape(k), "unknown member %q", k)
		}
	}
	if d.Format == "" {
		b.errf(ptr+"/format", "format is required, e.g. \"INV-{year}-{n:06}\"")
	}
	if !f.Indexed {
		b.errf(ptr, "x-sequence fields need \"x-indexed\": true")
	}
	b.ct.Sequences = append(b.ct.Sequences, d)
}
