package domain

// JSON Schemas of ui/app.json and ui/pages/*.json, generated from the
// catalog so editors and agents get completion through "$schema"
// (organon ui schema). They describe the shape only; organon validate does
// the type checking.

type obj = map[string]any

const draft = "https://json-schema.org/draft/2020-12/schema"

var exprSchema = obj{"type": []string{"string", "number", "boolean", "null"}}

func describe(p Prop) string {
	d := string(p.Kind)
	switch p.Kind {
	case KindText:
		d = "text with {{ expr }} interpolations"
	case KindExpr:
		d = "expression (string literals need inner quotes: \"'done'\")"
	case KindPath:
		d = "field path (JSON Pointer)"
	}
	if p.Doc != "" {
		d = p.Doc + "; " + d
	}
	return d
}

func propSchema(p Prop) obj {
	var s obj
	switch p.Kind {
	case KindExpr:
		s = obj{"type": exprSchema["type"]}
	case KindPath:
		s = obj{"type": "string", "pattern": "^/"}
	case KindEnum:
		s = obj{"enum": p.Enum}
	case KindInt:
		s = obj{"type": "integer"}
	case KindBool:
		s = obj{"type": "boolean"}
	case KindChildren:
		s = obj{"type": "array", "items": obj{"$ref": "#/$defs/node"}}
	case KindNode:
		s = obj{"$ref": "#/$defs/node"}
	case KindParams, KindSet:
		s = obj{"type": "object", "additionalProperties": exprSchema}
	case KindObjects:
		s = obj{"type": "array", "items": objectSchema(p.Item, nil)}
	case KindPaths:
		s = obj{"type": "array", "items": obj{"type": "string", "pattern": "^/"}}
	case KindSteps:
		s = obj{"type": "array", "items": obj{"$ref": "#/$defs/step"}}
	default:
		s = obj{"type": "string"}
	}
	if s["$ref"] == nil {
		s["description"] = describe(p)
	}
	return s
}

