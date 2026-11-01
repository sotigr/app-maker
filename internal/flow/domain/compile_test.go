package domain_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	flowapp "organon/internal/flow/application"
	"organon/internal/flow/domain"
	"organon/internal/runtime/domain/expression"
	schemadomain "organon/internal/schema/domain"
	"organon/internal/schema/infrastructure/jsonschema"
)

func deps(t *testing.T) domain.CompileDeps {
	t.Helper()
	files, _ := filepath.Glob("../../../examples/tracker/types/*.json")
	var srcs []schemadomain.Source
	for _, f := range files {
		b, _ := os.ReadFile(f)
		srcs = append(srcs, schemadomain.Source{File: f, Data: b})
	}
	srcs = append(srcs, schemadomain.Source{File: "metric.json", Data: []byte(
		`{"$id":"urn:app:feature:metric/v1","type":"object","x-repository":"metrics","properties":{"v":{"type":"number"}}}`)})
	reg, probs := schemadomain.NewCompiler(expression.NewCompiler(), jsonschema.Factory{}).Compile(schemadomain.Input{Types: srcs})
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	nodes := domain.NewNodeRegistry()
	for _, n := range flowapp.Builtins() {
		_ = nodes.Register(n)
	}
	return domain.CompileDeps{Types: reg, Exprs: expression.NewCompiler(), Nodes: nodes, Validators: jsonschema.Factory{}}
}

func compile(t *testing.T, src string) []domain.Problem {
	t.Helper()
	_, probs := domain.Compile(deps(t), []domain.Source{{File: "f.json", Data: []byte(src)}})
	return probs
}

