package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"

	accessapi "app-maker/internal/access/api"
	rt "app-maker/internal/runtime/api"
	schemaapi "app-maker/internal/schema/api"
)

// Source is one flow file.
type Source struct {
	File string
	Data []byte
}

// Problem is a flow validation error (spec 10.14).
type Problem struct {
	File    string `json:"file"`
	Flow    string `json:"flow,omitempty"`
	Pointer string `json:"pointer"`
	Message string `json:"message"`
}

func (p Problem) Error() string { return p.File + "#" + p.Pointer + ": " + p.Message }

// ValidatorFactory compiles JSON Schemas for flow input and output.
type ValidatorFactory interface {
	New(id string, schema []byte) (schemaapi.Validator, error)
}

// CompileDeps are the collaborators of the flow compiler.
type CompileDeps struct {
	Types      schemaapi.TypeRegistry
	Exprs      rt.Compiler
	Nodes      *NodeRegistry
	Validators ValidatorFactory
	// CodeFiles lists the manifest's code files (nodes/...); nil skips the
	// existence checks of code nodes.
	CodeFiles map[string]bool
}

// ExprTree mirrors an `in` or `result` JSON value: string leaves are
// compiled expressions, other leaves literals.
type ExprTree struct {
	Prog    rt.Program
	Literal any
	Map     map[string]*ExprTree
	List    []*ExprTree
}

