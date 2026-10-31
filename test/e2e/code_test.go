package e2e

import (
	"os/exec"
	"strings"
	"testing"
)

// Spec 11.1-11.2: a Python code node runs on the runner over gRPC, reads
// features and calls the model through host callbacks, and its output is
// validated before downstream nodes use it.
func TestPythonCodeNode(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	eachDriver(t, func(t *testing.T, driver string) {
		w := setup(t, driver)
		for _, c := range []struct{ title, label, source string }{
			{"Fix the login crash", "bug", "rule"},
			{"Write an onboarding guide", "feature", "model"},
		} {
			task := w.newTask(t, map[string]any{"title": c.title})
			r := mustOK(t, w.editor.do("POST", "/v1/actions/task/"+task+"/triage", map[string]any{}), 200)
			out, _ := r.Body["output"].(map[string]any)
			if out["label"] != c.label || out["source"] != c.source {
				t.Fatalf("%s: %s", c.title, r.Raw)
			}
			got := mustOK(t, w.admin.do("GET", "/v1/features/task/"+task, nil), 200)
			if got.str("data", "label") != c.label {
				t.Fatalf("label not saved: %s", got.Raw)
			}
		}
		// Callbacks run as the run's principal: a viewer cannot trigger it,
		// and the model call is recorded on the run.
		if r := w.viewer.do("POST", "/v1/actions/task/"+w.task+"/triage", map[string]any{}); r.Status != 403 {
			t.Fatalf("viewer triage: %s", r.Raw)
		}
		runs := mustOK(t, w.admin.do("POST", "/v1/query", map[string]any{"type": "flowRun",
			"where": map[string]any{"field": "/flow", "op": "eq", "value": "triageTask"}}), 200)
		if !strings.Contains(runs.Raw, `"alias":"fast"`) {
			t.Fatalf("llm call from code not recorded: %s", runs.Raw)
		}
	})
}
