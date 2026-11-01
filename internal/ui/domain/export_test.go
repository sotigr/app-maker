package domain

import (
	rt "organon/internal/runtime/api"
	"organon/internal/runtime/domain/expression"
)

var CheckSubset = checkSubset

// EvalForTest evaluates src with untyped variables, as the browser does.
func EvalForTest(src string, env map[string]any) (any, error) {
	vars := map[string]*rt.Type{}
	for k := range env {
		vars[k] = rt.Any
	}
	p, err := expression.NewCompiler().Compile(src, rt.Env{Vars: vars}, nil, true)
	if err != nil {
		return nil, err
	}
	return p.Eval(env)
}
