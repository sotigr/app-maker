// Package application holds the Schema use cases served over HTTP:
// describing a type's views and generating the OpenAPI document (spec 6.2
// GET /schema, 4.6).
package application

import (
	"encoding/json"
	"sort"

	"organon/internal/schema/api"
)

// TypeDescription is one type's views for an audience.
type TypeDescription struct {
	ID       string                     `json:"id"`
	Name     string                     `json:"name"`
	Title    string                     `json:"title,omitempty"`
	Version  int                        `json:"version"`
	Traits   []string                   `json:"traits"`
	Children []string                   `json:"children"`
	Views    map[string]json.RawMessage `json:"views"`
	Actions  []string                   `json:"actions"`
	States   map[string]StateMachineDTO `json:"stateMachines,omitempty"`
}

// StateMachineDTO publishes a state machine for clients and the console.
type StateMachineDTO struct {
	Initial     string          `json:"initial"`
	Transitions []TransitionDTO `json:"transitions"`
}

// TransitionDTO is one declared transition.
type TransitionDTO struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Action string `json:"action"`
}

// Describe returns the views of a type for audience a.
func Describe(ct *api.CompiledType, a api.Audience) TypeDescription {
	d := TypeDescription{ID: string(ct.ID), Name: ct.Name, Title: ct.Title, Version: ct.Version,
		Traits: []string{}, Children: []string{}, Views: map[string]json.RawMessage{}, Actions: []string{}}
	for _, t := range ct.Traits {
		d.Traits = append(d.Traits, string(t))
	}
	for _, c := range ct.Children {
		d.Children = append(d.Children, string(c))
	}
	for _, dir := range api.Directions {
		d.Views[dir.String()] = ct.View(a, dir).JSONSchema
	}
	actions := map[string]bool{}
	if len(ct.StateMachines) > 0 {
		d.States = map[string]StateMachineDTO{}
		actions["transition"] = true
	}
	for path, sm := range ct.StateMachines {
		dto := StateMachineDTO{Initial: sm.Initial}
		for _, t := range sm.Transitions {
			dto.Transitions = append(dto.Transitions, TransitionDTO{From: t.From, To: t.To, Action: t.Action})
			actions[t.Action] = true
		}
		d.States[path] = dto
	}
	if ct.HasTrait(api.TraitOwnable) {
		actions["transfer"] = true
	}
	for a := range actions {
		d.Actions = append(d.Actions, a)
	}
	sort.Strings(d.Actions)
	return d
}

// OpenAPI builds an OpenAPI 3.1 document of the generic API for audience a
// (spec 6.2, 13.1). Only the views of that audience are included, so
// private fields never appear in a public document.
func OpenAPI(reg api.TypeRegistry, a api.Audience, appName, version string) map[string]any {
	schemas := map[string]any{}
	paths := map[string]any{}
	problem := map[string]any{"$ref": "#/components/schemas/Problem"}
	errResp := map[string]any{"description": "Problem details", "content": map[string]any{"application/problem+json": map[string]any{"schema": problem}}}
	envelope := func(data string) map[string]any {
		return map[string]any{
			"type":     "object",
			"required": []string{"id", "type", "version", "data"},
			"properties": map[string]any{
				"id": map[string]any{"type": "string", "format": "uuid"}, "type": map[string]any{"type": "string"},
				"parent": map[string]any{"type": "string", "format": "uuid"}, "version": map[string]any{"type": "integer"},
				"createdAt": map[string]any{"type": "string", "format": "date-time"},
				"updatedAt": map[string]any{"type": "string", "format": "date-time"},
				"data":      map[string]any{"$ref": "#/components/schemas/" + data},
			},
		}
	}
	for _, ct := range reg.All() {
		if ct.Base() == api.TypeRoot {
			continue
		}
		name := ct.Name
		for _, dir := range []api.Direction{api.DirRead, api.DirCreate, api.DirUpdate} {
			var raw map[string]any
			_ = json.Unmarshal(ct.View(a, dir).JSONSchema, &raw)
			delete(raw, "$schema")
			delete(raw, "$id")
			schemas[name+"."+dir.String()] = raw
		}
		schemas[name] = envelope(name + ".read")
		ref := map[string]any{"$ref": "#/components/schemas/" + name}
		ok := func(desc string) map[string]any {
			return map[string]any{"description": desc, "content": map[string]any{"application/json": map[string]any{"schema": ref}}}
		}
		body := func(dir string) map[string]any {
			return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/" + name + "." + dir}}}}
		}
		paths["/v1/features/"+name] = map[string]any{
			"post": map[string]any{"operationId": "create_" + name, "requestBody": body("create"),
				"parameters": []any{map[string]any{"name": "parent", "in": "query", "schema": map[string]any{"type": "string", "format": "uuid"}}},
				"responses":  map[string]any{"201": ok("Created"), "default": errResp}},
		}
		idParam := map[string]any{"name": "id", "in": "path", "required": true, "schema": map[string]any{"type": "string", "format": "uuid"}}
		ifMatch := map[string]any{"name": "If-Match", "in": "header", "required": true, "schema": map[string]any{"type": "string"}}
		paths["/v1/features/"+name+"/{id}"] = map[string]any{
			"get":    map[string]any{"operationId": "get_" + name, "parameters": []any{idParam}, "responses": map[string]any{"200": ok("The feature"), "default": errResp}},
			"patch":  map[string]any{"operationId": "update_" + name, "parameters": []any{idParam, ifMatch}, "requestBody": body("update"), "responses": map[string]any{"200": ok("Updated"), "default": errResp}},
			"delete": map[string]any{"operationId": "delete_" + name, "parameters": []any{idParam, ifMatch}, "responses": map[string]any{"204": map[string]any{"description": "Deleted"}, "default": errResp}},
		}
	}
	schemas["Problem"] = map[string]any{"type": "object", "properties": map[string]any{
		"type": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "status": map[string]any{"type": "integer"},
		"code": map[string]any{"type": "string"}, "detail": map[string]any{"type": "string"},
		"errors": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string"}, "code": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}}}},
	}}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": appName, "version": version, "x-audience": a.String()},
		"paths":   paths,
		"components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{
			"bearer":  map[string]any{"type": "http", "scheme": "bearer"},
			"session": map[string]any{"type": "apiKey", "in": "cookie", "name": "organon_session"},
		}},
	}
}
