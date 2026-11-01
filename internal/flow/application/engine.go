// Package application runs flows (spec 10.6): it executes compiled graphs
// against a run context, stages writes and effects, commits them through
// Content in one batch, and records each run.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	flowapi "app-maker/internal/flow/api"
	"app-maker/internal/flow/domain"
	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
	schemaapi "app-maker/internal/schema/api"
)

// HTTPDoer performs outbound requests through the egress component.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Renderer renders manifest templates (templates/<name>).
type Renderer interface {
	Render(name string, vars map[string]any) (string, error)
}

// RunRecord is the outcome of one run (spec 10.12).
type RunRecord struct {
	RunID     kernel.ID
	Flow      string
	Version   int
	Trigger   domain.TriggerKind
	Principal kernel.ID
	Status    string // succeeded, failed, rejected, loop_stopped, dead_letter
	Started   time.Time
	Ended     time.Time
	Error     map[string]any
	Causation flowapi.CausationChain
	Nodes     map[string]any // per-node status and timing
	Trace     map[string]any // inputs and outputs when trace: true
	Feature   *kernel.ID
	// RetryArgs are the run_flow arguments of a dead-lettered run, kept so
	// an admin can retry it (spec 10.10).
	RetryArgs json.RawMessage
}

// RunRecorder stores run records as flowRun features.
type RunRecorder interface {
	Record(ctx context.Context, r RunRecord) error
}

// Deps are the engine's collaborators.
type Deps struct {
	Flows      *domain.FlowSet
	Content    contentapi.FlowAccess
	Authorizer accessapi.Authorizer
	Types      schemaapi.TypeRegistry
	Clock      kernel.Clock
	IDs        kernel.IDGenerator
	Jobs       kernel.JobEnqueuer
	HTTP       HTTPDoer
	Templates  Renderer
	Settings   map[string]any
	// Resolve loads a principal by ID (event-triggered runs act as the
	// principal that caused the event, spec 10.9).
	Resolve  func(ctx context.Context, id kernel.ID) (accessapi.Principal, error)
	Recorder RunRecorder
	Log      *slog.Logger
	Secrets  func(ref string) (string, bool)
	// Nodes is the node catalog, published to the visual editor.
	Nodes *domain.NodeRegistry
	// LLM serves llm.complete (spec 11.7); nil disables the node at run time.
	LLM rt.LLMClient
	// Code runs code nodes on runners (spec 11.1); Bundle returns the code
	// bundle and module path of a code file. Nil disables code nodes.
	Code   rt.CodeRunner
	Bundle func(code, requirements string) (rt.CodeBundle, string, error)
	// Ephemeral publishes ephemeral field values (llm.complete streamTo).
	Ephemeral func(ctx context.Context, p accessapi.Principal, id kernel.ID, values map[string]any) error
}

// Engine executes flows.
type Engine struct {
	d Deps
}

// NewEngine returns an engine.
func NewEngine(d Deps) *Engine {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Settings == nil {
		d.Settings = map[string]any{}
	}
	return &Engine{d: d}
}

// Flows returns the compiled flows.
func (e *Engine) Flows() *domain.FlowSet { return e.d.Flows }

// Request starts one run.
type Request struct {
	Flow      *domain.Flow
	Principal accessapi.Principal
	FeatureID *kernel.ID
	// Feature overrides the `feature` variable (beforeWrite passes the
	// state being written).
	Feature   map[string]any
	Input     map[string]any
	Event     map[string]any
	Trigger   map[string]any
	Causation flowapi.CausationChain
	// Action unlocks x-write: action:<name> fields when committing.
	Action string
	// Stage returns the write set instead of committing (beforeWrite).
	Stage bool
	// Background runs retry on version conflicts (spec 10.6).
	Background bool
	// Job is the run_flow arguments of a background run (dead-letter retry).
	Job json.RawMessage
}

// Result is a finished run.
type Result struct {
	RunID  kernel.ID
	Output any
	Ops    []contentapi.WriteOp
	Jobs   []kernel.JobRequest
	Commit contentapi.CommitResult
}

// ErrLoopStopped is returned when the causation limits stop a run.
var ErrLoopStopped = kernel.NewError(kernel.CodeConflict, "flow loop stopped")

