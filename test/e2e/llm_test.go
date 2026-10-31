package e2e

import (
	"strings"
	"testing"
)

// Spec 11.7: llm.complete sends the output schema, validates the reply,
// streams partial text to an ephemeral field, and records the call.
func TestLLMCompleteNode(t *testing.T) {
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		e := w.editor.ws(t)
		e.send(map[string]any{"t": "subscribe", "sid": "s", "target": "feature", "id": w.project, "paths": []string{"/summaryDraft"}})
		e.next("snapshot", is("snapshot", "s"))

		r := mustOK(t, w.editor.do("POST", "/v1/actions/project/"+w.project+"/summarize", map[string]any{}), 200)
		out, _ := r.Body["output"].(map[string]any)
		summary, _ := out["summary"].(string)
		// The fake provider answers with the simplest schema-valid document.
		if out["risk"] != "low" || !strings.HasPrefix(summary, "Project: P Tasks: - T (todo)") {
			t.Fatalf("output: %s", r.Raw)
		}
		got := mustOK(t, w.admin.do("GET", "/v1/features/project/"+w.project, nil), 200)
		if got.str("data", "summary") != summary {
			t.Fatalf("summary not saved: %s", got.Raw)
		}
		m := e.next("streamed draft", func(m map[string]any) bool {
			set, _ := m["set"].(map[string]any)
			return is("delta", "s")(m) && set["/summaryDraft"] != nil && strings.Contains(set["/summaryDraft"].(string), summary)
		})
		if m["ephemeral"] != true {
			t.Fatalf("draft not ephemeral: %v", m)
		}

		// The run records the call: model, tokens, latency, cost.
		runs := mustOK(t, w.admin.do("POST", "/v1/query", map[string]any{"type": "flowRun",
			"where": map[string]any{"field": "/flow", "op": "eq", "value": "summarizeProject"}}), 200)
		items, _ := runs.Body["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("runs: %s", runs.Raw)
		}
		nodes := items[0].(map[string]any)["data"].(map[string]any)["nodes"].(map[string]any)
		calls, _ := nodes["summarize"].(map[string]any)["llm"].([]any)
		if len(calls) != 1 || calls[0].(map[string]any)["provider"] != "fake" || calls[0].(map[string]any)["alias"] != "default" {
			t.Fatalf("llm call record: %v", nodes["summarize"])
		}
	})
}
