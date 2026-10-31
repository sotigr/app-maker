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
	VisOwner
	VisPrivate
	VisSystem
)

var visNames = [...]string{"public", "owner", "private", "system"}

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
	AudPublic Audience = iota
	AudOwner
	AudAdmin
	AudSystem
)

// Audiences lists all audiences in order.
var Audiences = []Audience{AudPublic, AudOwner, AudAdmin, AudSystem}

var audNames = [...]string{"public", "owner", "admin", "system"}

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
// public reads public; owner and admin read public and owner; private is
// for server-side code only (system); system reads everything.
func (a Audience) CanRead(v Visibility) bool {
	switch a {
	case AudPublic:
		return v == VisPublic
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
)

// WriteRule says who may write a field.
type WriteRule struct {
	Kind   WriteKind
	Action string // for WriteAction
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
	}
	return "any"
}

// Allows reports whether the audience may write the field through the
// generic API. Action-only fields are never writable there.
func (w WriteRule) Allows(a Audience) bool {
	switch w.Kind {
	case WriteAny:
		return true
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
