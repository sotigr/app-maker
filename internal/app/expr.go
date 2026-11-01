package app

import (
	rt "organon/internal/runtime/api"
	"organon/internal/runtime/domain/expression"
)

func exprCompiler() rt.Compiler { return expression.NewCompiler() }