// Eval evaluates the tree.
func (t *ExprTree) Eval(vars map[string]any) (any, error) {
	switch {
	case t == nil:
		return nil, nil
	case t.Prog != nil:
		return t.Prog.Eval(vars)
	case t.Map != nil:
		out := make(map[string]any, len(t.Map))
		for k, sub := range t.Map {
			v, err := sub.Eval(vars)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			out[k] = v
		}
		return out, nil
	case t.List != nil:
		out := make([]any, len(t.List))
		for i, sub := range t.List {
			v, err := sub.Eval(vars)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	}
	return t.Literal, nil
}

// CompiledNode is a validated node.
type CompiledNode struct {
	Name     string
	Spec     NodeSpec
	Type     NodeType
	In       *ExprTree
	When     rt.Program
	Deps     []string
	Body     *Graph // forEach
	MaxItems int
	// OutputV validates the output of nodes declaring an outputSchema
	// (spec 11.1) before downstream nodes see it.
	OutputV schemaapi.Validator
	// InputV validates the input of code nodes before it leaves the engine.
	InputV schemaapi.Validator
	// Limits of code nodes (spec 11.3).
	Limits rt.CodeLimits
}

// Graph is a validated DAG with topological levels.
type Graph struct {
	Nodes  map[string]*CompiledNode
	Levels [][]string
	Result *ExprTree
}

// Flow is a compiled flow.
type Flow struct {
	Def       *Definition
	File      string
	Name      string
	Version   int
	Trigger   TriggerKind
	On        schemaapi.TypeID        // base type or trait ID
	OnType    *schemaapi.CompiledType // set when On is a concrete type
	Cron      *Cron
	When      rt.Program
	Budget    time.Duration
	Graph     *Graph
	InputT    *rt.Type
	OutputT   *rt.Type
	InputV    schemaapi.Validator
	OutputV   schemaapi.Validator
	WebSecret string
}

// Matches reports whether the flow's `on` covers type ct.
func (f *Flow) Matches(ct *schemaapi.CompiledType) bool {
	if f.On == "" {
		return true
	}
	return ct.Is(f.On)
}

// FlowSet is the compiled flows of an app.
type FlowSet struct {
	byName map[string]*Flow
	all    []*Flow
}

// Get returns a flow by name.
func (s *FlowSet) Get(name string) (*Flow, bool) {
	f, ok := s.byName[name]
	return f, ok
}

// All returns flows sorted by name.
func (s *FlowSet) All() []*Flow { return s.all }

// ByTrigger returns the flows of one trigger kind.
func (s *FlowSet) ByTrigger(k TriggerKind) []*Flow {
	var out []*Flow
	for _, f := range s.all {
		if f.Trigger == k {
			out = append(out, f)
		}
	}
	return out
}

// Action returns the flow handling action name on type ct.
func (s *FlowSet) Action(ct *schemaapi.CompiledType, name string) (*Flow, bool) {
	for _, f := range s.all {
		if f.Trigger == TriggerAction && f.Def.Trigger.Action == name && f.Matches(ct) {
			return f, true
		}
	}
	return nil, false
}

// Webhook returns the flow for an inbound webhook name.
func (s *FlowSet) Webhook(name string) (*Flow, bool) {
	for _, f := range s.all {
		if f.Trigger == TriggerWebhook && f.Def.Trigger.Webhook == name {
			return f, true
		}
	}
	return nil, false
}

type compiler struct {
	d     CompileDeps
	probs []Problem
	file  string
	flow  string
}

func (c *compiler) errf(ptr, format string, args ...any) {
	c.probs = append(c.probs, Problem{File: c.file, Flow: c.flow, Pointer: ptr, Message: fmt.Sprintf(format, args...)})
}

// Compile validates and compiles every flow (spec 10.14). All problems are
// returned together.
func Compile(d CompileDeps, srcs []Source) (*FlowSet, []Problem) {
	c := &compiler{d: d}
	set := &FlowSet{byName: map[string]*Flow{}}
	actions := map[string]string{}
	webhooks := map[string]string{}
	for _, s := range srcs {
		c.file, c.flow = s.File, ""
		def, err := ParseDefinition(s.Data)
		if err != nil {
			c.errf("", "%v", err)
			continue
		}
		c.flow = def.Flow
		if other, dup := set.byName[def.Flow]; dup {
			c.errf("/flow", "flow %q is already defined in %s", def.Flow, other.File)
			continue
		}
		f := c.compileFlow(def, s.File)
		if f == nil {
			continue
		}
		switch f.Trigger {
		case TriggerAction:
			key := string(f.On) + "\x00" + def.Trigger.Action
			if other, dup := actions[key]; dup {
				c.errf("/trigger/action", "action %q on %s is already handled by flow %s", def.Trigger.Action, f.On, other)
			}
			actions[key] = def.Flow
		case TriggerWebhook:
			if other, dup := webhooks[def.Trigger.Webhook]; dup {
				c.errf("/trigger/webhook", "webhook %q is already handled by flow %s", def.Trigger.Webhook, other)
			}
			webhooks[def.Trigger.Webhook] = def.Flow
		}
		set.byName[def.Flow] = f
		set.all = append(set.all, f)
	}
	sort.Slice(set.all, func(i, j int) bool { return set.all[i].Name < set.all[j].Name })
	if len(c.probs) > 0 {
		return nil, c.probs
	}
	return set, nil
}

func (c *compiler) compileFlow(def *Definition, file string) *Flow {
	before := len(c.probs)
	f := &Flow{Def: def, File: file, Name: def.Flow, Version: def.Version, Trigger: def.Trigger.Kind()}
	if f.Version == 0 {
		f.Version = 1
	}
	if f.Trigger == "" {
		c.errf("/trigger", "trigger must set exactly one of action, beforeWrite, event, schedule, webhook")
		return nil
	}
	c.compileTrigger(f)
	// runAs (spec 10.9).
	switch def.RunAs {
	case "", "trigger":
	case "system":
		if !def.Privileged {
			c.errf("/runAs", "runAs: system requires privileged: true")
		}
	default:
		c.errf("/runAs", "runAs must be trigger or system")
	}
	// Budget (spec 10.11).
	ms := def.TimeoutMs
	if ms == 0 {
		ms = DefaultBudgetMs[f.Trigger]
	}
	if ms < 0 || ms > MaxBudgetMs[f.Trigger] {
		c.errf("/timeoutMs", "timeoutMs must be at most %d for %s flows", MaxBudgetMs[f.Trigger], f.Trigger)
	}
	f.Budget = time.Duration(ms) * time.Millisecond
	f.InputT = rt.OpenMap()
	if len(def.Input) > 0 {
		f.InputT = c.schemaType("/input", def.Input)
		f.InputV = c.validator(def.Flow+"/input", "/input", def.Input)
	}
	if len(def.Output) > 0 {
		f.OutputT = c.schemaType("/output", def.Output)
		f.OutputV = c.validator(def.Flow+"/output", "/output", def.Output)
	}
	f.Graph = c.compileGraph(f, def.Nodes, def.Edges, def.Result, "", nil)
	if f.Graph != nil && def.Result != nil && f.OutputT != nil {
		c.checkResult(def.Result, f.OutputT)
	}
	if len(c.probs) > before {
		return nil
	}
	return f
}

func (c *compiler) compileTrigger(f *Flow) {
	t := f.Def.Trigger
	if t.On != "" {
		id, err := schemaapi.ParseTypeID(t.On)
		if err != nil {
			c.errf("/trigger/on", "%v", err)
		} else {
			f.On = id.Base()
			if !id.IsTrait() {
				ct, ok := c.d.Types.Get(f.On)
				if !ok {
					c.errf("/trigger/on", "unknown type %s", t.On)
				}
				f.OnType = ct
			} else if len(c.d.Types.Resolve(id)) == 0 {
				c.errf("/trigger/on", "no type includes trait %s", t.On)
			}
		}
	}
	switch f.Trigger {
	case TriggerAction, TriggerBeforeWrite:
		if t.On == "" {
			c.errf("/trigger/on", "%s triggers need on: <type or trait>", f.Trigger)
		}
	case TriggerSchedule:
		cron, err := ParseCron(t.Schedule)
		if err != nil {
			c.errf("/trigger/schedule", "%v", err)
		}
		f.Cron = cron
	case TriggerWebhook:
		if t.Secret == "" {
			c.errf("/trigger/secret", "webhook triggers need a signing secret (${secret:<name>})")
		}
		f.WebSecret = t.Secret
	}
	for _, op := range t.BeforeWrite {
		if op != "create" && op != "update" && op != "delete" && op != "move" {
			c.errf("/trigger/beforeWrite", "unknown operation %q", op)
		}
	}
	for _, k := range t.Event {
		switch k {
		case "created", "updated", "moved", "deleted", "purged":
		default:
			if !CustomEvent.MatchString(k) {
				c.errf("/trigger/event", "unknown event kind %q (custom events emitted by the emit node contain a dot, e.g. task.reminded)", k)
			}
		}
	}
	if t.When != "" {
		if f.Trigger != TriggerEvent && f.Trigger != TriggerBeforeWrite {
			c.errf("/trigger/when", "when is only allowed on event and beforeWrite triggers")
			return
		}
		p, err := c.d.Exprs.Compile(t.When, c.triggerEnv(f), rt.Bool, false)
		if err != nil {
			c.errf("/trigger/when", "%v", err)
		}
		f.When = p
	}
}

func (c *compiler) featureType(f *Flow) *rt.Type {
	if f.OnType != nil {
		return f.OnType.ExprType()
	}
	return rt.OpenMap()
}

// triggerEnv types the condition of event and beforeWrite triggers.
func (c *compiler) triggerEnv(f *Flow) rt.Env {
	ft := c.featureType(f)
	return rt.Env{
		Vars: map[string]*rt.Type{
			"event": rt.MapOf(map[string]*rt.Type{"kind": rt.String, "changed": rt.ListOf(rt.String), "type": rt.String, "id": rt.String, "data": rt.Any}),
			"op":    rt.String, "before": ft, "after": ft, "feature": ft,
			"principal": accessapi.PrincipalExprType(),
		},
		Funcs: []string{rt.FnNow, rt.FnChanged},
	}
}

// runEnv types the run context of nodes and results (spec 10.4).
func (c *compiler) runEnv(f *Flow, nodes *rt.Type, extra map[string]*rt.Type) rt.Env {
	ft := c.featureType(f)
	vars := map[string]*rt.Type{
		"trigger":   rt.OpenMap(),
		"feature":   ft,
		"input":     f.InputT,
		"event":     rt.OpenMap(),
		"principal": accessapi.PrincipalExprType(),
		"nodes":     nodes,
		"env":       rt.OpenMap(),
	}
	for k, v := range extra {
		vars[k] = v
	}
	return rt.Env{Vars: vars, Funcs: []string{rt.FnNow, rt.FnUUID, rt.FnIsOwner, rt.FnHasRole, rt.FnInGroup}}
}

// compileGraph validates a (sub)graph: node types, inputs, kinds, edges,
// inferred dependencies, cycles (spec 10.14).
func (c *compiler) compileGraph(f *Flow, specs map[string]NodeSpec, edges [][2]string, result any, prefix string, extra map[string]*rt.Type) *Graph {
	g := &Graph{Nodes: map[string]*CompiledNode{}}
	nodeFields := map[string]*rt.Type{}
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := specs[name]
		ptr := prefix + "/nodes/" + name
		nt, ok := c.d.Nodes.Lookup(spec.Node)
		if !ok {
			c.errf(ptr+"/node", "unknown node type %q", spec.Node)
			nodeFields[name] = rt.MapOf(map[string]*rt.Type{"out": rt.Any, "error": rt.OpenMap()})
			continue
		}
		if !nt.Kind().AllowedIn(f.Trigger) {
			c.errf(ptr+"/node", "%s nodes (%s) are not allowed in %s flows", nt.Kind(), spec.Node, f.Trigger)
		}
		out := nt.Output()
		cn := &CompiledNode{Name: name, Spec: spec, Type: nt}
		if len(spec.OutputSchema) > 0 {
			out = c.schemaType(ptr+"/outputSchema", spec.OutputSchema)
			cn.OutputV = c.validator(f.Name+ptr+"/outputSchema", ptr+"/outputSchema", spec.OutputSchema)
			if spec.Node == "llm.complete" && out.Kind != rt.TMap {
				c.errf(ptr+"/outputSchema", "llm.complete output schemas must describe an object")
			}
		}
		if nt.Kind() == KindCode {
			c.compileCode(f, cn, ptr)
		}
		nodeFields[name] = rt.MapOf(map[string]*rt.Type{"out": out, "error": rt.OpenMap()})
		g.Nodes[name] = cn
	}
	nodesT := rt.MapOf(nodeFields)
	env := c.runEnv(f, nodesT, extra)
	deps := map[string]map[string]bool{}
	for _, name := range names {
		deps[name] = map[string]bool{}
	}
	for i, e := range edges {
		for _, end := range e {
			if _, ok := specs[end]; !ok {
				c.errf(fmt.Sprintf("%s/edges/%d", prefix, i), "edge references unknown node %q", end)
			}
		}
		if _, ok := deps[e[1]]; ok {
			deps[e[1]][e[0]] = true
		}
	}
	for _, name := range names {
		n := g.Nodes[name]
		if n == nil {
			continue
		}
		ptr := prefix + "/nodes/" + name
		n.In = c.compileInputs(ptr, n, env)
		for ref := range refsOf(n.Spec.In) {
			if _, ok := specs[ref]; ok && ref != name {
				deps[name][ref] = true
			} else if !ok {
				c.errf(ptr+"/in", "references unknown node %q", ref)
			}
		}
		if n.Spec.When != "" {
			p, err := c.d.Exprs.Compile(n.Spec.When, env, rt.Bool, false)
			if err != nil {
				c.errf(ptr+"/when", "%v", err)
			}
			n.When = p
			for ref := range refsOf(n.Spec.When) {
				if _, ok := specs[ref]; ok && ref != name {
					deps[name][ref] = true
				}
			}
		}
		if n.Spec.OnError != "" {
			if _, ok := specs[n.Spec.OnError]; !ok || n.Spec.OnError == name {
				c.errf(ptr+"/onError", "onError must name another node")
			} else {
				deps[n.Spec.OnError][name] = true
			}
		}
		if n.Spec.Retry != nil && n.Type.Kind() != KindExternal && n.Type.Kind() != KindCode {
			c.errf(ptr+"/retry", "retry is only allowed on External and Code nodes")
		}
		if n.Spec.Node == "forEach" {
			c.compileForEach(f, n, ptr, nodesT, extra)
		} else if n.Spec.Do != nil || n.Spec.MaxItems != 0 {
			c.errf(ptr, "do and maxItems are only allowed on forEach nodes")
		}
		if f.Trigger == TriggerBeforeWrite && n.Type.Kind() == KindDataWrite {
			c.checkSameRepository(f, n, ptr)
		}
	}
	for name, ds := range deps {
		if n := g.Nodes[name]; n != nil {
			for d := range ds {
				n.Deps = append(n.Deps, d)
			}
			sort.Strings(n.Deps)
		}
	}
	levels, cyclic := topoLevels(names, deps)
	if cyclic != "" {
		c.errf(prefix+"/edges", "the graph has a cycle through node %q", cyclic)
		return nil
	}
	g.Levels = levels
	if result != nil {
		for ref := range refsOf(result) {
			if _, ok := specs[ref]; !ok {
				c.errf(prefix+"/result", "references unknown node %q", ref)
			}
		}
		g.Result = c.compileTree(prefix+"/result", result, env, nil)
	}
	return g
}

