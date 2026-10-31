package sqlite

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"app-maker/internal/kernel"
)

// TimeFormat is fixed-width UTC, so text order is chronological order.
const TimeFormat = "2006-01-02T15:04:05.000000000Z"

// encodeFields splits a document into the queryable JSON (data column) and
// the kind descriptors that make decoding lossless (kinds column). The
// repository stays schema-agnostic like every FeatureRepository.
func encodeFields(fields map[string]kernel.Value) (data, kinds []byte, err error) {
	d := make(map[string]any, len(fields))
	k := make(map[string]any, len(fields))
	for path, v := range fields {
		name := path[1:]
		d[name], k[name] = encodeValue(v)
	}
	if data, err = json.Marshal(d); err != nil {
		return nil, nil, err
	}
	kinds, err = json.Marshal(k)
	return data, kinds, err
}

// encodeValue returns the queryable JSON value and its kind descriptor:
// a string for scalars, {"d": exact} for decimals, {"o": {...}} for
// objects, {"l": [...]} for lists, {"r": target} for refs.
func encodeValue(v kernel.Value) (any, any) {
	switch x := v.(type) {
	case nil, kernel.Null:
		return nil, "n"
	case kernel.String:
		return string(x), "s"
	case kernel.Enum:
		return string(x), "e"
	case kernel.Int64:
		return json.Number(strconv.FormatInt(int64(x), 10)), "i"
	case kernel.Float:
		return float64(x), "f"
	case kernel.DecimalValue:
		f, _ := strconv.ParseFloat(x.D.String(), 64)
		return f, map[string]any{"d": x.D.String()}
	case kernel.Bool:
		return bool(x), "b"
	case kernel.Time:
		return x.T.UTC().Format(TimeFormat), "t"
	case kernel.Ref:
		return x.ID.String(), map[string]any{"r": x.Target}
	case kernel.Blob:
		return map[string]any{"id": x.ID, "size": x.Size, "contentType": x.ContentType}, "bl"
	case kernel.JSON:
		return json.RawMessage(x), "j"
	case kernel.Object:
		d := map[string]any{}
		k := map[string]any{}
		for _, key := range x.Keys() {
			val, _ := x.Get(key)
			d[key], k[key] = encodeValue(val)
		}
		return d, map[string]any{"o": k, "order": x.Keys()}
	case kernel.List:
		d := make([]any, len(x.Items))
		k := make([]any, len(x.Items))
		for i, it := range x.Items {
			d[i], k[i] = encodeValue(it)
		}
		return d, map[string]any{"l": k, "elem": x.Elem.String()}
	}
	return nil, "n"
}

func decodeFields(data, kinds []byte) (map[string]kernel.Value, error) {
	var d map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		return nil, err
	}
	var k map[string]any
	if err := json.Unmarshal(kinds, &k); err != nil {
		return nil, err
	}
	out := make(map[string]kernel.Value, len(d))
	for name, raw := range d {
		v, err := decodeValue(raw, k[name])
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", name, err)
		}
		out["/"+name] = v
	}
	return out, nil
}

func decodeValue(raw, kind any) (kernel.Value, error) {
	switch k := kind.(type) {
	case string:
		switch k {
		case "n":
			return kernel.Null{}, nil
		case "s":
			return kernel.String(raw.(string)), nil
		case "e":
			return kernel.Enum(raw.(string)), nil
		case "i":
			n, err := raw.(json.Number).Int64()
			return kernel.Int64(n), err
		case "f":
			f, err := raw.(json.Number).Float64()
			return kernel.Float(f), err
		case "b":
			return kernel.Bool(raw.(bool)), nil
		case "t":
			t, err := time.Parse(time.RFC3339Nano, raw.(string))
			return kernel.NewTime(t), err
		case "bl":
			m := raw.(map[string]any)
			size, _ := m["size"].(json.Number).Int64()
			id, _ := m["id"].(string)
			ct, _ := m["contentType"].(string)
			return kernel.Blob{ID: id, Size: size, ContentType: ct}, nil
		case "j":
			b, err := json.Marshal(raw)
			return kernel.JSON(b), err
		}
	case map[string]any:
		if exact, ok := k["d"].(string); ok {
			d, err := kernel.ParseDecimal(exact)
			return kernel.DecimalValue{D: d}, err
		}
		if target, ok := k["r"]; ok {
			id, err := kernel.ParseID(raw.(string))
			t, _ := target.(string)
			return kernel.Ref{ID: id, Target: t}, err
		}
		if ok, isObj := k["o"].(map[string]any); isObj {
			m := raw.(map[string]any)
			order, _ := k["order"].([]any)
			keys := make([]string, 0, len(order))
			vals := map[string]kernel.Value{}
			for _, o := range order {
				name := o.(string)
				v, err := decodeValue(m[name], ok[name])
				if err != nil {
					return nil, err
				}
				keys = append(keys, name)
				vals[name] = v
			}
			return kernel.NewObject(keys, vals), nil
		}
		if items, isList := k["l"].([]any); isList {
			l := raw.([]any)
			elem, _ := kernel.ParseKind(fmt.Sprint(k["elem"]))
			out := kernel.List{Elem: elem, Items: make([]kernel.Value, len(l))}
			for i := range l {
				v, err := decodeValue(l[i], items[i])
				if err != nil {
					return nil, err
				}
				out.Items[i] = v
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("unknown kind descriptor %v", kind)
}

// sqlValue converts a predicate value to a SQL parameter matching the
// queryable JSON encoding.
func sqlValue(v kernel.Value) any {
	switch x := v.(type) {
	case nil, kernel.Null:
		return nil
	case kernel.String:
		return string(x)
	case kernel.Enum:
		return string(x)
	case kernel.Int64:
		return int64(x)
	case kernel.Float:
		return float64(x)
	case kernel.DecimalValue:
		f, _ := strconv.ParseFloat(x.D.String(), 64)
		return f
	case kernel.Bool:
		if x {
			return 1
		}
		return 0
	case kernel.Time:
		return x.T.UTC().Format(TimeFormat)
	case kernel.Ref:
		return x.ID.String()
	}
	return nil
}
