// Package application serves the compiled end-user UI (ADR 0016): the
// bundle a browser needs to render the pages its principal may see.
package application

import (
	"encoding/json"
	"path"
	"sort"
	"strings"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
	"organon/internal/ui/domain"
)

// Deps are the service's dependencies.
type Deps struct {
	UI    *domain.Compiled
	Types schemaapi.TypeRegistry
	Clock kernel.Clock
	// Version identifies the manifest (cache busting for styles and assets).
	Version string
	// FlowActions returns the flow actions of a type with their input
	// schemas (nil when a flow takes no declared input).
	FlowActions func(ct *schemaapi.CompiledType) map[string]json.RawMessage
}

// Service serves the UI bundle.
type Service struct{ d Deps }

// New returns a Service.
func New(d Deps) *Service { return &Service{d: d} }

// Enabled reports whether the app has an end-user UI.
func (s *Service) Enabled() bool { return s.d.UI != nil && s.d.UI.App != nil }

// Styles returns ui/styles.css, or nil.
func (s *Service) Styles() []byte {
	if s.d.UI == nil {
		return nil
	}
	return s.d.UI.Styles
}

// Asset returns a file of ui/assets by path and its content type.
func (s *Service) Asset(p string) (data []byte, contentType string, ok bool) {
	if s.d.UI == nil {
		return nil, "", false
	}
	contentType = domain.AssetTypes[strings.ToLower(path.Ext(p))]
	data, ok = s.d.UI.Assets[p]
	return data, contentType, ok && contentType != ""
}

// Catalog returns the component and step catalog.
func (s *Service) Catalog() domain.Catalog { return domain.GetCatalog() }

// Version identifies the manifest.
func (s *Service) Version() string { return s.d.Version }

// Bundle is what the renderer needs: the shell, the pages the principal
// may see, and metadata of the fields it may read.
type Bundle struct {
	Version string `json:"version"`
	App     any    `json:"app"`
	// LoginRequired is true when an anonymous caller must sign in first.
	LoginRequired bool                `json:"loginRequired"`
	Pages         []PageDTO           `json:"pages"`
	Types         map[string]TypeMeta `json:"types"`
	Styles        string              `json:"styles,omitempty"`
	// I18n is set when the app declares languages (ADR 0018).
	I18n *I18n `json:"i18n,omitempty"`
}

// I18n is the bundle's language and the translations it needs.
type I18n struct {
	Language  string   `json:"language"`
	Languages []string `json:"languages"`
	// Messages translate the texts of the shell, of the pages in the
	// bundle, and of the renderer; Pages override them per page.
	Messages map[string]string            `json:"messages"`
	Pages    map[string]map[string]string `json:"pages,omitempty"`
}

// PageDTO is one page of the bundle.
type PageDTO struct {
	Name   string                  `json:"name"`
	Route  string                  `json:"route"`
	Params map[string]domain.Param `json:"params"`
	Doc    json.RawMessage         `json:"doc"`
}

// TypeMeta describes a type for rendering: field kinds that published
// views do not carry (x-kind, x-ref), enums, and state machines.
type TypeMeta struct {
	Name          string               `json:"name"`
	Title         string               `json:"title,omitempty"`
	Fields        map[string]FieldMeta `json:"fields"`
	StateMachines map[string]StateMeta `json:"stateMachines,omitempty"`
	Children      []string             `json:"children,omitempty"`
	Ownable       bool                 `json:"ownable,omitempty"`
	// ActionLabels are translated transition buttons.
	ActionLabels map[string]string `json:"actionLabels,omitempty"`
	// Actions maps every action name to its input schema (null for
	// state-machine actions, transition, and transfer).
	Actions map[string]json.RawMessage `json:"actions"`
}

