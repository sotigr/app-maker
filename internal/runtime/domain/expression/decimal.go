package expression

import (
	"fmt"

	"github.com/expr-lang/expr"

	"app-maker/internal/kernel"
)

// Decimal arithmetic and comparison in expressions (spec 10.8: Decimal and
// Int64 have exact semantics; mixing Decimal with Float is a compile error
// because no Float overload exists).

func toDec(v any) (kernel.Decimal, error) {
	switch x := v.(type) {
	case kernel.Decimal:
		return x, nil
	case int:
		return kernel.DecimalFromInt64(int64(x)), nil
	case int64:
		return kernel.DecimalFromInt64(x), nil
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
		opts = append(opts,
			expr.Function(c.name, binDec(func(a, b kernel.Decimal) (any, error) { return c.ok(a.Cmp(b)), nil }), decSigs[bool]()...),
			expr.Operator(op, c.name))
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