// Milestone 8: every validation rule of spec 10.14 has a failing manifest.
func TestValidationRules(t *testing.T) {
	cases := []struct {
		rule, src, ptr, msg string
	}{
		{"cycle", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"expr","in":{"expr":"1"}},"b":{"node":"expr","in":{"expr":"2"}}},"edges":[["a","b"],["b","a"]]}`,
			"/edges", "cycle"},
		{"cycle through references", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"expr","in":{"expr":"nodes.b.out"}},"b":{"node":"expr","in":{"expr":"nodes.a.out"}}}}`,
			"/edges", "cycle"},
		{"unknown node type", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"teleport"}}}`, "/nodes/a/node", "unknown node type"},
		{"expression type error", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"expr","in":{"expr":"feature.titel"}}}}`, "/nodes/a/in/expr", "unknown field"},
		{"when must be bool", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"expr","when":"feature.title","in":{"expr":"1"}}}}`, "/nodes/a/when", "want bool"},
		{"in does not match input schema: unknown input", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"feature.get","in":{"id":"feature.id","bogus":"1"}}}}`, "/nodes/a/in/bogus", "no input"},
		{"in does not match input schema: missing required", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"feature.get","in":{}}}}`, "/nodes/a/in", "missing required input"},
		{"in does not match input schema: wrong type", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"if","in":{"cond":"feature.title"}}}}`, "/nodes/a/in/cond", "want bool"},
		{"node kind not allowed for trigger", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"email.send","in":{"to":"'x'","template":"'t'"}}}}`, "/nodes/a/node", "not allowed in beforeWrite"},
		{"forEach maxItems above the hard maximum", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"forEach","maxItems":5000,"in":{"items":"[1,2]"},"do":{"nodes":{"b":{"node":"expr","in":{"expr":"item"}}}}}}}`,
			"/nodes/a/maxItems", "between 1 and 1000"},
		{"DataWrite in beforeWrite to another repository", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"feature.create","in":{"type":"'metric'","data":{"v":"1"}}}}}`, "/nodes/a/in/type", "repository"},
		{"DataWrite in beforeWrite with a computed type", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"feature.create","in":{"type":"feature.title"}}}}`, "/nodes/a/in/type", "literally"},
		{"unknown lifecycle event", `{"flow":"f","trigger":{"event":["reminded"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"expr","in":{"expr":"1"}}}}`, "/trigger/event", "unknown event kind"},
		{"emit not allowed in beforeWrite", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"emit","in":{"event":"'task.x'","feature":"feature.id"}}}}`, "/nodes/a/node", "not allowed in beforeWrite"},
		{"code node without inputSchema", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"nodes/a.py","outputSchema":{"type":"object"}}}}`, "/nodes/a/inputSchema", "inputSchema"},
		{"code node input not in its inputSchema", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"nodes/a.py","in":{"x":"1"},"inputSchema":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]},"outputSchema":{"type":"object"}}}}`, "/nodes/a/in/x", "no input"},
		{"code node input type mismatch", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"nodes/a.py","in":{"id":"1"},"inputSchema":{"type":"object","properties":{"id":{"type":"string"}}},"outputSchema":{"type":"object"}}}}`, "/nodes/a/in/id", "want string"},
		{"code outside nodes/", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"flows/a.py","inputSchema":{"type":"object"},"outputSchema":{"type":"object"}}}}`, "/nodes/a/code", "under nodes/"},
		{"code node network entry without port", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"nodes/a.py","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"limits":{"network":["api.example.com"]}}}}`, "/nodes/a/limits/network/0", "host:port"},
		{"code node timeout above the budget", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"nodes/a.py","inputSchema":{"type":"object"},"outputSchema":{"type":"object"},"limits":{"timeoutMs":60000}}}}`, "/nodes/a/limits/timeoutMs", "budget"},
		{"code node not allowed in beforeWrite", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"python","code":"nodes/a.py","inputSchema":{"type":"object"},"outputSchema":{"type":"object"}}}}`, "/nodes/a/node", "not allowed in beforeWrite"},
		{"llm.complete output schema must be an object", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"llm.complete","in":{"prompt":"feature.title"},"outputSchema":{"type":"string"}}}}`, "/nodes/a/outputSchema", "object"},
		{"llm.complete not allowed in beforeWrite", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"llm.complete","in":{"prompt":"feature.title"}}}}`, "/nodes/a/node", "not allowed in beforeWrite"},
		{"runAs system without privileged", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},"runAs":"system",
			"nodes":{"a":{"node":"expr","in":{"expr":"1"}}}}`, "/runAs", "privileged"},
		{"result does not match the output schema", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"output":{"type":"object","properties":{"late":{"type":"boolean"}},"additionalProperties":false},
			"nodes":{"a":{"node":"expr","in":{"expr":"1"}}},"result":{"other":"nodes.a.out"}}`, "/result/other", "output schema"},
		{"trigger needs exactly one kind", `{"flow":"f","trigger":{"action":"x","schedule":"* * * * *"},"nodes":{}}`, "/trigger", "exactly one"},
		{"bad cron", `{"flow":"f","trigger":{"schedule":"61 * * * *"},"nodes":{}}`, "/trigger/schedule", "out of range"},
		{"webhook without secret", `{"flow":"f","trigger":{"webhook":"x"},"nodes":{}}`, "/trigger/secret", "secret"},
		{"unknown edge node", `{"flow":"f","trigger":{"action":"x","on":"urn:app:feature:task"},
			"nodes":{"a":{"node":"expr","in":{"expr":"1"}}},"edges":[["a","zzz"]]}`, "/edges/0", "unknown node"},
		{"budget above maximum", `{"flow":"f","trigger":{"beforeWrite":["create"],"on":"urn:app:feature:task"},"timeoutMs":60000,"nodes":{}}`,
			"/timeoutMs", "at most"},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			probs := compile(t, c.src)
			for _, p := range probs {
				if p.Pointer == c.ptr && strings.Contains(p.Message, c.msg) {
					return
				}
			}
			t.Fatalf("want problem at %s containing %q, got %+v", c.ptr, c.msg, probs)
		})
	}
}

func TestValidFlowCompiles(t *testing.T) {
	probs := compile(t, `{"flow":"ok","trigger":{"event":["updated"],"on":"urn:app:feature:task","when":"changed('/status')"},
		"nodes":{"get":{"node":"feature.get","in":{"id":"feature.id"}},
		         "each":{"node":"forEach","in":{"items":"[1,2,3]"},"do":{"nodes":{"double":{"node":"expr","in":{"expr":"item * 2"}}},"result":"nodes.double.out"}},
		         "mail":{"node":"email.send","when":"len(nodes.each.out) == 3","in":{"to":"'a@b.c'","template":"'t'","vars":{"n":"nodes.get.out.title"}}}}}`)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
}

func TestCron(t *testing.T) {
	c, err := domain.ParseCron("*/15 9-17 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	// Saturday 2026-10-31 10:00 -> Monday 09:00.
	got := c.Next(time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
	got = c.Next(time.Date(2026, 11, 2, 9, 7, 0, 0, time.UTC))
	if want := time.Date(2026, 11, 2, 9, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}
