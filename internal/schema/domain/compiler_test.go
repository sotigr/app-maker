package domain_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
	"organon/internal/runtime/domain/expression"
	"organon/internal/schema/api"
	"organon/internal/schema/domain"
	"organon/internal/schema/infrastructure/jsonschema"
)

var update = flag.Bool("update", false, "rewrite golden files")

func newCompiler() *domain.Compiler {
	return domain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{})
}

func trackerSources(t testing.TB) []domain.Source {
	t.Helper()
	files, err := filepath.Glob("../../../examples/tracker/types/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("tracker types not found: %v", err)
	}
	var out []domain.Source
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, domain.Source{File: filepath.Base(f), Data: b})
	}
	return out
}

func compileTracker(t testing.TB) *domain.Registry {
	t.Helper()
	reg, probs := newCompiler().Compile(domain.Input{Types: trackerSources(t)})
	if len(probs) > 0 {
		t.Fatalf("tracker types do not compile:\n%v", probs)
	}
	return reg
}

func compileDoc(t *testing.T, doc string) domain.Problems {
	t.Helper()
	_, probs := newCompiler().Compile(domain.Input{Types: []domain.Source{{File: "t.json", Data: []byte(doc)}}})
	return probs
}

func hasProblem(probs domain.Problems, pointer, substr string) bool {
	for _, p := range probs {
		if p.Pointer == pointer && strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

// Milestone 1: the tracker types compile (spec 4.3, 4.4).
func TestTrackerTypesCompile(t *testing.T) {
	reg := compileTracker(t)
	task, ok := reg.ByName("task")
	if !ok {
		t.Fatal("task not registered")
	}
	if task.ID != "urn:app:feature:task/v2" || task.Version != 2 {
		t.Fatalf("task id %s v%d", task.ID, task.Version)
	}
	want := map[string]kernel.Kind{
		"/title": kernel.KindString, "/status": kernel.KindEnum, "/assignee": kernel.KindRef,
		"/estimate": kernel.KindDecimal, "/dueAt": kernel.KindTime, "/progress": kernel.KindInt64,
		"/isLate": kernel.KindBool, "/internalScore": kernel.KindFloat, "/owner": kernel.KindRef,
		"/createdAt": kernel.KindTime,
	}
	for path, k := range want {
		f, ok := task.Field(path)
		if !ok || f.Kind != k {
			t.Errorf("%s: got %v want %s", path, f, k)
		}
	}
	if f, _ := task.Field("/assignee"); !f.Nullable || f.OnDelete != api.OnDeleteSetNull || f.RefTarget != api.TraitPrincipal {
		t.Errorf("assignee: %+v", f)
	}
	if f, _ := task.Field("/status"); f.Write.Kind != api.WriteAction || f.Default != "todo" {
		t.Errorf("status: %+v", f)
	}
	if f, _ := task.Field("/progress"); f.Stream == nil || f.Stream.MaxRate != 10 {
		t.Errorf("progress stream: %+v", f.Stream)
	}
	if len(task.Computed) != 1 || task.Computed[0].Path != "/isLate" {
		t.Fatalf("computed: %+v", task.Computed)
	}
	sm := task.StateMachines["/status"]
	if sm == nil || len(sm.Transitions) != 3 || sm.Transitions[1].GuardProgram == nil {
		t.Fatalf("state machine: %+v", sm)
	}
	if !task.HasTrait(api.TraitOwnable) || !task.AllowsChild(mustType(t, reg, "comment")) {
		t.Error("traits / children")
	}
	project := mustType(t, reg, "project")
	if len(project.Aggregates) != 2 || project.UniqueName != "/slug" {
		t.Errorf("project aggregates %+v unique %q", project.Aggregates, project.UniqueName)
	}
	// System types are always present (spec 3.7).
	for _, n := range []string{"root", "user", "team", "organization", "serviceAccount", "grant", "flowRun"} {
		mustType(t, reg, n)
	}
	if got := reg.Resolve(api.TraitPrincipal); len(got) != 4 {
		t.Errorf("principal trait resolves to %d types", len(got))
	}
}

func mustType(t testing.TB, reg *domain.Registry, name string) *api.CompiledType {
	t.Helper()
	ct, ok := reg.ByName(name)
	if !ok {
		t.Fatalf("type %s missing", name)
	}
	return ct
}

// Spec 4.3: the computed isLate field evaluates with the clock.
func TestComputedFieldEvaluates(t *testing.T) {
	task := mustType(t, compileTracker(t), "task")
	clock := kernel.NewFakeClock(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	vars := map[string]any{
		"dueAt": time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "status": "todo",
		"feature": map[string]any{}, rt.FnNow: clock.Now,
	}
	out, err := task.Computed[0].Program.Eval(vars)
	if err != nil || out != true {
		t.Fatalf("isLate = %v, %v", out, err)
	}
}

// Milestone 1: every rejected keyword is refused with its path (spec 4.1).
func TestRejectedKeywords(t *testing.T) {
	for _, kw := range []string{"anyOf", "allOf", "not", "if", "then", "else", "patternProperties",
		"dependentSchemas", "dependentRequired", "propertyNames", "unevaluatedProperties",
		"unevaluatedItems", "contains"} {
		doc := `{"$id":"urn:app:feature:x/v1","type":"object","properties":{"f":{"type":"string","` + kw + `":{}}}}`
		probs := compileDoc(t, doc)
		if !hasProblem(probs, "/properties/f/"+kw, kw) {
			t.Errorf("%s not rejected with its path: %v", kw, probs)
		}
	}
}

// Spec 4.1 restricted keywords and other profile rules.
func TestRestrictedKeywords(t *testing.T) {
	cases := []struct {
		name, props, ptr, msg string
	}{
		{"additionalProperties true", `"f":{"type":"object","properties":{},"additionalProperties":true}`, "/properties/f/additionalProperties", "must be false"},
		{"type array", `"f":{"type":["string","integer"]}`, "/properties/f/type", "[T, \"null\"]"},
		{"remote ref", `"f":{"$ref":"https://example.com/s.json"}`, "/properties/f/$ref", "local $ref"},
		{"unknown format", `"f":{"type":"string","format":"ipv4"}`, "/properties/f/format", "not supported"},
		{"oneOf without discriminator", `"f":{"oneOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"b":{"type":"string"}}}]}`, "/properties/f/oneOf/0", "discriminator"},
		{"oneOf mixed tags", `"f":{"oneOf":[{"type":"object","properties":{"k":{"const":"a"}}},{"type":"object","properties":{"j":{"const":"b"}}}]}`, "/properties/f/oneOf/1", "discriminator"},
		{"unknown x keyword", `"f":{"type":"string","x-bogus":1}`, "/properties/f/x-bogus", "unknown vocabulary"},
		{"type keyword on field", `"f":{"type":"string","x-traits":[]}`, "/properties/f/x-traits", "not allowed here"},
		{"no kind", `"f":{"title":"x"}`, "/properties/f", "cannot infer"},
		{"x-kind conflict", `"f":{"type":"boolean","x-kind":"decimal"}`, "/properties/f/x-kind", "conflicts"},
		{"searchable non string", `"f":{"type":"integer","x-searchable":true}`, "/properties/f/x-searchable", "string"},
		{"indexed object", `"f":{"type":"object","properties":{},"x-indexed":true}`, "/properties/f/x-indexed", "scalar"},
		{"bad default", `"f":{"type":"integer","default":"x"}`, "/properties/f/default", "default"},
		{"setNull not nullable", `"f":{"type":"string","x-kind":"ref","x-onDelete":"setNull"}`, "/properties/f/x-onDelete", "nullable"},
		{"computed type error", `"f":{"type":"boolean","x-computed":"g + 1"}, "g":{"type":"string"}`, "/properties/f/x-computed", "mismatched"},
		{"computed wrong result", `"f":{"type":"boolean","x-computed":"g"}, "g":{"type":"string"}`, "/properties/f/x-computed", "want bool"},
		{"state not in enum", `"f":{"enum":["a","b"],"x-stateMachine":{"initial":"a","transitions":[{"from":"a","to":"c","action":"go"}]}}`, "/properties/f/x-stateMachine/transitions/0", "not in the enum"},
		{"guard type error", `"f":{"enum":["a","b"],"x-stateMachine":{"initial":"a","transitions":[{"from":"a","to":"b","action":"go","guard":"principal.nme == ''"}]}}`, "/properties/f/x-stateMachine/transitions/0/guard", "unknown field"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probs := compileDoc(t, `{"$id":"urn:app:feature:x/v1","type":"object","properties":{`+c.props+`}}`)
			if !hasProblem(probs, c.ptr, c.msg) {
				t.Errorf("want problem at %s containing %q, got:\n%v", c.ptr, c.msg, probs)
			}
		})
	}
}

func TestRefsAndTraitConflicts(t *testing.T) {
	recursive := `{"$id":"urn:app:feature:x/v1","type":"object","$defs":{"a":{"$ref":"#/$defs/a"}},"properties":{"f":{"$ref":"#/$defs/a"}}}`
	if probs := compileDoc(t, recursive); !strings.Contains(probs.Error(), "recursive $ref") {
		t.Errorf("recursive ref not rejected: %v", probs)
	}
	local := `{"$id":"urn:app:feature:x/v1","type":"object","$defs":{"money":{"type":"string","x-kind":"decimal"}},"properties":{"f":{"$ref":"#/$defs/money","title":"Price"}}}`
	reg, probs := newCompiler().Compile(domain.Input{Types: []domain.Source{{File: "t.json", Data: []byte(local)}}})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	if f, _ := mustType(t, reg, "x").Field("/f"); f.Kind != kernel.KindDecimal || f.Title != "Price" {
		t.Errorf("resolved ref: %+v", f)
	}
	conflict := `{"$id":"urn:app:feature:x/v1","type":"object","x-traits":["urn:sys:trait:ownable"],"properties":{"owner":{"type":"string"}}}`
	if probs := compileDoc(t, conflict); !hasProblem(probs, "/properties/owner", "defined differently") {
		t.Errorf("trait conflict not reported: %v", probs)
	}
	reserved := `{"$id":"urn:sys:feature:mine","type":"object","properties":{}}`
	if probs := compileDoc(t, reserved); !strings.Contains(probs.Error(), "reserved") {
		t.Errorf("reserved namespace: %v", probs)
	}
	unknownChild := `{"$id":"urn:app:feature:x/v1","type":"object","x-allowedChildren":["urn:app:feature:nope"],"properties":{}}`
	if probs := compileDoc(t, unknownChild); !hasProblem(probs, "/x-allowedChildren/0", "unknown") {
		t.Errorf("unknown child: %v", probs)
	}
}

// Spec 5.4: children share the parent's repository; cross-repository refs
// must dangle.
func TestRepositoryRules(t *testing.T) {
	parent := `{"$id":"urn:app:feature:p/v1","type":"object","x-allowedChildren":["urn:app:feature:c"],"properties":{}}`
	child := `{"$id":"urn:app:feature:c/v1","type":"object","x-repository":"metrics","properties":{
		"r":{"type":"string","x-kind":"ref","x-ref":"urn:app:feature:p","x-onDelete":"restrict"}}}`
	_, probs := newCompiler().Compile(domain.Input{Types: []domain.Source{{File: "p", Data: []byte(parent)}, {File: "c", Data: []byte(child)}}})
	if !strings.Contains(probs.Error(), "children must share") {
		t.Errorf("child repository rule: %v", probs)
	}
	if !strings.Contains(probs.Error(), "must use x-onDelete: dangle") {
		t.Errorf("cross repository ref rule: %v", probs)
	}
}

// Spec 4.6: view field sets per audience and direction.
func TestViewSets(t *testing.T) {
	task := mustType(t, compileTracker(t), "task")
	pubRead := task.View(api.AudPublic, api.DirRead).Readable
	if pubRead["/internalScore"] || !pubRead["/title"] || !pubRead["/isLate"] {
		t.Errorf("public read: %v", pubRead)
	}
	if !task.View(api.AudSystem, api.DirRead).Readable["/internalScore"] {
		t.Error("system must read private fields")
	}
	if task.View(api.AudAdmin, api.DirRead).Readable["/internalScore"] {
		t.Error("admin must not read private fields")
	}
	create := task.View(api.AudPublic, api.DirCreate).Writable
	for _, p := range []string{"/status", "/isLate", "/owner", "/createdAt", "/internalScore"} {
		if create[p] {
			t.Errorf("public create must not write %s", p)
		}
	}
	if !create["/title"] || !create["/assignee"] {
		t.Errorf("public create: %v", create)
	}
	q := task.View(api.AudPublic, api.DirQuery).Queryable
	if !q["/status"] || !q["/dueAt"] || q["/estimate"] {
		t.Errorf("query view: %v", q)
	}
	grant := mustType(t, compileTracker(t), "grant")
	if grant.View(api.AudAdmin, api.DirUpdate).Writable["/subject"] || !grant.View(api.AudAdmin, api.DirCreate).Writable["/subject"] {
		t.Error("x-immutable must be writable on create only")
	}
}

// Milestone 1: views for every audience match golden files (spec 4.6).
func TestViewsGolden(t *testing.T) {
	reg := compileTracker(t)
	for _, name := range []string{"project", "task", "comment"} {
		ct := mustType(t, reg, name)
		for _, a := range api.Audiences {
			for _, d := range api.Directions {
				v := ct.View(a, d)
				checkGolden(t, filepath.Join("testdata", "golden", name+"."+a.String()+"."+d.String()+".json"), v.JSONSchema)
			}
		}
		checkGolden(t, filepath.Join("testdata", "golden", name+".storage.json"), ct.Storage.JSONSchema)
	}
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	var v any
	if err := json.Unmarshal(got, &v); err != nil {
		t.Fatalf("%s: invalid JSON: %v", path, err)
	}
	pretty, _ := json.MarshalIndent(v, "", "  ")
	pretty = append(pretty, '\n')
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != string(pretty) {
		t.Errorf("%s differs from golden file (run go test -update to accept):\n%s", path, pretty)
	}
}

// Spec 4.9: views validate wire documents.
func TestViewValidation(t *testing.T) {
	task := mustType(t, compileTracker(t), "task")
	create := task.View(api.AudPublic, api.DirCreate).Validator
	ok := map[string]any{"title": "T", "estimate": "12.50", "dueAt": "2026-11-01T00:00:00Z", "progress": json.Number("40")}
	if errs := create.Validate(ok); len(errs) > 0 {
		t.Fatalf("valid doc rejected: %v", errs)
	}
	bad := map[string]any{"estimate": "12,5", "dueAt": "tomorrow", "status": "done", "progress": json.Number("101")}
	errs := create.Validate(bad)
	codes := map[string]string{}
	for _, e := range errs {
		codes[e.Path] = e.Code
	}
	for path, code := range map[string]string{"/title": "required", "/estimate": "pattern", "/dueAt": "format", "/status": "additionalProperties", "/progress": "maximum"} {
		if codes[path] != code {
			t.Errorf("%s: want %s, got %q (all: %v)", path, code, codes[path], errs)
		}
	}
}

func TestValueTypeCoerceAndWire(t *testing.T) {
	task := mustType(t, compileTracker(t), "task")
	est, _ := task.Field("/estimate")
	v, err := est.Type.Coerce("/estimate", "12.50")
	if err != nil || est.Type.Wire(v) != "12.50" {
		t.Fatalf("decimal: %v %v", v, err)
	}
	prog, _ := task.Field("/progress")
	v, err = prog.Type.Coerce("/progress", json.Number("40"))
	if err != nil || v != kernel.Int64(40) || prog.Type.Wire(v) != json.Number("40") {
		t.Fatalf("integer: %v %v", v, err)
	}
	st, _ := task.Field("/status")
	if _, err := st.Type.Coerce("/status", "nope"); err == nil {
		t.Fatal("enum accepted unknown value")
	}
	as, _ := task.Field("/assignee")
	if v, err := as.Type.Coerce("/assignee", nil); err != nil || v != (kernel.Null{}) {
		t.Fatalf("nullable ref: %v %v", v, err)
	}
}

// ADR 0020: x-shareable is checked when a type compiles; its actions are
// checked against the app's flows by app.Compile.
func TestShareableKeyword(t *testing.T) {
	cases := []struct{ name, x, ptr, msg string }{
		{"not an object", `true`, "/x-shareable", "object"},
		{"share is reserved", `{"actions":["share"]}`, "/x-shareable/actions/0", "other than share"},
		{"maxDays range", `{"actions":[],"maxDays":400}`, "/x-shareable/maxDays", "1 to 365"},
		{"unknown member", `{"actions":[],"days":3}`, "/x-shareable/days", "unknown member"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			probs := compileDoc(t, `{"$id":"urn:app:feature:x/v1","type":"object","x-shareable":`+c.x+`,"properties":{}}`)
			if !hasProblem(probs, c.ptr, c.msg) {
				t.Errorf("want problem at %s containing %q, got:\n%v", c.ptr, c.msg, probs)
			}
		})
	}
}

// ADR 0021: x-sequence rules.
func TestSequenceKeyword(t *testing.T) {
	cases := []struct{ name, props, ptr, msg string }{
		{"not a string field", `"n":{"type":"integer","x-indexed":true,"x-sequence":{"format":"{n}"}}`, "/properties/n/x-sequence", "strings"},
		{"required", `"n":{"type":"string","x-indexed":true,"x-sequence":{"format":"{n}"}}`, "/properties/n/x-sequence", "required"},
		{"not indexed", `"n":{"type":"string","x-sequence":{"format":"{n}"}}`, "/properties/n/x-sequence", "x-indexed"},
		{"format without n", `"n":{"type":"string","x-indexed":true,"x-sequence":{"format":"INV-{year}"}}`, "/properties/n/x-sequence/format", "{n}"},
		{"unknown placeholder", `"n":{"type":"string","x-indexed":true,"x-sequence":{"format":"{n}-{week}"}}`, "/properties/n/x-sequence/format", "{n}"},
		{"stray brace", `"n":{"type":"string","x-indexed":true,"x-sequence":{"format":"{n}}"}}`, "/properties/n/x-sequence/format", "{n}"},
		{"series must be a string", `"n":{"type":"string","x-indexed":true,"x-sequence":{"format":"{n}","series":"1 + 1"}}`, "/properties/n/x-sequence/series", "string"},
		{"bad on", `"n":{"type":"string","x-indexed":true,"x-sequence":{"format":"{n}","on":"update"}}`, "/properties/n/x-sequence/on", "action:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := ""
			if c.name == "required" {
				req = `,"required":["n"]`
			}
			probs := compileDoc(t, `{"$id":"urn:app:feature:x/v1","type":"object","properties":{`+c.props+`}`+req+`}`)
			if !hasProblem(probs, c.ptr, c.msg) {
				t.Errorf("want problem at %s containing %q, got:\n%v", c.ptr, c.msg, probs)
			}
		})
	}
}

func TestFormatSequence(t *testing.T) {
	at := time.Date(2026, 3, 9, 23, 0, 0, 0, time.UTC)
	for format, want := range map[string]string{
		"INV-{year}-{n:06}":             "INV-2026-000042",
		"{series}/{yy}{month}{day}-{n}": "A/260309-42",
		"{n:2}":                         "42",
	} {
		if got := api.FormatSequence(format, 42, "A", at); got != want {
			t.Errorf("%s: %s, want %s", format, got, want)
		}
	}
}
