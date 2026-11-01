// Package api is the Runtime context's published contract for expression
// compilation and evaluation (spec 10.8). Other contexts describe the typed
// environment with Type values and never import expr directly.
package api

import (
	"sort"
	"strings"
)

// TypeKind is the kind of an expression type.
type TypeKind uint8

// Expression type kinds.
const (
	TAny TypeKind = iota
	TString
	TInt
	TFloat
	TDecimal
	TBool
	TTime
	TDuration
	TList
	TMap
	TNil
)

// Type describes the static type of an environment variable or result.
type Type struct {
	Kind   TypeKind
	Fields map[string]*Type // TMap: known keys
	Open   bool             // TMap: other keys allowed (typed any)
	Elem   *Type            // TList: element type
}

// Predefined scalar types.
var (
	Any      = &Type{Kind: TAny}
	String   = &Type{Kind: TString}
	Int      = &Type{Kind: TInt}
	Float    = &Type{Kind: TFloat}
	Decimal  = &Type{Kind: TDecimal}
	Bool     = &Type{Kind: TBool}
	Time     = &Type{Kind: TTime}
	Duration = &Type{Kind: TDuration}
	Nil      = &Type{Kind: TNil}
)

// ListOf returns a list type.
func ListOf(elem *Type) *Type { return &Type{Kind: TList, Elem: elem} }

// MapOf returns a strict map type with the given fields.
func MapOf(fields map[string]*Type) *Type { return &Type{Kind: TMap, Fields: fields} }

// OpenMap returns a map type accepting any keys with any values.
func OpenMap() *Type { return &Type{Kind: TMap, Open: true} }

func (t *Type) String() string {
	if t == nil {
		return "any"
	}
	switch t.Kind {
	case TList:
		return "[]" + t.Elem.String()
	case TMap:
		keys := make([]string, 0, len(t.Fields))
		for k := range t.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k + ": " + t.Fields[k].String())
		}
		if t.Open {
			b.WriteString(", ...")
		}
		b.WriteString("}")
		return b.String()
	}
	return [...]string{"any", "string", "int", "float", "decimal", "bool", "time", "duration", "list", "map", "nil"}[t.Kind]
}

// Framework built-in functions (spec 10.8). Their implementations are
// supplied per evaluation in the variables passed to Program.Eval, under
// the same names, so they can close over the request's principal and data.
const (
	FnNow     = "now"     // func() time.Time
	FnChanged = "changed" // func(path string) bool
	FnIsOwner = "isOwner" // func(f any) bool
	FnHasRole = "hasRole" // func(f any, role string) bool
	FnInGroup = "inGroup" // func(id string) bool
	FnUUID    = "uuid"    // func() string

	// UI built-ins (ADR 0016) are only type-checked on the server; the
	// browser renderer implements them.
	FnCan          = "can"          // func(f any, op string) bool
	FnFormatDate   = "formatDate"   // func(t any, style string) string
	FnFormatNumber = "formatNumber" // func(n any, digits int) string
)

// Env is the typed environment an expression is compiled against.
type Env struct {
	Vars map[string]*Type
	// Funcs lists the framework built-ins available in this environment.
	Funcs []string
}

// Program is a compiled, type-checked expression.
type Program interface {
	Source() string
	// Eval runs the program. vars holds the environment variables and the
	// implementations of the built-ins named in Env.Funcs.
	Eval(vars map[string]any) (any, error)
}

// Compiler compiles expressions against a typed environment. want, when
// non-nil, is the required result type; a nil result is allowed when
// nullable is true.
type Compiler interface {
	Compile(source string, env Env, want *Type, nullable bool) (Program, error)
}

// CompileError is a type or syntax error in an expression.
type CompileError struct {
	Source  string
	Message string
}

func (e *CompileError) Error() string { return e.Message }