// Run executes a flow (spec 10.6) and records the run.
func (e *Engine) Run(ctx context.Context, req Request) (res Result, err error) {
	f := req.Flow
	runID := e.d.IDs.NewID()
	ctx, end := kernel.Tel.StartSpan(ctx, "flow "+f.Name, kernel.A("flow.name", f.Name), kernel.A("flow.trigger", string(f.Trigger)),
		kernel.A("flow.run", runID.String()))
	defer func() { end(err) }()
	start := e.d.Clock.Now()
	rec := RunRecord{RunID: runID, Flow: f.Name, Version: f.Version, Trigger: f.Trigger, Principal: req.Principal.ID,
		Started: start, Causation: req.Causation, Feature: req.FeatureID}
	// Causation limits (spec 10.9).
	if len(req.Causation) >= flowapi.MaxCausationDepth ||
		(req.FeatureID != nil && req.Causation.Contains(f.Name, *req.FeatureID)) {
		rec.Status, rec.Ended = "loop_stopped", e.d.Clock.Now()
		e.record(ctx, rec)
		e.d.Log.Warn("flow loop stopped", "flow", f.Name, "depth", len(req.Causation))
		return Result{RunID: runID}, ErrLoopStopped
	}
	attempts := 1
	if req.Background {
		attempts = 3
	}
	var nodes, trace map[string]any
	for attempt := 1; attempt <= attempts; attempt++ {
		res, nodes, trace, err = e.runOnce(ctx, runID, req)
		if err == nil || !errors.Is(err, kernel.ErrVersionConflict) || attempt == attempts {
			break
		}
		// Nothing was committed or delivered; retry the whole run with
		// jittered backoff.
		time.Sleep(time.Duration(50*attempt+rand.IntN(50)) * time.Millisecond)
	}
	rec.Ended, rec.Nodes = e.d.Clock.Now(), nodes
	if f.Def.Trace {
		rec.Trace = trace
	}
	switch {
	case err == nil:
		rec.Status = "succeeded"
	case isReject(err):
		rec.Status = "rejected"
		rec.Error = errInfo(err)
	default:
		rec.Status = "failed"
		rec.Error = errInfo(err)
		if req.Background && kernel.FinalAttempt(ctx) {
			// The job will not be retried (spec 10.10).
			rec.Status, rec.RetryArgs = "dead_letter", req.Job
		}
	}
	if !req.Stage || err != nil {
		e.record(ctx, rec) // successful beforeWrite runs are not recorded: they run on every write
	}
	kernel.Tel.Count(ctx, "flow.runs", 1, kernel.A("flow.name", f.Name), kernel.A("flow.trigger", string(f.Trigger)), kernel.A("status", rec.Status))
	return res, err
}

func isReject(err error) bool {
	var re *domain.RejectError
	return errors.As(err, &re)
}

func errInfo(err error) map[string]any {
	var re *domain.RejectError
	if errors.As(err, &re) {
		return map[string]any{"code": re.Code, "message": re.Message}
	}
	return map[string]any{"code": kernel.CodeOf(err), "message": err.Error()}
}

func (e *Engine) record(ctx context.Context, r RunRecord) {
	if e.d.Recorder == nil {
		return
	}
	if err := e.d.Recorder.Record(context.WithoutCancel(ctx), r); err != nil {
		e.d.Log.Warn("recording flow run failed", "flow", r.Flow, "err", err)
	}
}

