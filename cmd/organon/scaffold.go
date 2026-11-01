package main

import "strings"

// scaffold returns the files of a new manifest (organon init).
func scaffold(name string) map[string]string {
	r := strings.NewReplacer("{{name}}", name)
	files := map[string]string{
		"app.json": `{
  "app": "{{name}}",
  "version": "0.1.0",
  "auth": { "password": { "enabled": true, "minLength": 12 } },
  "roles": { "viewer": [], "editor": ["viewer"], "admin": ["editor"] },
  "settings": { "notifyEmail": "${env.notifyEmail}" }
}
`,
		"env/dev.json": `{ "notifyEmail": "team@example.com" }
`,
		"types/item.json": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "urn:app:feature:item/v1",
  "title": "Item",
  "type": "object",
  "x-traits": ["urn:sys:trait:ownable", "urn:sys:trait:timestamped"],
  "properties": {
    "title":  { "type": "string", "maxLength": 200, "x-indexed": true },
    "status": { "enum": ["open", "closed"], "default": "open", "x-indexed": true, "x-write": "action:transition",
                "x-stateMachine": { "initial": "open", "transitions": [
                  { "from": "open", "to": "closed", "action": "close" },
                  { "from": "closed", "to": "open", "action": "reopen" } ] } }
  },
  "required": ["title"],
  "additionalProperties": false
}
`,
		"policies/item.json": `{
  "type": "urn:app:feature:item",
  "read":   "isOwner(feature) || hasRole(feature, 'viewer')",
  "create": "principal.kind != 'anonymous'",
  "update": "isOwner(feature) || hasRole(feature, 'editor')",
  "delete": "isOwner(feature)",
  "actions": { "close": "isOwner(feature) || hasRole(feature, 'editor')", "reopen": "isOwner(feature)" }
}
`,
		"flows/notify-closed.json": `{
  "flow": "notifyClosed",
  "trigger": { "event": ["updated"], "on": "urn:app:feature:item", "when": "changed('/status') && after.status == 'closed'" },
  "nodes": {
    "mail": { "node": "email.send", "in": { "to": "env.notifyEmail", "template": "'closed'", "vars": { "title": "feature.title" } } }
  }
}
`,
		"templates/email/closed.txt": "Subject: Closed: {{.title}}\n\n\"{{.title}}\" was closed.\n",
		"tests/item.test.json": `{
  "name": "owners close their items; others cannot",
  "given": {
    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user" } },
    "features": [ { "ref": "i", "type": "item", "owner": "ana", "data": { "title": "First" } } ]
  },
  "steps": [
    { "as": "ben", "action": "close", "on": "i", "expect": { "error": "not_found" } },
    { "as": "ana", "action": "close", "on": "i", "expect": { "feature": { "status": "closed" } } }
  ]
}
`,
		"seed/dev.json": `{
  "users": [ { "ref": "ana", "email": "ana@example.com", "password": "ana-password-123", "name": "Ana" } ],
  "features": [ { "ref": "i", "type": "item", "owner": "ana", "data": { "title": "Hello" } } ]
}
`,
		"ui/app.json": `{
  "title": "{{name}}",
  "home": "items",
  "nav": [ { "label": "Items", "page": "items" } ]
}
`,
		"ui/pages/items.json": `{
  "page": "items",
  "title": "Items",
  "route": "/",
  "data": { "items": { "query": { "type": "item", "sort": [ { "field": "/title" } ], "limit": 100 } } },
  "body": { "component": "stack", "children": [
    { "component": "row", "justify": "between", "children": [
      { "component": "heading", "text": "Items", "level": 1 },
      { "component": "button", "label": "New item", "variant": "primary",
        "on": { "click": [ { "do": "open", "dialog": "new" } ] } }
    ] },
    { "component": "table", "source": "items", "rowPage": "item", "rowParams": { "id": "item.id" }, "empty": "No items yet.",
      "columns": [ { "field": "/title" }, { "field": "/status", "display": "badge" }, { "field": "/updatedAt", "label": "Updated", "display": "date" } ] },
    { "component": "dialog", "name": "new", "title": "New item", "children": [
      { "component": "form", "mode": "create", "type": "item", "fields": [ { "path": "/title" } ], "submit": "Create",
        "on": { "submit": [ { "do": "close" }, { "do": "navigate", "page": "item", "params": { "id": "result.id" } } ] } }
    ] }
  ] }
}
`,
		"ui/pages/item.json": `{
  "page": "item",
  "title": "{{ data.item.title }}",
  "route": "/items/:id",
  "params": { "id": { "type": "string", "format": "uuid" } },
  "data": { "item": { "feature": "params.id", "type": "item" } },
  "body": { "component": "stack", "children": [
    { "component": "link", "text": "← Items", "page": "items" },
    { "component": "row", "justify": "between", "children": [
      { "component": "heading", "text": "{{ data.item.title }}", "level": 1 },
      { "component": "transitions", "source": "item" }
    ] },
    { "component": "detail", "source": "item", "fields": ["/status", "/createdAt", "/updatedAt"] },
    { "component": "card", "title": "Edit", "visible": "can(data.item, 'update')", "children": [
      { "component": "form", "mode": "update", "type": "item", "id": "params.id", "fields": [ { "path": "/title" } ],
        "on": { "submit": [ { "do": "toast", "text": "Saved", "tone": "success" } ] } }
    ] }
  ] }
}
`,
		"tests/ui.test.json": `{
  "name": "the item pages are offered to signed-in users only",
  "given": { "principals": { "ana": { "kind": "user" } } },
  "steps": [
    { "as": "ana", "page": "items", "expect": { "visible": true } },
    { "page": "items", "expect": { "visible": false } }
  ]
}
`,
		".gitignore": ".organon/\n",
	}
	out := map[string]string{}
	for k, v := range files {
		if k == "templates/email/closed.txt" {
			out[k] = v // Go template syntax, no substitution
			continue
		}
		out[k] = r.Replace(v)
	}
	return out
}
