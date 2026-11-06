package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Problem is a compile error located by file and JSON Pointer.
type Problem struct {
	File    string `json:"file,omitempty"`
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

func (p Problem) Error() string {
	loc := p.Pointer
	if loc == "" {
		loc = "/"
	}
	if p.File != "" {
		return p.File + "#" + loc + ": " + p.Message
	}
	return loc + ": " + p.Message
}

// Problems is a list of compile errors.
type Problems []Problem

func (ps Problems) Error() string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.Error()
	}
	return strings.Join(s, "\n")
}

// Keyword sets of the JSON Schema profile (spec 4.1).
var (
	allowedKeywords = set("type", "properties", "required", "items", "enum", "const", "default",
		"title", "description", "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum",
		"minLength", "maxLength", "pattern", "minItems", "maxItems", "uniqueItems", "format",
		"readOnly", "writeOnly", "$id", "$defs", "$ref",
		// restricted
		"oneOf", "additionalProperties")
	rejectedKeywords = set("anyOf", "allOf", "not", "if", "then", "else", "patternProperties",
		"dependentSchemas", "dependentRequired", "propertyNames", "unevaluatedProperties",
		"unevaluatedItems", "contains", "prefixItems", "$dynamicRef", "$dynamicAnchor", "$anchor",
		"$recursiveRef", "$recursiveAnchor", "minContains", "maxContains", "dependencies",
		"additionalItems", "minProperties", "maxProperties", "multipleOf", "contentEncoding",
		"contentMediaType", "contentSchema", "examples", "deprecated", "$vocabulary", "$comment")
	assertedFormats = set("date-time", "date", "email", "uri", "uuid")

	// x- vocabulary (spec 4.2) by position.
	typeVocab  = set("x-traits", "x-allowedChildren", "x-uniqueName", "x-retention", "x-repository", "x-shareable")
	fieldVocab = set("x-kind", "x-indexed", "x-visibility", "x-write", "x-immutable", "x-ref",
		"x-onDelete", "x-computed", "x-aggregate", "x-stream", "x-persist", "x-stateMachine",
		"x-searchable", "x-maxBytes", "x-contentTypes", "x-thumbnails", "x-sequence")
	nestedVocab = set("x-kind", "x-ref", "x-onDelete", "x-maxBytes", "x-contentTypes")
	jsonTypes   = set("string", "integer", "number", "boolean", "object", "array", "null")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

type position int

const (
	posTypeRoot position = iota
	posTraitRoot
	posField  // top-level property
	posNested // nested schema (items, nested properties, oneOf branches)
	posDef    // $defs entry: may be used as a field or nested schema
)

// checkProfile walks a schema document and reports every keyword outside
// the profile with its JSON Pointer (spec 4.1).
func checkProfile(node any, ptr string, pos position, out *Problems) {
	m, ok := node.(map[string]any)
	if !ok {
		*out = append(*out, Problem{Pointer: ptr, Message: "schema must be an object"})
		return
	}
	isJSONKind := m["x-kind"] == "json"
	keys := sortedKeys(m)
	for _, k := range keys {
		kp := ptr + "/" + escape(k)
		switch {
		case k == "$schema":
			if pos != posTypeRoot && pos != posTraitRoot {
				*out = append(*out, Problem{Pointer: kp, Message: "$schema is only allowed at the document root"})
			}
		case strings.HasPrefix(k, "x-"):
			var allowed map[string]bool
			switch pos {
			case posTypeRoot:
				allowed = typeVocab
			case posTraitRoot:
				allowed = map[string]bool{}
			case posField, posDef:
				allowed = fieldVocab
			case posNested:
				allowed = nestedVocab
			}
			if !allowed[k] {
				if fieldVocab[k] || typeVocab[k] {
					*out = append(*out, Problem{Pointer: kp, Message: fmt.Sprintf("keyword %s is not allowed here", k)})
				} else {
					*out = append(*out, Problem{Pointer: kp, Message: fmt.Sprintf("unknown vocabulary keyword %s", k)})
				}
			}
		case rejectedKeywords[k]:
			*out = append(*out, Problem{Pointer: kp, Message: fmt.Sprintf("keyword %s is rejected by the profile", k)})
		case !allowedKeywords[k]:
			*out = append(*out, Problem{Pointer: kp, Message: fmt.Sprintf("unknown keyword %s", k)})
		}
	}
	if isJSONKind {
		return // freeform: additionalProperties and nested content are not restricted
	}

	// type: a single type, or [T, "null"] for nullable fields.
	if t, ok := m["type"]; ok {
		switch tv := t.(type) {
		case string:
			if !jsonTypes[tv] || tv == "null" {
				*out = append(*out, Problem{Pointer: ptr + "/type", Message: fmt.Sprintf("invalid type %q", tv)})
			}
		case []any:
			if len(tv) != 2 || tv[1] != "null" || tv[0] == "null" {
				*out = append(*out, Problem{Pointer: ptr + "/type", Message: `type arrays are only allowed as [T, "null"]`})
			} else if s, ok := tv[0].(string); !ok || !jsonTypes[s] {
				*out = append(*out, Problem{Pointer: ptr + "/type", Message: "invalid type"})
			}
		default:
			*out = append(*out, Problem{Pointer: ptr + "/type", Message: "type must be a string or [T, \"null\"]"})
		}
	}
	if f, ok := m["format"]; ok {
		if s, ok := f.(string); !ok || !assertedFormats[s] {
			*out = append(*out, Problem{Pointer: ptr + "/format", Message: fmt.Sprintf("format %v is not supported (allowed: date-time, date, email, uri, uuid)", f)})
		}
	}
	if ap, ok := m["additionalProperties"]; ok && ap != false {
		*out = append(*out, Problem{Pointer: ptr + "/additionalProperties", Message: "additionalProperties must be false"})
	}
	if r, ok := m["$ref"]; ok {
		s, _ := r.(string)
		if !strings.HasPrefix(s, "#/$defs/") {
			*out = append(*out, Problem{Pointer: ptr + "/$ref", Message: "only local $ref to #/$defs/<name> is allowed"})
		}
	}
	if props, ok := m["properties"]; ok {
		pm, ok := props.(map[string]any)
		if !ok {
			*out = append(*out, Problem{Pointer: ptr + "/properties", Message: "properties must be an object"})
		} else {
			child := posNested
			if pos == posTypeRoot || pos == posTraitRoot {
				child = posField
			}
			for _, name := range sortedKeys(pm) {
				checkProfile(pm[name], ptr+"/properties/"+escape(name), child, out)
			}
		}
	}
	if it, ok := m["items"]; ok {
		if _, isArr := it.([]any); isArr {
			*out = append(*out, Problem{Pointer: ptr + "/items", Message: "tuple items are not allowed"})
		} else {
			checkProfile(it, ptr+"/items", posNested, out)
		}
	}
	if defs, ok := m["$defs"]; ok {
		dm, ok := defs.(map[string]any)
		if !ok {
			*out = append(*out, Problem{Pointer: ptr + "/$defs", Message: "$defs must be an object"})
		} else {
			for _, name := range sortedKeys(dm) {
				checkProfile(dm[name], ptr+"/$defs/"+escape(name), posDef, out)
			}
		}
	}
	if one, ok := m["oneOf"]; ok {
		checkOneOf(one, ptr+"/oneOf", out)
	}
	if req, ok := m["required"]; ok {
		if _, ok := req.([]any); !ok {
			*out = append(*out, Problem{Pointer: ptr + "/required", Message: "required must be an array"})
		}
	}
}

// checkOneOf enforces discriminated unions: every branch is an object with
// a const on the same tag property (spec 4.1).
func checkOneOf(node any, ptr string, out *Problems) {
	branches, ok := node.([]any)
	if !ok || len(branches) < 2 {
		*out = append(*out, Problem{Pointer: ptr, Message: "oneOf must list at least two branches"})
		return
	}
	var tag string
	for i, b := range branches {
		bp := fmt.Sprintf("%s/%d", ptr, i)
		checkProfile(b, bp, posNested, out)
		bm, _ := b.(map[string]any)
		props, _ := bm["properties"].(map[string]any)
		found := ""
		for _, name := range sortedKeys(props) {
			if pm, ok := props[name].(map[string]any); ok {
				if _, ok := pm["const"]; ok {
					found = name
					break
				}
			}
		}
		switch {
		case found == "":
			*out = append(*out, Problem{Pointer: bp, Message: "oneOf branch needs a const discriminator property"})
		case tag == "":
			tag = found
		case tag != found:
			*out = append(*out, Problem{Pointer: bp, Message: fmt.Sprintf("oneOf discriminator must be %q in every branch", tag)})
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// escape encodes a JSON Pointer token (RFC 6901).
func escape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
