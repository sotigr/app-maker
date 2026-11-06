package domain_test

import (
	"strings"
	"testing"
	"time"

	"organon/internal/flow/domain"
	"organon/internal/kernel"
	"organon/internal/runtime/domain/expression"
)

func compileDoc(t *testing.T, src string) (*domain.DocumentSet, []domain.Problem) {
	t.Helper()
	return domain.CompileDocuments(expression.NewCompiler(), []domain.Source{{File: "templates/documents/d.json", Data: []byte(src)}})
}

// ADR 0022: document templates are checked by organon validate.
func TestDocumentRules(t *testing.T) {
	vars := `"vars":{"type":"object","properties":{"total":{"type":"number"},"lines":{"type":"array","items":{"type":"object"}}}}`
	cases := []struct{ name, src, ptr, msg string }{
		{"unknown member", `{"body":[],"style":{}}`, "/style", "unknown member"},
		{"body required", `{"header":[]}`, "", "body is required"},
		{"block kind", `{"body":[{"paragraph":"x"}]}`, "/body/0", "one of"},
		{"two kinds", `{"body":[{"text":"x","heading":"y"}]}`, "/body/0", "one of"},
		{"unknown var", `{` + vars + `,"body":[{"text":"{{ vars.totl }}"}]}`, "/body/0/text", "totl"},
		{"when must be bool", `{` + vars + `,"body":[{"text":"x","when":"vars.total"}]}`, "/body/0/when", "bool"},
		{"rows must be a list", `{` + vars + `,"body":[{"table":{"rows":"vars.total","columns":[{"label":"a","value":"b"}]}}]}`, "/body/0/table/rows", "want []"},
		{"footer width", `{` + vars + `,"body":[{"table":{"rows":"vars.lines","columns":[{"label":"a","value":"b"}],"footer":[["x","y"]]}}]}`, "/body/0/table/footer/0", "one text per column"},
		{"page outside header", `{"body":[{"text":"{{ page }}"}]}`, "/body/0/text", "page"},
		{"page size", `{"page":{"size":"B9"},"body":[]}`, "/page/size", "A4"},
		{"time zone", `{"timezone":"Mars/Olympus","body":[]}`, "/timezone", "unknown time zone"},
		{"unclosed interpolation", `{"body":[{"text":"{{ vars"}]}`, "/body/0/text", "unclosed"},
		{"member for another block", `{"body":[{"text":"x","level":2}]}`, "/body/0/level", "unknown member"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, probs := compileDoc(t, c.src)
			for _, p := range probs {
				if p.Pointer == c.ptr && strings.Contains(p.Message, c.msg) {
					return
				}
			}
			t.Fatalf("want %s: ...%s..., got %+v", c.ptr, c.msg, probs)
		})
	}
}

// ADR 0022: evaluation interpolates vars, formats numbers and dates by the
// template's locale and time zone, repeats table rows, and skips blocks
// whose when is false.
func TestDocumentEvaluate(t *testing.T) {
	set, probs := compileDoc(t, `{
	  "vars": {"type":"object","properties":{"total":{"type":"number"},"lines":{"type":"array","items":{"type":"object"}},"at":{"type":"string","format":"date-time"}}},
	  "locale": "el", "timezone": "Europe/Athens",
	  "footer": [{"text":"{{ page }}/{{ pages }}"}],
	  "body": [
	    {"heading":"Σύνολο **{{ formatNumber(vars.total, 2) }} €**"},
	    {"text":"{{ formatDate(vars.at, 'datetime') }}"},
	    {"text":"hidden","when":"vars.total < 0"},
	    {"table":{"rows":"vars.lines","columns":[{"label":"#","value":"{{ index + 1 }}"},{"label":"Item","value":"{{ row.name }}"}]}}
	  ]}`)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	tpl, _ := set.Get("d")
	at, _ := time.Parse(time.RFC3339, "2026-11-06T10:30:00Z")
	doc, err := tpl.Evaluate(map[string]any{"total": 1234567.891, "at": at,
		"lines": []any{map[string]any{"name": "Λάδια"}, map[string]any{"name": "Φίλτρο"}}}, at, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := doc.Body[0].Runs
	if len(h) != 2 || h[0].Text != "Σύνολο " || h[1].Text != "1.234.567,89 €" || !h[1].Bold {
		t.Fatalf("heading runs: %+v", h)
	}
	if got := doc.Body[1].Runs[0].Text; got != "06/11/2026 12:30" {
		t.Fatalf("date in Athens: %q", got)
	}
	if len(doc.Body) != 3 || doc.Body[2].Kind != kernel.BlockTable {
		t.Fatalf("when skips the block: %+v", doc.Body)
	}
	if rows := doc.Body[2].Table.Rows; len(rows) != 2 || rows[1][0] != "2" || rows[1][1] != "Φίλτρο" {
		t.Fatalf("rows: %v", rows)
	}
	if doc.Footer[0].Runs[0].Text != "{page}/{pages}" {
		t.Fatalf("page placeholders: %+v", doc.Footer)
	}
}
