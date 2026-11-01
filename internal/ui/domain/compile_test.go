package domain_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"organon/internal/runtime/domain/expression"
	schemaapi "organon/internal/schema/api"
	schemadomain "organon/internal/schema/domain"
	"organon/internal/schema/infrastructure/jsonschema"
	"organon/internal/ui/domain"

	js "github.com/santhosh-tekuri/jsonschema/v6"
)

func deps(t *testing.T) domain.Deps {
	t.Helper()
	files, _ := filepath.Glob("../../../examples/tracker/types/*.json")
	var srcs []schemadomain.Source
	for _, f := range files {
		b, _ := os.ReadFile(f)
		srcs = append(srcs, schemadomain.Source{File: f, Data: b})
	}
	reg, probs := schemadomain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{}).Compile(schemadomain.Input{Types: srcs})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return domain.Deps{Types: reg, Exprs: expression.NewCompiler(),
		FlowAction: func(ct *schemaapi.CompiledType, name string) (json.RawMessage, bool) {
			if ct.Name == "task" && name == "complete" {
				return json.RawMessage(`{"type":"object","properties":{"note":{"type":"string"}}}`), true
			}
			return nil, (ct.Name == "project" && name == "summarize") || (ct.Name == "task" && name == "triage")
		}}
}

const appJSON = `{"title":"T","home":"home"}`

const homeJSON = `{"page":"home","route":"/","body":{"component":"text","text":"hi"}}`

func compile(t *testing.T, files map[string]string) (*domain.Compiled, []domain.Problem) {
	t.Helper()
	var srcs []domain.Source
	if _, ok := files["ui/app.json"]; !ok {
		srcs = append(srcs, domain.Source{File: "ui/app.json", Data: []byte(appJSON)})
	}
	if _, ok := files["ui/pages/home.json"]; !ok {
		srcs = append(srcs, domain.Source{File: "ui/pages/home.json", Data: []byte(homeJSON)})
	}
	for f, d := range files {
		srcs = append(srcs, domain.Source{File: f, Data: []byte(d)})
	}
	return domain.Compile(deps(t), srcs)
}

// page wraps a body and data sources into a page at /p/:id.
func page(data, body string) string {
	if data == "" {
		data = `{}`
	}
	return `{"page":"p","route":"/p/:id","params":{"id":{"type":"string"}},
		"state":{"n":{"type":"integer"},"s":{"type":"string"}},
		"data":` + data + `,"body":` + body + `}`
}

const taskData = `{"task":{"feature":"params.id","type":"task"},"tasks":{"children":"params.id","type":"task"}}`

