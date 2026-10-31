package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
)

// ValueType is the compiled structural type of a field value. It converts
// between wire JSON, Go-native expression values, and kernel.Value.
type ValueType struct {
	Kind     kernel.Kind
	Nullable bool
	// NumberWire is true for Int64 fields declared with "type": "integer";
	// their wire format is a JSON number (ADR 0005). Otherwise Int64 is a
	// string on the wire (spec 3.2).
	NumberWire bool
	Enum       []string   // Enum kind: allowed values
	Elem       *ValueType // List kind
	Props      []PropType // Object kind, ordered
	Union      *UnionType // Object kind declared as a discriminated oneOf
	RefTarget  TypeID     // Ref kind
	Format     string     // asserted format of String fields
}

// PropType is a property of an Object value type.
type PropType struct {
	Name     string
	Type     *ValueType
	Required bool
}

// UnionType is a discriminated union (spec 4.1 restricted oneOf).
type UnionType struct {
	Tag      string
	Variants map[string]*ValueType
}

// Prop returns the property named name.
func (t *ValueType) Prop(name string) (*PropType, bool) {
	for i := range t.Props {
		if t.Props[i].Name == name {
			return &t.Props[i], true
		}
	}
	return nil, false
}

// Coerce converts a wire-JSON or Go-native value into a kernel.Value of
// this type. Accepted inputs: nil, string, bool, json.Number, float64,
// int, int64, kernel.Decimal, time.Time, map[string]any, []any, and
// kernel.Value. The path is used in errors.
func (t *ValueType) Coerce(path string, x any) (kernel.Value, error) {
	if v, ok := x.(kernel.Value); ok {
		x = kernel.Native(v)
	}
	if x == nil {
		if t.Nullable {
			return kernel.Null{}, nil
		}
		return nil, fieldErr(path, "type", "must not be null")
	}
	switch t.Kind {
	case kernel.KindString:
		s, ok := x.(string)
		if !ok {
			return nil, typeErr(path, "string", x)
		}
		return kernel.String(s), nil
	case kernel.KindEnum:
		s, ok := x.(string)
		if !ok {
			return nil, typeErr(path, "string", x)
		}
		if !slices.Contains(t.Enum, s) {
			return nil, fieldErr(path, "enum", fmt.Sprintf("must be one of %v", t.Enum))
		}
		return kernel.Enum(s), nil
	case kernel.KindInt64:
		i, err := toInt64(x)
		if err != nil {
			return nil, fieldErr(path, "type", err.Error())
		}
		return kernel.Int64(i), nil
	case kernel.KindFloat:
		f, err := toFloat(x)
		if err != nil {
			return nil, fieldErr(path, "type", err.Error())
		}
		fv, ok := kernel.NewFloat(f)
		if !ok {
			return nil, fieldErr(path, "type", "NaN and Infinity are not allowed")
		}
		return fv, nil
	case kernel.KindDecimal:
		switch v := x.(type) {
		case kernel.Decimal:
			return kernel.DecimalValue{D: v}, nil
		case string:
			d, err := kernel.ParseDecimal(v)
			if err != nil {
				return nil, fieldErr(path, "type", err.Error())
			}
			return kernel.DecimalValue{D: d}, nil
		case json.Number:
			d, err := kernel.ParseDecimal(v.String())
			if err != nil {
				return nil, fieldErr(path, "type", err.Error())
			}
			return kernel.DecimalValue{D: d}, nil
		case int, int64:
			i, _ := toInt64(v)
			return kernel.DecimalValue{D: kernel.DecimalFromInt64(i)}, nil
		}
		return nil, typeErr(path, "decimal string", x)
	case kernel.KindBool:
		b, ok := x.(bool)
		if !ok {
			return nil, typeErr(path, "boolean", x)
		}
		return kernel.Bool(b), nil
	case kernel.KindTime:
		switch v := x.(type) {
		case time.Time:
			return kernel.NewTime(v), nil
		case string:
			tm, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return nil, fieldErr(path, "format", "must be an RFC 3339 date-time")
			}
			return kernel.NewTime(tm), nil
		}
		return nil, typeErr(path, "date-time string", x)
	case kernel.KindRef:
		s, ok := x.(string)
		if !ok {
			return nil, typeErr(path, "id string", x)
		}
		id, err := kernel.ParseID(s)
		if err != nil {
			return nil, fieldErr(path, "format", "must be a feature id")
		}
		return kernel.Ref{ID: id, Target: string(t.RefTarget)}, nil
	case kernel.KindBlob:
		m, ok := x.(map[string]any)
		if !ok {
			return nil, typeErr(path, "blob object", x)
		}
		id, _ := m["id"].(string)
		ct, _ := m["contentType"].(string)
		size, err := toInt64(m["size"])
		if id == "" || err != nil {
			return nil, fieldErr(path, "type", "blob needs id, size, contentType")
		}
		return kernel.Blob{ID: id, Size: size, ContentType: ct}, nil
	case kernel.KindJSON:
		b, err := json.Marshal(x)
		if err != nil {
			return nil, fieldErr(path, "type", "not representable as JSON")
		}
		return kernel.JSON(b), nil
	case kernel.KindList:
		l, ok := x.([]any)
		if !ok {
			return nil, typeErr(path, "array", x)
		}
		items := make([]kernel.Value, len(l))
		for i, it := range l {
			v, err := t.Elem.Coerce(fmt.Sprintf("%s/%d", path, i), it)
			if err != nil {
				return nil, err
			}
			items[i] = v
		}
		return kernel.List{Elem: t.Elem.Kind, Items: items}, nil
	case kernel.KindObject:
		m, ok := x.(map[string]any)
		if !ok {
			return nil, typeErr(path, "object", x)
		}
		return t.coerceObject(path, m)
	}
	return nil, fieldErr(path, "type", "unsupported kind "+t.Kind.String())
}

