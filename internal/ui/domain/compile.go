package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// Size limits of ui/ files (ADR 0016).
const (
	MaxPageBytes   = 256 << 10
	MaxStylesBytes = 256 << 10
	MaxAssetBytes  = 2 << 20
	MaxAssetsBytes = 20 << 20
)

// AssetTypes maps the allowed asset extensions to their content types.
var AssetTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".svg": "image/svg+xml", ".ico": "image/x-icon",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
}

// Source is one file below ui/ (or modules/<m>/ui/pages/).
type Source struct {
	File string
	Data []byte
}

// Deps are the compiler's dependencies.
type Deps struct {
	Types schemaapi.TypeRegistry
	Exprs rt.Compiler
	// FlowAction reports whether a flow implements action name on ct, and
	// its input schema.
	FlowAction func(ct *schemaapi.CompiledType, name string) (input json.RawMessage, ok bool)
}

// uiFuncEnv are the framework built-ins of UI expressions.
var uiFuncEnv = []string{rt.FnNow, rt.FnCan, rt.FnFormatDate, rt.FnFormatNumber}

type srcInfo struct {
	list bool
	ct   *schemaapi.CompiledType
}

type pageCtx struct {
	sources    map[string]*srcInfo
	state      map[string]*rt.Type
	dialogs    map[string]bool
	dialogRefs map[string]string // pointer -> dialog name
	types      map[string]bool
}

// scope is the expression environment at one place of a page.
type scope struct {
	vars  map[string]*rt.Type
	row   *schemaapi.CompiledType // item type inside row templates
	funcs []string
}

func (s *scope) with(vars map[string]*rt.Type) *scope {
	m := make(map[string]*rt.Type, len(s.vars)+len(vars))
	for k, v := range s.vars {
		m[k] = v
	}
	for k, v := range vars {
		m[k] = v
	}
	return &scope{vars: m, row: s.row, funcs: s.funcs}
}

type pageHead struct {
	name   string
	file   string
	route  string
	params map[string]Param
	needed []string // route parameters
	doc    map[string]any
}

type compiler struct {
	d      Deps
	probs  []Problem
	file   string
	pages  map[string]*pageHead
	assets map[string][]byte
	page   *pageCtx
	ftypes map[*schemaapi.CompiledType]*rt.Type
}

func (c *compiler) errf(ptr, format string, args ...any) {
	c.probs = append(c.probs, Problem{File: c.file, Pointer: ptr, Message: fmt.Sprintf(format, args...)})
}

// Compile validates the ui/ files. It returns nil and the problems when
// there are errors; warnings are returned with a non-nil result.
func Compile(d Deps, sources []Source) (*Compiled, []Problem) {
	c := &compiler{d: d, pages: map[string]*pageHead{}, assets: map[string][]byte{},
		ftypes: map[*schemaapi.CompiledType]*rt.Type{}}
	out := &Compiled{Assets: c.assets}
	var appSrc *Source
	var pageSrcs []Source
	total := 0
	for _, s := range sources {
		c.file = s.File
		rel := s.File
		if strings.HasPrefix(rel, "modules/") {
			rel = strings.Join(strings.Split(rel, "/")[2:], "/")
		}
		switch {
		case rel == "ui/app.json":
			appSrc = &s
		case rel == "ui/styles.css":
			if len(s.Data) > MaxStylesBytes {
				c.errf("", "styles.css is larger than %d bytes", MaxStylesBytes)
			}
			out.Styles = s.Data
		case strings.HasPrefix(rel, "ui/pages/") && path.Ext(rel) == ".json" && !strings.Contains(rel[len("ui/pages/"):], "/"):
			pageSrcs = append(pageSrcs, s)
		case strings.HasPrefix(rel, "ui/assets/"):
			name := rel[len("ui/assets/"):]
			if _, ok := AssetTypes[strings.ToLower(path.Ext(name))]; !ok {
				c.errf("", "asset type %q is not allowed (images and fonts only)", path.Ext(name))
				continue
			}
			if len(s.Data) > MaxAssetBytes {
				c.errf("", "asset is larger than %d bytes", MaxAssetBytes)
			}
			total += len(s.Data)
			c.assets[name] = s.Data
		default:
			c.errf("", "unexpected file: ui/ holds app.json, pages/*.json, styles.css, and assets/**")
		}
	}
	if total > MaxAssetsBytes {
		c.file = "ui/assets"
		c.errf("", "assets total %d bytes, more than %d", total, MaxAssetsBytes)
	}
	routes := map[string]string{}
	for _, s := range pageSrcs {
		if h := c.head(s, routes); h != nil {
			c.pages[h.name] = h
		}
	}
	if appSrc == nil && len(pageSrcs) > 0 {
		c.file = "ui/app.json"
		c.errf("", "ui/app.json is required when ui/pages exist")
	}
	if appSrc != nil {
		c.file = appSrc.File
		out.App = c.app(appSrc.Data)
	}
	names := make([]string, 0, len(c.pages))
	for n := range c.pages {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p := c.compilePage(c.pages[n]); p != nil {
			out.Pages = append(out.Pages, p)
		}
	}
	sort.SliceStable(c.probs, func(i, j int) bool {
		if c.probs[i].File != c.probs[j].File {
			return c.probs[i].File < c.probs[j].File
		}
		return c.probs[i].Pointer < c.probs[j].Pointer
	})
	for _, p := range c.probs {
		if !p.Warning {
			return nil, c.probs
		}
	}
	return out, c.probs
}

func decode(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the document")
	}
	if m == nil {
		return nil, fmt.Errorf("the document must be an object")
	}
	return m, nil
}

