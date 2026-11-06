package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	changeapi "organon/internal/change/api"
	flowapi "organon/internal/flow/api"
	"organon/internal/flow/domain"
	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// runFlowArgs are the arguments of a run_flow job (spec 2.5).
type runFlowArgs struct {
	Flow      string                 `json:"flow"`
	Version   int                    `json:"version"`
	Trigger   string                 `json:"trigger"`
	FeatureID *kernel.ID             `json:"feature,omitempty"`
	Event     map[string]any         `json:"event,omitempty"`
	Input     map[string]any         `json:"input,omitempty"`
	Principal *kernel.ID             `json:"principal,omitempty"`
	Causation flowapi.CausationChain `json:"causation,omitempty"`
	// Before and After are the event's documents (wire form): nodes see
	// them as before/after, and runs of deleted and purged features use
	// them as feature (ADR 0023).
	Before map[string]any `json:"before,omitempty"`
	After  map[string]any `json:"after,omitempty"`
	Type   string         `json:"type,omitempty"`
	Parent *kernel.ID     `json:"parent,omitempty"`
	// WhenError records a trigger condition that failed to evaluate: the
	// run is recorded as failed instead of running or being skipped.
	WhenError string `json:"whenError,omitempty"`
}

func runFlowJob(f *domain.Flow, a runFlowArgs) (kernel.JobRequest, error) {
	a.Flow, a.Version, a.Trigger = f.Name, f.Version, string(f.Trigger)
	raw, err := json.Marshal(a)
	if err != nil {
		return kernel.JobRequest{}, err
	}
	return kernel.JobRequest{Kind: JobRunFlow, Queue: kernel.QueueFlows, Args: raw, MaxAttempts: 5}, nil
}

// typedDoc converts a wire document to expression values using the type.
func typedDoc(ct *schemaapi.CompiledType, wire map[string]any) map[string]any {
	out := make(map[string]any, len(wire))
	for k, v := range wire {
		if ct != nil {
			if f, ok := ct.Field("/" + k); ok {
				if tv, err := f.Type.Coerce("/"+k, v); err == nil {
					out[k] = kernel.Native(tv)
					continue
				}
			}
		}
		out[k] = v
	}
	return out
}

// eventVars builds the variables of event-trigger conditions.
func (e *Engine) eventVars(ev changeapi.Event) (map[string]any, *schemaapi.CompiledType) {
	ct, ok := e.d.Types.Get(ev.TypeID)
	if !ok {
		ct, _ = e.d.Types.Get(ev.TypeID.Base())
	}
	after := typedDoc(ct, ev.After)
	after["id"] = ev.FeatureID.String()
	before := make(map[string]any, len(after))
	for k, v := range after {
		before[k] = v
	}
	for k, v := range typedDoc(ct, ev.Before) {
		if v == nil {
			delete(before, k)
		} else {
			before[k] = v
		}
	}
	changed := make([]any, len(ev.Changed))
	for i, c := range ev.Changed {
		changed[i] = c
	}
	name := ""
	if ct != nil {
		name = ct.Name
	}
	event := map[string]any{"kind": string(ev.Kind), "changed": changed, "type": name, "id": ev.FeatureID.String()}
	vars := map[string]any{
		"event": event, "before": before, "after": after, "feature": after, "op": string(ev.Kind),
		"principal":  map[string]any{"id": ev.Actor.String(), "kind": "", "roles": []any{}, "groups": []any{}},
		rt.FnNow:     e.d.Clock.Now,
		rt.FnChanged: func(p string) bool { return slices.Contains(ev.Changed, p) },
	}
	return vars, ct
}

// EventHook matches committed events against event triggers and enqueues
// one run_flow job per matching flow in the same transaction (spec 10.2).
// It implements Content's CommitHook.
type EventHook struct{ e *Engine }

// EventHook returns the commit hook for event triggers.
func (e *Engine) EventHook() *EventHook { return &EventHook{e: e} }