func (c *compiler) compileForEach(f *Flow, n *CompiledNode, ptr string, outer *rt.Type, extra map[string]*rt.Type) {
	n.MaxItems = n.Spec.MaxItems
	if n.MaxItems == 0 {
		n.MaxItems = DefaultMaxItems
	}
	if n.MaxItems < 0 || n.MaxItems > HardMaxItems {
		c.errf(ptr+"/maxItems", "forEach maxItems must be between 1 and %d", HardMaxItems)
	}
	if n.Spec.Do == nil || len(n.Spec.Do.Nodes) == 0 {
		c.errf(ptr+"/do", "forEach needs a do subgraph")
		return
	}
	inner := map[string]*rt.Type{"item": rt.Any, "index": rt.Int, "outer": outer}
	for k, v := range extra {
		inner[k] = v
	}
	n.Body = c.compileGraph(f, n.Spec.Do.Nodes, n.Spec.Do.Edges, n.Spec.Do.Result, ptr+"/do", inner)
}

// checkSameRepository: a DataWrite in a beforeWrite flow must target the
// triggering type's repository (spec 10.14).
func (c *compiler) checkSameRepository(f *Flow, n *CompiledNode, ptr string) {
	raw, ok := n.Spec.In["type"]
	if !ok {
		return // update/delete/move act on the triggering repository by ID
	}
	s, _ := raw.(string)
	name := strings.Trim(s, "'\"")
	if len(s) < 2 || s[0] != '\'' && s[0] != '"' || name == "" {
		c.errf(ptr+"/in/type", "DataWrite nodes in beforeWrite flows must name their type literally")
		return
	}
	ct, ok := c.d.Types.ByName(name)
	if !ok {
		c.errf(ptr+"/in/type", "unknown type %q", name)
		return
	}
	for _, t := range c.d.Types.Resolve(f.On) {
		if t.Repository != ct.Repository {
			c.errf(ptr+"/in/type", "DataWrite targets repository %q but the triggering write is in %q", ct.Repository, t.Repository)
			return
		}
	}
}

