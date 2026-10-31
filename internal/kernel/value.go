package kernel

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"time"
)

// Value is a sealed sum type of field values (spec 3.2). It is implemented
// only inside this package.
type Value interface {
	Kind() Kind
	isValue()
}

// String is a UTF-8 string value.
type String string

// Int64 is an exact 64-bit integer value.
type Int64 int64

// Float is a finite float64 value; NaN and Infinity are rejected.
type Float float64

// Bool is a boolean value.
type Bool bool

// Enum is a value drawn from a schema-declared set.
type Enum string

// Time is a UTC instant.
type Time struct{ T time.Time }

// DecimalValue is an exact decimal value.
type DecimalValue struct{ D Decimal }

// Ref is an association to another feature (spec 3.3).
type Ref struct {
	ID     ID
	Target string // target type or trait ID, from x-ref
}

// Blob references bytes in blob storage (spec 14.4).
type Blob struct {
	ID          string `json:"id"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
}

// Object is an ordered map of values with value semantics.
type Object struct {
	keys []string
	vals map[string]Value
}

// List is a list of values of one element kind.
type List struct {
	Elem  Kind
	Items []Value
}

// JSON is freeform raw JSON; not indexable or queryable.
type JSON json.RawMessage

// Null is the null value of nullable fields.
type Null struct{}

func (String) Kind() Kind       { return KindString }
func (Int64) Kind() Kind        { return KindInt64 }
func (Float) Kind() Kind        { return KindFloat }
func (Bool) Kind() Kind         { return KindBool }
func (Enum) Kind() Kind         { return KindEnum }
func (Time) Kind() Kind         { return KindTime }
func (DecimalValue) Kind() Kind { return KindDecimal }
func (Ref) Kind() Kind          { return KindRef }
func (Blob) Kind() Kind         { return KindBlob }
func (Object) Kind() Kind       { return KindObject }
func (List) Kind() Kind         { return KindList }
func (JSON) Kind() Kind         { return KindJSON }
func (Null) Kind() Kind         { return KindNull }

func (String) isValue()       {}
func (Int64) isValue()        {}
func (Float) isValue()        {}
func (Bool) isValue()         {}
func (Enum) isValue()         {}
func (Time) isValue()         {}
func (DecimalValue) isValue() {}
func (Ref) isValue()          {}
func (Blob) isValue()         {}
func (Object) isValue()       {}
func (List) isValue()         {}
func (JSON) isValue()         {}
func (Null) isValue()         {}

// NewTime returns a Time value normalized to UTC.
func NewTime(t time.Time) Time { return Time{T: t.UTC()} }

// NewFloat returns a Float, rejecting NaN and Infinity.
func NewFloat(f float64) (Float, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return Float(f), true
}

// NewObject builds an Object; keys keep the given order.
func NewObject(keys []string, vals map[string]Value) Object {
	k := append([]string(nil), keys...)
	v := make(map[string]Value, len(vals))
	for key, val := range vals {
		v[key] = val
	}
	return Object{keys: k, vals: v}
}

// Keys returns the object's keys in order.
func (o Object) Keys() []string { return append([]string(nil), o.keys...) }

// Get returns the value at key.
func (o Object) Get(key string) (Value, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Len returns the number of keys.
func (o Object) Len() int { return len(o.keys) }

// Native converts v to the Go representation used by expressions:
// string, int64, float64, Decimal, bool, time.Time, map[string]any, []any,
// nil. Refs become their ID string, blobs a map.
func Native(v Value) any {
	switch x := v.(type) {
	case nil, Null:
		return nil
	case String:
		return string(x)
	case Enum:
		return string(x)
	case Int64:
		return int64(x)
	case Float:
		return float64(x)
	case Bool:
		return bool(x)
	case Time:
		return x.T
	case DecimalValue:
		return x.D
	case Ref:
		return x.ID.String()
	case Blob:
		return map[string]any{"id": x.ID, "size": x.Size, "contentType": x.ContentType}
	case Object:
		m := make(map[string]any, len(x.keys))
		for _, k := range x.keys {
			m[k] = Native(x.vals[k])
		}
		return m
	case List:
		l := make([]any, len(x.Items))
		for i, it := range x.Items {
			l[i] = Native(it)
		}
		return l
	case JSON:
		var out any
		if err := json.Unmarshal(x, &out); err != nil {
			return nil
		}
		return out
	}
	return nil
}

// Equal reports whether a and b are the same value.
func Equal(a, b Value) bool {
	if a == nil {
		a = Null{}
	}
	if b == nil {
		b = Null{}
	}
	if a.Kind() != b.Kind() {
		// Enum and String compare by text.
		if as, ok := textOf(a); ok {
			if bs, ok := textOf(b); ok {
				return as == bs
			}
		}
		return false
	}
	switch x := a.(type) {
	case Time:
		return x.T.Equal(b.(Time).T)
	case DecimalValue:
		return x.D.Cmp(b.(DecimalValue).D) == 0
	case Ref:
		return x.ID == b.(Ref).ID
	case Object:
		y := b.(Object)
		if len(x.keys) != len(y.keys) {
			return false
		}
		for k, v := range x.vals {
			w, ok := y.vals[k]
			if !ok || !Equal(v, w) {
				return false
			}
		}
		return true
	case List:
		y := b.(List)
		if len(x.Items) != len(y.Items) {
			return false
		}
		for i := range x.Items {
			if !Equal(x.Items[i], y.Items[i]) {
				return false
			}
		}
		return true
	case JSON:
		return bytes.Equal(compactJSON(x), compactJSON(b.(JSON)))
	}
	return a == b
}

func compactJSON(b []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return b
	}
	return buf.Bytes()
}

func textOf(v Value) (string, bool) {
	switch x := v.(type) {
	case String:
		return string(x), true
	case Enum:
		return string(x), true
	}
	return "", false
}

// Compare orders two scalar values of the same kind. ok is false when the
// values are not comparable. Null sorts before every other value.
func Compare(a, b Value) (c int, ok bool) {
	_, an := a.(Null)
	_, bn := b.(Null)
	switch {
	case a == nil || an:
		if b == nil || bn {
			return 0, true
		}
		return -1, true
	case b == nil || bn:
		return 1, true
	}
	if as, aok := textOf(a); aok {
		if bs, bok := textOf(b); bok {
			return strings.Compare(as, bs), true
		}
		return 0, false
	}
	switch x := a.(type) {
	case Int64:
		switch y := b.(type) {
		case Int64:
			return cmp3(x < y, x > y), true
		case Float:
			return cmp3(float64(x) < float64(y), float64(x) > float64(y)), true
		case DecimalValue:
			return DecimalFromInt64(int64(x)).Cmp(y.D), true
		}
	case Float:
		switch y := b.(type) {
		case Float:
			return cmp3(x < y, x > y), true
		case Int64:
			return cmp3(float64(x) < float64(y), float64(x) > float64(y)), true
		}
	case DecimalValue:
		switch y := b.(type) {
		case DecimalValue:
			return x.D.Cmp(y.D), true
		case Int64:
			return x.D.Cmp(DecimalFromInt64(int64(y))), true
		}
	case Time:
		if y, ok := b.(Time); ok {
			return x.T.Compare(y.T), true
		}
	case Bool:
		if y, ok := b.(Bool); ok {
			return cmp3(!bool(x) && bool(y), bool(x) && !bool(y)), true
		}
	case Ref:
		if y, ok := b.(Ref); ok {
			return bytes.Compare(x.ID[:], y.ID[:]), true
		}
	}
	return 0, false
}

func cmp3(less, greater bool) int {
	switch {
	case less:
		return -1
	case greater:
		return 1
	}
	return 0
}