// ADR 0016: every UI validation rule has a failing page, located by a
// JSON Pointer.
func TestValidationRules(t *testing.T) {
	cases := []struct {
		rule, data, body, ptr, msg string
	}{
		{"unknown component", "", `{"component":"carousel"}`, "/body/component", "unknown component"},
		{"unknown prop", "", `{"component":"text","text":"x","colour":"red"}`, "/body/colour", "unknown member"},
		{"missing required prop", "", `{"component":"heading"}`, "/body", "text is required"},
		{"enum prop", "", `{"component":"text","text":"x","tone":"loud"}`, "/body/tone", "must be one of"},
		{"text interpolation type error", taskData, `{"component":"text","text":"{{ data.task.titel }}"}`, "/body/text", "unknown field titel"},
		{"unclosed interpolation", "", `{"component":"text","text":"{{ 1"}`, "/body/text", "unclosed"},
		{"bare word in expr prop", "", `{"component":"stat","label":"x","value":"done"}`, "/body/value", "write \"'done'\""},
		{"visible must be bool", taskData, `{"component":"divider","visible":"data.task.title"}`, "/body/visible", "want bool"},
		{"closures are server-only", taskData, `{"component":"stat","label":"x","value":"len(filter(data.tasks, .title != ''))"}`, "/body/value", "not available"},
		{"pipes are server-only", "", `{"component":"stat","label":"x","value":"'a' | upper()"}`, "/body/value", "pipes"},
		{"matches is server-only", "", `{"component":"stat","label":"x","value":"'a' matches 'b'"}`, "/body/value", "operator \"matches\""},
		{"server built-ins are unavailable", taskData, `{"component":"divider","visible":"hasRole(data.task, 'editor')"}`, "/body/visible", "not available"},
		{"bad can operation", taskData, `{"component":"divider","visible":"can(data.task, 'edit')"}`, "/body/visible", "can: operation"},
		{"unknown data type", `{"x":{"feature":"params.id","type":"tsk"}}`, `{"component":"divider"}`, "/data/x/type", "unknown type"},
		{"two source kinds", `{"x":{"feature":"params.id","children":"params.id","type":"task"}}`, `{"component":"divider"}`, "/data/x", "exactly one"},
		{"source cycle", `{"a":{"feature":"data.b.id","type":"task"},"b":{"feature":"data.a.id","type":"task"}}`, `{"component":"divider"}`, "/data/a", "cycle"},
		{"query on unindexed field", `{"q":{"query":{"type":"task","where":{"field":"/estimate","op":"eq","value":"'1'"}}}}`, `{"component":"divider"}`,
			"/data/q/query/where/field", "not queryable"},
		{"query op", `{"q":{"query":{"type":"task","where":{"field":"/status","op":"like","value":"'x'"}}}}`, `{"component":"divider"}`,
			"/data/q/query/where/op", "op must be"},
		{"unknown source", "", `{"component":"table","source":"nope","columns":[{"field":"/title"}]}`, "/body/source", "no data source"},
		{"list component on a feature source", taskData, `{"component":"table","source":"task","columns":[{"field":"/title"}]}`, "/body/source", "needs a list source"},
		{"unknown field path", taskData, `{"component":"table","source":"tasks","columns":[{"field":"/titel"}]}`, "/body/columns/0/field", "has no field /titel"},
		{"private field", taskData, `{"component":"field","source":"task","path":"/internalScore"}`, "/body/path", "not readable by users"},
		{"field outside a row needs source", "", `{"component":"field","path":"/title"}`, "/body", "source is required"},
		{"item outside rows", taskData, `{"component":"text","text":"{{ item.title }}"}`, "/body/text", "unknown name item"},
		{"column needs field or value", taskData, `{"component":"table","source":"tasks","columns":[{"label":"x"}]}`, "/body/columns/0", "exactly one of field and value"},
		{"unknown page", "", `{"component":"link","text":"x","page":"nowhere"}`, "/body/page", "unknown page"},
		{"missing route parameter", "", `{"component":"link","text":"x","page":"p"}`, "/body", "needs parameter \"id\""},
		{"unknown page parameter", "", `{"component":"link","text":"x","page":"p","params":{"id":"'1'","x":"1"}}`, "/body/params/x", "no parameter"},
		{"params without page", "", `{"component":"table","source":"tasks","columns":[{"field":"/title"}],"rowParams":{"id":"item.id"}}`,
			"/body/rowParams", "needs rowPage"},
		{"link needs page or url", "", `{"component":"link","text":"x"}`, "/body", "exactly one of page and url"},
		{"download needs url", "", `{"component":"link","text":"x","page":"p","params":{"id":"'1'"},"download":""}`, "/body/download", "needs url"},
		{"blob fields have url but no name", taskData, `{"component":"text","text":"{{ data.task.attachment.name }}"}`, "/body/text", "unknown field name"},
		{"form field not writable", "", `{"component":"form","mode":"create","type":"task","fields":[{"path":"/status"}]}`,
			"/body/fields/0/path", "cannot be written"},
		{"update form needs id", "", `{"component":"form","mode":"update","type":"task"}`, "/body", "id is required"},
		{"action form field", "", `{"component":"form","mode":"action","type":"task","id":"params.id","action":"complete","fields":[{"path":"/x"}]}`,
			"/body/fields/0/path", "no input field"},
		{"unknown action", "", `{"component":"form","mode":"action","type":"task","id":"params.id","action":"explode"}`, "/body/action", "no action"},
		{"board on a non-state-machine field", taskData, `{"component":"board","source":"tasks","field":"/label","card":{"component":"divider"}}`,
			"/body/field", "not a state machine"},
		{"chart needs groupBy", taskData, `{"component":"chart","kind":"bar","source":"tasks"}`, "/body", "groupBy is required"},
		{"unknown event", "", `{"component":"text","text":"x","on":{"click":[]}}`, "/body/on/click", "no event"},
		{"unknown step", "", `{"component":"button","label":"x","on":{"click":[{"do":"teleport"}]}}`, "/body/on/click/0/do", "unknown step"},
		{"step update of an action-only field", "", `{"component":"button","label":"x","on":{"click":[{"do":"update","type":"task","id":"params.id","set":{"/status":"'done'"}}]}}`,
			"/body/on/click/0/set/~1status", "cannot be written"},
		{"step action input", "", `{"component":"button","label":"x","on":{"click":[{"do":"action","type":"task","id":"params.id","action":"complete","input":{"nope":"1"}}]}}`,
			"/body/on/click/0/input/nope", "no input field"},
		{"step set of unknown state", "", `{"component":"button","label":"x","on":{"click":[{"do":"set","state":"zz","value":"1"}]}}`,
			"/body/on/click/0/state", "no state variable"},
		{"open unknown dialog", "", `{"component":"button","label":"x","on":{"click":[{"do":"open","dialog":"d"}]}}`,
			"/body/on/click/0/dialog", "no dialog named"},
		{"onError on a non-API step", "", `{"component":"button","label":"x","on":{"click":[{"do":"back","onError":[]}]}}`,
			"/body/on/click/0/onError", "unknown member"},
		{"tabs state must be an integer", "", `{"component":"tabs","state":"s","tabs":[{"label":"a","children":[]}]}`,
			"/body/state", "integer state variable"},
	}
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			data := tc.data
			if data == "" {
				data = taskData
			}
			_, probs := compile(t, map[string]string{"ui/pages/p.json": page(data, tc.body)})
			for _, p := range probs {
				if p.File == "ui/pages/p.json" && p.Pointer == tc.ptr && strings.Contains(p.Message, tc.msg) {
					return
				}
			}
			t.Fatalf("want %s: ...%s..., got %+v", tc.ptr, tc.msg, probs)
		})
	}
}