func (t *ValueType) coerceObject(path string, m map[string]any) (kernel.Value, error) {
	props := t.Props
	if t.Union != nil {
		tag, _ := m[t.Union.Tag].(string)
		variant, ok := t.Union.Variants[tag]
		if !ok {
			return nil, fieldErr(path+"/"+t.Union.Tag, "const", "unknown variant")
		}
		props = variant.Props
	}
	keys := make([]string, 0, len(props))
	vals := make(map[string]kernel.Value, len(props))
	for _, p := range props {
		raw, present := m[p.Name]
		if !present {
			continue
		}
		v, err := p.Type.Coerce(path+"/"+p.Name, raw)
		if err != nil {
			return nil, err
		}
		keys = append(keys, p.Name)
		vals[p.Name] = v
	}
	for k := range m {
		if _, ok := vals[k]; !ok {
			if _, known := findProp(props, k); !known {
				return nil, fieldErr(path+"/"+k, "additionalProperties", "unknown property")
			}
		}
	}
	return kernel.NewObject(keys, vals), nil
}

func findProp(props []PropType, name string) (*PropType, bool) {
	for i := range props {
		if props[i].Name == name {
			return &props[i], true
		}
	}
	return nil, false
}

// Wire converts a kernel.Value of this type to its wire JSON form
// (spec 3.2). Numbers are json.Number so that validators see exact values.
func (t *ValueType) Wire(v kernel.Value) any {
	switch x := v.(type) {
	case nil, kernel.Null:
		return nil
	case kernel.String:
		return string(x)
	case kernel.Enum:
		return string(x)
	case kernel.Int64:
		if t != nil && t.NumberWire {
			return json.Number(strconv.FormatInt(int64(x), 10))
		}
		return strconv.FormatInt(int64(x), 10)
	case kernel.Float:
		return json.Number(strconv.FormatFloat(float64(x), 'g', -1, 64))
	case kernel.DecimalValue:
		return x.D.String()
	case kernel.Bool:
		return bool(x)
	case kernel.Time:
		return x.T.UTC().Format(time.RFC3339Nano)
	case kernel.Ref:
		return x.ID.String()
	case kernel.Blob:
		return map[string]any{"id": x.ID, "size": json.Number(strconv.FormatInt(x.Size, 10)), "contentType": x.ContentType}
	case kernel.JSON:
		dec := json.NewDecoder(bytes.NewReader(x))
		dec.UseNumber()
		var out any
		if err := dec.Decode(&out); err != nil {
			return nil
		}
		return out
	case kernel.List:
		var elem *ValueType
		if t != nil {
			elem = t.Elem
		}
		out := make([]any, len(x.Items))
		for i, it := range x.Items {
			out[i] = elem.Wire(it)
		}
		return out
	case kernel.Object:
		props := []PropType(nil)
		if t != nil {
			props = t.Props
			if t.Union != nil {
				if tag, ok := x.Get(t.Union.Tag); ok {
					if s, ok := kernel.Native(tag).(string); ok {
						if vt, ok := t.Union.Variants[s]; ok {
							props = vt.Props
						}
					}
				}
			}
		}
		out := make(map[string]any, x.Len())
		for _, k := range x.Keys() {
			val, _ := x.Get(k)
			var pt *ValueType
			if p, ok := findProp(props, k); ok {
				pt = p.Type
			}
			out[k] = pt.Wire(val)
		}
		return out
	}
	return nil
}

// ExprType returns the expression type of values of this type.
func (t *ValueType) ExprType() *rt.Type {
	switch t.Kind {
	case kernel.KindString, kernel.KindEnum, kernel.KindRef:
		return rt.String
	case kernel.KindInt64:
		return rt.Int
	case kernel.KindFloat:
		return rt.Float
	case kernel.KindDecimal:
		return rt.Decimal
	case kernel.KindBool:
		return rt.Bool
	case kernel.KindTime:
		return rt.Time
	case kernel.KindBlob:
		return rt.MapOf(map[string]*rt.Type{"id": rt.String, "size": rt.Int, "contentType": rt.String})
	case kernel.KindList:
		return rt.ListOf(t.Elem.ExprType())
	case kernel.KindObject:
		if t.Union != nil {
			return rt.OpenMap()
		}
		fields := make(map[string]*rt.Type, len(t.Props))
		for _, p := range t.Props {
			fields[p.Name] = p.Type.ExprType()
		}
		return rt.MapOf(fields)
	}
	return rt.Any
}

func toInt64(x any) (int64, error) {
	switch v := x.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case json.Number:
		return strconv.ParseInt(v.String(), 10, 64)
	case string:
		return strconv.ParseInt(v, 10, 64)
	case float64:
		if v != math.Trunc(v) || math.Abs(v) > 1<<53 {
			return 0, fmt.Errorf("not an exact integer")
		}
		return int64(v), nil
	}
	return 0, fmt.Errorf("must be an integer")
}

func toFloat(x any) (float64, error) {
	switch v := x.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	}
	return 0, fmt.Errorf("must be a number")
}

func fieldErr(path, code, msg string) error {
	return kernel.NewValidationError(kernel.FieldError{Path: path, Code: code, Message: msg})
}

func typeErr(path, want string, got any) error {
	return fieldErr(path, "type", fmt.Sprintf("must be %s, got %T", want, got))
}
