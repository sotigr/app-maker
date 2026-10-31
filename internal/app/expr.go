package app

import (
	rt "app-maker/internal/runtime/api"
	"app-maker/internal/runtime/domain/expression"
)

func exprCompiler() rt.Compiler { return expression.NewCompiler() }
