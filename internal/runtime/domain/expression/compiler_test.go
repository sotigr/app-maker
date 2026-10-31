package expression

import (
	"testing"
	"time"

	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
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