func (e *Engine) runOnce(ctx context.Context, runID kernel.ID, req Request) (Result, map[string]any, map[string]any, error) {
	f := req.Flow
	ctx, cancel := context.WithTimeout(ctx, f.Budget)
	defer cancel()
	principal := req.Principal
	if f.Def.RunAs == "system" {
		principal = accessapi.System()
	}
	input := req.Input
	if input == nil {
		input = map[string]any{}
	}
	if f.InputV != nil {
		if errs := f.InputV.Validate(toWire(input)); len(errs) > 0 {
			return Result{}, nil, nil, kernel.NewValidationError(errs...)
		}
	}
	feature := req.Feature
	if feature == nil && req.FeatureID != nil {
		doc, err := e.d.Content.Storage(ctx, *req.FeatureID)
		if err != nil {
			return Result{}, nil, nil, err
		}
		feature = doc
	}
	if feature == nil {
		feature = map[string]any{}
	}
	trigger := req.Trigger
	if trigger == nil {
		trigger = map[string]any{}
	}
	trigger["kind"] = string(f.Trigger)
	event := req.Event
	if event == nil {
		event = map[string]any{}
	}
	link := flowapi.CausationLink{FlowID: f.Name, RunID: runID}
	if req.FeatureID != nil {
		link.FeatureID = *req.FeatureID
	}
	r := &run{e: e, ctx: ctx, id: runID, principal: principal, writes: newWriteSet(), depth: 0, nodes: map[string]any{}, trace: map[string]any{},
		calls: map[string][]rt.LLMCall{}, chain: req.Causation.With(link)}
	base := e.d.Authorizer.Bindings(ctx, principal)
	base["principal"] = principal.ExprValue()
	base["trigger"] = trigger
	base["feature"] = feature
	base["input"] = input
	base["event"] = event
	base["env"] = e.d.Settings
	base[rt.FnNow] = e.d.Clock.Now
	base[rt.FnUUID] = func() string { return e.d.IDs.NewID().String() }
	out, err := r.execGraph(f.Graph, base, nil)
	if err != nil {
		return Result{}, r.nodes, r.trace, err
	}
	if f.OutputV != nil && out != nil {
		if errs := f.OutputV.Validate(toWire(out)); len(errs) > 0 {
			return Result{}, r.nodes, r.trace, fmt.Errorf("flow %s: result does not match the output schema: %v", f.Name, errs)
		}
	}
	res := Result{RunID: runID, Output: out, Ops: r.writes.ops, Jobs: r.writes.jobs}
	if req.Stage {
		return res, r.nodes, r.trace, nil
	}
	cr, err := e.d.Content.Commit(ctx, contentapi.CommitRequest{
		Principal: principal, Action: req.Action, Causation: req.Causation.With(link), Ops: r.writes.ops, Jobs: r.writes.jobs,
	})
	if err != nil {
		return Result{}, r.nodes, r.trace, err
	}
	res.Commit = cr
	res.Output = replaceRefs(out, cr.Refs)
	return res, r.nodes, r.trace, nil
}

