package sqlite

import (
	"fmt"
	"regexp"
	"strings"

	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
)

var safeName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// builder turns predicates into SQL with bound parameters. Field paths are
// inlined only when every segment is a plain identifier, so the text can
// match expression indexes; anything else is bound.
type builder struct {
	args []any
}

func (b *builder) arg(v any) string {
	b.args = append(b.args, v)
	return "?"
}

// fieldExpr returns json_extract(f.data, '$."a"."b"').
func (b *builder) fieldExpr(path string) string {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	safe := true
	for _, s := range segs {
		if !safeName.MatchString(s) {
			safe = false
		}
	}
	if safe {
		return fmt.Sprintf(`json_extract(f.data, '$.%s')`, strings.Join(quoteAll(segs), "."))
	}
	return fmt.Sprintf(`json_extract(f.data, %s)`, b.arg("$."+strings.Join(quoteAll(segs), ".")))
}

func quoteAll(segs []string) []string {
	out := make([]string, len(segs))
	for i, s := range segs {
		out[i] = `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return out
}

func isNull(v kernel.Value) bool {
	_, n := v.(kernel.Null)
	return v == nil || n
}

// pred compiles a predicate. The semantics match queryapi.Match: absent and
// null fields compare like the memory repository.
func (b *builder) pred(p queryapi.Predicate) (string, error) {
	switch x := p.(type) {
	case nil, queryapi.True:
		return "1=1", nil
	case queryapi.False:
		return "1=0", nil
	case queryapi.And, queryapi.Or:
		var terms []queryapi.Predicate
		op := " AND "
		if a, ok := x.(queryapi.And); ok {
			terms = a.Terms
		} else {
			terms = x.(queryapi.Or).Terms
			op = " OR "
		}
		if len(terms) == 0 {
			if op == " AND " {
				return "1=1", nil
			}
			return "1=0", nil
		}
		parts := make([]string, len(terms))
		for i, t := range terms {
			s, err := b.pred(t)
			if err != nil {
				return "", err
			}
			parts[i] = "(" + s + ")"
		}
		return strings.Join(parts, op), nil
	case queryapi.Not:
		s, err := b.pred(x.Term)
		if err != nil {
			return "", err
		}
		return "NOT (" + s + ")", nil
	case queryapi.InScope:
		if len(x.IDs) == 0 {
			return "1=0", nil
		}
		ph := make([]string, len(x.IDs))
		for i, id := range x.IDs {
			ph[i] = b.arg(id.String())
		}
		in := "(" + strings.Join(ph, ",") + ")"
		switch x.Field {
		case queryapi.ScopeOwner:
			return "f.owner_id IN " + in, nil
		case queryapi.ScopeSelf:
			return "f.id IN " + in, nil
		case queryapi.ScopeAncestor:
			ph2 := make([]string, len(x.IDs))
			for i, id := range x.IDs {
				ph2[i] = b.arg(id.String())
			}
			return "f.id IN " + in + " OR EXISTS (SELECT 1 FROM content_feature_ancestors a WHERE a.feature_id = f.id AND a.ancestor_id IN (" +
				strings.Join(ph2, ",") + "))", nil
		}
		return "", fmt.Errorf("unknown scope field %q", x.Field)
	case queryapi.Compare:
		return b.compare(x)
	}
	return "", fmt.Errorf("unsupported predicate %T", p)
}

func (b *builder) compare(c queryapi.Compare) (string, error) {
	e := b.fieldExpr(c.Path)
	switch c.Op {
	case queryapi.OpExists:
		return e + " IS NOT NULL", nil
	case queryapi.OpEq:
		if isNull(c.Value) {
			return e + " IS NULL", nil
		}
		return e + " = " + b.arg(sqlValue(c.Value)), nil
	case queryapi.OpNe:
		if isNull(c.Value) {
			return e + " IS NOT NULL", nil
		}
		return "(" + e + " IS NULL OR " + e + " != " + b.arg(sqlValue(c.Value)) + ")", nil
	case queryapi.OpIn, queryapi.OpNin:
		l, _ := c.Value.(kernel.List)
		if len(l.Items) == 0 {
			if c.Op == queryapi.OpIn {
				return "1=0", nil
			}
			return "1=1", nil
		}
		ph := make([]string, len(l.Items))
		for i, it := range l.Items {
			ph[i] = b.arg(sqlValue(it))
		}
		in := e + " IN (" + strings.Join(ph, ",") + ")"
		if c.Op == queryapi.OpIn {
			return in, nil
		}
		return "(" + e + " IS NULL OR NOT (" + in + "))", nil
	case queryapi.OpLt, queryapi.OpLte, queryapi.OpGt, queryapi.OpGte:
		op := map[queryapi.Op]string{queryapi.OpLt: "<", queryapi.OpLte: "<=", queryapi.OpGt: ">", queryapi.OpGte: ">="}[c.Op]
		return e + " " + op + " " + b.arg(sqlValue(c.Value)), nil
	case queryapi.OpPrefix:
		s, _ := sqlValue(c.Value).(string)
		return fmt.Sprintf("(json_type(f.data, '$.%s') = 'text' AND substr(%s, 1, %d) = %s)",
			strings.Join(quoteAll(strings.Split(strings.TrimPrefix(c.Path, "/"), "/")), "."), e, len([]rune(s)), b.arg(s)), nil
	case queryapi.OpIContains, queryapi.OpIPrefix:
		// organon_fold is kernel.FoldText registered with the driver.
		s, _ := sqlValue(c.Value).(string)
		cmp := " > 0"
		if c.Op == queryapi.OpIPrefix {
			cmp = " = 1"
		}
		return "instr(organon_fold(" + e + "), " + b.arg(kernel.FoldText(s)) + ")" + cmp, nil
	case queryapi.OpContains:
		segs := quoteAll(strings.Split(strings.TrimPrefix(c.Path, "/"), "/"))
		return "EXISTS (SELECT 1 FROM json_each(f.data, " + b.arg("$."+strings.Join(segs, ".")) + ") j WHERE j.value = " +
			b.arg(sqlValue(c.Value)) + ")", nil
	}
	return "", fmt.Errorf("unsupported operator %q", c.Op)
}

// orderBy builds ORDER BY for sort keys; the ID always breaks ties.
func (b *builder) orderBy(keys []queryapi.SortKey) string {
	var parts []string
	idDesc := false
	for _, k := range keys {
		if k.Path == "" {
			idDesc = k.Desc
			continue
		}
		dir := " ASC"
		if k.Desc {
			dir = " DESC"
		}
		parts = append(parts, b.fieldExpr(k.Path)+dir)
	}
	dir := " ASC"
	if idDesc {
		dir = " DESC"
	}
	return strings.Join(append(parts, "f.id"+dir), ", ")
}

// after builds the keyset condition for a cursor. NULL sorts first in
// ascending order and last in descending order, as in kernel.Compare.
func (b *builder) after(keys []queryapi.SortKey, c *queryapi.Cursor) string {
	type key struct {
		expr string
		desc bool
		val  kernel.Value
	}
	var ks []key
	i := 0
	idDesc := false
	for _, k := range keys {
		if k.Path == "" {
			idDesc = k.Desc
			continue
		}
		var v kernel.Value = kernel.Null{}
		if i < len(c.Values) {
			v = c.Values[i]
		}
		i++
		ks = append(ks, key{expr: b.fieldExpr(k.Path), desc: k.Desc, val: v})
	}
	var ors []string
	var eqs []string
	for _, k := range ks {
		var gt, eq string
		switch {
		case isNull(k.val) && !k.desc:
			gt, eq = k.expr+" IS NOT NULL", k.expr+" IS NULL"
		case isNull(k.val) && k.desc:
			gt, eq = "1=0", k.expr+" IS NULL"
		case !k.desc:
			gt, eq = k.expr+" > "+b.arg(sqlValue(k.val)), k.expr+" = "+b.arg(sqlValue(k.val))
		default:
			gt = "(" + k.expr + " < " + b.arg(sqlValue(k.val)) + " OR " + k.expr + " IS NULL)"
			eq = k.expr + " = " + b.arg(sqlValue(k.val))
		}
		ors = append(ors, "("+strings.Join(append(append([]string{}, eqs...), gt), " AND ")+")")
		eqs = append(eqs, eq)
	}
	idOp := " > "
	if idDesc {
		idOp = " < "
	}
	ors = append(ors, "("+strings.Join(append(eqs, "f.id"+idOp+b.arg(c.ID.String())), " AND ")+")")
	return strings.Join(ors, " OR ")
}