// browserDoc strips the authoring-only members of a document.
func browserDoc(m map[string]any) json.RawMessage {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k != "$schema" && k != "x-layout" {
			out[k] = v
		}
	}
	b, _ := json.Marshal(out)
	return b
}

func (c *compiler) known(ptr string, m map[string]any, allowed ...string) {
	for k := range m {
		if !slices.Contains(allowed, k) {
			c.errf(ptr+"/"+esc(k), "unknown member %q", k)
		}
	}
}

// head reads the parts of a page other pages refer to: name, route, params.
func (c *compiler) head(s Source, routes map[string]string) *pageHead {
	c.file = s.File
	if len(s.Data) > MaxPageBytes {
		c.errf("", "page is larger than %d bytes", MaxPageBytes)
		return nil
	}
	m, err := decode(s.Data)
	if err != nil {
		c.errf("", "invalid JSON: %v", err)
		return nil
	}
	h := &pageHead{file: s.File, doc: m, params: map[string]Param{}}
	h.name, _ = m["page"].(string)
	if !nameRe.MatchString(h.name) {
		c.errf("/page", "page name must match %s", nameRe)
		return nil
	}
	if o, dup := c.pages[h.name]; dup {
		c.errf("/page", "page %q is also defined in %s", h.name, o.file)
		return nil
	}
	if ps, ok := m["params"]; ok {
		pm, ok := ps.(map[string]any)
		if !ok {
			c.errf("/params", "params must be an object")
		}
		for k, v := range pm {
			b, _ := json.Marshal(v)
			var p Param
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.DisallowUnknownFields()
			switch {
			case !identRe.MatchString(k):
				c.errf("/params/"+esc(k), "parameter names must be identifiers")
			case dec.Decode(&p) != nil:
				c.errf("/params/"+esc(k), "a parameter is {\"type\": ..., \"format\"?: ...}")
			default:
				if _, err := paramType(p); err != nil {
					c.errf("/params/"+esc(k)+"/type", "%v", err)
				}
				h.params[k] = p
			}
		}
	}
	h.route, _ = m["route"].(string)
	if h.route == "" {
		c.errf("/route", "route is required")
		return h
	}
	needed, shape, err := routeParams(h.route)
	if err != nil {
		c.errf("/route", "%v", err)
		return h
	}
	if o, dup := routes[shape]; dup {
		c.errf("/route", "route %s conflicts with page %s", h.route, o)
	}
	routes[shape] = h.name
	for _, p := range needed {
		if _, ok := h.params[p]; !ok {
			c.errf("/route", "route parameter %q is not declared in params", p)
		}
	}
	h.needed = needed
	return h
}

func (c *compiler) baseScope(params map[string]Param) *scope {
	pt := map[string]*rt.Type{}
	for k, p := range params {
		if t, err := paramType(p); err == nil {
			pt[k] = t
		}
	}
	return &scope{funcs: uiFuncEnv, vars: map[string]*rt.Type{
		"principal": accessapi.PrincipalExprType(),
		"params":    rt.MapOf(pt),
	}}
}

// principalScope is the environment of expressions the server evaluates
// to filter pages and navigation: the principal and now() only.
func principalScope() *scope {
	return &scope{funcs: []string{rt.FnNow}, vars: map[string]*rt.Type{"principal": accessapi.PrincipalExprType()}}
}

func (c *compiler) app(data []byte) *App {
	m, err := decode(data)
	if err != nil {
		c.errf("", "invalid JSON: %v", err)
		return nil
	}
	c.known("", m, "$schema", "title", "logo", "home", "requireLogin", "nav", "notFound", "x-layout")
	a := &App{RequireLogin: true, Doc: browserDoc(m)}
	ps := principalScope()
	a.Title, _ = m["title"].(string)
	if v, ok := m["title"]; ok {
		c.text("/title", v, ps)
	}
	if v, ok := m["logo"]; ok {
		s, _ := v.(string)
		if _, found := c.assets[strings.TrimPrefix(s, "assets/")]; !strings.HasPrefix(s, "assets/") || !found {
			c.errf("/logo", "logo must name a file in ui/assets as assets/<path>")
		}
	}
	if v, ok := m["requireLogin"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			c.errf("/requireLogin", "requireLogin must be a boolean")
		}
		a.RequireLogin = b
	}
	if v, ok := m["notFound"]; ok {
		c.text("/notFound", v, ps)
	}
	a.Home, _ = m["home"].(string)
	if h, ok := c.pages[a.Home]; !ok {
		c.errf("/home", "home must name a page")
	} else if len(h.needed) > 0 {
		c.errf("/home", "the home page must not need route parameters")
	}
	if v, ok := m["nav"]; ok {
		items, isList := v.([]any)
		if !isList {
			c.errf("/nav", "nav must be a list")
		}
		for i, it := range items {
			ptr := "/nav/" + strconv.Itoa(i)
			im, isObj := it.(map[string]any)
			if !isObj {
				c.errf(ptr, "a nav entry is an object")
				continue
			}
			c.known(ptr, im, "label", "page", "params", "visible")
			n := NavItem{}
			c.require(ptr, im, "label", "page")
			if l, ok := im["label"]; ok {
				c.text(ptr+"/label", l, ps)
			}
			n.Page, _ = im["page"].(string)
			c.navigate(ptr, "page", n.Page, "params", im["params"], ps)
			if v, ok := im["visible"]; ok {
				n.Visible = c.expr(ptr+"/visible", v, ps, "bool")
			}
			a.Nav = append(a.Nav, n)
		}
	}
	return a
}

func (c *compiler) require(ptr string, m map[string]any, names ...string) {
	for _, n := range names {
		if _, ok := m[n]; !ok {
			c.errf(ptr, "%s is required", n)
		}
	}
}

