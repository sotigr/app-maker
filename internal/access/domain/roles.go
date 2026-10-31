package domain

import (
	"fmt"
	"sort"
)

// Roles is the role hierarchy (spec 8.6): built-in viewer < editor < admin,
// plus app roles declared in app.json as role -> included roles.
type Roles struct {
	includes map[string][]string
}

// BuiltinRoles are always present.
var BuiltinRoles = map[string][]string{"viewer": {}, "editor": {"viewer"}, "admin": {"editor"}}

// NewRoles merges app roles over the built-ins and rejects cycles and
// unknown included roles.
func NewRoles(app map[string][]string) (*Roles, error) {
	r := &Roles{includes: map[string][]string{}}
	for k, v := range BuiltinRoles {
		r.includes[k] = v
	}
	for k, v := range app {
		r.includes[k] = v
	}
	for k, v := range r.includes {
		for _, inc := range v {
			if _, ok := r.includes[inc]; !ok {
				return nil, fmt.Errorf("role %q includes unknown role %q", k, inc)
			}
		}
	}
	for k := range r.includes {
		if r.reaches(k, k, map[string]bool{}, true) {
			return nil, fmt.Errorf("role %q includes itself", k)
		}
	}
	return r, nil
}

func (r *Roles) reaches(from, to string, seen map[string]bool, first bool) bool {
	if !first && from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for _, inc := range r.includes[from] {
		if r.reaches(inc, to, seen, false) {
			return true
		}
	}
	return false
}

// Satisfies reports whether holding role held grants role want.
func (r *Roles) Satisfies(held, want string) bool {
	return held == want || r.reaches(held, want, map[string]bool{}, true)
}

// AtLeast returns every role that satisfies want, sorted.
func (r *Roles) AtLeast(want string) []string {
	var out []string
	for k := range r.includes {
		if r.Satisfies(k, want) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Known reports whether the role exists.
func (r *Roles) Known(role string) bool {
	_, ok := r.includes[role]
	return ok
}
