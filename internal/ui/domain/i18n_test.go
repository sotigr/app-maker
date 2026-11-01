package domain_test

import (
	"strings"
	"testing"
)

const twoLangs = `{"title":"T","home":"home","languages":["en","el"]}`

// ADR 0018: every rule of translation files, located by a JSON Pointer;
// stale entries and missing translations are warnings.
func TestTranslationRules(t *testing.T) {
	text := func(s string) string {
		return page("", `{"component":"text","text":"`+s+`"}`)
	}
	cases := []struct {
		rule      string
		files     map[string]string
		file, ptr string
		msg       string
		warning   bool
	}{
		{"language tag", map[string]string{"ui/app.json": `{"home":"home","languages":["EN"]}`}, "ui/app.json", "/languages/0", "not a language tag", false},
		{"file of an unlisted language", map[string]string{"ui/i18n/de.json": `{}`}, "ui/i18n/de.json", "", "not listed", false},
		{"file name", map[string]string{"ui/app.json": twoLangs, "ui/i18n/Greek.json": `{}`}, "ui/i18n/Greek.json", "", "named by language tag", false},
		{"unknown member", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"msgs":{}}`}, "ui/i18n/el.json", "", "invalid translation file", false},
		{"explicit key needs languages", map[string]string{"ui/pages/p.json": text("@intro")}, "ui/pages/p.json", "/body/text", `needs "languages"`, false},
		{"explicit key needs its text", map[string]string{"ui/app.json": twoLangs, "ui/pages/p.json": text("@intro")},
			"ui/pages/p.json", "/body/text", "no text for @intro", false},
		{"explicit key text is type-checked where used", map[string]string{"ui/app.json": twoLangs, "ui/pages/p.json": text("@intro"),
			"ui/i18n/en.json": `{"messages":{"@intro":"Hi {{ data.nope }}"}}`}, "ui/pages/p.json", "/body/text", "in the text of @intro", false},
		{"translation keeps the expressions", map[string]string{"ui/app.json": twoLangs, "ui/pages/p.json": text("Id {{ params.id }}"),
			"ui/i18n/el.json": `{"messages":{"Id {{ params.id }}":"Κωδ. {{ params.other }}"}}`}, "ui/i18n/el.json", "/messages/Id {{ params.id }}", "same {{ }} expressions", false},
		{"explicit key translation keeps the expressions", map[string]string{"ui/app.json": twoLangs, "ui/pages/p.json": text("@intro"),
			"ui/i18n/en.json": `{"messages":{"@intro":"Id {{ params.id }}"}}`, "ui/i18n/el.json": `{"messages":{"@intro":"Κωδ."}}`},
			"ui/i18n/el.json", "/messages/@intro", "same {{ }} expressions", false},
		{"unused text", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"messages":{"Nope":"Όχι"}}`},
			"ui/i18n/el.json", "/messages/Nope", "no page or navigation entry uses", true},
		{"page override of a text the page lacks", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"pages":{"home":{"bye":"αντίο"}}}`},
			"ui/i18n/el.json", "/pages/home/bye", "does not use", true},
		{"unknown page", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"pages":{"zz":{}}}`}, "ui/i18n/el.json", "/pages/zz", "no page", true},
		{"unknown field", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"types":{"task":{"fields":{"/titel":"x"}}}}`},
			"ui/i18n/el.json", "/types/task/fields/~1titel", "no readable top-level field", true},
		{"unknown enum value", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"types":{"task":{"values":{"/status":{"lost":"x"}}}}}`},
			"ui/i18n/el.json", "/types/task/values/~1status/lost", "not a value", true},
		{"unknown transition", map[string]string{"ui/app.json": twoLangs, "ui/i18n/el.json": `{"types":{"task":{"actions":{"fly":"x"}}}}`},
			"ui/i18n/el.json", "/types/task/actions/fly", "no state-machine action", true},
		{"missing translations", map[string]string{"ui/app.json": twoLangs}, "ui/i18n/el.json", "", "have no el translation", true},
	}
	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			_, probs := compile(t, tc.files)
			for _, p := range probs {
				if p.File == tc.file && p.Pointer == tc.ptr && strings.Contains(p.Message, tc.msg) && p.Warning == tc.warning {
					return
				}
			}
			t.Fatalf("want %s#%s: ...%s... (warning %v), got %+v", tc.file, tc.ptr, tc.msg, tc.warning, probs)
		})
	}
}

// ADR 0018: the scaffold lists every text with words (not data-only
// interpolations), the renderer's texts, and the labels of the types
// pages show; it keeps translations and drops stale entries on prune.
func TestTranslationScaffold(t *testing.T) {
	c, probs := compile(t, map[string]string{
		"ui/app.json": twoLangs,
		"ui/pages/p.json": page(taskData, `{"component":"stack","children":[
			{"component":"text","text":"Hello {{ params.id }}"},{"component":"text","text":"{{ data.task.title }}"},
			{"component":"link","text":"Docs","url":"https://example.com/docs"}]}`),
		"ui/i18n/el.json": `{"messages":{"Hello {{ params.id }}":"Γεια {{ params.id }}","Old":"Παλιό"},
			"types":{"task":{"values":{"/status":{"done":"Ολοκληρώθηκε"}}}}}`,
	})
	if c == nil {
		t.Fatal(probs)
	}
	sc := c.Scaffold(deps(t).Types, "el", false)
	if sc.Messages["Hello {{ params.id }}"] != "Γεια {{ params.id }}" || sc.Messages["Old"] != "Παλιό" {
		t.Fatalf("translations are kept: %v", sc.Messages)
	}
	if v, ok := sc.Messages["Docs"]; !ok || v != "" {
		t.Fatalf("new texts are empty entries: %v", sc.Messages)
	}
	if _, ok := sc.Messages["Sign in"]; !ok {
		t.Fatal("the renderer's texts are listed")
	}
	if _, ok := sc.Messages["{{ data.task.title }}"]; ok {
		t.Fatal("data-only texts have nothing to translate")
	}
	if _, ok := sc.Messages["https://example.com/docs"]; ok {
		t.Fatal("URLs are not translated")
	}
	task := sc.Types["task"]
	if task.Values["/status"]["done"] != "Ολοκληρώθηκε" || task.Values["/status"]["todo"] != "" || task.Actions["start"] != "" {
		t.Fatalf("task labels: %+v", task)
	}
	if _, ok := task.Fields["/internalScore"]; ok {
		t.Fatal("private fields are never shown, so never translated")
	}
	if _, ok := c.Scaffold(deps(t).Types, "el", true).Messages["Old"]; ok {
		t.Fatal("prune drops entries no page uses")
	}
	m := c.Merged("el")
	if m.Messages["Hello {{ params.id }}"] != "Γεια {{ params.id }}" || m.Types["task"].Values["/status"]["done"] != "Ολοκληρώθηκε" {
		t.Fatalf("merged: %+v", m)
	}
	if c.Merged("en").Messages["Hello {{ params.id }}"] != "" {
		t.Fatal("the source language has only its own file")
	}
}
