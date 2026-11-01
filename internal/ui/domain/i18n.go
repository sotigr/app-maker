package domain

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"

	schemaapi "organon/internal/schema/api"
)

// Translations of the end-user UI (ADR 0018). Texts are their own keys:
// ui/i18n/<lang>.json maps the text as written in a page to its
// translation. A text that is exactly "@key" is an explicit key whose
// source-language text lives in the first language's file. Field labels,
// enum values, and transition buttons come from the types and are
// translated per type.

//go:embed texts.json
var builtinJSON []byte

// BuiltinTexts are the renderer's own texts (sign-in, Save, empty
// states); they are translated like page texts.
var BuiltinTexts = func() []string {
	var t []string
	if err := json.Unmarshal(builtinJSON, &t); err != nil {
		panic(err)
	}
	return t
}()

var (
	langRe = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{1,8})*$`)
	keyRe  = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
)

// Translations is one ui/i18n/<lang>.json. Empty translations are left out.
type Translations struct {
	// Messages maps texts (and @keys) to translations.
	Messages map[string]string `json:"messages,omitempty"`
	// Pages override Messages on one page, for texts whose meaning
	// depends on where they appear.
	Pages map[string]map[string]string `json:"pages,omitempty"`
	Types map[string]TypeLabels        `json:"types,omitempty"`
}

// TypeLabels translates what pages show of a type.
type TypeLabels struct {
	Fields  map[string]string            `json:"fields,omitempty"`  // field path -> label
	Values  map[string]map[string]string `json:"values,omitempty"`  // enum field path -> value -> label
	Actions map[string]string            `json:"actions,omitempty"` // transition action -> button label
}

func (c *Translations) init() {
	if c.Messages == nil {
		c.Messages = map[string]string{}
	}
	if c.Pages == nil {
		c.Pages = map[string]map[string]string{}
	}
	if c.Types == nil {
		c.Types = map[string]TypeLabels{}
	}
}

// dropEmpty removes untranslated ("") entries.
func (c *Translations) dropEmpty() {
	for k, v := range c.Messages {
		if v == "" {
			delete(c.Messages, k)
		}
	}
	for _, m := range c.Pages {
		for k, v := range m {
			if v == "" {
				delete(m, k)
			}
		}
	}
	for _, tl := range c.Types {
		for k, v := range tl.Fields {
			if v == "" {
				delete(tl.Fields, k)
			}
		}
		for _, m := range tl.Values {
			for k, v := range m {
				if v == "" {
					delete(m, k)
				}
			}
		}
		for k, v := range tl.Actions {
			if v == "" {
				delete(tl.Actions, k)
			}
		}
	}
}

func parseCatalog(data []byte) (*Translations, error) {
	var doc struct {
		Schema string `json:"$schema"`
		Translations
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	c := doc.Translations
	c.init()
	c.dropEmpty()
	return &c, nil
}

// Merged is the catalog for lang: its translations over the source
// language's file, so source-language rewordings and @keys fill gaps.
func (u *Compiled) Merged(lang string) *Translations {
	out := &Translations{}
	out.init()
	for _, l := range []string{u.Languages[0], lang} {
		c := u.Catalogs[l]
		if c == nil {
			continue
		}
		for k, v := range c.Messages {
			out.Messages[k] = v
		}
		for p, m := range c.Pages {
			if out.Pages[p] == nil {
				out.Pages[p] = map[string]string{}
			}
			for k, v := range m {
				out.Pages[p][k] = v
			}
		}
		for t, tl := range c.Types {
			o := out.Types[t]
			o.Fields = mergeMap(o.Fields, tl.Fields)
			o.Actions = mergeMap(o.Actions, tl.Actions)
			for f, m := range tl.Values {
				if o.Values == nil {
					o.Values = map[string]map[string]string{}
				}
				o.Values[f] = mergeMap(o.Values[f], m)
			}
			out.Types[t] = o
		}
	}
	return out
}

func mergeMap(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	if dst == nil {
		dst = map[string]string{}
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// Scaffold returns lang's catalog with an entry for everything the app
// shows: translations are kept, missing ones are "", and entries nothing
// uses are dropped when prune is set.
func (u *Compiled) Scaffold(types schemaapi.TypeRegistry, lang string, prune bool) *Translations {
	out := &Translations{}
	out.init()
	have := u.Catalogs[lang]
	if have == nil {
		have = &Translations{}
		have.init()
	}
	needs := u.needs(types)
	if !prune {
		for k, v := range have.Messages {
			out.Messages[k] = v
		}
		for p, m := range have.Pages {
			out.Pages[p] = mergeMap(nil, m)
		}
	} else {
		for p, m := range have.Pages {
			for k, v := range m {
				if slices.Contains(u.pageTexts(p), k) {
					if out.Pages[p] == nil {
						out.Pages[p] = map[string]string{}
					}
					out.Pages[p][k] = v
				}
			}
		}
	}
	for _, t := range needs.texts {
		if _, ok := out.Messages[t]; !ok {
			out.Messages[t] = have.Messages[t]
		}
	}
	for tn, need := range needs.labels {
		got := have.Types[tn]
		tl := TypeLabels{Fields: map[string]string{}, Values: map[string]map[string]string{}, Actions: map[string]string{}}
		for f := range need.Fields {
			tl.Fields[f] = got.Fields[f]
		}
		for f, vals := range need.Values {
			tl.Values[f] = map[string]string{}
			for v := range vals {
				tl.Values[f][v] = got.Values[f][v]
			}
		}
		for a := range need.Actions {
			tl.Actions[a] = got.Actions[a]
		}
		out.Types[tn] = tl
	}
	if !prune {
		for tn, got := range have.Types {
			tl := out.Types[tn]
			tl.Fields = mergeMap(tl.Fields, got.Fields)
			tl.Actions = mergeMap(tl.Actions, got.Actions)
			for f, m := range got.Values {
				if tl.Values == nil {
					tl.Values = map[string]map[string]string{}
				}
				tl.Values[f] = mergeMap(tl.Values[f], m)
			}
			out.Types[tn] = tl
		}
	}
	return out
}

func (u *Compiled) pageTexts(name string) []string {
	for _, p := range u.Pages {
		if p.Name == name {
			return p.Texts
		}
	}
	return nil
}

type i18nNeeds struct {
	texts  []string
	labels map[string]TypeLabels
}

// needs lists the texts and type labels a full translation covers.
func (u *Compiled) needs(types schemaapi.TypeRegistry) i18nNeeds {
	set := map[string]bool{}
	for _, t := range u.AppTexts {
		set[t] = true
	}
	shown := map[string]bool{}
	for _, p := range u.Pages {
		for _, t := range p.Texts {
			set[t] = true
		}
		for _, t := range p.Types {
			shown[t] = true
		}
	}
	for _, t := range BuiltinTexts {
		set[t] = true
	}
	n := i18nNeeds{labels: map[string]TypeLabels{}}
	for t := range set {
		n.texts = append(n.texts, t)
	}
	sort.Strings(n.texts)
	for tn := range shown {
		ct, ok := types.ByName(tn)
		if !ok {
			continue
		}
		n.labels[tn] = typeNeeds(ct)
	}
	return n
}

// typeNeeds lists the labels pages may show of ct: its readable top-level
// fields, their enum values, and its transition actions.
func typeNeeds(ct *schemaapi.CompiledType) TypeLabels {
	tl := TypeLabels{Fields: map[string]string{}, Values: map[string]map[string]string{}, Actions: map[string]string{}}
	read := ct.View(schemaapi.AudOwner, schemaapi.DirRead)
	for _, f := range ct.TopFields() {
		if read == nil || !read.Readable[f.Path] {
			continue
		}
		tl.Fields[f.Path] = ""
		if f.Type != nil && len(f.Type.Enum) > 0 {
			tl.Values[f.Path] = map[string]string{}
			for _, v := range f.Type.Enum {
				tl.Values[f.Path][v] = ""
			}
		}
	}
	for _, sm := range ct.StateMachines {
		for _, t := range sm.Transitions {
			tl.Actions[t.Action] = ""
		}
	}
	return tl
}

// missing counts the entries of needs that cat does not translate.
func (n i18nNeeds) missing(cat *Translations) (missing, total int) {
	for _, t := range n.texts {
		total++
		if _, ok := cat.Messages[t]; !ok {
			missing++
		}
	}
	for tn, need := range n.labels {
		got := cat.Types[tn]
		for f := range need.Fields {
			total++
			if _, ok := got.Fields[f]; !ok {
				missing++
			}
		}
		for f, vals := range need.Values {
			for v := range vals {
				total++
				if _, ok := got.Values[f][v]; !ok {
					missing++
				}
			}
		}
		for a := range need.Actions {
			total++
			if _, ok := got.Actions[a]; !ok {
				missing++
			}
		}
	}
	return missing, total
}

// languages reads ui/app.json "languages".
func (c *compiler) languages(v any) {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		c.errf("/languages", "languages is a non-empty list of language tags such as \"en\"; the first is the language of the ui/ files")
		return
	}
	for i, it := range list {
		s, _ := it.(string)
		switch {
		case !langRe.MatchString(s):
			c.errf(fmt.Sprintf("/languages/%d", i), "%q is not a language tag such as \"en\" or \"pt-BR\"", it)
		case slices.Contains(c.langs, s):
			c.errf(fmt.Sprintf("/languages/%d", i), "%s is listed twice", s)
		default:
			c.langs = append(c.langs, s)
		}
	}
}

// useText records a translatable text of the current page (or the app
// shell) and returns the text to type-check: an @key's source text.
func (c *compiler) useText(ptr, s string) (string, bool) {
	if !keyRe.MatchString(s) && !hasWords(s) {
		return s, true // only data, such as "{{ item.title }}"
	}
	if c.used[s] == nil {
		c.used[s] = map[string]bool{}
	}
	c.used[s][c.pageName] = true
	if !keyRe.MatchString(s) {
		return s, true
	}
	if len(c.langs) == 0 {
		c.errf(ptr, "the explicit key %s needs \"languages\" in ui/app.json and its text in ui/i18n/<language>.json (write @@ for a literal @)", s)
		return "", false
	}
	src := c.cats[c.langs[0]]
	if src == nil || src.Messages[s] == "" {
		c.errf(ptr, "no text for %s: add it to \"messages\" of ui/i18n/%s.json", s, c.langs[0])
		return "", false
	}
	return src.Messages[s], true
}

// hasWords reports whether a text has letters outside its {{ }}
// interpolations, that is, anything to translate.
func hasWords(s string) bool {
	for {
		i := strings.Index(s, "{{")
		j := strings.Index(s, "}}")
		if i < 0 || j < i {
			return strings.IndexFunc(s, unicode.IsLetter) >= 0
		}
		if strings.IndexFunc(s[:i], unicode.IsLetter) >= 0 {
			return true
		}
		s = s[j+2:]
	}
}

// checkCatalogs checks every ui/i18n file against the texts in use.
func (c *compiler) checkCatalogs(out *Compiled) {
	for lang, file := range c.catFiles {
		if !slices.Contains(c.langs, lang) {
			c.file = file
			c.errf("", "language %s is not listed in \"languages\" of ui/app.json", lang)
		}
	}
	if len(c.langs) == 0 {
		return
	}
	out.Languages = c.langs
	out.Catalogs = map[string]*Translations{}
	for _, lang := range c.langs {
		cat := c.cats[lang]
		if cat == nil {
			cat = &Translations{}
			cat.init()
		}
		out.Catalogs[lang] = cat
		if f, ok := c.catFiles[lang]; ok {
			c.file = f
			c.checkCatalog(lang, cat)
		}
	}
	needs := out.needs(c.d.Types)
	for _, lang := range c.langs[1:] {
		if m, total := needs.missing(out.Catalogs[lang]); m > 0 {
			file := c.catFiles[lang]
			if file == "" {
				file = "ui/i18n/" + lang + ".json"
			}
			c.file = file
			c.probs = append(c.probs, Problem{File: file, Warning: true,
				Message: fmt.Sprintf("%d of %d texts and labels have no %s translation (organon ui i18n --lang %s adds them)", m, total, lang, lang)})
		}
	}
}

func (c *compiler) warnf(ptr, format string, args ...any) {
	c.probs = append(c.probs, Problem{File: c.file, Pointer: ptr, Message: fmt.Sprintf(format, args...), Warning: true})
}

func (c *compiler) checkCatalog(lang string, cat *Translations) {
	isSource := lang == c.langs[0]
	src := c.cats[c.langs[0]]
	builtin := map[string]bool{}
	for _, t := range BuiltinTexts {
		builtin[t] = true
	}
	// original returns the text a translation of key must match.
	original := func(key string) (string, bool) {
		if keyRe.MatchString(key) {
			if isSource {
				return "", false
			}
			if src == nil || src.Messages[key] == "" {
				return "", false
			}
			return src.Messages[key], true
		}
		return key, true
	}
	for key, val := range cat.Messages {
		ptr := "/messages/" + esc(key)
		switch {
		case keyRe.MatchString(key):
			if c.used[key] == nil {
				c.warnf(ptr, "no page uses %s", key)
			} else if !isSource && (src == nil || src.Messages[key] == "") {
				c.warnf(ptr, "%s has no text in ui/i18n/%s.json", key, c.langs[0])
			}
		case builtin[key], c.used[key] != nil:
		default:
			c.warnf(ptr, "no page or navigation entry uses this text")
			continue
		}
		if o, ok := original(key); ok {
			c.sameExprs(ptr, o, val)
		}
	}
	for page, m := range cat.Pages {
		ptr := "/pages/" + esc(page)
		if _, ok := c.pages[page]; !ok {
			c.warnf(ptr, "no page %q", page)
			continue
		}
		for key, val := range m {
			if !c.used[key][page] {
				c.warnf(ptr+"/"+esc(key), "page %s does not use this text", page)
				continue
			}
			if o, ok := original(key); ok {
				c.sameExprs(ptr+"/"+esc(key), o, val)
			}
		}
	}
	for tn, tl := range cat.Types {
		ptr := "/types/" + esc(tn)
		ct, ok := c.d.Types.ByName(tn)
		if !ok {
			c.warnf(ptr, "unknown type %q", tn)
			continue
		}
		need := typeNeeds(ct)
		for f := range tl.Fields {
			if _, ok := need.Fields[f]; !ok {
				c.warnf(ptr+"/fields/"+esc(f), "%s has no readable top-level field %s", tn, f)
			}
		}
		for f, vals := range tl.Values {
			if _, ok := need.Values[f]; !ok {
				c.warnf(ptr+"/values/"+esc(f), "field %s of %s is not an enum", f, tn)
				continue
			}
			for v := range vals {
				if _, ok := need.Values[f][v]; !ok {
					c.warnf(ptr+"/values/"+esc(f)+"/"+esc(v), "%s is not a value of %s", v, f)
				}
			}
		}
		for a := range tl.Actions {
			if _, ok := need.Actions[a]; !ok {
				c.warnf(ptr+"/actions/"+esc(a), "%s has no state-machine action %q", tn, a)
			}
		}
	}
}

// sameExprs requires a translation to interpolate the same expressions as
// its original; they were type-checked where the original is used.
func (c *compiler) sameExprs(ptr, original, translation string) {
	want, err := splitTemplate(original)
	if err != nil {
		return
	}
	got, err := splitTemplate(translation)
	if err != nil {
		c.errf(ptr, "%v", err)
		return
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(want, got) {
		c.errf(ptr, "a translation uses the same {{ }} expressions as its text (%s), in any order", exprList(want))
	}
}

func exprList(exprs []string) string {
	if len(exprs) == 0 {
		return "none"
	}
	parts := make([]string, len(exprs))
	for i, e := range exprs {
		parts[i] = "{{ " + e + " }}"
	}
	return strings.Join(parts, ", ")
}
