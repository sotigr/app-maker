package e2e

import (
	"strings"
	"testing"
)

// ADR 0002: the console is embedded, and the editor validates flows with
// the same compiler as organon validate.
func TestEditorEndpoints(t *testing.T) {
	w := setup(t, "memory")
	if r := w.anon.do("GET", "/admin/", nil); r.Status != 200 || !strings.Contains(r.Raw, "<div id=\"root\">") && !strings.Contains(r.Raw, "make ui") {
		t.Fatalf("admin console: %d %.80s", r.Status, r.Raw)
	}
	bad := map[string]any{"flow": "bad", "trigger": map[string]any{"action": "x", "on": "urn:app:feature:task"},
		"nodes": map[string]any{"a": map[string]any{"node": "expr", "in": map[string]any{"expr": "feature.titel"}}}}
	r := mustOK(t, w.admin.do("POST", "/v1/flows/validate", map[string]any{"definition": bad}), 200)
	if r.Body["valid"] != false || !strings.Contains(r.Raw, `"/nodes/a/in/expr"`) {
		t.Fatalf("validate: %s", r.Raw)
	}
	good := map[string]any{"flow": "good", "trigger": map[string]any{"action": "x", "on": "urn:app:feature:task"},
		"nodes": map[string]any{"a": map[string]any{"node": "expr", "in": map[string]any{"expr": "feature.title"}}}}
	if r := mustOK(t, w.admin.do("POST", "/v1/flows/validate", map[string]any{"definition": good}), 200); r.Body["valid"] != true {
		t.Fatalf("validate good: %s", r.Raw)
	}
	if r := w.editor.do("GET", "/v1/flows", nil); r.Status != 403 {
		t.Fatalf("non-admins must not read flows: %d", r.Status)
	}
	cat := mustOK(t, w.admin.do("GET", "/v1/flows", nil), 200)
	if !strings.Contains(cat.Raw, `"feature.update"`) || !strings.Contains(cat.Raw, `"completeTask"`) {
		t.Fatalf("catalog: %.200s", cat.Raw)
	}
}