func (c *compiler) compilePage(h *pageHead) *Page {
	c.file = h.file
	m := h.doc
	c.known("", m, "$schema", "page", "title", "route", "params", "visible", "state", "data", "body", "x-layout")
	c.page = &pageCtx{sources: map[string]*srcInfo{}, state: map[string]*rt.Type{}, dialogs: map[string]bool{},
		dialogRefs: map[string]string{}, types: map[string]bool{}}
	p := &Page{Name: h.name, File: h.file, Route: h.route, Params: h.params, Doc: browserDoc(m)}
	if v, ok := m["visible"]; ok {
		p.Visible = c.expr("/visible", v, principalScope(), "bool")
	}
	p.Public = VisibleTo(p.Visible, accessapi.Anonymous(), time.Now())
	sc := c.baseScope(h.params)
	if v, ok := m["state"]; ok {
		c.state(v)
	}
	sc.vars["state"] = rt.MapOf(c.page.state)
	c.sources(m["data"], sc)
	if v, ok := m["title"]; ok {
		c.text("/title", v, sc)
	}
	if b, ok := m["body"]; ok {
		c.node("/body", b, sc)
	} else {
		c.errf("", "body is required")
	}
	for ptr, name := range c.page.dialogRefs {
		if !c.page.dialogs[name] {
			c.errf(ptr, "no dialog named %q on this page", name)
		}
	}
	for t := range c.page.types {
		p.Types = append(p.Types, t)
	}
	sort.Strings(p.Types)
	return p
}

func (c *compiler) state(v any) {
	sm, ok := v.(map[string]any)
	if !ok {
		c.errf("/state", "state must be an object")
		return
	}
	for k, sv := range sm {
		ptr := "/state/" + esc(k)
		decl, ok := sv.(map[string]any)
		if !ok || !identRe.MatchString(k) {
			c.errf(ptr, "a state variable is an identifier mapped to {\"type\": ..., \"default\"?: ...}")
			continue
		}
		c.known(ptr, decl, "type", "default")
		ts, _ := decl["type"].(string)
		t, err := stateType(ts)
		if err != nil {
			c.errf(ptr+"/type", "%v", err)
			continue
		}
		c.page.state[k] = t
	}
}

var dataRef = regexp.MustCompile(`\bdata\.([A-Za-z_][A-Za-z0-9_]*)`)

// sources declares the page's data sources, then type-checks their
// expressions with every source in scope and rejects dependency cycles.
func (c *compiler) sources(v any, sc *scope) {
	data := map[string]*rt.Type{}
	sc.vars["data"] = rt.MapOf(data)
	if v == nil {
		return
	}
	dm, ok := v.(map[string]any)
	if !ok {
		c.errf("/data", "data must be an object")
		return
	}
	names := make([]string, 0, len(dm))
	for k := range dm {
		names = append(names, k)
	}
	sort.Strings(names)
	decls := map[string]map[string]any{}
	for _, k := range names {
		ptr := "/data/" + esc(k)
		m, ok := dm[k].(map[string]any)
		if !ok || !identRe.MatchString(k) {
			c.errf(ptr, "a data source is an identifier mapped to an object")
			continue
		}
		var kinds []string
		for _, kind := range []string{"feature", "children", "query", "search"} {
			if _, ok := m[kind]; ok {
				kinds = append(kinds, kind)
			}
		}
		if len(kinds) != 1 {
			c.errf(ptr, "a data source has exactly one of feature, children, query, search")
			continue
		}
		var tn string
		if kinds[0] == "query" {
			c.known(ptr, m, "query")
			q, _ := m["query"].(map[string]any)
			tn, _ = q["type"].(string)
			ptr += "/query"
		} else {
			allowed := []string{kinds[0], "type"}
			if kinds[0] != "feature" {
				allowed = append(allowed, "limit")
			}
			c.known(ptr, m, allowed...)
			tn, _ = m["type"].(string)
		}
		ct := c.typeRef(ptr+"/type", tn)
		info := &srcInfo{list: kinds[0] != "feature", ct: ct}
		c.page.sources[k] = info
		if ct == nil {
			data[k] = rt.Any // reported once; uses are not checked further
			continue
		}
		if info.list {
			data[k] = rt.ListOf(c.featureType(ct))
		} else {
			data[k] = c.featureType(ct)
		}
		decls[k] = m
	}
	deps := map[string][]string{}
	for _, k := range names {
		m := decls[k]
		if m == nil {
			continue
		}
		ptr := "/data/" + esc(k)
		var srcs []string
		collect := func(s any) {
			if str, ok := s.(string); ok {
				srcs = append(srcs, str)
			}
		}
		switch {
		case m["feature"] != nil:
			collect(m["feature"])
			c.expr(ptr+"/feature", m["feature"], sc, "string")
		case m["children"] != nil:
			collect(m["children"])
			c.expr(ptr+"/children", m["children"], sc, "string")
		case m["search"] != nil:
			collect(m["search"])
			c.expr(ptr+"/search", m["search"], sc, "string")
		default:
			c.query(ptr+"/query", m["query"], sc, c.page.sources[k].ct, collect)
		}
		if l, ok := m["limit"]; ok {
			c.intProp(ptr+"/limit", l, 1, 500)
		}
		for _, s := range srcs {
			for _, ref := range dataRef.FindAllStringSubmatch(s, -1) {
				deps[k] = append(deps[k], ref[1])
			}
		}
	}
	// Cycles: depth-first search over data.<name> references.
	state := map[string]int{}
	var visit func(n string, trail []string) bool
	visit = func(n string, trail []string) bool {
		switch state[n] {
		case 1:
			c.errf("/data/"+esc(n), "data sources form a cycle: %s", strings.Join(append(trail, n), " -> "))
			return false
		case 2:
			return true
		}
		state[n] = 1
		for _, d := range deps[n] {
			if !visit(d, append(trail, n)) {
				return false
			}
		}
		state[n] = 2
		return true
	}
	for _, n := range names {
		visit(n, nil)
	}
}

