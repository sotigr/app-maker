// Package api is the Access context's published contract: the Principal
// value, the Authorizer port, and related DTOs (spec 8.3, 8.5).
package api

import (
	"slices"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

// PrincipalKind is the kind of a principal (spec 8.1).
type PrincipalKind string

// Principal kinds.
const (
	KindUser         PrincipalKind = "user"
	KindOrganization PrincipalKind = "organization"
	KindTeam         PrincipalKind = "team"
	KindService      PrincipalKind = "service"
	KindSystem       PrincipalKind = "system"
	KindAnonymous    PrincipalKind = "anonymous"
	// KindLink is the holder of a share link (ADR 0020): ID is the
	// shareLink feature, Link what it opens.
	KindLink PrincipalKind = "link"
)

// LinkScope is what a share link grants: reading Object and its subtree,
// and running Actions on Object.
type LinkScope struct {
	Object  kernel.ID
	Actions []string
}

// Principal is the authenticated actor of a request (spec 8.3).
type Principal struct {
	ID     kernel.ID
	Kind   PrincipalKind
	Roles  []string
	Groups []kernel.ID // transitive memberships, from the relationship index
	// Locale is the user's preferred UI language, or empty (ADR 0018).
	Locale string
	// Link is set for KindLink principals.
	Link *LinkScope
}

// SystemPrincipalID is the fixed ID of the system principal (spec 8.4).
var SystemPrincipalID = kernel.MustParseID("00000000-0000-7000-8000-000000000001")

// System returns the system principal.
func System() Principal {
	return Principal{ID: SystemPrincipalID, Kind: KindSystem, Roles: []string{"admin"}}
}

// Anonymous returns the unauthenticated principal.
func Anonymous() Principal { return Principal{Kind: KindAnonymous} }

// IsSystem reports whether p is the system principal.
func (p Principal) IsSystem() bool { return p.Kind == KindSystem }

// IsAnonymous reports whether p is unauthenticated.
func (p Principal) IsAnonymous() bool { return p.Kind == KindAnonymous }

// IsLink reports whether p holds a share link.
func (p Principal) IsLink() bool { return p.Kind == KindLink && p.Link != nil }

// HasRole reports whether p holds the app-level role.
func (p Principal) HasRole(role string) bool { return slices.Contains(p.Roles, role) }

// ExprValue returns p as seen by expressions.
func (p Principal) ExprValue() map[string]any {
	groups := make([]any, len(p.Groups))
	for i, g := range p.Groups {
		groups[i] = g.String()
	}
	roles := make([]any, len(p.Roles))
	for i, r := range p.Roles {
		roles[i] = r
	}
	id := ""
	if !p.ID.IsZero() {
		id = p.ID.String()
	}
	object := ""
	if p.Link != nil {
		object = p.Link.Object.String()
	}
	return map[string]any{"id": id, "kind": string(p.Kind), "roles": roles, "groups": groups, "object": object}
}

// PrincipalExprType is the expression type of `principal`.
func PrincipalExprType() *rt.Type {
	return rt.MapOf(map[string]*rt.Type{
		"id":     rt.String,
		"kind":   rt.String,
		"roles":  rt.ListOf(rt.String),
		"groups": rt.ListOf(rt.String),
		"object": rt.String, // the shared feature of a link principal, else ""
	})
}
