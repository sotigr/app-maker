package domain

import (
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// ScopeExpr is a read policy translated into a principal-independent form
// that the Authorizer resolves into a query predicate (spec 8.5, 8.6).
type ScopeExpr interface{ isScope() }

// Scope expression nodes.
type (
	// ScConst is a constant.
	ScConst struct{ Value bool }
	// ScOwner is isOwner(feature).
	ScOwner struct{}
	// ScRole is hasRole(feature, Role).
	ScRole struct{ Role string }
	// ScAnd / ScOr combine terms.
	ScAnd struct{ Terms []ScopeExpr }
	ScOr  struct{ Terms []ScopeExpr }
	// ScPrincipal is a condition on the principal only, evaluated once per
	// query (e.g. principal.kind != 'anonymous').
	ScPrincipal struct{ Source string }
	// ScField compares a feature field with a literal or the principal ID.
	ScField struct {
		Path        string
		Negate      bool // !=
		Literal     any  // string, int, bool, or nil
		PrincipalID bool // compares with principal.id
	}
)

func (ScConst) isScope()     {}
func (ScOwner) isScope()     {}
func (ScRole) isScope()      {}
func (ScAnd) isScope()       {}
func (ScOr) isScope()        {}
func (ScPrincipal) isScope() {}
func (ScField) isScope()     {}

// TranslateRead turns a read policy into a ScopeExpr. ok is false when the
// policy uses anything beyond isOwner, hasRole, comparisons of feature
// fields with literals or principal.id, principal-only conditions, and the
// boolean operators.
func TranslateRead(src string) (ScopeExpr, bool) {
	tree, err := parser.Parse(src)
	if err != nil {
		return nil, false
	}
	return translate(tree.Node)
}

func translate(n ast.Node) (ScopeExpr, bool) {
	if !mentionsFeature(n) {
		return ScPrincipal{Source: n.String()}, true
	}
	switch x := n.(type) {
	case *ast.BinaryNode:
		switch x.Operator {
		case "||", "or", "&&", "and":
			l, ok1 := translate(x.Left)
			r, ok2 := translate(x.Right)
			if !ok1 || !ok2 {
				return nil, false
			}
			if x.Operator == "||" || x.Operator == "or" {
				return ScOr{Terms: []ScopeExpr{l, r}}, true
			}
			return ScAnd{Terms: []ScopeExpr{l, r}}, true
		case "==", "!=":
			field, other := featureField(x.Left), x.Right
			if field == "" {
				field, other = featureField(x.Right), x.Left
			}
			if field == "" {
				return nil, false
			}
			sf := ScField{Path: "/" + field, Negate: x.Operator == "!="}
			switch o := other.(type) {
			case *ast.StringNode:
				sf.Literal = o.Value
			case *ast.IntegerNode:
				sf.Literal = int64(o.Value)
			case *ast.BoolNode:
				sf.Literal = o.Value
			case *ast.NilNode:
				sf.Literal = nil
			case *ast.MemberNode:
				if isPrincipalID(o) {
					sf.PrincipalID = true
				} else {
					return nil, false
				}
			default:
				return nil, false
			}
			return sf, true
		}
	case *ast.CallNode:
		id, ok := x.Callee.(*ast.IdentifierNode)
		if !ok || len(x.Arguments) == 0 || !isFeature(x.Arguments[0]) {
			return nil, false
		}
		switch {
		case id.Value == "isOwner" && len(x.Arguments) == 1:
			return ScOwner{}, true
		case id.Value == "hasRole" && len(x.Arguments) == 2:
			if s, ok := x.Arguments[1].(*ast.StringNode); ok {
				return ScRole{Role: s.Value}, true
			}
		}
	}
	return nil, false
}

func isFeature(n ast.Node) bool {
	id, ok := n.(*ast.IdentifierNode)
	return ok && id.Value == "feature"
}

// featureField returns "x" for feature.x, else "".
func featureField(n ast.Node) string {
	m, ok := n.(*ast.MemberNode)
	if !ok || !isFeature(m.Node) {
		return ""
	}
	if s, ok := m.Property.(*ast.StringNode); ok {
		return s.Value
	}
	return ""
}

func isPrincipalID(m *ast.MemberNode) bool {
	id, ok := m.Node.(*ast.IdentifierNode)
	s, ok2 := m.Property.(*ast.StringNode)
	return ok && ok2 && id.Value == "principal" && s.Value == "id"
}

type featureFinder struct{ found bool }

func (f *featureFinder) Visit(n *ast.Node) {
	if id, ok := (*n).(*ast.IdentifierNode); ok && (id.Value == "feature" || id.Value == "patch" || id.Value == "parent") {
		f.found = true
	}
	if c, ok := (*n).(*ast.CallNode); ok {
		if id, ok := c.Callee.(*ast.IdentifierNode); ok && (id.Value == "isOwner" || id.Value == "hasRole") {
			f.found = true
		}
	}
}

func mentionsFeature(n ast.Node) bool {
	f := &featureFinder{}
	ast.Walk(&n, f)
	return f.found
}