func TestFileRules(t *testing.T) {
	cases := []struct {
		rule        string
		files       map[string]string
		file, ptr   string
		msg         string
		withoutHome bool
	}{
		{"app.json required", map[string]string{"ui/app.json": ""}, "ui/app.json", "", "invalid JSON", false},
		{"home page exists", map[string]string{"ui/app.json": `{"home":"x"}`}, "ui/app.json", "/home", "must name a page", false},
		{"home without route params", map[string]string{"ui/app.json": `{"home":"p"}`, "ui/pages/p.json": page("", `{"component":"divider"}`)},
			"ui/app.json", "/home", "route parameters", false},
		{"nav target", map[string]string{"ui/app.json": `{"home":"home","nav":[{"label":"x","page":"zz"}]}`}, "ui/app.json", "/nav/0/page", "unknown page", false},
		{"nav visible sees only the principal", map[string]string{"ui/app.json": `{"home":"home","nav":[{"label":"x","page":"home","visible":"can(principal, 'update')"}]}`},
			"ui/app.json", "/nav/0/visible", "unknown", false},
		{"logo must be an asset", map[string]string{"ui/app.json": `{"home":"home","logo":"logo.png"}`}, "ui/app.json", "/logo", "ui/assets", false},
		{"reserved route", map[string]string{"ui/pages/x.json": `{"page":"x","route":"/v1/x","body":{"component":"divider"}}`},
			"ui/pages/x.json", "/route", "reserved", false},
		{"route conflict", map[string]string{"ui/pages/x.json": `{"page":"x","route":"/","body":{"component":"divider"}}`},
			"ui/pages/x.json", "/route", "conflicts", false},
		{"route param not declared", map[string]string{"ui/pages/x.json": `{"page":"x","route":"/x/:id","body":{"component":"divider"}}`},
			"ui/pages/x.json", "/route", "not declared", false},
		{"duplicate page name", map[string]string{"ui/pages/x.json": `{"page":"home","route":"/x","body":{"component":"divider"}}`},
			"", "/page", "also defined", false},
		{"unknown top-level member", map[string]string{"ui/pages/x.json": `{"page":"x","route":"/x","body":{"component":"divider"},"style":{}}`},
			"ui/pages/x.json", "/style", "unknown member", false},
		{"asset type", map[string]string{"ui/assets/run.js": "alert(1)"}, "ui/assets/run.js", "", "not allowed", false},
		{"unexpected file", map[string]string{"ui/script.js": "x"}, "ui/script.js", "", "unexpected file", false},
		{"pages need app.json", map[string]string{"ui/pages/x.json": `{"page":"x","route":"/x","body":{"component":"divider"}}`},
			"ui/app.json", "", "required", true},
	}
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			var probs []domain.Problem
			if tc.withoutHome {
				var srcs []domain.Source
				for f, d := range tc.files {
					srcs = append(srcs, domain.Source{File: f, Data: []byte(d)})
				}
				_, probs = domain.Compile(deps(t), srcs)
			} else {
				_, probs = compile(t, tc.files)
			}
			for _, p := range probs {
				if (tc.file == "" || p.File == tc.file) && p.Pointer == tc.ptr && strings.Contains(p.Message, tc.msg) {
					return
				}
			}
			t.Fatalf("want %s#%s: ...%s..., got %+v", tc.file, tc.ptr, tc.msg, probs)
		})
	}
}

