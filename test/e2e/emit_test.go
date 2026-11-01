package e2e

import (
	"strings"
	"testing"
)

const remindFlow = `{
  "flow": "remindTask",
  "trigger": { "action": "remind", "on": "urn:app:feature:task" },
  "nodes": {
    "raise": { "node": "emit", "in": { "event": "'task.reminded'", "feature": "feature.id", "data": { "level": "40" } } }
  }
}`

const onRemindedFlow = `{
  "flow": "onReminded",
  "trigger": { "event": ["task.reminded"], "on": "urn:app:feature:task", "when": "event.data.level > 10" },
  "nodes": {
    "set": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/progress": "int(event.data.level)" } } },
    "again": { "node": "emit", "in": { "event": "'task.reminded'", "feature": "feature.id", "data": { "level": "event.data.level" } } }
  }
}`

// Spec 10.5, 14.5: emit raises a custom event after commit; flows with
// that event trigger run next, and causation limits stop emit loops.
func TestEmitChainsFlows(t *testing.T) {
	s := startWith(t, "memory", map[string]string{"remind.json": remindFlow, "on-reminded.json": onRemindedFlow})
	admin := s.admin()
	p := mustOK(t, admin.create("project", "", map[string]any{"title": "P"}), 201).str("id")
	task := mustOK(t, admin.create("task", p, map[string]any{"title": "T"}), 201).str("id")
	mustOK(t, admin.do("POST", "/v1/actions/task/"+task+"/remind", map[string]any{}), 200)
	eventually(t, "progress from the emitted event", func() bool {
		r := admin.do("GET", "/v1/features/task/"+task, nil)
		return r.Body["data"].(map[string]any)["progress"] == float64(40)
	})
	// onReminded emits the same event again: the second delivery is stopped.
	eventually(t, "loop stop", func() bool {
		r := admin.do("POST", "/v1/query", map[string]any{"type": "flowRun",
			"where": map[string]any{"field": "/flow", "op": "eq", "value": "onReminded"}})
		return strings.Contains(r.Raw, `"status":"loop_stopped"`) && strings.Contains(r.Raw, `"status":"succeeded"`)
	})
}
