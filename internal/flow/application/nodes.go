package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"

	contentapi "app-maker/internal/content/api"
	"app-maker/internal/flow/domain"
	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
)

type builtin struct {
	name, desc string
	kind       domain.NodeKind
	inputs     []domain.Param
	out        *rt.Type
	exec       func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error)
}

func (b *builtin) Name() string           { return b.name }
func (b *builtin) Kind() domain.NodeKind  { return b.kind }
func (b *builtin) Description() string    { return b.desc }
func (b *builtin) Inputs() []domain.Param { return b.inputs }
func (b *builtin) Output() *rt.Type       { return b.out }
func (b *builtin) Execute(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
	return b.exec(ctx, in, r)
}

func param(name string, t *rt.Type, required bool, doc string) domain.Param {
	return domain.Param{Name: name, Type: t, TypeName: t.String(), Required: required, Doc: doc}
}

func mapParam(name, doc string) domain.Param {
	return domain.Param{Name: name, Type: rt.OpenMap(), TypeName: "map", Map: true, Doc: doc}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	}
	return 0
}

// setToData turns {"/a": v, "/b/c": w} into a merge-patch document.
func setToData(set map[string]any) map[string]any {
	out := map[string]any{}
	for path, v := range set {
		segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
		cur := out
		for i, s := range segs {
			if i == len(segs)-1 {
				cur[s] = v
				break
			}
			next, ok := cur[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[s] = next
			}
			cur = next
		}
	}
	return out
}

// toWire converts expression values (time.Time, Decimal, int64) to JSON.
func toWire(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&out) != nil {
		return v
	}
	return out
}

func featureMap() *rt.Type { return rt.OpenMap() }