// compileInputs checks `in` against the node's parameters and compiles it.
func (c *compiler) compileInputs(ptr string, n *CompiledNode, env rt.Env) *ExprTree {
	params := map[string]Param{}
	inputs := n.Type.Inputs()
	if n.Type.Kind() == KindCode {
		inputs = schemaParams(n.Spec.InputSchema)
	}
	for _, p := range inputs {
		params[p.Name] = p
	}
	tree := &ExprTree{Map: map[string]*ExprTree{}}
	for k, v := range n.Spec.In {
		p, ok := params[k]
		if !ok {
			c.errf(ptr+"/in/"+k, "node type %s has no input %q", n.Spec.Node, k)
			continue
		}
		if p.Map {
			m, isMap := v.(map[string]any)
			if !isMap {
				c.errf(ptr+"/in/"+k, "%s must be an object of expressions", k)
				continue
			}
			tree.Map[k] = c.compileTree(ptr+"/in/"+k, m, env, nil)
			continue
		}
		tree.Map[k] = c.compileTree(ptr+"/in/"+k, v, env, p.Type)
	}
	for _, p := range inputs {
		if _, ok := n.Spec.In[p.Name]; p.Required && !ok {
			c.errf(ptr+"/in", "missing required input %q of %s", p.Name, n.Spec.Node)
		}
	}
	return tree
}