// FieldMeta describes one readable top-level field.
type FieldMeta struct {
	Kind     string   `json:"kind"`
	Title    string   `json:"title,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Ref      []string `json:"ref,omitempty"` // names of the types a ref may point to
	Nullable bool     `json:"nullable,omitempty"`
	Required bool     `json:"required,omitempty"`
	ReadOnly bool     `json:"readOnly,omitempty"`
	Format   string   `json:"format,omitempty"`
	Thumbs   bool     `json:"thumbnails,omitempty"`
	// Labels are translated enum values.
	Labels map[string]string `json:"labels,omitempty"`
}

// StateMeta is a state machine without guards.
type StateMeta struct {
	Initial     string      `json:"initial"`
	Transitions []TransMeta `json:"transitions"`
}

// TransMeta is one transition.
type TransMeta struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
}

// audience mirrors the schema API: admins see admin views, signed-in
// users owner views (the projector still trims per feature), anonymous
// callers public views.
func audience(p accessapi.Principal) schemaapi.Audience {
	switch {
	case p.IsSystem():
		return schemaapi.AudSystem
	case p.HasRole("admin"):
		return schemaapi.AudAdmin
	case !p.IsAnonymous() && !p.IsLink():
		return schemaapi.AudOwner
	}
	return schemaapi.AudPublic
}

// Language picks the UI language for p: the user's saved choice, then
// prefs (the requested language, then the browser's) in order, then the
// first language. Tags match exactly, else by base language ("el-GR"
// matches "el").
func (s *Service) Language(p accessapi.Principal, prefs []string) string {
	langs := s.d.UI.Languages
	if len(langs) == 0 {
		return ""
	}
	base := func(t string) string { return strings.ToLower(strings.SplitN(t, "-", 2)[0]) }
	for _, want := range append([]string{p.Locale}, prefs...) {
		if want == "" {
			continue
		}
		for _, l := range langs {
			if strings.EqualFold(l, want) {
				return l
			}
		}
		for _, l := range langs {
			if base(l) == base(want) {
				return l
			}
		}
	}
	return langs[0]
}

// Bundle returns the UI for p in the language Language picks from prefs.
func (s *Service) Bundle(p accessapi.Principal, prefs []string) Bundle {
	b := Bundle{Version: s.d.Version, Pages: []PageDTO{}, Types: map[string]TypeMeta{}}
	if !s.Enabled() {
		return b
	}
	ui := s.d.UI
	now := s.d.Clock.Now()
	var tr *domain.Translations
	if len(ui.Languages) > 0 {
		lang := s.Language(p, prefs)
		tr = ui.Merged(lang)
		b.I18n = &I18n{Language: lang, Languages: ui.Languages, Messages: map[string]string{}}
		// Once the pages are chosen; I18n is shared with the returned copy.
		defer func() { b.I18n.Messages, b.I18n.Pages = s.messages(tr, b.Pages) }()
	}
	var app map[string]any
	_ = json.Unmarshal(ui.App.Doc, &app)
	if _, ok := app["nav"]; ok {
		nav := []any{}
		raw, _ := app["nav"].([]any)
		for i, n := range ui.App.Nav {
			if domain.VisibleTo(n.Visible, p, now) && i < len(raw) {
				nav = append(nav, raw[i])
			}
		}
		app["nav"] = nav
	}
	app["requireLogin"] = ui.App.RequireLogin
	b.App = app
	if ui.Styles != nil {
		b.Styles = "/assets/styles.css?v=" + s.d.Version
	}
	if p.IsAnonymous() && ui.App.RequireLogin {
		b.LoginRequired = true
		delete(app, "nav")
		return b
	}
	used := map[string]bool{}
	for _, pg := range ui.Pages {
		if !domain.VisibleTo(pg.Visible, p, now) {
			continue
		}
		b.Pages = append(b.Pages, PageDTO{Name: pg.Name, Route: pg.Route, Params: pg.Params, Doc: pg.Doc})
		for _, t := range pg.Types {
			used[t] = true
		}
	}
	aud := audience(p)
	for name := range used {
		ct, ok := s.d.Types.ByName(name)
		if !ok {
			continue
		}
		b.Types[name] = s.meta(ct, aud, labelsOf(tr, name))
		// Ref targets are rendered by label, so describe them too.
		for _, f := range ct.TopFields() {
			if f.Kind != kernel.KindRef {
				continue
			}
			for _, t := range s.d.Types.Resolve(f.RefTarget) {
				if _, done := b.Types[t.Name]; !done {
					b.Types[t.Name] = s.meta(t, aud, labelsOf(tr, t.Name))
				}
			}
		}
	}
	return b
}

func labelsOf(tr *domain.Translations, typ string) domain.TypeLabels {
	if tr == nil {
		return domain.TypeLabels{}
	}
	return tr.Types[typ]
}

// messages returns the translations of the shell, the renderer, and the
// given pages only, so a bundle reveals no text of pages it leaves out.
func (s *Service) messages(tr *domain.Translations, pages []PageDTO) (map[string]string, map[string]map[string]string) {
	msgs := map[string]string{}
	add := func(texts []string) {
		for _, t := range texts {
			if v, ok := tr.Messages[t]; ok {
				msgs[t] = v
			}
		}
	}
	add(s.d.UI.AppTexts)
	add(domain.BuiltinTexts)
	var byPage map[string]map[string]string
	for _, dto := range pages {
		for _, pg := range s.d.UI.Pages {
			if pg.Name != dto.Name {
				continue
			}
			add(pg.Texts)
			if m := tr.Pages[pg.Name]; len(m) > 0 {
				if byPage == nil {
					byPage = map[string]map[string]string{}
				}
				byPage[pg.Name] = m
			}
		}
	}
	return msgs, byPage
}

func (s *Service) meta(ct *schemaapi.CompiledType, aud schemaapi.Audience, labels domain.TypeLabels) TypeMeta {
	m := TypeMeta{Name: ct.Name, Title: ct.Title, Fields: map[string]FieldMeta{},
		Ownable: ct.HasTrait(schemaapi.TraitOwnable), Actions: map[string]json.RawMessage{}}
	if m.Ownable {
		m.Actions["transfer"] = nil
	}
	if ct.Shareable != nil {
		m.Actions["share"] = domain.ShareInput
	}
	if s.d.FlowActions != nil {
		for name, in := range s.d.FlowActions(ct) {
			m.Actions[name] = in
		}
	}
	read := ct.View(aud, schemaapi.DirRead)
	for _, f := range ct.TopFields() {
		if read == nil || !read.Readable[f.Path] {
			continue
		}
		fm := FieldMeta{Kind: f.Kind.String(), Title: f.Title, Nullable: f.Nullable, Required: f.Required,
			ReadOnly: f.ReadOnly || f.Computed != ""}
		if f.Type != nil {
			fm.Enum, fm.Format = f.Type.Enum, f.Type.Format
		}
		if f.Kind == kernel.KindRef {
			for _, t := range s.d.Types.Resolve(f.RefTarget) {
				fm.Ref = append(fm.Ref, t.Name)
			}
			sort.Strings(fm.Ref)
		}
		if f.Blob != nil && len(f.Blob.Thumbnails) > 0 {
			fm.Thumbs = true
		}
		if l := labels.Fields[f.Path]; l != "" {
			fm.Title = l
		}
		fm.Labels = labels.Values[f.Path]
		m.Fields[f.Path] = fm
	}
	if len(ct.StateMachines) > 0 {
		m.StateMachines = map[string]StateMeta{}
		for path, sm := range ct.StateMachines {
			sm2 := StateMeta{Initial: sm.Initial, Transitions: []TransMeta{}}
			m.Actions["transition"] = nil
			for _, t := range sm.Transitions {
				sm2.Transitions = append(sm2.Transitions, TransMeta{From: t.From, To: t.To, Action: t.Action})
				if _, ok := m.Actions[t.Action]; !ok {
					m.Actions[t.Action] = nil
				}
			}
			m.StateMachines[path] = sm2
		}
		m.ActionLabels = labels.Actions
	}
	for _, c := range ct.Children {
		for _, t := range s.d.Types.Resolve(c) {
			m.Children = append(m.Children, t.Name)
		}
	}
	sort.Strings(m.Children)
	return m
}

// AllTypes describes every type for p's audience; the builder offers them
// to new pages, which no bundle mentions yet.
func (s *Service) AllTypes(p accessapi.Principal) map[string]TypeMeta {
	out := map[string]TypeMeta{}
	aud := audience(p)
	for _, ct := range s.d.Types.All() {
		out[ct.Name] = s.meta(ct, aud, domain.TypeLabels{})
	}
	return out
}
