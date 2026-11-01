package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
)

// CursorCodec signs cursors with HMAC-SHA256 (spec 6.5). The signature
// covers the cursor and the query binding, so clients can neither forge
// cursors nor replay them against another query.
type CursorCodec struct {
	key []byte
}

// NewCursorCodec returns a codec for key.
func NewCursorCodec(key []byte) *CursorCodec { return &CursorCodec{key: key} }

type wireValue struct {
	K string `json:"k"`
	V string `json:"v,omitempty"`
}

type wireCursor struct {
	V  []wireValue `json:"v,omitempty"`
	ID string      `json:"id"`
}

func (c *CursorCodec) sign(payload []byte, binding string) []byte {
	m := hmac.New(sha256.New, c.key)
	m.Write(payload)
	m.Write([]byte{0})
	m.Write([]byte(binding))
	return m.Sum(nil)
}

// Encode returns the opaque cursor string.
func (c *CursorCodec) Encode(cur *queryapi.Cursor, binding string) string {
	w := wireCursor{ID: cur.ID.String()}
	for _, v := range cur.Values {
		w.V = append(w.V, encodeValue(v))
	}
	payload, _ := json.Marshal(w)
	b64 := base64.RawURLEncoding
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(c.sign(payload, binding))
}

// Decode verifies and decodes a cursor.
func (c *CursorCodec) Decode(s, binding string) (*queryapi.Cursor, error) {
	bad := kernel.NewError(kernel.CodeInvalidQuery, "invalid cursor")
	parts := strings.Split(s, ".")
	if len(parts) != 2 {
		return nil, bad
	}
	b64 := base64.RawURLEncoding
	payload, err1 := b64.DecodeString(parts[0])
	sig, err2 := b64.DecodeString(parts[1])
	if err1 != nil || err2 != nil || !hmac.Equal(sig, c.sign(payload, binding)) {
		return nil, bad
	}
	var w wireCursor
	if err := json.Unmarshal(payload, &w); err != nil {
		return nil, bad
	}
	id, err := kernel.ParseID(w.ID)
	if err != nil {
		return nil, bad
	}
	out := &queryapi.Cursor{ID: id}
	for _, wv := range w.V {
		v, err := decodeValue(wv)
		if err != nil {
			return nil, bad
		}
		out.Values = append(out.Values, v)
	}
	return out, nil
}

func encodeValue(v kernel.Value) wireValue {
	switch x := v.(type) {
	case kernel.String:
		return wireValue{K: "s", V: string(x)}
	case kernel.Enum:
		return wireValue{K: "e", V: string(x)}
	case kernel.Int64:
		return wireValue{K: "i", V: strconv.FormatInt(int64(x), 10)}
	case kernel.Float:
		return wireValue{K: "f", V: strconv.FormatFloat(float64(x), 'g', -1, 64)}
	case kernel.DecimalValue:
		return wireValue{K: "d", V: x.D.String()}
	case kernel.Bool:
		return wireValue{K: "b", V: strconv.FormatBool(bool(x))}
	case kernel.Time:
		return wireValue{K: "t", V: x.T.Format(time.RFC3339Nano)}
	case kernel.Ref:
		return wireValue{K: "r", V: x.ID.String()}
	}
	return wireValue{K: "n"}
}

func decodeValue(w wireValue) (kernel.Value, error) {
	switch w.K {
	case "s":
		return kernel.String(w.V), nil
	case "e":
		return kernel.Enum(w.V), nil
	case "i":
		n, err := strconv.ParseInt(w.V, 10, 64)
		return kernel.Int64(n), err
	case "f":
		f, err := strconv.ParseFloat(w.V, 64)
		return kernel.Float(f), err
	case "d":
		d, err := kernel.ParseDecimal(w.V)
		return kernel.DecimalValue{D: d}, err
	case "b":
		b, err := strconv.ParseBool(w.V)
		return kernel.Bool(b), err
	case "t":
		t, err := time.Parse(time.RFC3339Nano, w.V)
		return kernel.NewTime(t), err
	case "r":
		id, err := kernel.ParseID(w.V)
		return kernel.Ref{ID: id}, err
	}
	return kernel.Null{}, nil
}