func (c *compiler) compileTree(ptr string, v any, env rt.Env, want *rt.Type) *ExprTree {
	switch x := v.(type) {
	case string:
		p, err := c.d.Exprs.Compile(x, env, want, true)
		if err != nil {
			c.errf(ptr, "%v", err)
			return &ExprTree{}
		}
		return &ExprTree{Prog: p}
	case map[string]any:
		t := &ExprTree{Map: map[string]*ExprTree{}}
		for k, sub := range x {
			var w *rt.Type
			if want != nil && want.Kind == rt.TMap {
				w = want.Fields[k]
			}
			t.Map[k] = c.compileTree(ptr+"/"+k, sub, env, w)
		}
		return t
	case []any:
		t := &ExprTree{List: []*ExprTree{}}
		for i, sub := range x {
			t.List = append(t.List, c.compileTree(fmt.Sprintf("%s/%d", ptr, i), sub, env, nil))
		}
		return t
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return &ExprTree{Literal: n}
		}
		f, _ := x.Float64()
		return &ExprTree{Literal: f}
	}
	return &ExprTree{Literal: v}
}

// checkResult: result keys must be declared by the output schema (spec 10.14).
func (c *compiler) checkResult(result map[string]any, out *rt.Type) {
	if out.Kind != rt.TMap || out.Open {
		return
	}
	for k := range result {
		if _, ok := out.Fields[k]; !ok {
			c.errf("/result/"+k, "result key %q is not in the output schema", k)
		}
	}
}

