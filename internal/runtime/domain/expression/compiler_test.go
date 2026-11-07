package expression

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

func taskEnv() rt.Env {
	return rt.Env{
		Vars: map[string]*rt.Type{
			"feature": rt.MapOf(map[string]*rt.Type{
				"title":    rt.String,
				"status":   rt.String,
				"dueAt":    rt.Time,
				"estimate": rt.Decimal,
				"count":    rt.Int,
				"tags":     rt.ListOf(rt.String),
			}),
		},
		Funcs: []string{rt.FnNow},
	}
}

// Spec 4.3 / 10.8: the isLate computed field compiles and evaluates with
// a clock-backed now().
func TestComputedIsLate(t *testing.T) {
	c := NewCompiler()
	p, err := c.Compile("feature.dueAt != nil && feature.status != 'done' && now() > feature.dueAt", taskEnv(), rt.Bool, false)
	if err != nil {
		t.Fatal(err)
	}
	clock := kernel.NewFakeClock(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC))
	vars := map[string]any{
		"feature": map[string]any{"status": "todo", "dueAt": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		rt.FnNow:  clock.Now,
	}
	out, err := p.Eval(vars)
	if err != nil || out != true {
		t.Fatalf("got %v, %v", out, err)
	}
	vars["feature"] = map[string]any{"status": "todo", "dueAt": nil}
	if out, _ := p.Eval(vars); out != false {
		t.Fatalf("nil dueAt: got %v", out)
	}
}

// Spec 4.4 step 4: type errors are compile errors.
func TestTypeErrorsFailCompile(t *testing.T) {
	c := NewCompiler()
	bad := []struct {
		src  string
		want *rt.Type
	}{
		{"feature.titel == ''", rt.Bool}, // unknown field
		{"feature.title + 1", nil},       // mismatched types
		{"feature.title", rt.Bool},       // wrong result type
		{"feature.estimate + 1.5", nil},  // spec 10.8: decimal mixed with float
		{"isOwner(feature)", rt.Bool},    // built-in not in env
		{"feature.status ==", rt.Bool},   // syntax
		{"feature.count > 'a'", rt.Bool}, // compare int with string
	}
	for _, b := range bad {
		if _, err := c.Compile(b.src, taskEnv(), b.want, false); err == nil {
			t.Errorf("%q compiled, want error", b.src)
		}
	}
}

