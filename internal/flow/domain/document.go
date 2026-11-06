package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

// Document templates (ADR 0022): templates/documents/<name>.json describe
// a document as blocks whose text has {{ expr }} interpolations over
// "vars". document.render evaluates one into a kernel.Document, which
// platform/pdf writes as PDF.

// DocumentSet holds the compiled document templates by name.
type DocumentSet struct{ byName map[string]*DocumentTemplate }

// Get returns a template.
func (s *DocumentSet) Get(name string) (*DocumentTemplate, bool) {
	if s == nil {
		return nil, false
	}
	t, ok := s.byName[name]
	return t, ok
}

// Names lists the templates.
func (s *DocumentSet) Names() []string {
	var out []string
	if s != nil {
		for n := range s.byName {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// DocumentTemplate is a compiled template.
type DocumentTemplate struct {
	Name      string
	File      string
	size      string
	landscape bool
	margin    float64
	locale    string
	loc       *time.Location
	header    []*docBlock
	footer    []*docBlock
	body      []*docBlock
}

type docText struct {
	parts []string     // literal text between interpolations
	progs []rt.Program // one per interpolation
}

type docBlock struct {
	kind    string
	when    rt.Program
	text    *docText
	level   int
	align   string
	size    float64
	columns int
	fields  [][2]*docText
	rows    rt.Program
	cols    []docColumn
	footer  [][]*docText
	image   rt.Program
	width   float64
	space   float64
}

type docColumn struct {
	label *docText
	value *docText
	width float64
	align string
}

// DocumentFuncs are the functions document expressions may call.
var DocumentFuncs = []string{rt.FnNow, rt.FnFormatDate, rt.FnFormatNumber}

type docCompiler struct {
	exprs rt.Compiler
	file  string
	probs []Problem
}

func (c *docCompiler) errf(ptr, format string, args ...any) {
	c.probs = append(c.probs, Problem{File: c.file, Pointer: ptr, Message: fmt.Sprintf(format, args...)})
}

// CompileDocuments compiles templates/documents/*.json.
func CompileDocuments(exprs rt.Compiler, srcs []Source) (*DocumentSet, []Problem) {
	set := &DocumentSet{byName: map[string]*DocumentTemplate{}}
	c := &docCompiler{exprs: exprs}
	for _, s := range srcs {
		c.file = s.File
		name := strings.TrimSuffix(path.Base(s.File), ".json")
		if t := c.compile(name, s.Data); t != nil {
			t.File = s.File
			set.byName[name] = t
		}
	}
	return set, c.probs
}

var docBlockKinds = []string{"heading", "text", "fields", "table", "image", "qr", "spacer", "pageBreak"}

func (c *docCompiler) compile(name string, data []byte) *DocumentTemplate {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		c.errf("", "invalid JSON: %v", err)
		return nil
	}
	t := &DocumentTemplate{Name: name, size: "A4", margin: 15, locale: "en", loc: time.UTC}
	for k := range doc {
		switch k {
		case "$schema", "vars", "page", "locale", "timezone", "header", "footer", "body":
		default:
			c.errf("/"+k, "unknown member %q (vars, page, locale, timezone, header, footer, body)", k)
		}
	}
	varsT := rt.OpenMap()
	if v, ok := doc["vars"]; ok {
		m, isObj := v.(map[string]any)
		if !isObj || m["type"] != "object" {
			c.errf("/vars", "vars is a JSON Schema of type object")
		} else {
			varsT = SchemaType(m)
			// Declared vars are closed, so a misspelt name is an error.
			_, hasProps := m["properties"]
			varsT.Open = !hasProps || m["additionalProperties"] == true
		}
	}
	if v, ok := doc["page"]; ok {
		m, _ := v.(map[string]any)
		for k, pv := range m {
			switch k {
			case "size":
				s, _ := pv.(string)
				switch s {
				case "A3", "A4", "A5", "Letter", "Legal":
					t.size = s
				default:
					c.errf("/page/size", "size is A3, A4, A5, Letter, or Legal")
				}
			case "orientation":
				switch pv {
				case "portrait":
				case "landscape":
					t.landscape = true
				default:
					c.errf("/page/orientation", "orientation is portrait or landscape")
				}
			case "margin":
				n, ok := number(pv)
				if !ok || n < 5 || n > 50 {
					c.errf("/page/margin", "margin is 5 to 50 millimetres")
				}
				t.margin = n
			default:
				c.errf("/page/"+k, "unknown member %q (size, orientation, margin)", k)
			}
		}
	}
	if v, ok := doc["locale"]; ok {
		t.locale, _ = v.(string)
		if t.locale == "" {
			c.errf("/locale", "locale is a language tag such as el or en")
		}
	}
	if v, ok := doc["timezone"]; ok {
		s, _ := v.(string)
		loc, err := time.LoadLocation(s)
		if err != nil {
			c.errf("/timezone", "unknown time zone %q", s)
		} else {
			t.loc = loc
		}
	}
	env := rt.Env{Vars: map[string]*rt.Type{"vars": varsT}, Funcs: DocumentFuncs}
	pageEnv := rt.Env{Vars: map[string]*rt.Type{"vars": varsT, "page": rt.String, "pages": rt.String}, Funcs: DocumentFuncs}
	t.header = c.blocks("/header", doc["header"], pageEnv)
	t.footer = c.blocks("/footer", doc["footer"], pageEnv)
	t.body = c.blocks("/body", doc["body"], env)
	if _, ok := doc["body"]; !ok {
		c.errf("", "body is required")
	}
	return t
}

func (c *docCompiler) blocks(ptr string, v any, env rt.Env) []*docBlock {
	if v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		c.errf(ptr, "a list of blocks")
		return nil
	}
	var out []*docBlock
	for i, it := range list {
		if b := c.block(fmt.Sprintf("%s/%d", ptr, i), it, env); b != nil {
			out = append(out, b)
		}
	}
	return out
}

func (c *docCompiler) block(ptr string, v any, env rt.Env) *docBlock {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "a block is an object such as {\"text\": \"...\"}")
		return nil
	}
	b := &docBlock{}
	for _, k := range docBlockKinds {
		if _, ok := m[k]; ok {
			if b.kind != "" {
				c.errf(ptr, "a block has one of %s", strings.Join(docBlockKinds, ", "))
				return nil
			}
			b.kind = k
		}
	}
	if b.kind == "" {
		c.errf(ptr, "a block has one of %s", strings.Join(docBlockKinds, ", "))
		return nil
	}
	allowed := map[string][]string{
		"heading": {"level"}, "text": {"align", "size"}, "fields": {"columns"}, "table": {},
		"image": {"width", "align"}, "qr": {"size", "align"}, "spacer": {}, "pageBreak": {},
	}[b.kind]
	for k := range m {
		if k != b.kind && k != "when" && !contains(allowed, k) {
			c.errf(ptr+"/"+k, "unknown member %q for a %s block", k, b.kind)
		}
	}
	if w, ok := m["when"]; ok {
		b.when = c.expr(ptr+"/when", w, env, rt.Bool)
	}
	if a, ok := m["align"]; ok {
		b.align, _ = a.(string)
		if b.align != "left" && b.align != "center" && b.align != "right" {
			c.errf(ptr+"/align", "align is left, center, or right")
		}
	}
	val := m[b.kind]
	switch b.kind {
	case "heading", "text":
		b.text = c.text(ptr+"/"+b.kind, val, env)
		b.level = 1
		if l, ok := m["level"]; ok {
			n, isNum := number(l)
			if !isNum || n < 1 || n > 3 {
				c.errf(ptr+"/level", "level is 1, 2, or 3")
			}
			b.level = int(n)
		}
		if s, ok := m["size"]; ok {
			n, isNum := number(s)
			if !isNum || n < 6 || n > 36 {
				c.errf(ptr+"/size", "size is 6 to 36 points")
			}
			b.size = n
		}
	case "fields":
		list, _ := val.([]any)
		if len(list) == 0 {
			c.errf(ptr+"/fields", "fields is a list of {\"label\": ..., \"value\": ...}")
		}
		for i, it := range list {
			fp := fmt.Sprintf("%s/fields/%d", ptr, i)
			fm, _ := it.(map[string]any)
			if fm == nil || fm["label"] == nil || fm["value"] == nil || len(fm) != 2 {
				c.errf(fp, "a field is {\"label\": ..., \"value\": ...}")
				continue
			}
			b.fields = append(b.fields, [2]*docText{c.text(fp+"/label", fm["label"], env), c.text(fp+"/value", fm["value"], env)})
		}
		b.columns = 1
		if cl, ok := m["columns"]; ok {
			n, isNum := number(cl)
			if !isNum || n < 1 || n > 4 {
				c.errf(ptr+"/columns", "columns is 1 to 4")
			}
			b.columns = int(n)
		}
	case "table":
		c.table(ptr+"/table", val, env, b)
	case "image":
		b.image = c.expr(ptr+"/image", val, env, nil)
		if w, ok := m["width"]; ok {
			b.width, _ = number(w)
		}
	case "qr":
		b.text = c.text(ptr+"/qr", val, env)
		b.width = 30
		if s, ok := m["size"]; ok {
			n, isNum := number(s)
			if !isNum || n < 10 || n > 100 {
				c.errf(ptr+"/size", "size is 10 to 100 millimetres")
			}
			b.width = n
		}
	case "spacer":
		n, isNum := number(val)
		if !isNum || n <= 0 || n > 200 {
			c.errf(ptr+"/spacer", "spacer is a height in millimetres")
		}
		b.space = n
	case "pageBreak":
		if val != true {
			c.errf(ptr+"/pageBreak", "pageBreak is true")
		}
	}
	return b
}

