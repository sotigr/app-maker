package app

import (
	"encoding/json"

	flowdomain "organon/internal/flow/domain"
	"organon/internal/runtime/domain/expression"
	schemaapi "organon/internal/schema/api"
	uidomain "organon/internal/ui/domain"
)

// uiDeps are the UI compiler's view of the app: its types and the actions
// its flows implement. flows is nil when the flows failed to compile.
func uiDeps(types schemaapi.TypeRegistry, flows *flowdomain.FlowSet) uidomain.Deps {
	return uidomain.Deps{Types: types, Exprs: expression.NewCompiler(),
		FlowAction: func(ct *schemaapi.CompiledType, name string) (json.RawMessage, bool) {
			if flows == nil {
				return nil, true // flow errors are reported already
			}
			f, ok := flows.Action(ct, name)
			if !ok {
				return nil, false
			}
			return f.Def.Input, true
		}}
}

// UICatalog returns the UI component and step catalog (ADR 0016).
func UICatalog() uidomain.Catalog { return uidomain.GetCatalog() }

// UISchemas returns the JSON Schemas of ui/ files by file name.
func UISchemas() map[string]map[string]any {
	return map[string]map[string]any{"ui-app.json": uidomain.AppSchema(), "ui-page.json": uidomain.PageSchema(),
		"ui-i18n.json": uidomain.I18nSchema()}
}

// UITranslations is one ui/i18n/<lang>.json (ADR 0018).
type UITranslations = uidomain.Translations

// UIProp is one prop of a UI component or step.
type UIProp = uidomain.Prop