// Builtins returns the built-in node catalog (spec 10.5) except code and
// LLM nodes, which arrive with the runner (phase 12).
func Builtins() []domain.NodeType {
	idOut := rt.MapOf(map[string]*rt.Type{"id": rt.String})
	return []domain.NodeType{
		// Pure.
		&builtin{name: "expr", kind: domain.KindPure, desc: "Evaluates an expression.",
			inputs: []domain.Param{param("expr", rt.Any, true, "the value")}, out: rt.Any,
			exec: func(_ context.Context, in map[string]any, _ domain.Runtime) (any, error) { return in["expr"], nil }},
		&builtin{name: "template", kind: domain.KindPure, desc: "Renders a Go text/template string with vars.",
			inputs: []domain.Param{param("text", rt.String, true, "template text"), mapParam("vars", "template variables")}, out: rt.String,
			exec: func(_ context.Context, in map[string]any, _ domain.Runtime) (any, error) {
				t, err := template.New("t").Option("missingkey=error").Parse(str(in["text"]))
				if err != nil {
					return nil, err
				}
				var b strings.Builder
				if err := t.Execute(&b, in["vars"]); err != nil {
					return nil, err
				}
				return b.String(), nil
			}},
		// DataRead.
		&builtin{name: "feature.get", kind: domain.KindDataRead, desc: "Reads a feature as the run's principal (through the write set).",
			inputs: []domain.Param{param("id", rt.String, true, "feature ID")}, out: featureMap(),
			exec: func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
				f, err := r.Read(ctx, str(in["id"]))
				return map[string]any(f), err
			}},
		&builtin{name: "feature.query", kind: domain.KindDataRead, desc: "Runs a query document (spec 7.1).",
			inputs: []domain.Param{param("type", rt.String, true, "type or trait"), param("where", rt.Any, false, "where clause"),
				param("sort", rt.Any, false, "sort keys"), param("scope", rt.Any, false, "scope"), param("limit", rt.Int, false, "page size")},
			out: rt.ListOf(featureMap()),
			exec: func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
				doc := map[string]any{"type": in["type"]}
				for _, k := range []string{"where", "sort", "scope", "limit"} {
					if v, ok := in[k]; ok && v != nil {
						doc[k] = toWire(v)
					}
				}
				fs, err := r.Query(ctx, doc)
				out := make([]any, len(fs))
				for i, f := range fs {
					out[i] = map[string]any(f)
				}
				return out, err
			}},
		&builtin{name: "feature.children", kind: domain.KindDataRead, desc: "Lists readable children.",
			inputs: []domain.Param{param("id", rt.String, true, "parent ID"), param("type", rt.String, false, "child type"), param("limit", rt.Int, false, "page size")},
			out:    rt.ListOf(featureMap()),
			exec: func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
				fs, err := r.Children(ctx, str(in["id"]), str(in["type"]), asInt(in["limit"]))
				out := make([]any, len(fs))
				for i, f := range fs {
					out[i] = map[string]any(f)
				}
				return out, err
			}},
		// DataWrite.
		&builtin{name: "feature.create", kind: domain.KindDataWrite, desc: "Stages a create; committed with the run.",
			inputs: []domain.Param{param("type", rt.String, true, "type name"), param("parent", rt.String, false, "parent ID (default root)"),
				mapParam("data", "field values")}, out: idOut,
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				data, _ := toWire(in["data"]).(map[string]any)
				id, err := r.Stage(contentapi.WriteOp{Op: "create", Type: str(in["type"]), Parent: str(in["parent"]), Data: data})
				return map[string]any{"id": id}, err
			}},
		&builtin{name: "feature.update", kind: domain.KindDataWrite, desc: "Stages a merge patch; set maps JSON Pointers to values.",
			inputs: []domain.Param{param("id", rt.String, true, "feature ID"), mapParam("set", "JSON Pointer -> value"),
				param("version", rt.Int, false, "expected version (default: current)")}, out: idOut,
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				set, _ := toWire(in["set"]).(map[string]any)
				id, err := r.Stage(contentapi.WriteOp{Op: "update", ID: str(in["id"]), Version: kernel.Version(asInt(in["version"])), Data: setToData(set)})
				return map[string]any{"id": id}, err
			}},
		&builtin{name: "feature.delete", kind: domain.KindDataWrite, desc: "Stages a soft delete.",
			inputs: []domain.Param{param("id", rt.String, true, "feature ID"), param("version", rt.Int, false, "expected version")}, out: idOut,
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				id, err := r.Stage(contentapi.WriteOp{Op: "delete", ID: str(in["id"]), Version: kernel.Version(asInt(in["version"]))})
				return map[string]any{"id": id}, err
			}},
		&builtin{name: "feature.move", kind: domain.KindDataWrite, desc: "Stages a move to a new parent.",
			inputs: []domain.Param{param("id", rt.String, true, "feature ID"), param("parent", rt.String, true, "new parent ID"),
				param("version", rt.Int, false, "expected version")}, out: idOut,
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				id, err := r.Stage(contentapi.WriteOp{Op: "move", ID: str(in["id"]), Parent: str(in["parent"]), Version: kernel.Version(asInt(in["version"]))})
				return map[string]any{"id": id}, err
			}},
		// Effects (spec 14.5): staged as jobs, delivered after commit.
		&builtin{name: "email.send", kind: domain.KindEffect, desc: "Sends an email from a manifest template after commit.",
			inputs: []domain.Param{param("to", rt.String, true, "recipient"), param("template", rt.String, true, "templates/email/<name>"),
				param("subject", rt.String, false, "subject (default from template)"), mapParam("vars", "template variables")},
			out: rt.MapOf(map[string]*rt.Type{"queued": rt.Bool}),
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				args, _ := json.Marshal(map[string]any{"to": in["to"], "template": in["template"], "subject": in["subject"],
					"vars": toWire(in["vars"]), "delivery": r.RunID().String() + ":" + r.NodeID()})
				r.Effect(kernel.JobRequest{Kind: JobDeliverEmail, Queue: kernel.QueueEffects, Args: args, MaxAttempts: 25})
				return map[string]any{"queued": true}, nil
			}},
		&builtin{name: "webhook.call", kind: domain.KindEffect, desc: "POSTs a signed JSON payload after commit.",
			inputs: []domain.Param{param("url", rt.String, true, "target URL"), param("payload", rt.Any, false, "JSON body"),
				param("secret", rt.String, false, "signing secret reference ${secret:name}")},
			out: rt.MapOf(map[string]*rt.Type{"queued": rt.Bool}),
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				args, _ := json.Marshal(map[string]any{"url": in["url"], "payload": toWire(in["payload"]), "secret": in["secret"],
					"delivery": r.RunID().String() + ":" + r.NodeID()})
				r.Effect(kernel.JobRequest{Kind: JobDeliverWebhook, Queue: kernel.QueueEffects, Args: args, MaxAttempts: 25})
				return map[string]any{"queued": true}, nil
			}},
		// External.
		&builtin{name: "http.request", kind: domain.KindExternal, desc: "Calls an HTTP API through the egress policy (GET, HEAD, or idempotent calls).",
			inputs: []domain.Param{param("url", rt.String, true, "URL"), param("method", rt.String, false, "GET by default"),
				mapParam("headers", "request headers"), param("body", rt.Any, false, "JSON body")},
			out: rt.MapOf(map[string]*rt.Type{"status": rt.Int, "body": rt.Any, "headers": rt.OpenMap()}),
			exec: func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
				method := strings.ToUpper(str(in["method"]))
				if method == "" {
					method = http.MethodGet
				}
				var body io.Reader
				if b, ok := in["body"]; ok && b != nil {
					raw, err := json.Marshal(toWire(b))
					if err != nil {
						return nil, err
					}
					body = bytes.NewReader(raw)
				}
				req, err := http.NewRequestWithContext(ctx, method, str(in["url"]), body)
				if err != nil {
					return nil, err
				}
				if h, ok := in["headers"].(map[string]any); ok {
					for k, v := range h {
						req.Header.Set(k, fmt.Sprint(v))
					}
				}
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				res, err := r.HTTP(ctx, req)
				if err != nil {
					return nil, &retryable{err}
				}
				defer func() { _ = res.Body.Close() }()
				raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				if err != nil {
					return nil, &retryable{err}
				}
				if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
					return nil, &retryable{fmt.Errorf("HTTP %d", res.StatusCode)}
				}
				var parsed any = string(raw)
				var js any
				if json.Unmarshal(raw, &js) == nil {
					parsed = js
				}
				headers := map[string]any{}
				for k := range res.Header {
					headers[k] = res.Header.Get(k)
				}
				return map[string]any{"status": int64(res.StatusCode), "body": parsed, "headers": headers}, nil
			}},
		// Control.
		&builtin{name: "if", kind: domain.KindControl, desc: "A condition; use when: nodes.<if>.out on the branches.",
			inputs: []domain.Param{param("cond", rt.Bool, true, "condition")}, out: rt.Bool,
			exec: func(_ context.Context, in map[string]any, _ domain.Runtime) (any, error) {
				return in["cond"] == true, nil
			}},
		&builtin{name: "switch", kind: domain.KindControl, desc: "Selects a value; branches compare nodes.<switch>.out.",
			inputs: []domain.Param{param("value", rt.Any, true, "value"), param("cases", rt.Any, false, "allowed values")}, out: rt.Any,
			exec: func(_ context.Context, in map[string]any, _ domain.Runtime) (any, error) {
				cases, _ := in["cases"].([]any)
				if len(cases) == 0 {
					return in["value"], nil
				}
				for _, c := range cases {
					if fmt.Sprint(c) == fmt.Sprint(in["value"]) {
						return c, nil
					}
				}
				return "default", nil
			}},
		&builtin{name: "forEach", kind: domain.KindControl, desc: "Runs the do subgraph per item (bounded by maxItems); outputs a list.",
			inputs: []domain.Param{param("items", rt.ListOf(rt.Any), true, "items")}, out: rt.ListOf(rt.Any),
			exec: func(context.Context, map[string]any, domain.Runtime) (any, error) {
				return nil, fmt.Errorf("forEach is executed by the engine")
			}},
		&builtin{name: "subflow", kind: domain.KindControl, desc: "Runs another flow inline (depth at most 4).",
			inputs: []domain.Param{param("flow", rt.String, true, "flow name"), mapParam("input", "input")}, out: rt.Any,
			exec: func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
				input, _ := in["input"].(map[string]any)
				return r.Subflow(ctx, str(in["flow"]), input)
			}},
		&builtin{name: "reject", kind: domain.KindControl, desc: "Aborts a beforeWrite or action flow with a declared error.",
			inputs: []domain.Param{param("code", rt.String, true, "error code"), param("message", rt.String, false, "message"),
				param("status", rt.Int, false, "HTTP status (default 400)")}, out: rt.Any,
			exec: func(_ context.Context, in map[string]any, _ domain.Runtime) (any, error) {
				st := asInt(in["status"])
				if st == 0 {
					st = http.StatusBadRequest
				}
				return nil, &domain.RejectError{Code: str(in["code"]), Message: str(in["message"]), Status: st}
			}},
		&builtin{name: "fail", kind: domain.KindControl, desc: "Fails the run.",
			inputs: []domain.Param{param("message", rt.String, false, "message")}, out: rt.Any,
			exec: func(_ context.Context, in map[string]any, _ domain.Runtime) (any, error) {
				return nil, &domain.FailError{Message: str(in["message"])}
			}},
	}
}

// retryable marks errors External nodes may retry (spec 10.10).
type retryable struct{ err error }

func (r *retryable) Error() string { return r.err.Error() }
func (r *retryable) Unwrap() error { return r.err }

// Job kinds of the Integration context's effect workers (spec 2.5).
const (
	JobDeliverEmail   = "deliver_email"
	JobDeliverWebhook = "deliver_webhook"
	JobRunFlow        = "run_flow"
)
