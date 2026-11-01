package domain

import (
	"fmt"
	"strings"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// uiFuncs are the functions UI expressions may call. The browser
// interpreter (@organon/ui expr.ts) implements exactly these.
var uiFuncs = map[string]bool{
	"len": true, "lower": true, "upper": true, "trim": true, "string": true, "int": true, "float": true,
	"join": true, "split": true, "abs": true, "round": true, "floor": true, "ceil": true, "min": true, "max": true,
	"now": true, "can": true, "formatDate": true, "formatNumber": true,
}

// uiOps are the binary operators of the subset.
var uiOps = map[string]bool{
	"==": true, "!=": true, "<": true, "<=": true, ">": true, ">=": true,
	"&&": true, "||": true, "and": true, "or": true,
	"+": true, "-": true, "*": true, "/": true, "%": true,
	"in": true, "contains": true, "startsWith": true, "endsWith": true, "??": true,
}

// CanOps are the operation forms accepted by can(f, op).
func validCanOp(op string) bool {
	switch {
	case op == "update", op == "delete":
		return true
	case strings.HasPrefix(op, "action:"), strings.HasPrefix(op, "create:"):
		return len(op) > 7
	}
	return false
}

// checkSubset reports the first construct of src outside the UI
// expression subset (ADR 0016).
func checkSubset(src string) error {
	if err := scanSubset(src); err != nil {
		return err
	}
	tree, err := parser.Parse(src)
	if err != nil {
		return err
	}
	v := &subsetVisitor{}
	ast.Walk(&tree.Node, v)
	return v.err
}

// scanSubset rejects syntax the parser rewrites before the AST can show
// it: pipes become calls, and backtick strings become plain strings.
func scanSubset(src string) error {
	var quote byte
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '`':
			return fmt.Errorf("backtick strings are not available in UI expressions")
		case c == '|':
			if i+1 < len(src) && src[i+1] == '|' {
				i++
			} else {
				return fmt.Errorf("pipes are not available in UI expressions")
			}
		}
	}
	return nil
}

type subsetVisitor struct{ err error }

func (v *subsetVisitor) fail(format string, args ...any) {
	if v.err == nil {
		v.err = fmt.Errorf(format, args...)
	}
}

func (v *subsetVisitor) Visit(node *ast.Node) {
	switch n := (*node).(type) {
	case *ast.NilNode, *ast.IntegerNode, *ast.FloatNode, *ast.BoolNode, *ast.StringNode, *ast.ConstantNode,
		*ast.IdentifierNode, *ast.MemberNode, *ast.ChainNode, *ast.ConditionalNode, *ast.ArrayNode,
		*ast.MapNode, *ast.PairNode:
	case *ast.UnaryNode:
		if n.Operator != "!" && n.Operator != "not" && n.Operator != "-" && n.Operator != "+" {
			v.fail("operator %q is not available in UI expressions", n.Operator)
		}
	case *ast.BinaryNode:
		if !uiOps[n.Operator] {
			v.fail("operator %q is not available in UI expressions", n.Operator)
		}
	case *ast.CallNode:
		id, ok := n.Callee.(*ast.IdentifierNode)
		if !ok || !uiFuncs[id.Value] {
			v.fail("function %s is not available in UI expressions", calleeName(n.Callee))
			return
		}
		if id.Value == "can" && len(n.Arguments) == 2 {
			if s, ok := n.Arguments[1].(*ast.StringNode); ok && !validCanOp(s.Value) {
				v.fail("can: operation %q must be update, delete, action:<name>, or create:<type>", s.Value)
			}
		}
	case *ast.BuiltinNode:
		if !uiFuncs[n.Name] {
			v.fail("function %s is not available in UI expressions", n.Name)
		}
	default:
		v.fail("%T is not available in UI expressions (closures, slices, pipes, and variables are server-only)", n)
	}
}

func calleeName(n ast.Node) string {
	if id, ok := n.(*ast.IdentifierNode); ok {
		return id.Value
	}
	return n.String()
}

// splitTemplate splits a text prop into literal parts and the sources of
// its {{ expr }} interpolations.
func splitTemplate(s string) (exprs []string, err error) {
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			if strings.Contains(s, "}}") {
				return nil, fmt.Errorf("unmatched }} in text")
			}
			return exprs, nil
		}
		if strings.Contains(s[:i], "}}") {
			return nil, fmt.Errorf("unmatched }} in text")
		}
		j := strings.Index(s[i+2:], "}}")
		if j < 0 {
			return nil, fmt.Errorf("unclosed {{ in text")
		}
		e := strings.TrimSpace(s[i+2 : i+2+j])
		if e == "" {
			return nil, fmt.Errorf("empty {{ }} in text")
		}
		exprs = append(exprs, e)
		s = s[i+2+j+2:]
	}
}
