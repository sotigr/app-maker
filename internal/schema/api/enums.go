package api

import (
	"fmt"
	"strings"
)

// Visibility is a field's visibility tier (spec 4.6).
type Visibility uint8

// Visibility tiers, from most to least visible.
const (
	VisPublic Visibility = iota
	VisMember            // signed-in callers (users, teams, service accounts); not anonymous callers or share links (ADR 0024)
	VisOwner
	VisPrivate
	VisSystem
)

var visNames = [...]string{"public", "member", "owner", "private", "system"}

func (v Visibility) String() string { return visNames[v] }

// ParseVisibility parses an x-visibility value.
func ParseVisibility(s string) (Visibility, bool) {
	for i, n := range visNames {
		if n == s {
			return Visibility(i), true
		}
	}
	return 0, false
}

// Audience is the tier a principal gets for a feature (spec 4.6, 8.7).
type Audience uint8

// Audiences.
const (
	AudPublic Audience = iota // anonymous callers and share links
	AudMember                 // signed-in callers who neither own nor administer the feature (ADR 0024)
	AudOwner
	AudAdmin
	AudSystem
)

// Audiences lists all audiences in order.
var Audiences = []Audience{AudPublic, AudMember, AudOwner, AudAdmin, AudSystem}

var audNames = [...]string{"public", "member", "owner", "admin", "system"}

func (a Audience) String() string { return audNames[a] }

// ParseAudience parses an audience name.
func ParseAudience(s string) (Audience, bool) {
	for i, n := range audNames {
		if n == s {
			return Audience(i), true
		}
	}
	return 0, false
}

// CanRead reports whether the audience may read fields of tier v:
// public reads public; member reads public and member; owner and admin
// read public, member, and owner; private is for server-side code only
// (system); system reads everything.
func (a Audience) CanRead(v Visibility) bool {
	switch a {
	case AudPublic:
		return v == VisPublic
	case AudMember:
		return v <= VisMember
	case AudOwner, AudAdmin:
		return v <= VisOwner
	case AudSystem:
		return true
	}
	return false
}

// Direction is the direction of a view (spec 4.5).
type Direction uint8

// Directions.
const (
	DirRead Direction = iota
	DirCreate
	DirUpdate
	DirQuery
	DirEvent
)

// Directions lists all directions.
var Directions = []Direction{DirRead, DirCreate, DirUpdate, DirQuery, DirEvent}

var dirNames = [...]string{"read", "create", "update", "query", "event"}

func (d Direction) String() string { return dirNames[d] }

// ViewKey selects a view.
type ViewKey struct {
	Audience  Audience
	Direction Direction
}

func (k ViewKey) String() string { return k.Audience.String() + "." + k.Direction.String() }

// WriteKind is the kind of an x-write rule.
type WriteKind uint8

// Write rule kinds (spec 4.2).
const (
	WriteAny WriteKind = iota
	WriteOwner
	WriteAdmin
	WriteSystem
	WriteAction
	WriteRole  // role:<name>: callers holding the role on the feature (or, on create, its parent)
	WriteAnyOf // a list: any of Alts allows
)

// WriteRule says who may write a field.
type WriteRule struct {
	Kind   WriteKind
	Action string      // for WriteAction
	Role   string      // for WriteRole
	Alts   []WriteRule // for WriteAnyOf
}

// ParseWriteRules parses an x-write value: one rule, or a list of rules
// of which any allows (gap #35: "x-write": ["action:addLine", "role:manager"]).
func ParseWriteRules(v any) (WriteRule, error) {
	switch x := v.(type) {
	case string:
		return ParseWriteRule(x)
	case []any:
		if len(x) == 0 {
			return WriteRule{}, fmt.Errorf("x-write list is empty")
		}
		out := WriteRule{Kind: WriteAnyOf}
		for _, it := range x {
			s, _ := it.(string)
			r, err := ParseWriteRule(s)
			if err != nil {
				return WriteRule{}, err
			}
			out.Alts = append(out.Alts, r)
		}
		if len(out.Alts) == 1 {
			return out.Alts[0], nil
		}
		return out, nil
	}
	return WriteRule{}, fmt.Errorf("x-write must be a string or a list of strings")
}