func (c *compiler) validator(id, ptr string, raw json.RawMessage) schemaapi.Validator {
	v, err := c.d.Validators.New(id, raw)
	if err != nil {
		c.errf(ptr, "invalid JSON Schema: %v", err)
		return nil
	}
	return v
}

// schemaType converts a plain JSON Schema to an expression type.
func (c *compiler) schemaType(ptr string, raw json.RawMessage) *rt.Type {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		c.errf(ptr, "invalid JSON Schema: %v", err)
		return rt.Any
	}
	return SchemaType(m)
}

// SchemaType maps a JSON Schema node to an expression type.
func SchemaType(m map[string]any) *rt.Type {
	t, _ := m["type"].(string)
	if l, ok := m["type"].([]any); ok && len(l) > 0 {
		t, _ = l[0].(string)
	}
	switch t {
	case "string":
		if m["format"] == "date-time" {
			return rt.Time
		}
		return rt.String
	case "integer":
		return rt.Int
	case "number":
		return rt.Float
	case "boolean":
		return rt.Bool
	case "array":
		items, _ := m["items"].(map[string]any)
		return rt.ListOf(SchemaType(items))
	case "object":
		props, _ := m["properties"].(map[string]any)
		if len(props) == 0 {
			return rt.OpenMap()
		}
		fields := map[string]*rt.Type{}
		for k, v := range props {
			pm, _ := v.(map[string]any)
			fields[k] = SchemaType(pm)
		}
		mt := rt.MapOf(fields)
		mt.Open = m["additionalProperties"] != false
		return mt
	}
	if _, ok := m["enum"]; ok {
		return rt.String
	}
	return rt.Any
}

// refsOf finds the nodes referenced as nodes.<name> in expressions.
func refsOf(v any) map[string]bool {
	out := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			tree, err := parser.Parse(x)
			if err != nil {
				return
			}
			f := &nodeRefs{out: out}
			ast.Walk(&tree.Node, f)
		case map[string]any:
			for _, sub := range x {
				walk(sub)
			}
		case []any:
			for _, sub := range x {
				walk(sub)
			}
		}
	}
	walk(v)
	return out
}

type nodeRefs struct{ out map[string]bool }