// replaceRefs maps temporary references in the output to real IDs.
func replaceRefs(v any, refs map[string]string) any {
	switch x := v.(type) {
	case string:
		if id, ok := refs[x]; ok {
			return id
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, sub := range x {
			out[k] = replaceRefs(sub, refs)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, sub := range x {
			out[i] = replaceRefs(sub, refs)
		}
		return out
	}
	return v
}

// run is the state of one execution; it implements domain.Runtime per node.
type run struct {
	e         *Engine
	ctx       context.Context
	id        kernel.ID
	principal accessapi.Principal
	writes    *writeSet
	depth     int
	mu        sync.Mutex
	nodes     map[string]any // per-node status for the run record
	trace     map[string]any
	calls     map[string][]rt.LLMCall // LLM calls per node (spec 11.7)
	chain     flowapi.CausationChain  // causation including this run
}

func (r *run) llmCalls(node string) []rt.LLMCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[node]
}

type nodeState struct {
	out     any
	err     map[string]any
	skipped bool
}

// execGraph runs a graph level by level; nodes of one level run
// concurrently (spec 10.4: nodes without edges between them may run
// concurrently). It returns the graph's result.
func (r *run) execGraph(g *domain.Graph, base map[string]any, prefix []string) (any, error) {
	states := map[string]*nodeState{}
	nodesVar := func() map[string]any {
		m := make(map[string]any, len(states))
		for k, s := range states {
			entry := map[string]any{"out": s.out}
			if s.err != nil {
				entry["error"] = s.err
			}
			m[k] = entry
		}
		return m
	}
	for _, level := range g.Levels {
		vars := make(map[string]any, len(base)+1)
		for k, v := range base {
			vars[k] = v
		}
		vars["nodes"] = nodesVar()
		// Node goroutines record into results; states is only touched by
		// this goroutine and merged once the level is done.
		var mu sync.Mutex
		results := map[string]*nodeState{}
		eg, ctx := errgroup.WithContext(r.ctx)
		for _, name := range level {
			n := g.Nodes[name]
			skip := len(n.Deps) > 0
			for _, d := range n.Deps {
				if !states[d].skipped {
					skip = false
				}
			}
			if n.Spec.OnError != "" || isHandler(g, name) {
				skip = skip && !handlesError(g, name, states)
			}
			if !skip && n.When != nil {
				ok, err := n.When.Eval(vars)
				if err != nil {
					return nil, fmt.Errorf("node %s when: %w", name, err)
				}
				skip = ok != true
			}
			if skip {
				states[name] = &nodeState{skipped: true}
				r.note(prefix, name, map[string]any{"status": "skipped"})
				continue
			}
			eg.Go(func() error {
				start := time.Now()
				out, err := r.execNode(ctx, n, vars, prefix)
				st := &nodeState{out: out}
				status := map[string]any{"status": "succeeded", "ms": time.Since(start).Milliseconds()}
				if calls := r.llmCalls(strings.Join(append(append([]string{}, prefix...), name), ".")); calls != nil {
					status["llm"] = calls
				}
				if err != nil {
					var re *domain.RejectError
					var fe *domain.FailError
					if errors.As(err, &re) || errors.As(err, &fe) || n.Spec.OnError == "" {
						status["status"] = "failed"
						status["error"] = err.Error()
						r.note(prefix, name, status)
						return nodeError(name, err)
					}
					st.err = map[string]any{"code": kernel.CodeOf(err), "message": err.Error()}
					status["status"] = "failed_handled"
				}
				r.note(prefix, name, status)
				mu.Lock()
				results[name] = st
				mu.Unlock()
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return nil, err
		}
		for name, st := range results {
			states[name] = st
		}
	}
	if g.Result == nil {
		return nil, nil
	}
	vars := make(map[string]any, len(base)+1)
	for k, v := range base {
		vars[k] = v
	}
	vars["nodes"] = nodesVar()
	return g.Result.Eval(vars)
}

// isHandler reports whether name is some node's onError handler.
func isHandler(g *domain.Graph, name string) bool {
	for _, n := range g.Nodes {
		if n.Spec.OnError == name {
			return true
		}
	}
	return false
}

// handlesError: a handler runs when a node routing to it failed.
func handlesError(g *domain.Graph, name string, states map[string]*nodeState) bool {
	for src, n := range g.Nodes {
		if n.Spec.OnError == name && states[src] != nil && states[src].err != nil {
			return true
		}
	}
	return false
}

func nodeError(name string, err error) error {
	var re *domain.RejectError
	var fe *domain.FailError
	if errors.As(err, &re) || errors.As(err, &fe) {
		return err
	}
	var ke *kernel.Error
	if errors.As(err, &ke) || errors.Is(err, kernel.ErrVersionConflict) || errors.Is(err, kernel.ErrValidation) {
		return fmt.Errorf("node %s: %w", name, err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return kernel.NewError(kernel.CodeFlowTimeout, "node %s exceeded the flow's time budget", name)
	}
	return &kernel.Error{Code: kernel.CodeFlowFailed, Message: fmt.Sprintf("node %s: %v", name, err)}
}

func (r *run) note(prefix []string, name string, v map[string]any) {
	key := strings.Join(append(append([]string{}, prefix...), name), ".")
	r.mu.Lock()
	r.nodes[key] = v
	r.mu.Unlock()
}

func (r *run) execNode(ctx context.Context, n *domain.CompiledNode, vars map[string]any, prefix []string) (any, error) {
	inV, err := n.In.Eval(vars)
	if err != nil {
		return nil, fmt.Errorf("inputs: %w", err)
	}
	in, _ := inV.(map[string]any)
	r.mu.Lock() // node inputs are kept for runs with trace: true (spec 10.12)
	r.trace[strings.Join(append(append([]string{}, prefix...), n.Name), ".")] = map[string]any{"in": toWire(in)}
	r.mu.Unlock()
	if n.Spec.Node == "forEach" {
		return r.forEach(n, in, vars, prefix)
	}
	nr := &nodeRuntime{run: r, node: strings.Join(append(append([]string{}, prefix...), n.Name), "."), spec: n.Spec, cn: n}
	attempts := 1
	if n.Spec.Retry != nil && n.Spec.Retry.Max > 0 {
		attempts = 1 + n.Spec.Retry.Max
	}
	var out any
	for i := 1; i <= attempts; i++ {
		out, err = n.Type.Execute(ctx, in, nr)
		if err == nil && n.OutputV != nil {
			// Spec 11.1/11.7: output is validated before downstream nodes run;
			// a mismatch is retryable for External and Code nodes.
			if errs := n.OutputV.Validate(toWire(out)); len(errs) > 0 {
				err = fmt.Errorf("output does not match the output schema: %v", errs)
				if k := n.Type.Kind(); k == domain.KindExternal || k == domain.KindCode {
					err = &retryable{err}
				}
			}
		}
		var re *retryable
		if err == nil || !errors.As(err, &re) || i == attempts || ctx.Err() != nil {
			break
		}
		delay := 100 * time.Millisecond * time.Duration(1<<(i-1))
		if n.Spec.Retry.Backoff == "fixed" {
			delay = 100 * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
		}
	}
	return out, err
}

// forEach runs the body per item, bounded by maxItems (spec 10.5).
func (r *run) forEach(n *domain.CompiledNode, in map[string]any, vars map[string]any, prefix []string) (any, error) {
	items, _ := in["items"].([]any)
	if len(items) > n.MaxItems {
		return nil, kernel.NewError(kernel.CodeFlowFailed, "forEach %s: %d items exceed maxItems %d", n.Name, len(items), n.MaxItems)
	}
	out := make([]any, len(items))
	for i, it := range items {
		inner := make(map[string]any, len(vars)+3)
		for k, v := range vars {
			inner[k] = v
		}
		inner["item"], inner["index"], inner["outer"] = it, int64(i), vars["nodes"]
		res, err := r.execGraph(n.Body, inner, append(append([]string{}, prefix...), fmt.Sprintf("%s[%d]", n.Name, i)))
		if err != nil {
			return nil, err
		}
		out[i] = res
	}
	return out, nil
}

// nodeRuntime implements domain.Runtime for one node.
type nodeRuntime struct {
	run  *run
	node string
	spec domain.NodeSpec
	cn   *domain.CompiledNode
}

func (n *nodeRuntime) Principal() accessapi.Principal { return n.run.principal }
func (n *nodeRuntime) RunID() kernel.ID               { return n.run.id }
func (n *nodeRuntime) NodeID() string                 { return n.node }
func (n *nodeRuntime) Now() time.Time                 { return n.run.e.d.Clock.Now() }
func (n *nodeRuntime) NewID() kernel.ID               { return n.run.e.d.IDs.NewID() }

func (n *nodeRuntime) OutputSchema() json.RawMessage { return n.spec.OutputSchema }

// Causation implements domain.Runtime.
func (n *nodeRuntime) Causation() flowapi.CausationChain { return n.run.chain }

// LLM calls the router and records the call on the run (spec 11.7).
func (n *nodeRuntime) LLM(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, error) {
	if n.run.e.d.LLM == nil {
		return rt.LLMResponse{}, kernel.NewError(kernel.CodeUnsupportedCapability, "no LLM provider is configured")
	}
	res, call, err := n.run.e.d.LLM.Complete(ctx, req, onDelta)
	if call.Model != "" {
		n.run.mu.Lock()
		n.run.calls[n.node] = append(n.run.calls[n.node], call)
		n.run.mu.Unlock()
	}
	return res, err
}

// RunCode executes a code node (spec 11.1-11.6).
func (n *nodeRuntime) RunCode(ctx context.Context, input map[string]any) (json.RawMessage, error) {
	d := n.run.e.d
	if d.Code == nil || d.Bundle == nil {
		return nil, kernel.NewError(kernel.CodeUnsupportedCapability, "no code runner is configured")
	}
	wire := toWire(input)
	if n.cn.InputV != nil {
		if errs := n.cn.InputV.Validate(wire); len(errs) > 0 {
			return nil, kernel.NewValidationError(errs...)
		}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	bundle, entry, err := d.Bundle(n.spec.Code, n.spec.Requirements)
	if err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if t := n.cn.Limits.TimeoutMs; t > 0 {
		if lim := time.Now().Add(time.Duration(t) * time.Millisecond); !ok || lim.Before(deadline) {
			deadline = lim
		}
	}
	req := rt.CodeRequest{RunID: n.run.id.String(), NodeID: n.node, Entry: entry,
		IdempotencyKey: n.run.id.String() + ":" + n.node, Deadline: deadline, Input: raw, Bundle: bundle, Limits: n.cn.Limits}
	return d.Code.Run(ctx, req, n.host)
}

// host serves code callbacks as the run's principal (spec 11.1): reads go
// through the write set; code cannot stage writes or effects.
func (n *nodeRuntime) host(ctx context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
	var a map[string]any
	if len(args) > 0 {
		v, err := fromJSON(args)
		if err != nil {
			return nil, kernel.NewError(kernel.CodeBadRequest, "invalid callback arguments: %v", err)
		}
		a, _ = v.(map[string]any)
	}
	var out any
	var err error
	switch method {
	case "feature.get":
		var f contentapi.FlowFeature
		f, err = n.Read(ctx, str(a["id"]))
		out = map[string]any(f)
	case "feature.query":
		var fs []contentapi.FlowFeature
		fs, err = n.Query(ctx, a)
		list := make([]any, len(fs))
		for i, f := range fs {
			list[i] = map[string]any(f)
		}
		out = list
	case "llm.complete":
		out, err = n.codeLLM(ctx, a)
	case "log":
		kv := []any{}
		if m, ok := a["kv"].(map[string]any); ok {
			for k, v := range m {
				kv = append(kv, k, v)
			}
		}
		n.Log("code: "+str(a["msg"]), kv...)
	default:
		return nil, kernel.NewError(kernel.CodeBadRequest, "unknown host call %q", method)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(toWire(out))
}

// codeLLM serves ctx.llm: provider keys stay in the engine (spec 11.7).
func (n *nodeRuntime) codeLLM(ctx context.Context, a map[string]any) (any, error) {
	req := rt.LLMRequest{Model: str(a["model"]), System: str(a["system"])}
	if req.Model == "" {
		req.Model = "default"
	}
	if s, ok := a["schema"]; ok && s != nil {
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		req.OutputSchema = raw
	}
	if m, ok := a["maxTokens"].(int64); ok {
		req.MaxTokens = int(m)
	}
	if msgs, ok := a["messages"].([]any); ok {
		for _, m := range msgs {
			mm, _ := m.(map[string]any)
			req.Messages = append(req.Messages, rt.LLMMessage{Role: str(mm["role"]), Content: str(mm["content"])})
		}
	}
	if p := str(a["prompt"]); p != "" {
		req.Messages = append(req.Messages, rt.LLMMessage{Role: "user", Content: p})
	}
	if len(req.Messages) == 0 {
		return nil, kernel.NewError(kernel.CodeBadRequest, "ctx.llm needs a prompt or messages")
	}
	res, err := n.LLM(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	if req.OutputSchema == nil {
		return map[string]any{"text": res.Text}, nil
	}
	return fromJSON([]byte(res.Text))
}

// Ephemeral publishes ephemeral values as the run's principal.
func (n *nodeRuntime) Ephemeral(ctx context.Context, featureID string, values map[string]any) error {
	if n.run.e.d.Ephemeral == nil {
		return kernel.NewError(kernel.CodeUnsupportedCapability, "ephemeral fields are not available")
	}
	id, err := kernel.ParseID(featureID)
	if err != nil {
		return kernel.ErrNotFound
	}
	return n.run.e.d.Ephemeral(ctx, n.run.principal, id, values)
}

func (n *nodeRuntime) Log(msg string, kv ...any) {
	n.run.e.d.Log.Info(msg, append([]any{"run", n.run.id.String(), "node", n.node}, kv...)...)
}

// Read reads through the write set (spec 10.6: a run sees its own
// staged writes).
func (n *nodeRuntime) Read(ctx context.Context, id string) (contentapi.FlowFeature, error) {
	if staged, ok := n.run.writes.created(id); ok {
		return staged, nil
	}
	fid, err := kernel.ParseID(id)
	if err != nil {
		return nil, kernel.ErrNotFound
	}
	f, err := n.run.e.d.Content.Read(ctx, n.run.principal, fid)
	if err != nil {
		return nil, err
	}
	return n.run.writes.overlay(id, f), nil
}

func (n *nodeRuntime) Query(ctx context.Context, doc map[string]any) ([]contentapi.FlowFeature, error) {
	raw, err := jsonMarshal(doc)
	if err != nil {
		return nil, err
	}
	return n.run.e.d.Content.Query(ctx, n.run.principal, raw)
}

func (n *nodeRuntime) Children(ctx context.Context, id, typeName string, limit int) ([]contentapi.FlowFeature, error) {
	fid, err := kernel.ParseID(id)
	if err != nil {
		return nil, kernel.ErrNotFound
	}
	return n.run.e.d.Content.Children(ctx, n.run.principal, fid, typeName, limit)
}

// Stage adds a write; updates without a version use the current one.
func (n *nodeRuntime) Stage(op contentapi.WriteOp) (string, error) {
	if op.Op != "create" && op.Version == 0 && !strings.HasPrefix(op.ID, "$") {
		fid, err := kernel.ParseID(op.ID)
		if err != nil {
			return "", kernel.NewError(kernel.CodeValidationFailed, "invalid feature id %q", op.ID)
		}
		f, err := n.run.e.d.Content.Read(n.run.ctx, n.run.principal, fid)
		if err != nil {
			return "", err
		}
		v, _ := f["version"].(int64)
		op.Version = kernel.Version(v)
		if op.Type == "" {
			op.Type, _ = f["type"].(string)
		}
	}
	return n.run.writes.stage(op)
}

func (n *nodeRuntime) Effect(job kernel.JobRequest) { n.run.writes.effect(job) }

func (n *nodeRuntime) HTTP(_ context.Context, req *http.Request) (*http.Response, error) {
	if n.run.e.d.HTTP == nil {
		return nil, errors.New("outbound HTTP is not configured")
	}
	return n.run.e.d.HTTP.Do(req)
}

func (n *nodeRuntime) Render(name string, vars map[string]any) (string, error) {
	if n.run.e.d.Templates == nil {
		return "", errors.New("no templates")
	}
	return n.run.e.d.Templates.Render(name, vars)
}

// Subflow runs another flow inline; its writes and effects join this run
// (spec 10.5: depth at most 4).
func (n *nodeRuntime) Subflow(_ context.Context, name string, input map[string]any) (any, error) {
	if n.run.depth+1 > domain.MaxSubflowDepth {
		return nil, kernel.NewError(kernel.CodeFlowFailed, "subflow depth exceeds %d", domain.MaxSubflowDepth)
	}
	f, ok := n.run.e.d.Flows.Get(name)
	if !ok {
		return nil, kernel.NewError(kernel.CodeFlowFailed, "unknown flow %q", name)
	}
	if f.InputV != nil {
		if errs := f.InputV.Validate(toWire(input)); len(errs) > 0 {
			return nil, kernel.NewValidationError(errs...)
		}
	}
	child := &run{e: n.run.e, ctx: n.run.ctx, id: n.run.id, principal: n.run.principal, writes: n.run.writes, calls: n.run.calls, chain: n.run.chain,
		depth: n.run.depth + 1, nodes: n.run.nodes, trace: n.run.trace}
	base := n.run.e.d.Authorizer.Bindings(n.run.ctx, n.run.principal)
	base["principal"] = n.run.principal.ExprValue()
	base["trigger"] = map[string]any{"kind": "subflow"}
	base["feature"] = map[string]any{}
	base["input"] = input
	base["event"] = map[string]any{}
	base["env"] = n.run.e.d.Settings
	base[rt.FnNow] = n.run.e.d.Clock.Now
	base[rt.FnUUID] = func() string { return n.run.e.d.IDs.NewID().String() }
	return child.execGraph(f.Graph, base, []string{n.node})
}

// SetHTTP replaces the outbound HTTP client (manifest tests use fixtures).
func (e *Engine) SetHTTP(h HTTPDoer) { e.d.HTTP = h }