func (c *docCompiler) table(ptr string, v any, env rt.Env, b *docBlock) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "a table is {\"rows\": <expr>, \"columns\": [...], \"footer\"?: [[...]]}")
		return
	}
	for k := range m {
		if k != "rows" && k != "columns" && k != "footer" {
			c.errf(ptr+"/"+k, "unknown member %q (rows, columns, footer)", k)
		}
	}
	b.rows = c.expr(ptr+"/rows", m["rows"], env, rt.ListOf(rt.Any))
	rowEnv := rt.Env{Vars: map[string]*rt.Type{"row": rt.Any, "index": rt.Int}, Funcs: env.Funcs}
	for k, t := range env.Vars {
		rowEnv.Vars[k] = t
	}
	cols, _ := m["columns"].([]any)
	if len(cols) == 0 {
		c.errf(ptr+"/columns", "columns is a list of {\"label\", \"value\", \"width\"?, \"align\"?}")
	}
	for i, it := range cols {
		cp := fmt.Sprintf("%s/columns/%d", ptr, i)
		cm, _ := it.(map[string]any)
		if cm == nil {
			c.errf(cp, "a column is an object")
			continue
		}
		col := docColumn{width: 1}
		for k, cv := range cm {
			switch k {
			case "label":
				col.label = c.text(cp+"/label", cv, env)
			case "value":
				col.value = c.text(cp+"/value", cv, rowEnv)
			case "width":
				n, isNum := number(cv)
				if !isNum || n <= 0 {
					c.errf(cp+"/width", "width is a positive relative weight")
				}
				col.width = n
			case "align":
				col.align, _ = cv.(string)
				if col.align != "left" && col.align != "center" && col.align != "right" {
					c.errf(cp+"/align", "align is left, center, or right")
				}
			default:
				c.errf(cp+"/"+k, "unknown member %q (label, value, width, align)", k)
			}
		}
		if col.label == nil || col.value == nil {
			c.errf(cp, "label and value are required")
			continue
		}
		b.cols = append(b.cols, col)
	}
	if f, ok := m["footer"]; ok {
		rows, _ := f.([]any)
		for i, r := range rows {
			cells, _ := r.([]any)
			if len(cells) != len(cols) {
				c.errf(fmt.Sprintf("%s/footer/%d", ptr, i), "a footer row has one text per column")
				continue
			}
			var out []*docText
			for j, cell := range cells {
				out = append(out, c.text(fmt.Sprintf("%s/footer/%d/%d", ptr, i, j), cell, env))
			}
			b.footer = append(b.footer, out)
		}
	}
}