// Spec 10.8: Decimal arithmetic is exact.
func TestDecimalArithmetic(t *testing.T) {
	c := NewCompiler()
	p, err := c.Compile("feature.estimate * 3 + decimal('0.1')", taskEnv(), rt.Decimal, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Eval(map[string]any{"feature": map[string]any{"estimate": kernel.MustDecimal("0.1")}})
	if err != nil {
		t.Fatal(err)
	}
	if out.(kernel.Decimal).String() != "0.4" {
		t.Fatalf("got %v", out)
	}
	p, err = c.Compile("feature.estimate > 2", taskEnv(), rt.Bool, false)
	if err != nil {
		t.Fatal(err)
	}
	if out, _ := p.Eval(map[string]any{"feature": map[string]any{"estimate": kernel.MustDecimal("2.5")}}); out != true {
		t.Fatalf("2.5 > 2 = %v", out)
	}
}

func TestIntResultNormalizedToInt64(t *testing.T) {
	c := NewCompiler()
	p, err := c.Compile("feature.count + 1", taskEnv(), rt.Int, false)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := p.Eval(map[string]any{"feature": map[string]any{"count": int64(1)}})
	if out != int64(2) {
		t.Fatalf("got %#v", out)
	}
}

func TestNilResultOnlyWhenNullable(t *testing.T) {
	c := NewCompiler()
	if _, err := c.Compile("nil", taskEnv(), rt.String, false); err == nil {
		t.Fatal("nil result accepted for non-nullable")
	}
	if _, err := c.Compile("nil", taskEnv(), rt.String, true); err != nil {
		t.Fatal(err)
	}
}

// Optional fields are nil at run time; equality with them must be false,
// not a type assertion panic.
func TestEqualityWithAbsentOptionalField(t *testing.T) {
	c := NewCompiler()
	env := rt.Env{Vars: map[string]*rt.Type{
		"principal": rt.MapOf(map[string]*rt.Type{"id": rt.String}),
		"feature":   rt.MapOf(map[string]*rt.Type{"assignee": rt.String, "n": rt.Int}),
	}}
	for _, src := range []string{"principal.id == feature.assignee", "feature.n == 1"} {
		p, err := c.Compile(src, env, rt.Bool, false)
		if err != nil {
			t.Fatal(err)
		}
		out, err := p.Eval(map[string]any{"principal": map[string]any{"id": "x"}, "feature": map[string]any{}})
		if err != nil || out != false {
			t.Fatalf("%s: got %v, %v", src, out, err)
		}
	}
}

// Gap 33 (ADR 0023): decimal equality works for equal values, absent
// (nil) values, and wire strings, instead of failing (which made trigger
// guards fail open).
func TestGap33DecimalEquality(t *testing.T) {
	c := NewCompiler()
	env := rt.Env{Vars: map[string]*rt.Type{"a": rt.Decimal, "b": rt.Decimal}}
	for _, tc := range []struct {
		src  string
		a, b any
		want bool
	}{
		{"a == b", kernel.MustDecimal("2.50"), kernel.MustDecimal("2.5"), true},
		{"a != b", kernel.MustDecimal("2.5"), kernel.MustDecimal("2.5"), false},
		{"a == b", nil, kernel.MustDecimal("1"), false},
		{"a != b", nil, kernel.MustDecimal("1"), true},
		{"a == b", nil, nil, true},
		{"a == b", "3.10", kernel.MustDecimal("3.1"), true},
		{"a < b", "1", kernel.MustDecimal("2"), true},
	} {
		p, err := c.Compile(tc.src, env, rt.Bool, false)
		if err != nil {
			t.Fatal(err)
		}
		out, err := p.Eval(map[string]any{"a": tc.a, "b": tc.b})
		if err != nil || out != tc.want {
			t.Errorf("%s with a=%v b=%v: %v %v, want %v", tc.src, tc.a, tc.b, out, err, tc.want)
		}
	}
}

// Gap #52: decimals inside values of unknown type (node outputs, read
// features) use decimal semantics at run time; other values keep expr's.
func TestGap52DecimalsOfUnknownType(t *testing.T) {
	c := NewCompiler()
	env := rt.Env{Vars: map[string]*rt.Type{"n": nil, "d": rt.Decimal}}
	d := kernel.MustDecimal
	n := map[string]any{"a": d("2.50"), "b": d("4"), "s": "x", "i": 3, "nil": nil}
	for _, tc := range []struct {
		src  string
		want any
	}{
		{"n.a + n.b", d("6.50")},
		{"n.a * n.b - n.a", d("7.500")},
		{"n.a + d", d("3.50")},
		{"n.b / 2", d("2")},
		{"(d + d) * n.a", d("5.00")},
		{"n.a * (n.b + d)", d("12.50")},
		{"n.a < n.b", true},
		{"n.a == d('2.5')", true},
		{"n.a == '2.5'", true},
		{"n.a != n.nil", true},
		{"n.nil == nil", true},
		{"n.s + 'y'", "xy"},
		{"n.i + 1", 4},
		{"n.i > 2", true},
		{"n.s == 'x'", true},
	} {
		src := strings.ReplaceAll(tc.src, "d(", "decimal(")
		p, err := c.Compile(src, env, nil, false)
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		out, err := p.Eval(map[string]any{"n": n, "d": d("1")})
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if w, ok := tc.want.(kernel.Decimal); ok {
			if g, ok := out.(kernel.Decimal); !ok || g.Cmp(w) != 0 {
				t.Errorf("%s = %v, want %v", src, out, w)
			}
			continue
		}
		if fmt.Sprint(out) != fmt.Sprint(tc.want) {
			t.Errorf("%s = %v, want %v", src, out, tc.want)
		}
	}
}

// Gap #15: round(x, places), floor, ceil, and abs take decimals (exact,
// half away from zero) and keep the decimal type, so results mix with
// other decimals; floats keep expr's behaviour.
func TestGap15DecimalRounding(t *testing.T) {
	c := NewCompiler()
	env := rt.Env{Vars: map[string]*rt.Type{"d": rt.Decimal, "f": rt.Float, "n": nil}}
	vars := map[string]any{"d": kernel.MustDecimal("2.345"), "f": 2.5, "n": map[string]any{"x": kernel.MustDecimal("-1.005")}}
	for src, want := range map[string]string{
		"round(d, 2)":             "2.35",
		"round(d, 2) + d":         "4.695",
		"round(d * 3, 1)":         "7.0",
		"floor(d)":                "2",
		"ceil(d)":                 "3",
		"abs(n.x)":                "1.005",
		"round(n.x, 2)":           "-1.01",
		"round(n.x * 2, 2) + d":   "0.335",
		"round(f)":                "3",
		"round(-f)":               "-3",
		"round(2.345678, 3)":      "2.346",
		"floor(f) + ceil(f)":      "5",
		"abs(-2)":                 "2",
		"round('2.345', 2) + 'x'": "2.35x",
	} {
		p, err := c.Compile(src, env, nil, false)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		out, err := p.Eval(vars)
		if err != nil || fmt.Sprint(out) != want {
			t.Errorf("%s = %v (%v), want %s", src, out, err, want)
		}
	}
	for _, bad := range []string{"round(d, 1.5)", "floor(d, 2)", "round(true)", "round(d) + 1.5"} {
		if _, err := c.Compile(bad, env, nil, false); err == nil {
			t.Errorf("%s compiled, want error", bad)
		}
	}
	p, _ := c.Compile("round(d, 2)", env, rt.Decimal, false)
	if p == nil {
		t.Error("round(decimal, 2) is not a decimal")
	}
}

// Gap #18: date() takes a time (a date-time field, also one read in a
// flow) as well as text.
func TestGap18DateOfTime(t *testing.T) {
	c := NewCompiler()
	at := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	env := rt.Env{Vars: map[string]*rt.Type{"t": rt.Time, "n": nil}}
	for src, want := range map[string]string{
		"date(t)":                  at.String(),
		"date(n.at)":               at.String(),
		"date(n.at).Year()":        "2026",
		"date('2026-11-07').Day()": "7",
		"date(timezone('Europe/Athens'), t).Hour()": "12",
	} {
		p, err := c.Compile(src, env, nil, false)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		out, err := p.Eval(map[string]any{"t": at, "n": map[string]any{"at": at}})
		if err != nil || fmt.Sprint(out) != want {
			t.Errorf("%s = %v (%v), want %s", src, out, err, want)
		}
	}
}