var queryOps = []string{"eq", "ne", "lt", "lte", "gt", "gte", "in", "nin", "prefix", "contains", "exists"}

func (c *compiler) query(ptr string, v any, sc *scope, ct *schemaapi.CompiledType, collect func(any)) {
	q, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "query must be a query document")
		return
	}
	c.known(ptr, q, "type", "where", "sort", "scope", "limit")
	if w, ok := q["where"]; ok {
		c.predicate(ptr+"/where", w, sc, ct, collect, 0)
	}
	if s, ok := q["sort"]; ok {
		list, isList := s.([]any)
		if !isList {
			c.errf(ptr+"/sort", "sort must be a list")
		}
		for i, it := range list {
			sp := ptr + "/sort/" + strconv.Itoa(i)
			m, _ := it.(map[string]any)
			c.known(sp, m, "field", "dir")
			f, _ := m["field"].(string)
			c.queryField(sp+"/field", f, ct)
			if d, ok := m["dir"]; ok && d != "asc" && d != "desc" {
				c.errf(sp+"/dir", "dir must be asc or desc")
			}
		}
	}
	if s, ok := q["scope"]; ok {
		m, _ := s.(map[string]any)
		c.known(ptr+"/scope", m, "childrenOf", "descendantsOf")
		if len(m) != 1 {
			c.errf(ptr+"/scope", "scope has one of childrenOf, descendantsOf")
		}
		for k, e := range m {
			collect(e)
			c.expr(ptr+"/scope/"+k, e, sc, "string")
		}
	}
	if l, ok := q["limit"]; ok {
		c.intProp(ptr+"/limit", l, 1, 500)
	}
}

func (c *compiler) predicate(ptr string, v any, sc *scope, ct *schemaapi.CompiledType, collect func(any), depth int) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "a predicate is an object")
		return
	}
	if depth > 4 {
		c.errf(ptr, "predicates nest at most 4 deep")
		return
	}
	for _, comb := range []string{"and", "or"} {
		if l, ok := m[comb]; ok {
			c.known(ptr, m, comb)
			list, _ := l.([]any)
			for i, p := range list {
				c.predicate(ptr+"/"+comb+"/"+strconv.Itoa(i), p, sc, ct, collect, depth+1)
			}
			return
		}
	}
	if n, ok := m["not"]; ok {
		c.known(ptr, m, "not")
		c.predicate(ptr+"/not", n, sc, ct, collect, depth+1)
		return
	}
	c.known(ptr, m, "field", "op", "value")
	f, _ := m["field"].(string)
	c.queryField(ptr+"/field", f, ct)
	op, _ := m["op"].(string)
	if !slices.Contains(queryOps, op) {
		c.errf(ptr+"/op", "op must be one of %s", strings.Join(queryOps, ", "))
	}
	if val, ok := m["value"]; ok {
		collect(val)
		c.expr(ptr+"/value", val, sc, "")
	} else {
		c.errf(ptr, "value is required")
	}
}

// queryField checks that users may filter and sort on path: it must be in
// the public query view (x-indexed and public).
func (c *compiler) queryField(ptr, p string, ct *schemaapi.CompiledType) {
	if !c.readPath(ptr, p, ct) {
		return
	}
	if q := ct.View(schemaapi.AudPublic, schemaapi.DirQuery); q != nil && !q.Queryable[top(p)] {
		c.errf(ptr, "field %s of %s is not queryable: add \"x-indexed\": true (owner-only fields cannot be filtered)", p, ct.Name)
	}
}

func top(p string) string {
	if i := strings.Index(p[1:], "/"); i >= 0 {
		return p[:i+1]
	}
	return p
}

var pseudoFields = map[string]bool{"/id": true, "/type": true, "/parentId": true, "/version": true}

// readPath checks that path names a field users can read.
func (c *compiler) readPath(ptr, p string, ct *schemaapi.CompiledType) bool {
	if !strings.HasPrefix(p, "/") {
		c.errf(ptr, "a field path is a JSON Pointer such as \"/title\"")
		return false
	}
	if pseudoFields[p] {
		return true
	}
	f, ok := ct.Field(p)
	if !ok {
		c.errf(ptr, "%s has no field %s", ct.Name, p)
		return false
	}
	if f.Visibility == schemaapi.VisPrivate || f.Visibility == schemaapi.VisSystem || f.WriteOnly {
		c.errf(ptr, "field %s of %s is not readable by users (x-visibility %s)", p, ct.Name, f.Visibility)
		return false
	}
	return true
}

func (c *compiler) writePath(ptr, p string, ct *schemaapi.CompiledType, dir schemaapi.Direction) bool {
	if !strings.HasPrefix(p, "/") {
		c.errf(ptr, "a field path is a JSON Pointer such as \"/title\"")
		return false
	}
	if _, ok := ct.Field(p); !ok {
		c.errf(ptr, "%s has no field %s", ct.Name, p)
		return false
	}
	if v := ct.View(schemaapi.AudOwner, dir); v == nil || !v.Writable[top(p)] {
		c.errf(ptr, "field %s of %s cannot be written on %s (readOnly, x-write, or x-visibility)", p, ct.Name, dir)
		return false
	}
	return true
}