func TestCompiledTracker(t *testing.T) {
	files, _ := filepath.Glob("../../../examples/tracker/ui/*/*.json")
	more, _ := filepath.Glob("../../../examples/tracker/ui/*.*")
	var srcs []domain.Source
	for _, f := range append(files, more...) {
		b, _ := os.ReadFile(f)
		rel, _ := filepath.Rel("../../../examples/tracker", f)
		srcs = append(srcs, domain.Source{File: filepath.ToSlash(rel), Data: b})
	}
	c, probs := domain.Compile(deps(t), srcs)
	if c == nil {
		t.Fatal(probs)
	}
	if c.App == nil || c.App.Home != "projects" || len(c.App.Nav) != 2 || c.Styles == nil {
		t.Fatalf("app: %+v", c.App)
	}
	byName := map[string]*domain.Page{}
	for _, p := range c.Pages {
		byName[p.Name] = p
	}
	task := byName["task"]
	if task == nil || task.Route != "/tasks/:id" || strings.Join(task.Types, ",") != "comment,project,task" {
		t.Fatalf("task page: %+v", task)
	}
	if byName["myTasks"].Public || !byName["projects"].Public {
		t.Fatal("myTasks is for users only; projects has no visible rule")
	}
	if strings.Contains(string(task.Doc), "x-layout") || strings.Contains(string(task.Doc), "$schema") {
		t.Fatal("browser documents drop authoring members")
	}
}

// expressionCase is one vector of testdata/uiexpr/cases.json, shared with
// the browser interpreter's test (sdk/ts/ui/test/expr.test.ts).
type expressionCase struct {
	Expr   string         `json:"expr"`
	Env    map[string]any `json:"env"`
	Want   any            `json:"want"`
	Reject bool           `json:"reject"`
}

// ADR 0016: the server's checker and evaluator agree with the browser's on
// every shared vector.
func TestExpressionVectors(t *testing.T) {
	b, err := os.ReadFile("../../../testdata/uiexpr/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []expressionCase
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		err := domain.CheckSubset(tc.Expr)
		if tc.Reject {
			if err == nil {
				t.Errorf("%s: want rejected", tc.Expr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.Expr, err)
			continue
		}
		got, err := domain.EvalForTest(tc.Expr, tc.Env)
		if err != nil {
			t.Errorf("%s: %v", tc.Expr, err)
			continue
		}
		gb, _ := json.Marshal(got)
		wb, _ := json.Marshal(tc.Want)
		var g, w any
		_ = json.Unmarshal(gb, &g)
		_ = json.Unmarshal(wb, &w)
		if string(mustJSON(g)) != string(mustJSON(w)) {
			t.Errorf("%s = %s, want %s", tc.Expr, gb, wb)
		}
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// The generated JSON Schemas accept every tracker UI file, so editor
// completion never contradicts organon validate.
func TestSchemasAcceptTracker(t *testing.T) {
	for name, schema := range map[string]map[string]any{"page": domain.PageSchema(), "app": domain.AppSchema()} {
		c := js.NewCompiler()
		raw, _ := json.Marshal(schema)
		doc, _ := js.UnmarshalJSON(strings.NewReader(string(raw)))
		if err := c.AddResource(name+".json", doc); err != nil {
			t.Fatal(err)
		}
		sch, err := c.Compile(name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob("../../../examples/tracker/ui/pages/*.json")
		if name == "app" {
			files = []string{"../../../examples/tracker/ui/app.json"}
		}
		for _, f := range files {
			b, _ := os.ReadFile(f)
			doc, err := js.UnmarshalJSON(strings.NewReader(string(b)))
			if err != nil {
				t.Fatal(err)
			}
			if err := sch.Validate(doc); err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
	}
}