// Jobs implements the content CommitHook port.
func (h *EventHook) Jobs(_ context.Context, events []changeapi.Event, causation flowapi.CausationChain) ([]kernel.JobRequest, error) {
	flows := h.e.d.Flows.ByTrigger(domain.TriggerEvent)
	if len(flows) == 0 {
		return nil, nil
	}
	var jobs []kernel.JobRequest
	for _, ev := range events {
		vars, ct := h.e.eventVars(ev)
		if ct == nil {
			continue
		}
		for _, f := range flows {
			if len(f.Def.Trigger.Event) > 0 && !slices.Contains(f.Def.Trigger.Event, string(ev.Kind)) {
				continue
			}
			if !f.Matches(ct) {
				continue
			}
			whenErr := ""
			if f.When != nil {
				ok, err := f.When.Eval(vars)
				if err != nil {
					// A failing guard neither runs nor silently skips the
					// flow: the failure is recorded (ADR 0023).
					h.e.d.Log.Warn("event trigger condition failed", "flow", f.Name, "err", err)
					whenErr = err.Error()
				} else if ok != true {
					continue
				}
			}
			id, actor := ev.FeatureID, ev.Actor
			j, err := runFlowJob(f, runFlowArgs{FeatureID: &id, Event: vars["event"].(map[string]any), Principal: &actor, Causation: causation,
				Before: ev.Before, After: ev.After, Type: string(ev.TypeID), Parent: ev.ParentID, WhenError: whenErr})
			if err != nil {
				return nil, err
			}
			jobs = append(jobs, j)
		}
	}
	return jobs, nil
}

// HandleRunFlow is the run_flow job worker (spec 2.5, 10.2). Failures fail
// the job so River retries it; loop stops are final.
func (e *Engine) HandleRunFlow(ctx context.Context, raw json.RawMessage) error {
	var a runFlowArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	f, ok := e.d.Flows.Get(a.Flow)
	if !ok {
		e.d.Log.Warn("run_flow for an unknown flow", "flow", a.Flow)
		return nil // flow removed by a later deploy: nothing to run
	}
	p := accessapi.System()
	if a.Principal != nil && *a.Principal != accessapi.SystemPrincipalID && !a.Principal.IsZero() && e.d.Resolve != nil {
		if rp, err := e.d.Resolve(ctx, *a.Principal); err == nil {
			p = rp
		}
	}
	if a.WhenError != "" {
		now := e.d.Clock.Now()
		e.record(ctx, RunRecord{RunID: e.d.IDs.NewID(), Flow: f.Name, Version: f.Version, Trigger: f.Trigger, Principal: p.ID,
			Started: now, Ended: now, Status: "failed", Causation: a.Causation, Feature: a.FeatureID,
			Error: map[string]any{"code": "when_failed", "message": "trigger condition: " + a.WhenError}})
		return nil
	}
	req := Request{Flow: f, Principal: p, FeatureID: a.FeatureID, Input: a.Input, Event: a.Event,
		Causation: a.Causation, Background: true, Trigger: map[string]any{"kind": a.Trigger}, Job: raw}
	if a.Before != nil || a.After != nil {
		ct, ok := e.d.Types.Get(schemaapi.TypeID(a.Type))
		if !ok {
			ct, _ = e.d.Types.Get(schemaapi.TypeID(a.Type).Base())
		}
		// Events carry the changed fields' old values; the rest of before
		// is after, as in trigger conditions.
		req.After = typedDoc(ct, a.After)
		req.Before = make(map[string]any, len(req.After))
		for k, v := range req.After {
			req.Before[k] = v
		}
		for k, v := range typedDoc(ct, a.Before) {
			if v == nil {
				delete(req.Before, k)
			} else {
				req.Before[k] = v
			}
		}
		if kind, _ := a.Event["kind"].(string); kind == "deleted" || kind == "purged" {
			// The feature is gone from reads; its last state stands in.
			doc := typedDoc(ct, a.After)
			if len(doc) == 0 {
				doc = typedDoc(ct, a.Before)
			}
			if a.FeatureID != nil {
				doc["id"] = a.FeatureID.String()
			}
			if a.Parent != nil {
				doc["parentId"] = a.Parent.String()
			}
			if ct != nil {
				doc["type"] = ct.Name
			}
			req.Feature = doc
		}
	}
	if f.Trigger == domain.TriggerSchedule || f.Trigger == domain.TriggerWebhook {
		req.Principal = accessapi.System() // spec 10.9: system principal for schedules
	}
	_, err := e.Run(ctx, req)
	if errors.Is(err, ErrLoopStopped) {
		return nil
	}
	return err
}