// featureType is the expression type of a feature in the browser: its
// readable fields plus id, type, parentId, and version.
func (c *compiler) featureType(ct *schemaapi.CompiledType) *rt.Type {
	if t, ok := c.ftypes[ct]; ok {
		return t
	}
	read := ct.View(schemaapi.AudOwner, schemaapi.DirRead)
	fields := map[string]*rt.Type{}
	for _, p := range ct.Root.Props {
		if read == nil || read.Readable["/"+p.Name] {
			fields[p.Name] = browserType(p.Type)
		}
	}
	fields["id"] = rt.String
	fields["type"] = rt.String
	fields["parentId"] = rt.String
	fields["version"] = rt.Int
	t := rt.MapOf(fields)
	c.ftypes[ct] = t
	return t
}

// browserType is the expression type of a wire value in the browser.
// Decimals and string-wire int64 values stay strings there, so arithmetic
// on them is rejected instead of silently going wrong.
func browserType(vt *schemaapi.ValueType) *rt.Type {
	switch vt.Kind {
	case kernel.KindDecimal:
		return rt.String
	case kernel.KindInt64:
		if !vt.NumberWire {
			return rt.String
		}
	case kernel.KindBlob:
		// Reads add a short-lived signed url and thumbnail urls by size.
		return rt.MapOf(map[string]*rt.Type{"id": rt.String, "size": rt.Int, "contentType": rt.String,
			"url": rt.String, "thumbnails": rt.OpenMap()})
	case kernel.KindList:
		return rt.ListOf(browserType(vt.Elem))
	case kernel.KindObject:
		if vt.Union == nil {
			fields := map[string]*rt.Type{}
			for _, p := range vt.Props {
				fields[p.Name] = browserType(p.Type)
			}
			return rt.MapOf(fields)
		}
	}
	return vt.ExprType()
}

func (c *compiler) typeRef(ptr, name string) *schemaapi.CompiledType {
	if name == "" {
		c.errf(ptr, "type is required")
		return nil
	}
	ct, ok := c.d.Types.ByName(name)
	if !ok {
		c.errf(ptr, "unknown type %q", name)
		return nil
	}
	if c.page != nil {
		c.page.types[ct.Name] = true
	}
	return ct
}

func wantType(want string) *rt.Type {
	switch want {
	case "bool":
		return rt.Bool
	case "string":
		return rt.String
	case "number":
		return rt.Float
	}
	return nil
}

// expr checks an expression prop. JSON literals stand for themselves.
func (c *compiler) expr(ptr string, v any, sc *scope, want string) rt.Program {
	var src string
	switch x := v.(type) {
	case string:
		src = x
	case bool, json.Number, nil:
		b, _ := json.Marshal(x)
		src = string(b)
	default:
		c.errf(ptr, "an expression is a string (string literals need inner quotes: \"'done'\")")
		return nil
	}
	if strings.TrimSpace(src) == "" {
		c.errf(ptr, "empty expression")
		return nil
	}
	if err := checkSubset(src); err != nil {
		c.errf(ptr, "%v", err)
		return nil
	}
	p, err := c.d.Exprs.Compile(src, rt.Env{Vars: sc.vars, Funcs: sc.funcs}, wantType(want), true)
	if err != nil {
		c.errf(ptr, "%v%s", err, quoteHint(src, err))
		return nil
	}
	return p
}

// quoteHint suggests quoting when a bare word was probably meant as text.
func quoteHint(src string, err error) string {
	if identRe.MatchString(src) && strings.Contains(err.Error(), "unknown name") {
		return fmt.Sprintf(" (for the text %s write \"'%s'\")", src, src)
	}
	return ""
}

func (c *compiler) text(ptr string, v any, sc *scope) {
	s, ok := v.(string)
	if !ok {
		c.errf(ptr, "text must be a string")
		return
	}
	exprs, err := splitTemplate(s)
	if err != nil {
		c.errf(ptr, "%v", err)
		return
	}
	for _, e := range exprs {
		c.expr(ptr, e, sc, "")
	}
}

func (c *compiler) intProp(ptr string, v any, lo, hi int64) {
	n, ok := v.(json.Number)
	i, err := n.Int64()
	if !ok || err != nil || i < lo || i > hi {
		c.errf(ptr, "must be an integer from %d to %d", lo, hi)
	}
}

// navigate checks a page reference (member pageKey of the object at ptr)
// and its parameters (member paramsKey).
func (c *compiler) navigate(ptr, pageKey, page, paramsKey string, params any, sc *scope) {
	h, ok := c.pages[page]
	if !ok {
		c.errf(ptr+"/"+pageKey, "unknown page %q", page)
		return
	}
	pm, _ := params.(map[string]any)
	if params != nil && pm == nil {
		c.errf(ptr+"/"+paramsKey, "%s must be an object", paramsKey)
	}
	for k, v := range pm {
		if _, ok := h.params[k]; !ok {
			c.errf(ptr+"/"+paramsKey+"/"+esc(k), "page %s has no parameter %q", page, k)
		}
		c.expr(ptr+"/"+paramsKey+"/"+esc(k), v, sc, "")
	}
	for _, need := range h.needed {
		if _, ok := pm[need]; !ok {
			c.errf(ptr, "page %s needs parameter %q", page, need)
		}
	}
}

// nodeCtx is what the props of one component refer to.
type nodeCtx struct {
	comp *Component
	ct   *schemaapi.CompiledType // the type of path and action props
	row  *schemaapi.CompiledType // item type of row props
	// path checks a path prop; defaults to readPath on ct.
	path func(ptr, p string)
}