// Rules returns the rule's alternatives (itself when it is not a list).
func (w WriteRule) Rules() []WriteRule {
	if w.Kind == WriteAnyOf {
		return w.Alts
	}
	return []WriteRule{w}
}

// Unlocks reports whether running action may write the field
// (action:<name> rules).
func (w WriteRule) Unlocks(action string) bool {
	for _, r := range w.Rules() {
		if r.Kind == WriteAction && r.Action == action {
			return true
		}
	}
	return false
}

// RolesFor returns the roles one of which the audience must hold for a
// generic write, or nil when a rule allows the audience without a role.
// Admins and the system need no role.
func (w WriteRule) RolesFor(a Audience) []string {
	if a >= AudAdmin {
		return nil
	}
	var roles []string
	for _, r := range w.Rules() {
		switch {
		case r.Kind == WriteRole:
			roles = append(roles, r.Role)
		case r.Allows(a):
			return nil
		}
	}
	return roles
}

// ParseWriteRule parses an x-write value.
func ParseWriteRule(s string) (WriteRule, error) {
	switch s {
	case "any":
		return WriteRule{Kind: WriteAny}, nil
	case "owner":
		return WriteRule{Kind: WriteOwner}, nil
	case "admin":
		return WriteRule{Kind: WriteAdmin}, nil
	case "system":
		return WriteRule{Kind: WriteSystem}, nil
	}
	if name, ok := strings.CutPrefix(s, "action:"); ok && name != "" {
		return WriteRule{Kind: WriteAction, Action: name}, nil
	}
	if name, ok := strings.CutPrefix(s, "role:"); ok && name != "" {
		return WriteRule{Kind: WriteRole, Role: name}, nil
	}
	return WriteRule{}, fmt.Errorf("invalid x-write %q", s)
}

func (w WriteRule) String() string {
	switch w.Kind {
	case WriteOwner:
		return "owner"
	case WriteAdmin:
		return "admin"
	case WriteSystem:
		return "system"
	case WriteAction:
		return "action:" + w.Action
	case WriteRole:
		return "role:" + w.Role
	case WriteAnyOf:
		parts := make([]string, len(w.Alts))
		for i, r := range w.Alts {
			parts[i] = r.String()
		}
		return strings.Join(parts, ",")
	}
	return "any"
}

// Allows reports whether the audience may write the field through the
// generic API. Action-only fields are never writable there. Role rules
// allow signed-in audiences; the write path then checks the role.
func (w WriteRule) Allows(a Audience) bool {
	switch w.Kind {
	case WriteAny:
		return true
	case WriteRole:
		return a >= AudMember
	case WriteAnyOf:
		for _, r := range w.Alts {
			if r.Allows(a) {
				return true
			}
		}
		return false
	case WriteOwner:
		return a >= AudOwner
	case WriteAdmin:
		return a >= AudAdmin
	case WriteSystem:
		return a == AudSystem
	}
	return false
}

// OnDelete is a Ref field's deletion behavior (spec 3.3).
type OnDelete uint8

// OnDelete values.
const (
	OnDeleteDangle OnDelete = iota
	OnDeleteRestrict
	OnDeleteSetNull
)

var onDeleteNames = [...]string{"dangle", "restrict", "setNull"}

func (o OnDelete) String() string { return onDeleteNames[o] }

// ParseOnDelete parses an x-onDelete value.
func ParseOnDelete(s string) (OnDelete, bool) {
	for i, n := range onDeleteNames {
		if n == s {
			return OnDelete(i), true
		}
	}
	return 0, false
}

// StreamMode is a streamed field's delivery mode (spec 9.7).
type StreamMode uint8

// Stream modes.
const (
	StreamState StreamMode = iota
	StreamAppend
)

func (m StreamMode) String() string {
	if m == StreamAppend {
		return "append"
	}
	return "state"
}

// StreamSpec makes a field subscribable (x-stream).
type StreamSpec struct {
	Mode    StreamMode
	MaxRate float64 // deliveries per second for state mode; 0 = unlimited
}