func (n *nodeRefs) Visit(node *ast.Node) {
	m, ok := (*node).(*ast.MemberNode)
	if !ok {
		return
	}
	id, ok := m.Node.(*ast.IdentifierNode)
	if !ok || id.Value != "nodes" {
		return
	}
	if s, ok := m.Property.(*ast.StringNode); ok {
		n.out[s.Value] = true
	}
}

// topoLevels groups nodes into levels that can run concurrently; it
// returns a node on a cycle when the graph is not a DAG.
func topoLevels(names []string, deps map[string]map[string]bool) ([][]string, string) {
	done := map[string]bool{}
	var levels [][]string
	for len(done) < len(names) {
		var level []string
		for _, n := range names {
			if done[n] {
				continue
			}
			ready := true
			for d := range deps[n] {
				if !done[d] {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, n)
			}
		}
		if len(level) == 0 {
			for _, n := range names {
				if !done[n] {
					return nil, n
				}
			}
		}
		for _, n := range level {
			done[n] = true
		}
		levels = append(levels, level)
	}
	return levels, ""
}

// CustomEvent names events raised by the emit node (spec 10.5, 14.5):
// lower-case words joined by dots, so they cannot be lifecycle kinds.
var CustomEvent = regexp.MustCompile(`^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)+$`)

// schemaParams derives the inputs of a code node from its inputSchema.
func schemaParams(raw json.RawMessage) []Param {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	props, _ := m["properties"].(map[string]any)
	req := map[string]bool{}
	if l, ok := m["required"].([]any); ok {
		for _, r := range l {
			if s, ok := r.(string); ok {
				req[s] = true
			}
		}
	}
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]Param, 0, len(names))
	for _, k := range names {
		pm, _ := props[k].(map[string]any)
		out = append(out, Param{Name: k, Type: SchemaType(pm), Required: req[k]})
	}
	return out
}

// compileCode checks a code node (spec 11.2, 11.3): typed input and
// output, an existing code file and lock file, and well-formed limits.
func (c *compiler) compileCode(f *Flow, n *CompiledNode, ptr string) {
	spec := n.Spec
	if len(spec.InputSchema) == 0 {
		c.errf(ptr+"/inputSchema", "code nodes need an inputSchema")
	} else {
		n.InputV = c.validator(f.Name+ptr+"/inputSchema", ptr+"/inputSchema", spec.InputSchema)
	}
	if len(spec.OutputSchema) == 0 {
		c.errf(ptr+"/outputSchema", "code nodes need an outputSchema")
	}
	switch {
	case spec.Code == "":
		c.errf(ptr+"/code", "code nodes need a code file (nodes/<name>.py)")
	case !strings.HasSuffix(spec.Code, ".py") || !strings.Contains("/"+spec.Code, "/nodes/"):
		c.errf(ptr+"/code", "code must be a .py file under nodes/")
	case c.d.CodeFiles != nil && !c.d.CodeFiles[spec.Code]:
		c.errf(ptr+"/code", "code file %s does not exist", spec.Code)
	}
	if spec.Requirements != "" && c.d.CodeFiles != nil && !c.d.CodeFiles[spec.Requirements] {
		c.errf(ptr+"/requirements", "requirements file %s does not exist", spec.Requirements)
	}
	if len(spec.Limits) > 0 {
		dec := json.NewDecoder(bytes.NewReader(spec.Limits))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&n.Limits); err != nil {
			c.errf(ptr+"/limits", "invalid limits: %v", err)
		}
		for i, h := range n.Limits.Network {
			if host, port, err := net.SplitHostPort(h); err != nil || host == "" || port == "" {
				c.errf(fmt.Sprintf("%s/limits/network/%d", ptr, i), "network entries are host:port (port may be *)")
			}
		}
		if n.Limits.TimeoutMs < 0 || n.Limits.MemoryMb < 0 || n.Limits.CPU < 0 {
			c.errf(ptr+"/limits", "limits must not be negative")
		}
		if budget := int64(MaxBudgetMs[f.Trigger]); n.Limits.TimeoutMs > budget {
			c.errf(ptr+"/limits/timeoutMs", "timeoutMs exceeds the %s budget of %d ms", f.Trigger, budget)
		}
	}
}