// BeforeWrite runs matching beforeWrite flows inside the write pipeline
// (spec 10.2). It returns the flows' staged writes, which join the batch;
// a reject node aborts the write with its declared error.
func (e *Engine) BeforeWrite(ctx context.Context, op string, ct *schemaapi.CompiledType, p accessapi.Principal,
	before, after map[string]any) ([]Result, error) {
	var out []Result
	for _, f := range e.d.Flows.ByTrigger(domain.TriggerBeforeWrite) {
		if !slices.Contains(f.Def.Trigger.BeforeWrite, op) || !f.Matches(ct) {
			continue
		}
		feature := after
		if feature == nil {
			feature = before
		}
		if f.When != nil {
			vars := map[string]any{"op": op, "before": orEmpty(before), "after": orEmpty(after), "feature": feature,
				"principal": p.ExprValue(), "event": map[string]any{"kind": op, "changed": []any{}, "type": ct.Name, "id": feature["id"]},
				rt.FnNow: e.d.Clock.Now, rt.FnChanged: func(path string) bool { return changedPath(before, after, path) }}
			ok, err := f.When.Eval(vars)
			if err != nil {
				return nil, err
			}
			if ok != true {
				continue
			}
		}
		var fid *kernel.ID
		if s, _ := feature["id"].(string); s != "" {
			if id, err := kernel.ParseID(s); err == nil {
				fid = &id
			}
		}
		res, err := e.Run(ctx, Request{Flow: f, Principal: p, FeatureID: fid, Feature: feature, Stage: true, WritingType: ct.Name,
			Trigger: map[string]any{"op": op, "before": orEmpty(before), "after": orEmpty(after)}})
		if err != nil {
			return nil, asDeclared(err)
		}
		out = append(out, res)
	}
	return out, nil
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func changedPath(before, after map[string]any, path string) bool {
	k := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 2)[0]
	b, _ := json.Marshal(before[k])
	a, _ := json.Marshal(after[k])
	return string(a) != string(b)
}

