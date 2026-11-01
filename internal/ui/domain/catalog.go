// Package domain is the UI context's domain: the page and app-shell
// documents of ui/ (ADR 0016), the component catalog, and their
// validation against the app's types and actions. Rendering happens in
// the browser (@organon/ui); the server only validates and serves.
package domain

// PropKind says how a prop value is written and checked.
type PropKind string

// Prop kinds. Text props are literal text with {{ expr }} interpolations;
// expr props are expressions, as in flows.
const (
	KindText     PropKind = "text"     // "Tasks of {{ data.project.title }}"
	KindExpr     PropKind = "expr"     // "item.status == 'done'"
	KindPath     PropKind = "path"     // JSON Pointer into the component's type
	KindType     PropKind = "type"     // feature type name
	KindPage     PropKind = "page"     // page name
	KindAction   PropKind = "action"   // action name on the component's type
	KindEnum     PropKind = "enum"     // one of Enum
	KindInt      PropKind = "int"      // JSON integer
	KindBool     PropKind = "bool"     // JSON boolean
	KindString   PropKind = "string"   // plain string (class names, dialog names)
	KindSource   PropKind = "source"   // name of a page data source
	KindChildren PropKind = "children" // list of nodes
	KindNode     PropKind = "node"     // one node
	KindParams   PropKind = "params"   // map of name -> expr
	KindSet      PropKind = "set"      // map of JSON Pointer -> expr
	KindObjects  PropKind = "objects"  // list of objects described by Item
	KindPaths    PropKind = "paths"    // list of JSON Pointers
	KindSteps    PropKind = "steps"    // list of steps
)