func (c *compiler) node(ptr string, v any, sc *scope) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "a component is an object with a \"component\" member")
		return
	}
	name, _ := m["component"].(string)
	comp, ok := component(name)
	if !ok {
		c.errf(ptr+"/component", "unknown component %q", name)
		return
	}
	nc := &nodeCtx{comp: comp}
	c.bind(ptr, m, sc, nc)
	if vis, ok := m["visible"]; ok {
		c.expr(ptr+"/visible", vis, sc, "bool")
	}
	if cl, ok := m["class"]; ok {
		if _, isStr := cl.(string); !isStr {
			c.errf(ptr+"/class", "class must be a string")
		}
	}
	if on, ok := m["on"]; ok {
		om, isObj := on.(map[string]any)
		if !isObj {
			c.errf(ptr+"/on", "on maps event names to step lists")
		}
		for ev, st := range om {
			if !slices.Contains(comp.Events, ev) {
				c.errf(ptr+"/on/"+esc(ev), "%s has no event %q", name, ev)
				continue
			}
			ssc := sc
			if name == "form" {
				ssc = sc.with(map[string]*rt.Type{"form": rt.MapOf(map[string]*rt.Type{"values": rt.OpenMap()})})
			}
			c.steps(ptr+"/on/"+esc(ev), st, ssc)
		}
	}
	allowed := []string{"component", "visible", "class", "on"}
	for _, p := range comp.Props {
		allowed = append(allowed, p.Name)
	}
	c.known(ptr, m, allowed...)
	c.props(ptr, m, comp.Props, sc, nc)
	c.special(ptr, m, sc, nc)
}

// bind resolves the type a component's path and action props refer to.
func (c *compiler) bind(ptr string, m map[string]any, sc *scope, nc *nodeCtx) {
	nc.path = func(p, path string) {
		if nc.ct != nil {
			c.readPath(p, path, nc.ct)
		}
	}
	switch {
	case nc.comp.Name == "form":
		tn, _ := m["type"].(string)
		nc.ct = c.typeRef(ptr+"/type", tn)
		nc.path = func(string, string) {} // form checks writability
		return
	case nc.comp.Source != "":
		sn, has := m["source"].(string)
		if !has {
			if _, present := m["source"]; present {
				c.errf(ptr+"/source", "source must be a data source name")
				return
			}
			if nc.comp.Source == "feature" && sc.row != nil {
				nc.ct = sc.row
				return
			}
			if nc.comp.Name == "chart" {
				return
			}
			c.errf(ptr, "source is required (outside rows of a list)")
			return
		}
		src, ok := c.page.sources[sn]
		switch {
		case !ok:
			c.errf(ptr+"/source", "no data source named %q", sn)
			return
		case src.ct == nil:
			return // its type error is reported
		case nc.comp.Source == "list" && !src.list:
			c.errf(ptr+"/source", "%s needs a list source (children, query, or search)", nc.comp.Name)
			return
		case nc.comp.Source == "feature" && src.list:
			c.errf(ptr+"/source", "%s needs a single-feature source", nc.comp.Name)
			return
		}
		nc.ct = src.ct
		if src.list {
			nc.row = src.ct
		}
	}
}

func (c *compiler) props(ptr string, m map[string]any, props []Prop, sc *scope, nc *nodeCtx) {
	rowScope := func(base *scope) *scope {
		if nc.row == nil {
			return base
		}
		r := base.with(map[string]*rt.Type{"item": c.featureType(nc.row), "index": rt.Int})
		r.row = nc.row
		return r
	}
	for _, p := range props {
		v, ok := m[p.Name]
		if !ok {
			if p.Required {
				c.errf(ptr, "%s is required", p.Name)
			}
			continue
		}
		psc := sc
		if p.Row {
			psc = rowScope(sc)
		}
		// Page parameters are checked with their page below.
		if pk := pageKeyOf(p.Name); pk != "" {
			if _, ok := m[pk]; !ok {
				c.errf(ptr+"/"+p.Name, "%s needs %s", p.Name, pk)
			}
			continue
		}
		c.prop(ptr+"/"+esc(p.Name), p, v, psc, nc)
	}
	if pg, ok := m["page"].(string); ok && hasProp(props, "page") {
		c.navigate(ptr, "page", pg, "params", m["params"], sc)
	}
	if pg, ok := m["rowPage"].(string); ok && hasProp(props, "rowPage") {
		c.navigate(ptr, "rowPage", pg, "rowParams", m["rowParams"], rowScope(sc))
	}
}

func pageKeyOf(paramsKey string) string {
	switch paramsKey {
	case "params":
		return "page"
	case "rowParams":
		return "rowPage"
	}
	return ""
}

func hasProp(props []Prop, name string) bool {
	for _, p := range props {
		if p.Name == name {
			return true
		}
	}
	return false
}

