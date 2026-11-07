package expression

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/builtin"
	"github.com/expr-lang/expr/checker"
	"github.com/expr-lang/expr/conf"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm/runtime"

	"organon/internal/kernel"
)

// Decimal arithmetic and comparison in expressions (spec 10.8: Decimal and
// Int64 have exact semantics; mixing Decimal with Float is a compile error
// because no Float overload exists).

// toDec accepts the forms a decimal takes at run time: kernel.Decimal,
// integers, and the wire string or JSON number of a decimal field.
func toDec(v any) (kernel.Decimal, error) {
	switch x := v.(type) {
	case kernel.Decimal:
		return x, nil
	case int:
		return kernel.DecimalFromInt64(int64(x)), nil
	case int64:
		return kernel.DecimalFromInt64(x), nil
	case string:
		return kernel.ParseDecimal(x)
	case json.Number:
		return kernel.ParseDecimal(x.String())
	}
	return kernel.Decimal{}, fmt.Errorf("not a decimal: %T", v)
}

func binDec(f func(a, b kernel.Decimal) (any, error)) func(params ...any) (any, error) {
	return func(params ...any) (any, error) {
		a, err := toDec(params[0])
		if err != nil {
			return nil, err
		}
		b, err := toDec(params[1])
		if err != nil {
			return nil, err
		}
		return f(a, b)
	}
}

func decSigs[R any]() []any {
	return []any{
		new(func(kernel.Decimal, kernel.Decimal) R),
		new(func(kernel.Decimal, int) R),
		new(func(int, kernel.Decimal) R),
		new(func(kernel.Decimal, int64) R),
		new(func(int64, kernel.Decimal) R),
	}
}

func decimalOptions() []expr.Option {
	arith := map[string]func(a, b kernel.Decimal) (kernel.Decimal, error){
		"+": kernel.Decimal.Add, "-": kernel.Decimal.Sub, "*": kernel.Decimal.Mul, "/": kernel.Decimal.Quo,
	}
	names := map[string]string{"+": "decAdd", "-": "decSub", "*": "decMul", "/": "decQuo"}
	var opts []expr.Option
	for op, f := range arith {
		f := f
		name := names[op]
		opts = append(opts,
			expr.Function(name, binDec(func(a, b kernel.Decimal) (any, error) { return f(a, b) }), decSigs[kernel.Decimal]()...),
			expr.Operator(op, name))
	}
	cmps := map[string]struct {
		name string
		ok   func(int) bool
	}{
		"<":  {"decLt", func(c int) bool { return c < 0 }},
		"<=": {"decLte", func(c int) bool { return c <= 0 }},
		">":  {"decGt", func(c int) bool { return c > 0 }},
		">=": {"decGte", func(c int) bool { return c >= 0 }},
		"==": {"decEq", func(c int) bool { return c == 0 }},
		"!=": {"decNe", func(c int) bool { return c != 0 }},
	}
	for op, c := range cmps {
		c := c
		cmp := binDec(func(a, b kernel.Decimal) (any, error) { return c.ok(a.Cmp(b)), nil })
		fn := cmp
		if op == "==" || op == "!=" {
			// Equality with an absent (nil) value is false, never an error
			// (ADR 0023: guards must not fail on missing optional fields).
			fn = func(params ...any) (any, error) {
				if params[0] == nil || params[1] == nil {
					same := params[0] == nil && params[1] == nil
					return (op == "==") == same, nil
				}
				return cmp(params...)
			}
		}
		opts = append(opts, expr.Function(c.name, fn, decSigs[bool]()...), expr.Operator(op, c.name))
	}
	opts = append(opts, expr.Function("decimal", func(params ...any) (any, error) {
		switch x := params[0].(type) {
		case string:
			return kernel.ParseDecimal(x)
		default:
			return toDec(x)
		}
	}, new(func(string) kernel.Decimal), new(func(int) kernel.Decimal), new(func(int64) kernel.Decimal)))
	return opts
}