// asDeclared maps a reject to its declared error code and status (spec 6.6).
func asDeclared(err error) error {
	var re *domain.RejectError
	if errors.As(err, &re) {
		return &kernel.Error{Code: re.Code, Message: re.Message, Status: re.Status}
	}
	var fe *domain.FailError
	if errors.As(err, &fe) {
		return &kernel.Error{Code: kernel.CodeFlowFailed, Message: fe.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return kernel.NewError(kernel.CodeFlowTimeout, "flow exceeded its time budget")
	}
	return err
}

// ActionResult is returned by InvokeAction.
type ActionResult struct {
	Output  any            `json:"output,omitempty"`
	Feature map[string]any `json:"feature,omitempty"`
}

// InvokeAction runs a named action on a feature (spec 10.3): a flow with
// an action trigger, a state-machine transition (or the generated
// "transition" action), or the built-in "transfer".
func (e *Engine) InvokeAction(ctx context.Context, p accessapi.Principal, typeName string, id kernel.ID, action string, input map[string]any) (ActionResult, error) {
	ct, ok := e.d.Types.ByName(typeName)
	if !ok {
		return ActionResult{}, kernel.ErrNotFound
	}
	if input == nil {
		input = map[string]any{}
	}
	// The generated transition action dispatches to a declared transition.
	if action == "transition" {
		name, _ := input["action"].(string)
		to, _ := input["to"].(string)
		storage, err := e.d.Content.Storage(ctx, id)
		if err != nil {
			return ActionResult{}, err
		}
		for _, sm := range ct.StateMachines {
			from, _ := storage[sm.Path[1:]].(string)
			for _, t := range sm.Transitions {
				if t.From == from && (t.Action == name || (name == "" && t.To == to)) {
					rest := map[string]any{}
					for k, v := range input {
						if k != "action" && k != "to" {
							rest[k] = v // the dispatch keys belong to the transition action
						}
					}
					return e.InvokeAction(ctx, p, typeName, id, t.Action, rest)
				}
			}
		}
		return ActionResult{}, kernel.NewError(kernel.CodeInvalidTransition, "no transition matches")
	}
	storage, err := e.d.Content.AuthorizeAction(ctx, p, id, action, input)
	if err != nil {
		return ActionResult{}, err
	}
	if storage["type"] != ct.Name {
		return ActionResult{}, kernel.ErrNotFound
	}
	if f, ok := e.d.Flows.Action(ct, action); ok {
		res, err := e.Run(ctx, Request{Flow: f, Principal: p, FeatureID: &id, Input: input, Action: action,
			Trigger: map[string]any{"action": action}})
		if err != nil {
			return ActionResult{}, asDeclared(err)
		}
		return ActionResult{Output: res.Output}, nil
	}
	if action == "share" && ct.Shareable != nil && e.d.Share != nil {
		out, err := e.d.Share(ctx, p, id, ct.Shareable, input)
		if err != nil {
			return ActionResult{}, err
		}
		return ActionResult{Output: out}, nil
	}
	version, _ := storage["version"].(int64)
	commit := func(data map[string]any) (ActionResult, error) {
		cr, err := e.d.Content.Commit(ctx, commitReq(p, action, id, ct.Name, kernel.Version(version), data))
		if err != nil {
			return ActionResult{}, err
		}
		out := ActionResult{}
		if len(cr.Features) > 0 {
			b, _ := json.Marshal(cr.Features[0])
			_ = json.Unmarshal(b, &out.Feature)
		}
		return out, nil
	}
	if action == "transfer" && ct.HasTrait(schemaapi.TraitOwnable) {
		to, _ := input["to"].(string)
		if _, err := kernel.ParseID(to); err != nil {
			return ActionResult{}, kernel.NewValidationError(kernel.FieldError{Path: "/to", Code: "format", Message: "a principal id is required"})
		}
		return commit(map[string]any{"owner": to})
	}
	for path, sm := range ct.StateMachines {
		from, _ := storage[path[1:]].(string)
		if t, ok := sm.Find(from, action); ok {
			return commit(map[string]any{path[1:]: t.To})
		}
		for _, t := range sm.Transitions {
			if t.Action == action {
				return ActionResult{}, kernel.NewError(kernel.CodeInvalidTransition, "%s is not allowed from %q", action, from)
			}
		}
	}
	return ActionResult{}, kernel.NewError(kernel.CodeNotFound, "unknown action %q", action)
}

// Webhook verifies an inbound webhook with the trigger's scheme and
// secret (domain.VerifyWebhook, ADR 0019) and enqueues its run (spec 10.2).
func (e *Engine) Webhook(ctx context.Context, name string, body []byte, headers http.Header) error {
	f, ok := e.d.Flows.Webhook(name)
	if !ok {
		return kernel.ErrNotFound
	}
	secret, ok := "", false
	if e.d.Secrets != nil {
		secret, ok = e.d.Secrets(f.WebSecret)
	}
	if !ok || secret == "" {
		return kernel.NewError(kernel.CodeForbidden, "webhook secret is not configured")
	}
	if domain.VerifyWebhook(f.Verify, secret, body, headers, e.d.Clock.Now()) != nil {
		return kernel.NewError(kernel.CodeUnauthenticated, "invalid webhook signature")
	}
	var input map[string]any
	if len(body) > 0 {
		if err := json.Unmarshal(body, &input); err != nil {
			return kernel.NewError(kernel.CodeBadRequest, "webhook body must be a JSON object")
		}
	}
	j, err := runFlowJob(f, runFlowArgs{Input: input})
	if err != nil {
		return err
	}
	return e.d.Jobs.Enqueue(ctx, []kernel.JobRequest{j})
}

// Schedule is a periodic run of a scheduled flow.
type Schedule struct {
	Name string
	Next func(time.Time) time.Time
	Job  kernel.JobRequest
}

// Schedules returns the periodic jobs of scheduled flows; every worker
// replica registers the same set and the job system enqueues each
// occurrence once (spec 2.5).
func (e *Engine) Schedules() ([]Schedule, error) {
	var out []Schedule
	for _, f := range e.d.Flows.ByTrigger(domain.TriggerSchedule) {
		j, err := runFlowJob(f, runFlowArgs{})
		if err != nil {
			return nil, err
		}
		out = append(out, Schedule{Name: "flow:" + f.Name, Next: f.Cron.Next, Job: j})
	}
	return out, nil
}

// JobEmitEvent delivers a custom event raised by an emit node.
const JobEmitEvent = "emit_event"

type emitArgs struct {
	Event     string                 `json:"event"`
	Feature   string                 `json:"feature"`
	Data      any                    `json:"data,omitempty"`
	Principal *kernel.ID             `json:"principal,omitempty"`
	Causation flowapi.CausationChain `json:"causation,omitempty"`
}

// HandleEmitEvent starts the flows triggered by a custom event (spec 10.5,
// 14.5: chaining effects through events). `event.data` carries the data;
// the causation chain continues, so emit loops stop like write loops.
func (e *Engine) HandleEmitEvent(ctx context.Context, raw json.RawMessage) error {
	var a emitArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	id, err := kernel.ParseID(a.Feature)
	if err != nil {
		return nil // nothing to deliver to
	}
	doc, err := e.d.Content.Storage(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	typeName, _ := doc["type"].(string)
	ct, ok := e.d.Types.ByName(typeName)
	if !ok {
		return nil
	}
	event := map[string]any{"kind": a.Event, "changed": []any{}, "type": typeName, "id": a.Feature, "data": a.Data}
	vars := map[string]any{"event": event, "before": map[string]any(doc), "after": map[string]any(doc), "feature": map[string]any(doc),
		"op": a.Event, "principal": map[string]any{"id": "", "kind": "", "roles": []any{}, "groups": []any{}},
		rt.FnNow: e.d.Clock.Now, rt.FnChanged: func(string) bool { return false }}
	var jobs []kernel.JobRequest
	for _, f := range e.d.Flows.ByTrigger(domain.TriggerEvent) {
		if !slices.Contains(f.Def.Trigger.Event, a.Event) || !f.Matches(ct) {
			continue
		}
		if f.When != nil {
			if ok, err := f.When.Eval(vars); err != nil || ok != true {
				continue
			}
		}
		j, err := runFlowJob(f, runFlowArgs{FeatureID: &id, Event: event, Principal: a.Principal, Causation: a.Causation})
		if err != nil {
			return err
		}
		jobs = append(jobs, j)
	}
	if len(jobs) == 0 {
		return nil
	}
	return e.d.Jobs.Enqueue(ctx, jobs)
}

// RetryRun re-enqueues a dead-lettered run with its original arguments
// (admins; spec 10.10).
func (e *Engine) RetryRun(ctx context.Context, p accessapi.Principal, runRecord kernel.ID) error {
	if !p.IsSystem() && !p.HasRole("admin") {
		return kernel.ErrForbidden
	}
	doc, err := e.d.Content.Storage(ctx, runRecord)
	if err != nil {
		return err
	}
	if doc["type"] != "flowRun" || doc["status"] != "dead_letter" {
		return kernel.NewError(kernel.CodeConflict, "only dead-lettered runs can be retried")
	}
	args, err := json.Marshal(doc["retryArgs"])
	if err != nil || string(args) == "null" {
		return kernel.NewError(kernel.CodeConflict, "the run has no retry arguments")
	}
	return e.d.Jobs.Enqueue(ctx, []kernel.JobRequest{{Kind: JobRunFlow, Queue: kernel.QueueFlows, Args: args, MaxAttempts: 5}})
}

// RunForFeature runs a flow as the system for one feature (flow-based data
// migrations, spec 12.5).
func (e *Engine) RunForFeature(ctx context.Context, name string, feature kernel.ID) error {
	f, ok := e.d.Flows.Get(name)
	if !ok {
		return kernel.NewError(kernel.CodeNotFound, "unknown flow %q", name)
	}
	_, err := e.Run(ctx, Request{Flow: f, Principal: accessapi.System(), FeatureID: &feature, Background: true,
		Trigger: map[string]any{"kind": "migration"}})
	return err
}
