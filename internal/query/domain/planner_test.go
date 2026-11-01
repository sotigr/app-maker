package domain

import (
	"fmt"
	"strings"
	"testing"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
)

// Spec 7.2, 7.3 step 1: limits and malformed documents fail with invalid_query.
func TestParseLimits(t *testing.T) {
	p := NewPlanner([]byte("k"))
	leaves := make([]string, 21)
	for i := range leaves {
		leaves[i] = `{"field":"/a","op":"eq","value":1}`
	}
	in := make([]string, 101)
	for i := range in {
		in[i] = fmt.Sprint(i)
	}
	bad := []string{
		`{}`,
		`{"type":"t","bogus":1}`,
		`{"type":"t","where":{"field":"/a","op":"like","value":"x"}}`,
		`{"type":"t","where":{"field":"a","op":"eq","value":"x"}}`,
		`{"type":"t","where":{"and":[` + strings.Join(leaves, ",") + `]}}`,
		`{"type":"t","where":{"and":[{"or":[{"and":[{"not":{"field":"/a","op":"eq","value":1}}]}]}]}}`,
		`{"type":"t","where":{"field":"/a","op":"in","value":[` + strings.Join(in, ",") + `]}}`,
		`{"type":"t","where":{"field":"/a","op":"eq","value":1,"and":[]}}`,
		`{"type":"t","scope":{"childrenOf":"x","descendantsOf":"y"}}`,
		`{"type":"t","limit":501}`,
	}
	for _, b := range bad {
		if _, err := p.Parse([]byte(b)); kernel.CodeOf(err) != kernel.CodeInvalidQuery {
			t.Errorf("%s: got %v", b, err)
		}
	}
	if _, err := p.Parse([]byte(`{"type":"task","where":{"and":[{"field":"/status","op":"in","value":["a"]}]},"sort":[{"field":"/dueAt","dir":"desc"}]}`)); err != nil {
		t.Fatal(err)
	}
}

// Spec 6.5: cursors are signed and bound to their query.
func TestCursorSigning(t *testing.T) {
	c := NewCursorCodec([]byte("secret"))
	id := kernel.UUIDv7Generator{}.NewID()
	cur := &queryapi.Cursor{ID: id, Values: []kernel.Value{kernel.String("x"), kernel.Int64(7), kernel.Null{}}}
	s := c.Encode(cur, "q1")
	got, err := c.Decode(s, "q1")
	if err != nil || got.ID != id || len(got.Values) != 3 || got.Values[1] != kernel.Int64(7) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if _, err := c.Decode(s, "q2"); err == nil {
		t.Fatal("cursor replayed against another query")
	}
	if _, err := NewCursorCodec([]byte("other")).Decode(s, "q1"); err == nil {
		t.Fatal("cursor accepted with another key")
	}
	if _, err := c.Decode("x"+s[1:], "q1"); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}
