package domain

import (
	"reflect"
	"testing"
)

// Spec 8.6: built-in roles include the one before; app roles extend them.
func TestRoles(t *testing.T) {
	r, err := NewRoles(map[string][]string{"billing": {"viewer"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		held, want string
		ok         bool
	}{
		{"admin", "viewer", true}, {"editor", "viewer", true}, {"viewer", "editor", false},
		{"billing", "viewer", true}, {"billing", "editor", false}, {"admin", "admin", true},
	} {
		if got := r.Satisfies(c.held, c.want); got != c.ok {
			t.Errorf("%s satisfies %s = %v", c.held, c.want, got)
		}
	}
	if got := r.AtLeast("editor"); !reflect.DeepEqual(got, []string{"admin", "editor"}) {
		t.Errorf("AtLeast(editor) = %v", got)
	}
	if _, err := NewRoles(map[string][]string{"a": {"b"}, "b": {"a"}}); err == nil {
		t.Error("role cycle accepted")
	}
	if _, err := NewRoles(map[string][]string{"a": {"nope"}}); err == nil {
		t.Error("unknown included role accepted")
	}
}

// Spec 8.5: read policies using isOwner, hasRole, and comparisons become
// predicates; anything else falls back to post-filtering.
func TestTranslateRead(t *testing.T) {
	ok := map[string]ScopeExpr{
		"isOwner(feature)":              ScOwner{},
		"hasRole(feature, 'viewer')":    ScRole{Role: "viewer"},
		"true":                          ScPrincipal{Source: "true"},
		"principal.kind != 'anonymous'": ScPrincipal{Source: `principal.kind != "anonymous"`},
		"hasRole(feature, 'viewer') || isOwner(feature)": ScOr{Terms: []ScopeExpr{ScRole{Role: "viewer"}, ScOwner{}}},
		"feature.status == 'open' && isOwner(feature)": ScAnd{Terms: []ScopeExpr{
			ScField{Path: "/status", Literal: "open"}, ScOwner{}}},
		"principal.id == feature.assignee": ScField{Path: "/assignee", PrincipalID: true},
	}
	for src, want := range ok {
		got, good := TranslateRead(src)
		if !good || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v (%v), want %#v", src, got, good, want)
		}
	}
	for _, src := range []string{
		"len(feature.title) > 3",
		"feature.dueAt < now()",
		"hasRole(parent, 'viewer')",
		"!isOwner(feature)",
	} {
		if _, good := TranslateRead(src); good {
			t.Errorf("%s should not translate", src)
		}
	}
}