func (c *docCompiler) expr(ptr string, v any, env rt.Env, want *rt.Type) rt.Program {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		c.errf(ptr, "an expression string")
		return nil
	}
	p, err := c.exprs.Compile(s, env, want, true)
	if err != nil {
		c.errf(ptr, "%v", err)
		return nil
	}
	return p
}

// text compiles literal text with {{ expr }} interpolations.
func (c *docCompiler) text(ptr string, v any, env rt.Env) *docText {
	s, ok := v.(string)
	if !ok {
		c.errf(ptr, "text is a string with {{ expr }} interpolations")
		return &docText{parts: []string{""}}
	}
	t := &docText{}
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			if strings.Contains(s, "}}") {
				c.errf(ptr, "unmatched }} in text")
			}
			t.parts = append(t.parts, s)
			return t
		}
		j := strings.Index(s[i:], "}}")
		if j < 0 {
			c.errf(ptr, "unclosed {{ in text")
			t.parts = append(t.parts, s)
			return t
		}
		t.parts = append(t.parts, s[:i])
		src := strings.TrimSpace(s[i+2 : i+j])
		p, err := c.exprs.Compile(src, env, nil, true)
		if err != nil {
			c.errf(ptr, "%s: %v", src, err)
		}
		t.progs = append(t.progs, p)
		s = s[i+j+2:]
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	}
	return 0, false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ImageFunc loads the bytes of an image block's value: a blob value or a
