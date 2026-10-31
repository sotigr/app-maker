package api

import (
	"slices"
	"strings"

	"app-maker/internal/kernel"
)

// Doc is the view of a feature that predicates are evaluated against.
type Doc interface {
	FeatureID() kernel.ID
	Owner() *kernel.ID
	AncestorIDs() []kernel.ID // root first
	// Value returns the value at a JSON Pointer, navigating objects.
	Value(path string) (kernel.Value, bool)
}

// Match evaluates p against d. It is the reference semantics of the
// operators in spec 7.2; repositories translating plans natively must
// agree with it (checked by the conformance suite).
func Match(p Predicate, d Doc) bool {
	switch x := p.(type) {
	case nil, True:
		return true
	case False:
		return false
	case And:
		for _, t := range x.Terms {
			if !Match(t, d) {
				return false
			}
		}
		return true
	case Or:
		for _, t := range x.Terms {
			if Match(t, d) {
				return true
			}
		}
		return false
	case Not:
		return !Match(x.Term, d)
	case InScope:
		switch x.Field {
		case ScopeOwner:
			o := d.Owner()
			return o != nil && slices.Contains(x.IDs, *o)
		case ScopeSelf:
			return slices.Contains(x.IDs, d.FeatureID())
		case ScopeAncestor:
			if slices.Contains(x.IDs, d.FeatureID()) {
				return true
			}
			for _, a := range d.AncestorIDs() {
				if slices.Contains(x.IDs, a) {
					return true
				}
			}
		}
		return false
	case Compare:
		return compare(x, d)
	}
	return false
}

func compare(c Compare, d Doc) bool {
	v, present := d.Value(c.Path)
	if present {
		if _, isNull := v.(kernel.Null); isNull {
			present = false
		}
	}
	switch c.Op {
	case OpExists:
		return present
	case OpEq:
		if _, wantNull := c.Value.(kernel.Null); wantNull || c.Value == nil {
			return !present
		}
		return present && kernel.Equal(v, c.Value)
	case OpNe:
		if _, wantNull := c.Value.(kernel.Null); wantNull || c.Value == nil {
			return present
		}
		return !present || !kernel.Equal(v, c.Value)
	case OpIn, OpNin:
		l, _ := c.Value.(kernel.List)
		in := false
		if present {
			for _, it := range l.Items {
				if kernel.Equal(v, it) {
					in = true
					break
				}
			}
		}
		if c.Op == OpIn {
			return in
		}
		return !in
	case OpLt, OpLte, OpGt, OpGte:
		if !present {
			return false
		}
		r, ok := kernel.Compare(v, c.Value)
		if !ok {
			return false
		}
		switch c.Op {
		case OpLt:
			return r < 0
		case OpLte:
			return r <= 0
		case OpGt:
			return r > 0
		}
		return r >= 0
	case OpPrefix:
		if !present {
			return false
		}
		s, ok1 := kernel.Native(v).(string)
		pre, ok2 := kernel.Native(c.Value).(string)
		return ok1 && ok2 && strings.HasPrefix(s, pre)
	case OpContains:
		l, ok := v.(kernel.List)
		if !present || !ok {
			return false
		}
		for _, it := range l.Items {
			if kernel.Equal(it, c.Value) {
				return true
			}
		}
	}
	return false
}