func (c *compiler) prop(ptr string, p Prop, v any, sc *scope, nc *nodeCtx) {
	str, isStr := v.(string)
	switch p.Kind {
	case KindText:
		c.text(ptr, v, sc)
	case KindExpr:
		c.expr(ptr, v, sc, p.Want)
	case KindPath:
		if !isStr {
			c.errf(ptr, "a field path is a string")
			return
		}
		nc.path(ptr, str)
	case KindPaths:
		list, ok := v.([]any)
		if !ok {
			c.errf(ptr, "a list of field paths")
			return
		}
		for i, it := range list {
			s, _ := it.(string)
			nc.path(ptr+"/"+strconv.Itoa(i), s)
		}
	case KindType:
		if !isStr {
			c.errf(ptr, "a type name is a string")
		}
	case KindPage:
		if !isStr {
			c.errf(ptr, "a page name is a string")
		}
		// checked with its params by props
	case KindAction:
		if !isStr {
			c.errf(ptr, "an action name is a string")
			return
		}
		if nc.ct != nil {
			if _, ok := c.action(nc.ct, str); !ok {
				c.errf(ptr, "%s has no action %q", nc.ct.Name, str)
			}
		}
	case KindEnum:
		if !isStr || !slices.Contains(p.Enum, str) {
			c.errf(ptr, "must be one of %s", strings.Join(p.Enum, ", "))
		}
	case KindInt:
		c.intProp(ptr, v, 0, 100000)
	case KindBool:
		if _, ok := v.(bool); !ok {
			c.errf(ptr, "must be a boolean")
		}
	case KindString, KindSource:
		if !isStr || str == "" {
			c.errf(ptr, "must be a non-empty string")
		}
	case KindChildren:
		list, ok := v.([]any)
		if !ok {
			c.errf(ptr, "children is a list of components")
			return
		}
		for i, ch := range list {
			c.node(ptr+"/"+strconv.Itoa(i), ch, sc)
		}
	case KindNode:
		c.node(ptr, v, sc)
	case KindParams:
		pm, ok := v.(map[string]any)
		if !ok {
			c.errf(ptr, "must be an object of expressions")
			return
		}
		for k, e := range pm {
			c.expr(ptr+"/"+esc(k), e, sc, "")
		}
	case KindSet:
		c.errf(ptr, "set is only valid in update steps")
	case KindSteps:
		c.steps(ptr, v, sc)
	case KindObjects:
		list, ok := v.([]any)
		if !ok {
			c.errf(ptr, "must be a list of objects")
			return
		}
		for i, it := range list {
			ip := ptr + "/" + strconv.Itoa(i)
			im, ok := it.(map[string]any)
			if !ok {
				c.errf(ip, "must be an object")
				continue
			}
			names := make([]string, len(p.Item))
			for j, ip := range p.Item {
				names[j] = ip.Name
			}
			c.known(ip, im, names...)
			c.props(ip, im, p.Item, sc, nc)
		}
	}
}

// special holds the per-component checks the prop kinds cannot express.
func (c *compiler) special(ptr string, m map[string]any, sc *scope, nc *nodeCtx) {
	switch nc.comp.Name {
	case "link":
		_, page := m["page"]
		_, url := m["url"]
		if page == url {
			c.errf(ptr, "link needs exactly one of page and url")
		}
		if _, dl := m["download"]; dl && !url {
			c.errf(ptr+"/download", "download needs url")
		}
	case "heading":
		if l, ok := m["level"]; ok {
			c.intProp(ptr+"/level", l, 1, 4)
		}
	case "dialog":
		if n, ok := m["name"].(string); ok {
			if c.page.dialogs[n] {
				c.errf(ptr+"/name", "dialog %q is defined twice", n)
			}
			c.page.dialogs[n] = true
		}
	case "tabs":
		if s, ok := m["state"].(string); ok {
			if t, found := c.page.state[s]; !found || t.Kind != rt.TInt {
				c.errf(ptr+"/state", "state must name an integer state variable")
			}
		}
	case "table":
		cols, _ := m["columns"].([]any)
		for i, col := range cols {
			cm, _ := col.(map[string]any)
			_, f := cm["field"]
			_, v := cm["value"]
			if cm != nil && f == v {
				c.errf(ptr+"/columns/"+strconv.Itoa(i), "a column has exactly one of field and value")
			}
		}
	case "board":
		if f, ok := m["field"].(string); ok && nc.ct != nil {
			if _, sm := nc.ct.StateMachines[f]; !sm {
				c.errf(ptr+"/field", "field %s of %s is not a state machine (x-stateMachine)", f, nc.ct.Name)
			}
		}
	case "transitions":
		if nc.ct == nil {
			return
		}
		if f, ok := m["field"].(string); ok {
			if _, sm := nc.ct.StateMachines[f]; !sm {
				c.errf(ptr+"/field", "field %s of %s is not a state machine (x-stateMachine)", f, nc.ct.Name)
			}
		} else if len(nc.ct.StateMachines) != 1 {
			c.errf(ptr, "%s has %d state machines; set field", nc.ct.Name, len(nc.ct.StateMachines))
		}
	case "chart":
		_, series := m["series"]
		_, src := m["source"]
		_, group := m["groupBy"]
		switch {
		case series == src:
			c.errf(ptr, "chart needs exactly one of source (with groupBy) and series")
		case src && !group:
			c.errf(ptr, "groupBy is required with source")
		}
		if op, _ := m["op"].(string); (op == "sum" || op == "avg") && m["value"] == nil {
			c.errf(ptr+"/op", "%s needs value", op)
		}
	case "form":
		c.form(ptr, m, sc, nc)
	}
}

func (c *compiler) form(ptr string, m map[string]any, sc *scope, nc *nodeCtx) {
	ct := nc.ct
	if ct == nil {
		return
	}
	mode, _ := m["mode"].(string)
	var check func(p, path string)
	switch mode {
	case "create":
		if _, ok := m["id"]; ok {
			c.errf(ptr+"/id", "create forms take parent, not id")
		}
		check = func(p, path string) { c.writePath(p, path, ct, schemaapi.DirCreate) }
	case "update":
		c.require(ptr, m, "id")
		check = func(p, path string) { c.writePath(p, path, ct, schemaapi.DirUpdate) }
	case "action":
		c.require(ptr, m, "id", "action")
		an, _ := m["action"].(string)
		input, _ := c.action(ct, an)
		props := schemaProps(input)
		check = func(p, path string) {
			if _, ok := props[strings.TrimPrefix(path, "/")]; !ok || !strings.HasPrefix(path, "/") {
				c.errf(p, "action %s has no input field %s", an, path)
			}
		}
	default:
		return
	}
	if mode != "create" {
		if _, ok := m["parent"]; ok {
			c.errf(ptr+"/parent", "only create forms take parent")
		}
	}
	fields, _ := m["fields"].([]any)
	for i, f := range fields {
		fm, _ := f.(map[string]any)
		if p, ok := fm["path"].(string); ok {
			check(ptr+"/fields/"+strconv.Itoa(i)+"/path", p)
		}
	}
}