// Operands whose type is unknown at compile time (node outputs, read
// features, json fields) cannot use the overloads above: expr picks an
// overload by static type. dynamicOps rewrites such operators into calls
// that dispatch at run time: decimal semantics when either value is a
// kernel.Decimal, expr's own operator otherwise (gap #52).
var dynamicOps = map[string]struct {
	name string
	dec  func(a, b kernel.Decimal) (any, error)
	gen  func(a, b any) any
	cmp  bool
}{
	"+":  {"dynAdd", func(a, b kernel.Decimal) (any, error) { return a.Add(b) }, func(a, b any) any { return runtime.Add(a, b) }, false},
	"-":  {"dynSub", func(a, b kernel.Decimal) (any, error) { return a.Sub(b) }, func(a, b any) any { return runtime.Subtract(a, b) }, false},
	"*":  {"dynMul", func(a, b kernel.Decimal) (any, error) { return a.Mul(b) }, func(a, b any) any { return runtime.Multiply(a, b) }, false},
	"/":  {"dynQuo", func(a, b kernel.Decimal) (any, error) { return a.Quo(b) }, func(a, b any) any { return runtime.Divide(a, b) }, false},
	"<":  {"dynLt", func(a, b kernel.Decimal) (any, error) { return a.Cmp(b) < 0, nil }, func(a, b any) any { return runtime.Less(a, b) }, true},
	"<=": {"dynLte", func(a, b kernel.Decimal) (any, error) { return a.Cmp(b) <= 0, nil }, func(a, b any) any { return runtime.LessOrEqual(a, b) }, true},
	">":  {"dynGt", func(a, b kernel.Decimal) (any, error) { return a.Cmp(b) > 0, nil }, func(a, b any) any { return runtime.More(a, b) }, true},
	">=": {"dynGte", func(a, b kernel.Decimal) (any, error) { return a.Cmp(b) >= 0, nil }, func(a, b any) any { return runtime.MoreOrEqual(a, b) }, true},
	"==": {"dynEq", func(a, b kernel.Decimal) (any, error) { return a.Cmp(b) == 0, nil }, func(a, b any) any { return runtime.Equal(a, b) }, true},
	"!=": {"dynNe", func(a, b kernel.Decimal) (any, error) { return a.Cmp(b) != 0, nil }, func(a, b any) any { return !runtime.Equal(a, b) }, true},
}

func isDec(v any) bool { _, ok := v.(kernel.Decimal); return ok }

// dynamicFunctions registers the run-time dispatch functions.
func dynamicFunctions() []expr.Option {
	var opts []expr.Option
	for op, d := range dynamicOps {
		d := d
		fn := func(params ...any) (any, error) {
			a, b := params[0], params[1]
			if isDec(a) || isDec(b) {
				if a == nil || b == nil {
					if op == "==" || op == "!=" {
						return (op == "==") == (a == nil && b == nil), nil
					}
				} else if x, err := toDec(a); err == nil {
					if y, err := toDec(b); err == nil {
						return d.dec(x, y)
					}
				}
				if op == "==" || op == "!=" {
					return op == "!=", nil
				}
			}
			return d.gen(a, b), nil
		}
		if d.cmp {
			opts = append(opts, expr.Function(d.name, fn, new(func(any, any) bool)))
		} else {
			opts = append(opts, expr.Function(d.name, fn, new(func(any, any) any)))
		}
	}
	return opts
}

// dynamicPatcher runs before operator overloading, so statically typed
// decimal operands still take the overloads. base holds the options
// without patchers: each operand is checked alone before an operator is
// rewritten, because expr does not report a type error inside an operand
// once its operator has been patched (an unknown field would compile).
type dynamicPatcher struct{ base []expr.Option }

func (p dynamicPatcher) operandOK(n ast.Node) bool {
	cfg := conf.CreateNew()
	for _, o := range p.base {
		o(cfg)
	}
	// PatchAndCheck applies the decimal overloads inside the operand in
	// place, as the compile would do anyway.
	tree := &parser.Tree{Node: n}
	_, err := new(checker.Checker).PatchAndCheck(tree, cfg)
	return err == nil
}

func unknownType(t reflect.Type) bool { return t == nil || t.Kind() == reflect.Interface }

func (p dynamicPatcher) Visit(node *ast.Node) {
	b, ok := (*node).(*ast.BinaryNode)
	if !ok {
		return
	}
	d, ok := dynamicOps[b.Operator]
	if !ok {
		return
	}
	l, r := b.Left.Type(), b.Right.Type()
	if !unknownType(l) && !unknownType(r) {
		return
	}
	// Leave nil literals and two unknowns compared for equality with nil
	// to expr: "x == nil" keeps its meaning.
	if _, isNil := b.Left.(*ast.NilNode); isNil {
		return
	}
	if _, isNil := b.Right.(*ast.NilNode); isNil {
		return
	}
	if !p.operandOK(b.Left) || !p.operandOK(b.Right) {
		return
	}
	ast.Patch(node, &ast.CallNode{Callee: &ast.IdentifierNode{Value: d.name}, Arguments: []ast.Node{b.Left, b.Right}})
}