func objectSchema(props []Prop, extra obj) obj {
	ps := obj{}
	var req []string
	for k, v := range extra {
		ps[k] = v
	}
	for _, p := range props {
		ps[p.Name] = propSchema(p)
		if p.Required {
			req = append(req, p.Name)
		}
	}
	s := obj{"type": "object", "properties": ps, "additionalProperties": false}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

func uiDefs() obj {
	var nodes, stepDefs []any
	for _, c := range components {
		extra := obj{"component": obj{"const": c.Name, "description": c.Doc}}
		for _, p := range common {
			extra[p.Name] = propSchema(p)
		}
		if len(c.Events) > 0 {
			ev := obj{}
			for _, e := range c.Events {
				ev[e] = obj{"type": "array", "items": obj{"$ref": "#/$defs/step"}}
			}
			extra["on"] = obj{"type": "object", "properties": ev, "additionalProperties": false}
		}
		s := objectSchema(c.Props, extra)
		s["required"] = append([]string{"component"}, asStrings(s["required"])...)
		nodes = append(nodes, s)
	}
	for _, st := range steps {
		extra := obj{"do": obj{"const": st.Name, "description": st.Doc}}
		if st.API {
			extra["onError"] = obj{"type": "array", "items": obj{"$ref": "#/$defs/step"}}
		}
		s := objectSchema(st.Props, extra)
		s["required"] = append([]string{"do"}, asStrings(s["required"])...)
		stepDefs = append(stepDefs, s)
	}
	pred := obj{"type": "object", "properties": obj{
		"field": obj{"type": "string", "pattern": "^/"},
		"op":    obj{"enum": queryOps},
		"value": exprSchema,
		"and":   obj{"type": "array", "items": obj{"$ref": "#/$defs/predicate"}},
		"or":    obj{"type": "array", "items": obj{"$ref": "#/$defs/predicate"}},
		"not":   obj{"$ref": "#/$defs/predicate"},
	}, "additionalProperties": false}
	source := obj{"type": "object", "additionalProperties": false, "properties": obj{
		"feature":  obj{"type": "string", "description": "expression: feature ID"},
		"children": obj{"type": "string", "description": "expression: parent ID"},
		"search":   obj{"type": "string", "description": "expression: search words"},
		"type":     obj{"type": "string"},
		"limit":    obj{"type": "integer", "minimum": 1, "maximum": 500},
		"query": obj{"type": "object", "required": []string{"type"}, "additionalProperties": false, "properties": obj{
			"type":  obj{"type": "string"},
			"where": obj{"$ref": "#/$defs/predicate"},
			"sort": obj{"type": "array", "items": obj{"type": "object", "additionalProperties": false, "properties": obj{
				"field": obj{"type": "string", "pattern": "^/"}, "dir": obj{"enum": []string{"asc", "desc"}}}}},
			"scope": obj{"type": "object", "additionalProperties": false, "properties": obj{
				"childrenOf": obj{"type": "string"}, "descendantsOf": obj{"type": "string"}}},
			"limit": obj{"type": "integer", "minimum": 1, "maximum": 500},
		}},
	}}
	return obj{"node": obj{"oneOf": nodes}, "step": obj{"oneOf": stepDefs}, "predicate": pred, "source": source}
}

func asStrings(v any) []string {
	s, _ := v.([]string)
	return s
}

// PageSchema returns the JSON Schema of ui/pages/*.json.
func PageSchema() map[string]any {
	scalar := obj{"enum": []string{"string", "integer", "number", "boolean"}}
	return obj{
		"$schema": draft, "title": "Organon UI page", "type": "object",
		"required": []string{"page", "route", "body"}, "additionalProperties": false,
		"properties": obj{
			"$schema": obj{"type": "string"},
			"page":    obj{"type": "string", "pattern": nameRe.String()},
			"title":   obj{"type": "string", "description": "text with {{ expr }} interpolations"},
			"route":   obj{"type": "string", "description": "path such as /projects/:id"},
			"params": obj{"type": "object", "additionalProperties": obj{"type": "object", "required": []string{"type"},
				"additionalProperties": false, "properties": obj{"type": scalar, "format": obj{"type": "string"}}}},
			"visible": obj{"type": "string", "description": "expression over principal; hides the page"},
			"state": obj{"type": "object", "additionalProperties": obj{"type": "object", "required": []string{"type"},
				"additionalProperties": false, "properties": obj{
					"type":    obj{"enum": []string{"string", "integer", "number", "boolean", "array", "object"}},
					"default": obj{}}}},
			"data":     obj{"type": "object", "additionalProperties": obj{"$ref": "#/$defs/source"}},
			"body":     obj{"$ref": "#/$defs/node"},
			"x-layout": obj{},
		},
		"$defs": uiDefs(),
	}
}

// AppSchema returns the JSON Schema of ui/app.json.
func AppSchema() map[string]any {
	return obj{
		"$schema": draft, "title": "Organon UI app shell", "type": "object",
		"required": []string{"home"}, "additionalProperties": false,
		"properties": obj{
			"$schema":      obj{"type": "string"},
			"title":        obj{"type": "string"},
			"logo":         obj{"type": "string", "pattern": "^assets/"},
			"home":         obj{"type": "string", "description": "page shown at the app root"},
			"requireLogin": obj{"type": "boolean", "default": true},
			"notFound":     obj{"type": "string"},
			"languages": obj{"type": "array", "minItems": 1, "uniqueItems": true,
				"description": "UI languages; the first is the language the ui/ files are written in",
				"items":       obj{"type": "string", "pattern": langRe.String()}},
			"nav": obj{"type": "array", "items": obj{"type": "object", "required": []string{"label", "page"},
				"additionalProperties": false, "properties": obj{
					"label":   obj{"type": "string", "description": "text with {{ expr }} interpolations"},
					"page":    obj{"type": "string"},
					"params":  obj{"type": "object", "additionalProperties": exprSchema},
					"visible": obj{"type": "string", "description": "expression over principal"},
				}}},
			"x-layout": obj{},
		},
	}
}

// I18nSchema returns the JSON Schema of ui/i18n/<lang>.json (ADR 0018).
func I18nSchema() map[string]any {
	strs := obj{"type": "object", "additionalProperties": obj{"type": "string"}}
	return obj{
		"$schema": draft, "title": "Organon UI translations", "type": "object", "additionalProperties": false,
		"properties": obj{
			"$schema":  obj{"type": "string"},
			"messages": obj{"type": "object", "description": "text as written (or @key) -> translation", "additionalProperties": obj{"type": "string"}},
			"pages":    obj{"type": "object", "description": "page name -> text -> translation on that page", "additionalProperties": strs},
			"types": obj{"type": "object", "additionalProperties": obj{"type": "object", "additionalProperties": false,
				"properties": obj{
					"fields":  obj{"type": "object", "description": "field path -> label", "additionalProperties": obj{"type": "string"}},
					"values":  obj{"type": "object", "description": "enum field path -> value -> label", "additionalProperties": strs},
					"actions": obj{"type": "object", "description": "transition action -> button label", "additionalProperties": obj{"type": "string"}},
				}}},
		},
	}
}