// manifest file path ("templates/...").
type ImageFunc func(v any) (data []byte, imageType string, err error)

// Evaluate renders the template with vars at time now.
func (t *DocumentTemplate) Evaluate(vars map[string]any, now time.Time, image ImageFunc) (kernel.Document, error) {
	if vars == nil {
		vars = map[string]any{}
	}
	e := &docEval{t: t, image: image}
	e.env = map[string]any{"vars": vars, rt.FnNow: func() time.Time { return now },
		rt.FnFormatDate: e.formatDate, rt.FnFormatNumber: e.formatNumber}
	doc := kernel.Document{Size: t.size, Landscape: t.landscape, Margin: t.margin}
	pageEnv := clone(e.env)
	pageEnv["page"], pageEnv["pages"] = "{page}", "{pages}"
	var err error
	if doc.Header, err = e.blocks(t.header, pageEnv); err != nil {
		return doc, err
	}
	if doc.Footer, err = e.blocks(t.footer, pageEnv); err != nil {
		return doc, err
	}
	doc.Body, err = e.blocks(t.body, e.env)
	return doc, err
}

type docEval struct {
	t     *DocumentTemplate
	env   map[string]any
	image ImageFunc
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (e *docEval) blocks(bs []*docBlock, env map[string]any) ([]kernel.DocBlock, error) {
	var out []kernel.DocBlock
	for _, b := range bs {
		if b.when != nil {
			ok, err := b.when.Eval(env)
			if err != nil {
				return nil, err
			}
			if ok != true {
				continue
			}
		}
		kb, err := e.block(b, env)
		if err != nil {
			return nil, err
		}
		out = append(out, kb)
	}
	return out, nil
}

func (e *docEval) block(b *docBlock, env map[string]any) (kernel.DocBlock, error) {
	kb := kernel.DocBlock{Kind: b.kind, Level: b.level, Align: b.align, Size: b.size, Columns: b.columns, Width: b.width, Space: b.space}
	switch b.kind {
	case "heading", "text":
		s, err := e.text(b.text, env)
		if err != nil {
			return kb, err
		}
		kb.Runs = runs(s)
	case "qr":
		s, err := e.text(b.text, env)
		if err != nil {
			return kb, err
		}
		kb.QR = s
	case "fields":
		for _, f := range b.fields {
			l, err := e.text(f[0], env)
			if err != nil {
				return kb, err
			}
			v, err := e.text(f[1], env)
			if err != nil {
				return kb, err
			}
			kb.Fields = append(kb.Fields, [2]string{l, v})
		}
	case "table":
		tbl := &kernel.DocTable{}
		for _, c := range b.cols {
			l, err := e.text(c.label, env)
			if err != nil {
				return kb, err
			}
			tbl.Columns = append(tbl.Columns, kernel.DocColumn{Label: l, Width: c.width, Align: c.align})
		}
		rows, err := b.rows.Eval(env)
		if err != nil {
			return kb, err
		}
		list, _ := rows.([]any)
		rowEnv := clone(env)
		for i, r := range list {
			rowEnv["row"], rowEnv["index"] = r, int64(i)
			var cells []string
			for _, c := range b.cols {
				s, err := e.text(c.value, rowEnv)
				if err != nil {
					return kb, err
				}
				cells = append(cells, s)
			}
			tbl.Rows = append(tbl.Rows, cells)
		}
		for _, fr := range b.footer {
			var cells []string
			for _, cell := range fr {
				s, err := e.text(cell, env)
				if err != nil {
					return kb, err
				}
				cells = append(cells, s)
			}
			tbl.Footer = append(tbl.Footer, cells)
		}
		kb.Table = tbl
	case "image":
		v, err := b.image.Eval(env)
		if err != nil {
			return kb, err
		}
		if v == nil {
			kb.Kind = kernel.BlockSpacer
			return kb, nil // no image: nothing drawn
		}
		if e.image == nil {
			return kb, fmt.Errorf("images are not available")
		}
		kb.Image, kb.ImageType, err = e.image(v)
		if err != nil {
			return kb, err
		}
	}
	return kb, nil
}

func (e *docEval) text(t *docText, env map[string]any) (string, error) {
	var s strings.Builder
	for i, part := range t.parts {
		s.WriteString(part)
		if i < len(t.progs) && t.progs[i] != nil {
			v, err := t.progs[i].Eval(env)
			if err != nil {
				return "", err
			}
			s.WriteString(e.show(v))
		}
	}
	return s.String(), nil
}

// runs splits **bold** spans.
func runs(s string) []kernel.DocRun {
	var out []kernel.DocRun
	for i, part := range strings.Split(s, "**") {
		if part != "" {
			out = append(out, kernel.DocRun{Text: part, Bold: i%2 == 1})
		}
	}
	return out
}

// show renders an interpolated value: numbers and dates in the template's
// locale, nil as empty.
func (e *docEval) show(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case time.Time:
		return e.formatDate(x, "date")
	case int64, int, float64, kernel.Decimal:
		return e.formatNumber(x, -1)
	case bool:
		if x {
			return "✓"
		}
		return ""
	case []any:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = e.show(it)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// separators returns the decimal and group separators of a locale.
func separators(locale string) (dec, group string) {
	switch strings.ToLower(strings.SplitN(locale, "-", 2)[0]) {
	case "el", "de", "es", "it", "pt", "nl", "da", "id", "tr", "ro", "hr", "sl":
		return ",", "."
	case "fr", "fi", "sv", "nb", "cs", "pl", "ru", "uk", "bg", "sk", "hu":
		return ",", " "
	}
	return ".", ","
}

// formatNumber formats n with digits decimals (-1: as needed) and the
// locale's separators.
func (e *docEval) formatNumber(n any, digits int) string {
	var s string
	switch x := n.(type) {
	case kernel.Decimal:
		s = x.String()
	case int64:
		s = strconv.FormatInt(x, 10)
	case int:
		s = strconv.Itoa(x)
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		s = x
	case nil:
		return ""
	default:
		return fmt.Sprint(n)
	}
	if digits >= 0 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return s
		}
		s = strconv.FormatFloat(roundHalfAway(f, digits), 'f', digits, 64)
	}
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	whole, frac, hasFrac := strings.Cut(s, ".")
	dec, group := separators(e.t.locale)
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(group)
		}
		b.WriteRune(r)
	}
	out := b.String()
	if hasFrac {
		out += dec + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func roundHalfAway(f float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(f*p) / p
}

// formatDate formats a time in the template's time zone: date, datetime,
// time, or a Go layout such as "02/01/2006".
func (e *docEval) formatDate(v any, style string) string {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	case string:
		p, err := time.Parse(time.RFC3339, x)
		if err != nil {
			return x
		}
		t = p
	default:
		return ""
	}
	t = t.In(e.t.loc)
	dayFirst := !strings.HasPrefix(strings.ToLower(e.t.locale), "en-us") && strings.ToLower(e.t.locale) != "en"
	switch style {
	case "", "date":
		if dayFirst {
			return t.Format("02/01/2006")
		}
		return t.Format("Jan 2, 2006")
	case "datetime":
		if dayFirst {
			return t.Format("02/01/2006 15:04")
		}
		return t.Format("Jan 2, 2006 15:04")
	case "time":
		return t.Format("15:04")
	}
	return t.Format(style)
}