// numericFunctions replaces expr's round, floor, ceil, and abs with
// versions that also take decimals (exact) and, for round, a number of
// decimal places: round(x, 2). Floats round half away from zero, like
// decimals (gap #15). A Validate hook types them like expr's built-ins:
// the result follows the argument, and is unknown for unknown arguments.
func numericFunctions() []expr.Option {
	floatT := reflect.TypeOf(float64(0))
	anyT := reflect.TypeOf((*any)(nil)).Elem()
	validate := func(name string, maxArgs int, keep bool) func([]reflect.Type) (reflect.Type, error) {
		return func(args []reflect.Type) (reflect.Type, error) {
			if len(args) < 1 || len(args) > maxArgs {
				return anyT, fmt.Errorf("%s takes %d argument(s), got %d", name, maxArgs, len(args))
			}
			if len(args) == 2 {
				switch args[1].Kind() {
				case reflect.Int, reflect.Int64, reflect.Int32, reflect.Interface:
				default:
					return anyT, fmt.Errorf("%s: the number of places must be an integer, not %s", name, args[1])
				}
			}
			switch t := args[0]; {
			case t == decimalType:
				return decimalType, nil
			case t.Kind() == reflect.String:
				// Decimal text (decimal fields in UI expressions).
				return t, nil
			case t.Kind() == reflect.Interface:
				return anyT, nil
			case t.Kind() >= reflect.Int && t.Kind() <= reflect.Float64:
				if keep {
					return t, nil
				}
				return floatT, nil
			}
			return anyT, fmt.Errorf("invalid argument for %s (type %s)", name, args[0])
		}
	}
	places := func(params []any) (int32, error) {
		if len(params) < 2 {
			return 0, nil
		}
		switch n := params[1].(type) {
		case int:
			return int32(n), nil
		case int64:
			return int32(n), nil
		}
		return 0, fmt.Errorf("round: the number of places must be an integer")
	}
	toFloat := func(name string, v any) (float64, error) {
		switch x := v.(type) {
		case float64:
			return x, nil
		case float32:
			return float64(x), nil
		case int:
			return float64(x), nil
		case int64:
			return float64(x), nil
		case int32:
			return float64(x), nil
		}
		return 0, fmt.Errorf("%s: not a number: %T", name, v)
	}
	def := func(name string, maxArgs int, keep bool, dec func(kernel.Decimal, int32) (any, error), num func(v any, places int32) (any, error)) expr.Option {
		return func(c *conf.Config) {
			c.Functions[name] = &builtin.Function{
				Name:     name,
				Validate: validate(name, maxArgs, keep),
				Func: func(params ...any) (any, error) {
					n, err := places(params)
					if err != nil {
						return nil, err
					}
					switch x := params[0].(type) {
					case nil:
						return nil, nil
					case kernel.Decimal:
						return dec(x, n)
					case string, json.Number:
						d, err := toDec(x)
						if err != nil {
							return nil, fmt.Errorf("%s: %w", name, err)
						}
						r, err := dec(d, n)
						if err != nil {
							return nil, err
						}
						if _, ok := x.(string); ok {
							return fmt.Sprint(r), nil // text in, text out
						}
						return r, nil
					}
					return num(params[0], n)
				},
			}
		}
	}
	return []expr.Option{
		def("round", 2, false, func(d kernel.Decimal, p int32) (any, error) { return d.Round(p) },
			func(v any, p int32) (any, error) {
				f, err := toFloat("round", v)
				if err != nil {
					return nil, err
				}
				scale := math.Pow(10, float64(p))
				return math.Copysign(math.Round(math.Abs(f)*scale)/scale, f), nil
			}),
		def("floor", 1, false, func(d kernel.Decimal, _ int32) (any, error) { return d.Floor() },
			func(v any, _ int32) (any, error) { f, err := toFloat("floor", v); return math.Floor(f), err }),
		def("ceil", 1, false, func(d kernel.Decimal, _ int32) (any, error) { return d.Ceil() },
			func(v any, _ int32) (any, error) { f, err := toFloat("ceil", v); return math.Ceil(f), err }),
		def("abs", 1, true, func(d kernel.Decimal, _ int32) (any, error) { return d.Abs(), nil },
			func(v any, _ int32) (any, error) {
				switch x := v.(type) {
				case int:
					if x < 0 {
						return -x, nil
					}
					return x, nil
				case int64:
					if x < 0 {
						return -x, nil
					}
					return x, nil
				}
				f, err := toFloat("abs", v)
				return math.Abs(f), err
			}),
	}
}

// dateFunction wraps expr's date(): a value that already is a time (a
// date-time field, also one read inside a flow) is returned as it is, in
// the given time zone if any; text is parsed as before (gap #18).
func dateFunction() expr.Option {
	var orig *builtin.Function
	for _, f := range builtin.Builtins {
		if f.Name == "date" {
			orig = f
		}
	}
	return func(c *conf.Config) {
		if orig == nil {
			return
		}
		c.Functions["date"] = &builtin.Function{
			Name:     "date",
			Validate: orig.Validate,
			Func: func(args ...any) (any, error) {
				tz, _ := args[0].(*time.Location)
				rest := args
				if tz != nil {
					rest = args[1:]
				}
				if len(rest) > 0 {
					if t, ok := rest[0].(time.Time); ok {
						if tz != nil {
							return t.In(tz), nil
						}
						return t, nil
					}
					if _, ok := rest[0].(string); !ok {
						return nil, fmt.Errorf("date: want a time or text, got %T", rest[0])
					}
				}
				return orig.Func(args...)
			},
		}
	}
}