// Prop describes one prop of a component or step.
type Prop struct {
	Name     string   `json:"name"`
	Kind     PropKind `json:"kind"`
	Required bool     `json:"required,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	// Want is the result type of an expr prop: bool, string, number, or
	// empty for any.
	Want string `json:"want,omitempty"`
	Doc  string `json:"doc,omitempty"`
	// Item describes the members of an objects prop.
	Item []Prop `json:"item,omitempty"`
	// Row means the prop is evaluated once per row of the component's
	// source, with item and index in scope.
	Row bool `json:"row,omitempty"`
}

// Component is one entry of the component catalog.
type Component struct {
	Name   string   `json:"name"`
	Family string   `json:"family"`
	Doc    string   `json:"doc"`
	Props  []Prop   `json:"props"`
	Events []string `json:"events,omitempty"`
	// Source is "list" or "feature" when the component binds a data
	// source of that shape through its source prop.
	Source string `json:"source,omitempty"`
}

// Step is one entry of the step catalog (the "do" of an event handler).
type Step struct {
	Name  string `json:"name"`
	Doc   string `json:"doc"`
	Props []Prop `json:"props"`
	// API steps call the server and may have onError steps.
	API bool `json:"api,omitempty"`
}

// Families of the component catalog, in palette order.
var Families = []string{"layout", "text", "data", "forms", "charts"}

var (
	gapEnum   = []string{"none", "sm", "md", "lg"}
	toneEnum  = []string{"default", "muted", "accent", "success", "warning", "danger"}
	dispEnum  = []string{"auto", "text", "date", "datetime", "number", "badge", "ref", "image"}
	widgEnum  = []string{"auto", "text", "textarea", "number", "select", "checkbox", "datetime", "ref", "file", "decimal"}
	pSource   = Prop{Name: "source", Kind: KindSource, Doc: "Data source of the page"}
	pChildren = Prop{Name: "children", Kind: KindChildren}
)

var components = []Component{
	// Layout.
	{Name: "stack", Family: "layout", Doc: "Children stacked vertically.", Props: []Prop{
		{Name: "gap", Kind: KindEnum, Enum: gapEnum}, {Name: "align", Kind: KindEnum, Enum: []string{"start", "center", "end", "stretch"}}, pChildren}},
	{Name: "row", Family: "layout", Doc: "Children side by side; wraps on narrow screens.", Props: []Prop{
		{Name: "gap", Kind: KindEnum, Enum: gapEnum}, {Name: "align", Kind: KindEnum, Enum: []string{"start", "center", "end", "stretch"}},
		{Name: "justify", Kind: KindEnum, Enum: []string{"start", "center", "end", "between"}}, pChildren}},
	{Name: "columns", Family: "layout", Doc: "Equal columns, one per child; collapses below min width.", Props: []Prop{
		{Name: "min", Kind: KindInt, Doc: "Minimum column width in pixels (default 240)"}, pChildren}},
	{Name: "card", Family: "layout", Doc: "A framed panel.", Props: []Prop{{Name: "title", Kind: KindText}, pChildren}},
	{Name: "tabs", Family: "layout", Doc: "Tabbed panels.", Props: []Prop{
		{Name: "tabs", Kind: KindObjects, Required: true, Item: []Prop{{Name: "label", Kind: KindText, Required: true}, pChildren}},
		{Name: "state", Kind: KindString, Doc: "Page state variable holding the selected tab index"}}},
	{Name: "dialog", Family: "layout", Doc: "A modal dialog, opened by an open step.", Props: []Prop{
		{Name: "name", Kind: KindString, Required: true}, {Name: "title", Kind: KindText}, pChildren}},
	{Name: "divider", Family: "layout", Doc: "A horizontal rule."},

	// Text.
	{Name: "heading", Family: "text", Doc: "A heading.", Props: []Prop{
		{Name: "text", Kind: KindText, Required: true}, {Name: "level", Kind: KindInt, Doc: "1 to 4 (default 2)"}}},
	{Name: "text", Family: "text", Doc: "A paragraph.", Props: []Prop{
		{Name: "text", Kind: KindText, Required: true}, {Name: "tone", Kind: KindEnum, Enum: toneEnum}}},
	{Name: "markdown", Family: "text", Doc: "Markdown text; raw HTML is not rendered.", Props: []Prop{
		{Name: "text", Kind: KindText, Required: true}}},
	{Name: "badge", Family: "text", Doc: "A small label.", Props: []Prop{
		{Name: "text", Kind: KindText, Required: true}, {Name: "tone", Kind: KindEnum, Enum: toneEnum}}},
	{Name: "image", Family: "text", Doc: "An image from ui/assets (assets/<path>) or a URL expression.", Props: []Prop{
		{Name: "src", Kind: KindText, Required: true}, {Name: "alt", Kind: KindText}, {Name: "width", Kind: KindInt}}},
	{Name: "link", Family: "text", Doc: "A link to a page or an http(s)/mailto URL.", Props: []Prop{
		{Name: "text", Kind: KindText, Required: true}, {Name: "page", Kind: KindPage}, {Name: "params", Kind: KindParams},
		{Name: "url", Kind: KindText},
		{Name: "download", Kind: KindText, Doc: "With url: save the file under this name instead of opening it (empty: the browser picks)"}}},

	// Data display.
	{Name: "table", Family: "data", Source: "list", Doc: "Rows of a list source.", Props: []Prop{
		{Name: "source", Kind: KindSource, Required: true},
		{Name: "columns", Kind: KindObjects, Required: true, Row: true, Item: []Prop{
			{Name: "field", Kind: KindPath}, {Name: "value", Kind: KindExpr}, {Name: "label", Kind: KindText},
			{Name: "display", Kind: KindEnum, Enum: dispEnum}, {Name: "page", Kind: KindPage}, {Name: "params", Kind: KindParams}}},
		{Name: "rowPage", Kind: KindPage, Doc: "Page opened when a row is clicked"}, {Name: "rowParams", Kind: KindParams, Row: true},
		{Name: "empty", Kind: KindText}, {Name: "pageSize", Kind: KindInt}}},
	{Name: "list", Family: "data", Source: "list", Doc: "One item template per row of a list source.", Props: []Prop{
		{Name: "source", Kind: KindSource, Required: true}, {Name: "item", Kind: KindNode, Required: true, Row: true},
		{Name: "empty", Kind: KindText}}},
	{Name: "field", Family: "data", Source: "feature", Doc: "One field, rendered by its kind. Inside rows the source defaults to item.", Props: []Prop{
		pSource, {Name: "path", Kind: KindPath, Required: true}, {Name: "label", Kind: KindText},
		{Name: "display", Kind: KindEnum, Enum: dispEnum}}},
	{Name: "detail", Family: "data", Source: "feature", Doc: "Label/value pairs of a feature.", Props: []Prop{
		pSource, {Name: "fields", Kind: KindPaths, Doc: "Default: every readable field"}}},
	{Name: "stat", Family: "data", Doc: "A big number with a label.", Props: []Prop{
		{Name: "label", Kind: KindText, Required: true}, {Name: "value", Kind: KindExpr, Required: true},
		{Name: "format", Kind: KindEnum, Enum: []string{"number", "percent", "date"}}}},
	{Name: "empty", Family: "data", Doc: "An empty-state message.", Props: []Prop{{Name: "text", Kind: KindText, Required: true}}},
	{Name: "transitions", Family: "data", Source: "feature", Doc: "Buttons for the state-machine actions the caller may run now.", Props: []Prop{
		pSource, {Name: "field", Kind: KindPath, Doc: "State-machine field (default: the only one)"}}},

	// Forms and actions.
	{Name: "form", Family: "forms", Doc: "A form generated from the caller's create or update view, or an action's input schema.",
		Events: []string{"submit"}, Props: []Prop{
			{Name: "mode", Kind: KindEnum, Required: true, Enum: []string{"create", "update", "action"}},
			{Name: "type", Kind: KindType, Required: true},
			{Name: "parent", Kind: KindExpr, Want: "string", Doc: "create: parent feature ID"},
			{Name: "id", Kind: KindExpr, Want: "string", Doc: "update and action: feature ID"},
			{Name: "action", Kind: KindAction, Doc: "action mode: action name"},
			{Name: "fields", Kind: KindObjects, Item: []Prop{
				{Name: "path", Kind: KindPath, Required: true}, {Name: "label", Kind: KindText},
				{Name: "widget", Kind: KindEnum, Enum: widgEnum}, {Name: "help", Kind: KindText},
				{Name: "labelField", Kind: KindString, Doc: "ref widget: field of the target shown in the picker"}}},
			{Name: "submit", Kind: KindText, Doc: "Submit button label"}}},
	{Name: "button", Family: "forms", Doc: "Runs its click steps.", Events: []string{"click"}, Props: []Prop{
		{Name: "label", Kind: KindText, Required: true},
		{Name: "variant", Kind: KindEnum, Enum: []string{"primary", "secondary", "danger", "link"}},
		{Name: "disabled", Kind: KindExpr, Want: "bool"}}},
	{Name: "menu", Family: "forms", Doc: "A drop-down of commands.", Props: []Prop{
		{Name: "label", Kind: KindText, Required: true},
		{Name: "items", Kind: KindObjects, Required: true, Item: []Prop{
			{Name: "label", Kind: KindText, Required: true}, {Name: "visible", Kind: KindExpr, Want: "bool"},
			{Name: "steps", Kind: KindSteps, Required: true}}}}},

	// Board and charts.
	{Name: "board", Family: "charts", Source: "list", Doc: "Kanban columns by a state-machine field; dropping a card runs the transition.", Props: []Prop{
		{Name: "source", Kind: KindSource, Required: true}, {Name: "field", Kind: KindPath, Required: true},
		{Name: "card", Kind: KindNode, Required: true, Row: true}}},
	{Name: "chart", Family: "charts", Source: "list", Doc: "Bar, line, or pie chart over the loaded rows of a source, or explicit series.", Props: []Prop{
		{Name: "kind", Kind: KindEnum, Required: true, Enum: []string{"bar", "line", "pie"}},
		{Name: "title", Kind: KindText}, pSource,
		{Name: "groupBy", Kind: KindPath}, {Name: "value", Kind: KindPath, Doc: "Field summed or averaged (count when absent)"},
		{Name: "op", Kind: KindEnum, Enum: []string{"count", "sum", "avg"}},
		{Name: "series", Kind: KindObjects, Item: []Prop{
			{Name: "label", Kind: KindText, Required: true}, {Name: "value", Kind: KindExpr, Required: true, Want: "number"}}},
		{Name: "height", Kind: KindInt}}},
}

var pConfirm = Prop{Name: "confirm", Kind: KindText, Doc: "Ask before running"}

var steps = []Step{
	{Name: "action", API: true, Doc: "Runs an action; result is {output, feature}.", Props: []Prop{
		{Name: "type", Kind: KindType, Required: true}, {Name: "id", Kind: KindExpr, Required: true, Want: "string"},
		{Name: "action", Kind: KindAction, Required: true}, {Name: "input", Kind: KindParams}, pConfirm}},
	{Name: "create", API: true, Doc: "Creates a feature; result is the feature.", Props: []Prop{
		{Name: "type", Kind: KindType, Required: true}, {Name: "parent", Kind: KindExpr, Want: "string"},
		{Name: "data", Kind: KindParams, Required: true}, pConfirm}},
	{Name: "update", API: true, Doc: "Updates fields; result is the feature.", Props: []Prop{
		{Name: "type", Kind: KindType, Required: true}, {Name: "id", Kind: KindExpr, Required: true, Want: "string"},
		{Name: "set", Kind: KindSet, Required: true}, pConfirm}},
	{Name: "delete", API: true, Doc: "Deletes a feature.", Props: []Prop{
		{Name: "type", Kind: KindType, Required: true}, {Name: "id", Kind: KindExpr, Required: true, Want: "string"}, pConfirm}},
	{Name: "navigate", Doc: "Opens a page.", Props: []Prop{{Name: "page", Kind: KindPage, Required: true}, {Name: "params", Kind: KindParams}}},
	{Name: "back", Doc: "Goes back in history."},
	{Name: "set", Doc: "Sets a page state variable.", Props: []Prop{
		{Name: "state", Kind: KindString, Required: true}, {Name: "value", Kind: KindExpr, Required: true}}},
	{Name: "open", Doc: "Opens a dialog of the page.", Props: []Prop{{Name: "dialog", Kind: KindString, Required: true}}},
	{Name: "close", Doc: "Closes the open dialog."},
	{Name: "toast", Doc: "Shows a short message.", Props: []Prop{
		{Name: "text", Kind: KindText, Required: true}, {Name: "tone", Kind: KindEnum, Enum: toneEnum}}},
	{Name: "logout", Doc: "Signs out."},
}

// Catalog is the published component and step catalog.
type Catalog struct {
	Families   []string    `json:"families"`
	Components []Component `json:"components"`
	Steps      []Step      `json:"steps"`
	// Common lists the props every component accepts.
	Common []Prop `json:"common"`
}

var common = []Prop{
	{Name: "visible", Kind: KindExpr, Want: "bool", Doc: "Render only when true"},
	{Name: "class", Kind: KindString, Doc: "CSS classes from ui/styles.css"},
}

// GetCatalog returns the catalog. Callers must not modify it.
func GetCatalog() Catalog {
	return Catalog{Families: Families, Components: components, Steps: steps, Common: common}
}

func component(name string) (*Component, bool) {
	for i := range components {
		if components[i].Name == name {
			return &components[i], true
		}
	}
	return nil, false
}

func step(name string) (*Step, bool) {
	for i := range steps {
		if steps[i].Name == name {
			return &steps[i], true
		}
	}
	return nil, false
}
