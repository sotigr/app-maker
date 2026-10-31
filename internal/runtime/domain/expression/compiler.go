// Package expression implements the Runtime context's ExpressionCompiler
// (spec 10.8, 15.3) on top of github.com/expr-lang/expr.
package expression

import (
	"fmt"
	"reflect"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/types"
	"github.com/expr-lang/expr/vm"

	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
)

// MaxNodes bounds compiled program size (spec 10.8 limits).
const MaxNodes = 10_000

var (
	timeType     = reflect.TypeOf(time.Time{})
	durationType = reflect.TypeOf(time.Duration(0))
	decimalType  = reflect.TypeOf(kernel.Decimal{})
)

// builtinSigs are the static signatures of the framework built-ins.
var builtinSigs = map[string]types.Type{
	rt.FnNow:     types.TypeOf(func() time.Time { return time.Time{} }),
	rt.FnChanged: types.TypeOf(func(string) bool { return false }),
	rt.FnIsOwner: types.TypeOf(func(any) bool { return false }),
	rt.FnHasRole: types.TypeOf(func(any, string) bool { return false }),
	rt.FnInGroup: types.TypeOf(func(string) bool { return false }),
	rt.FnUUID:    types.TypeOf(func() string { return "" }),
}

// Compiler compiles expressions with expr. It is stateless and safe for
// concurrent use.
type Compiler struct{}

// NewCompiler returns a Compiler.
func NewCompiler() *Compiler { return &Compiler{} }

var _ rt.Compiler = (*Compiler)(nil)

// Compile type-checks source against env and returns a Program.
func (c *Compiler) Compile(source string, env rt.Env, want *rt.Type, nullable bool) (rt.Program, error) {
	m := types.Map{}
	for name, t := range env.Vars {
		m[name] = toExprType(t)
	}
	for _, fn := range env.Funcs {
		sig, ok := builtinSigs[fn]
		if !ok {
			return nil, fmt.Errorf("unknown built-in %q", fn)
		}
		m[fn] = sig
	}
	opts := []expr.Option{
		expr.Env(m),
		expr.MaxNodes(MaxNodes),
		expr.DisableBuiltin("now"), // replaced by the clock-backed built-in
	}
	opts = append(opts, decimalOptions()...)
	p, err := expr.Compile(source, opts...)
	if err != nil {
		return nil, &rt.CompileError{Source: source, Message: err.Error()}
	}
	nilSafeEquality(p)
	if want != nil {
		got := p.Node().Type()
		if p.Node().Nature().Nil {
			got = nil
		}
		if !assignable(got, want, nullable) {
			return nil, &rt.CompileError{Source: source,
				Message: fmt.Sprintf("expression %q has type %s, want %s", source, typeName(got), want)}
		}
	}
	return &program{src: source, p: p, want: want}, nil
}

// nilSafeEquality replaces expr's specialized string and int equality
// opcodes with the generic one. Typed environments make expr emit
// OpEqualString for two string-typed operands, which panics when an
// optional field is absent (nil at run time); "principal.id ==
// feature.assignee" must be false, not an error, for unassigned tasks.
func nilSafeEquality(p *vm.Program) {
	for i, op := range p.Bytecode {
		if op == vm.OpEqualString || op == vm.OpEqualInt {
			p.Bytecode[i] = vm.OpEqual
		}
	}
}

type program struct {
	src  string
	p    *vm.Program
	want *rt.Type
}

func (p *program) Source() string { return p.src }

func (p *program) Eval(vars map[string]any) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("expression %q: %v", p.src, r)
		}
	}()
	out, err = expr.Run(p.p, vars)
	if err != nil {
		return nil, fmt.Errorf("expression %q: %w", p.src, err)
	}
	return normalize(out, p.want), nil
}

// normalize converts expr's int results to int64 for Int-typed programs.
func normalize(v any, want *rt.Type) any {
	switch x := v.(type) {
	case int:
		if want != nil && want.Kind == rt.TFloat {
			return float64(x)
		}
		return int64(x)
	case int32:
		return int64(x)
	case float32:
		return float64(x)
	}
	return v
}

func toExprType(t *rt.Type) types.Type {
	if t == nil {
		return types.Any
	}
	switch t.Kind {
	case rt.TString:
		return types.String
	case rt.TInt:
		return types.Int64
	case rt.TFloat:
		return types.Float64
	case rt.TDecimal:
		return types.TypeOf(kernel.Decimal{})
	case rt.TBool:
		return types.Bool
	case rt.TTime:
		return types.TypeOf(time.Time{})
	case rt.TDuration:
		return types.TypeOf(time.Duration(0))
	case rt.TList:
		return types.Array(toExprType(t.Elem))
	case rt.TMap:
		m := types.Map{}
		for k, ft := range t.Fields {
			m[k] = toExprType(ft)
		}
		if t.Open {
			m[types.Extra] = types.Any
		}
		return m
	case rt.TNil:
		return types.Nil
	}
	return types.Any
}

// assignable reports whether a program whose static result type is got can
// produce values of type want. Unknown (interface) results are accepted and
// checked at evaluation time by the caller.
func assignable(got reflect.Type, want *rt.Type, nullable bool) bool {
	if got == nil { // the nil literal
		return nullable || want.Kind == rt.TAny
	}
	if got.Kind() == reflect.Interface {
		return true
	}
	switch want.Kind {
	case rt.TAny:
		return true
	case rt.TString:
		return got.Kind() == reflect.String
	case rt.TInt:
		return isInt(got)
	case rt.TFloat:
		return got.Kind() == reflect.Float64 || got.Kind() == reflect.Float32 || isInt(got)
	case rt.TDecimal:
		return got == decimalType || isInt(got)
	case rt.TBool:
		return got.Kind() == reflect.Bool
	case rt.TTime:
		return got == timeType
	case rt.TDuration:
		return got == durationType
	case rt.TList:
		return got.Kind() == reflect.Slice || got.Kind() == reflect.Array
	case rt.TMap:
		return got.Kind() == reflect.Map
	case rt.TNil:
		return nullable
	}
	return false
}

func isInt(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	}
	return false
}

func typeName(t reflect.Type) string {
	switch {
	case t == nil:
		return "nil"
	case t == timeType:
		return "time"
	case t == decimalType:
		return "decimal"
	case isInt(t):
		return "int"
	}
	return t.String()
}