func schemaProps(schema json.RawMessage) map[string]json.RawMessage {
	var s struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(schema, &s)
	return s.Properties
}

// action reports whether ct has action name: a flow action, a
// state-machine transition, transition, or transfer.
func (c *compiler) action(ct *schemaapi.CompiledType, name string) (json.RawMessage, bool) {
	if c.d.FlowAction != nil {
		if in, ok := c.d.FlowAction(ct, name); ok {
			return in, true
		}
	}
	if name == "transition" && len(ct.StateMachines) > 0 {
		return nil, true
	}
	if name == "transfer" && ct.HasTrait(schemaapi.TraitOwnable) {
		return nil, true
	}
	for _, sm := range ct.StateMachines {
		for _, t := range sm.Transitions {
			if t.Action == name {
				return nil, true
			}
		}
	}
	return nil, false
}

func (c *compiler) steps(ptr string, v any, sc *scope) {
	list, ok := v.([]any)
	if !ok {
		c.errf(ptr, "steps are a list of {\"do\": ...} objects")
		return
	}
	ssc := sc.with(map[string]*rt.Type{"result": rt.Any, "error": rt.OpenMap()})
	for i, it := range list {
		c.step(ptr+"/"+strconv.Itoa(i), it, ssc)
	}
}

func (c *compiler) step(ptr string, v any, sc *scope) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "a step is an object with a \"do\" member")
		return
	}
	name, _ := m["do"].(string)
	st, ok := step(name)
	if !ok {
		c.errf(ptr+"/do", "unknown step %q", name)
		return
	}
	allowed := []string{"do"}
	if st.API {
		allowed = append(allowed, "onError")
	}
	for _, p := range st.Props {
		allowed = append(allowed, p.Name)
	}
	c.known(ptr, m, allowed...)
	nc := &nodeCtx{comp: &Component{Name: "step:" + name, Props: st.Props}}
	if tn, ok := m["type"].(string); ok {
		nc.ct = c.typeRef(ptr+"/type", tn)
	}
	nc.path = func(p, path string) {}
	for _, p := range st.Props {
		val, ok := m[p.Name]
		if !ok {
			if p.Required {
				c.errf(ptr, "%s is required", p.Name)
			}
			continue
		}
		switch {
		case p.Kind == KindSet:
			c.setMap(ptr+"/set", val, sc, nc.ct)
		case p.Kind == KindParams && name == "create":
			c.dataMap(ptr+"/data", val, sc, nc.ct)
		case p.Kind == KindParams && name == "action":
			c.inputMap(ptr+"/input", val, sc, nc.ct, m["action"])
		case p.Kind == KindParams && name == "navigate":
			// checked with the page below
		default:
			c.prop(ptr+"/"+esc(p.Name), p, val, sc, nc)
		}
	}
	switch name {
	case "navigate":
		if pg, ok := m["page"].(string); ok {
			c.navigate(ptr, "page", pg, "params", m["params"], sc)
		}
	case "set":
		if s, ok := m["state"].(string); ok {
			if _, found := c.page.state[s]; !found {
				c.errf(ptr+"/state", "no state variable %q on this page", s)
			}
		}
	case "open":
		if d, ok := m["dialog"].(string); ok {
			c.page.dialogRefs[ptr+"/dialog"] = d
		}
	}
	if oe, ok := m["onError"]; ok {
		c.steps(ptr+"/onError", oe, sc)
	}
}

func (c *compiler) setMap(ptr string, v any, sc *scope, ct *schemaapi.CompiledType) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "set maps field paths to expressions")
		return
	}
	for k, e := range m {
		if ct != nil {
			c.writePath(ptr+"/"+esc(k), k, ct, schemaapi.DirUpdate)
		}
		c.expr(ptr+"/"+esc(k), e, sc, "")
	}
}

func (c *compiler) dataMap(ptr string, v any, sc *scope, ct *schemaapi.CompiledType) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "data maps field names to expressions")
		return
	}
	for k, e := range m {
		if ct != nil {
			c.writePath(ptr+"/"+esc(k), "/"+k, ct, schemaapi.DirCreate)
		}
		c.expr(ptr+"/"+esc(k), e, sc, "")
	}
}

func (c *compiler) inputMap(ptr string, v any, sc *scope, ct *schemaapi.CompiledType, action any) {
	m, ok := v.(map[string]any)
	if !ok {
		c.errf(ptr, "input maps input fields to expressions")
		return
	}
	var props map[string]json.RawMessage
	an, _ := action.(string)
	if ct != nil {
		if in, found := c.action(ct, an); found && in != nil {
			props = schemaProps(in)
		}
	}
	for k, e := range m {
		if ct != nil && an == "transition" {
			if k != "action" && k != "to" {
				c.errf(ptr+"/"+esc(k), "transition takes action or to")
			}
		} else if ct != nil && props == nil && an != "transfer" {
			c.errf(ptr+"/"+esc(k), "action %s takes no input", an)
		} else if props != nil {
			if _, ok := props[k]; !ok {
				c.errf(ptr+"/"+esc(k), "action %s has no input field %q", an, k)
			}
		}
		c.expr(ptr+"/"+esc(k), e, sc, "")
	}
}

// esc escapes a JSON Pointer reference token (RFC 6901).
func esc(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
