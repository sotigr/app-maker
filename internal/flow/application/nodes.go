package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"text/template"
	"time"

	"golang.org/x/text/encoding/htmlindex"

	contentapi "organon/internal/content/api"
	"organon/internal/flow/domain"
	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
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
				param("subject", rt.String, false, "subject (default from template)"), mapParam("vars", "template variables"),
				param("attachments", rt.ListOf(rt.Any), false, "files: blob values, or {blob, name}"),
				param("lang", rt.String, false, "language: uses templates/email/<name>.<lang>.txt when it exists")},
			out: rt.MapOf(map[string]*rt.Type{"queued": rt.Bool}),
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				atts, err := attachments(in["attachments"])
				if err != nil {
					return nil, err
				}
				args, _ := json.Marshal(map[string]any{"to": in["to"], "template": in["template"], "subject": in["subject"],
					"vars": toWire(in["vars"]), "delivery": r.RunID().String() + ":" + r.NodeID(), "attachments": atts, "lang": in["lang"]})
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
		&builtin{name: "emit", kind: domain.KindEffect,
			desc: "Raises a custom event (e.g. task.reminded) about a feature after commit; flows with that event trigger run next.",
			inputs: []domain.Param{param("event", rt.String, true, "event name with a dot"), param("feature", rt.String, true, "feature ID"),
				mapParam("data", "event data")},
			out: rt.MapOf(map[string]*rt.Type{"queued": rt.Bool}),
			exec: func(_ context.Context, in map[string]any, r domain.Runtime) (any, error) {
				name := str(in["event"])
				if !domain.CustomEvent.MatchString(name) {
					return nil, kernel.NewError(kernel.CodeValidationFailed, "event names are dotted lower-case words, got %q", name)
				}
				pid := r.Principal().ID
				args, err := json.Marshal(emitArgs{Event: name, Feature: str(in["feature"]), Data: toWire(in["data"]), Principal: &pid, Causation: r.Causation()})
				if err != nil {
					return nil, err
				}
				r.Effect(kernel.JobRequest{Kind: JobEmitEvent, Queue: kernel.QueueEvents, Args: args, MaxAttempts: 10})
				return map[string]any{"queued": true}, nil
			}},
		// External.
		&builtin{name: "document.render", kind: domain.KindExternal,
			desc: "Renders templates/documents/<template>.json to a PDF and stores it for a Blob field; set the output on that field with feature.update.",
			inputs: []domain.Param{param("template", rt.String, true, "templates/documents/<name>"), mapParam("vars", "template vars"),
				param("type", rt.String, true, "type of the field the PDF is for"), param("field", rt.String, true, "Blob field, e.g. '/invoicePdf'")},
			out: rt.MapOf(map[string]*rt.Type{"id": rt.String, "size": rt.Int, "contentType": rt.String}),
			exec: func(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
				vars, _ := in["vars"].(map[string]any)
				b, err := r.Document(ctx, str(in["template"]), vars, str(in["type"]), str(in["field"]))
				if err != nil {
					return nil, err
				}
				return map[string]any{"id": b.ID, "size": b.Size, "contentType": b.ContentType}, nil
			}},
		&builtin{name: "http.request", kind: domain.KindExternal, desc: "Calls an HTTP API through the egress policy (GET, HEAD, or idempotent calls).",
			inputs: []domain.Param{param("url", rt.String, true, "URL"), param("method", rt.String, false, "GET by default"),
				mapParam("headers", "request headers"), param("body", rt.Any, false, "JSON body"),
				param("rawBody", rt.String, false, "text body sent as is (text/plain unless a Content-Type header is set), e.g. XML"),
				mapParam("form", "form fields sent as application/x-www-form-urlencoded; values may use ${secret:name}")},
			out:  rt.MapOf(map[string]*rt.Type{"status": rt.Int, "body": rt.Any, "headers": rt.OpenMap()}),
			exec: httpRequest},
		&builtin{name: "llm.complete", kind: domain.KindExternal,
			desc: "Calls a language model by alias (default, fast, large); with an outputSchema the reply is that JSON document, otherwise {text}.",
			inputs: []domain.Param{param("prompt", rt.String, false, "user message"), param("model", rt.String, false, "model alias; 'default' by default"),
				param("system", rt.String, false, "system prompt"), param("messages", rt.ListOf(rt.Any), false, "prior turns [{role, content}]"),
				param("maxTokens", rt.Int, false, "output token limit"), param("temperature", rt.Float, false, "sampling temperature (ignored by providers that reject it)"),
				param("streamTo", rt.Any, false, "{feature, path}: stream partial text to an ephemeral field")},
			out:  rt.MapOf(map[string]*rt.Type{"text": rt.String}),
			exec: llmComplete},
		// Code.
		&builtin{name: "python", kind: domain.KindCode,
			desc: "Runs a Python function run(input, ctx) from nodes/ on a runner; inputs follow the node's inputSchema.",
			out:  rt.OpenMap(), exec: runCode},
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

// llmComplete runs the llm.complete node (spec 11.7).
func llmComplete(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
	req := rt.LLMRequest{Model: str(in["model"]), System: str(in["system"]), OutputSchema: r.OutputSchema()}
	if req.Model == "" {
		req.Model = "default"
	}
	if n, ok := in["maxTokens"].(int64); ok {
		req.MaxTokens = int(n)
	}
	if t, ok := in["temperature"].(float64); ok {
		req.Temperature = &t
	}
	msgs, _ := in["messages"].([]any)
	for i, m := range msgs {
		mm, _ := m.(map[string]any)
		role := str(mm["role"])
		if role != "user" && role != "assistant" {
			return nil, kernel.NewError(kernel.CodeBadRequest, "messages[%d].role must be user or assistant", i)
		}
		req.Messages = append(req.Messages, rt.LLMMessage{Role: role, Content: str(mm["content"])})
	}
	if p := str(in["prompt"]); p != "" {
		req.Messages = append(req.Messages, rt.LLMMessage{Role: "user", Content: p})
	}
	if len(req.Messages) == 0 {
		return nil, kernel.NewError(kernel.CodeBadRequest, "llm.complete needs a prompt or messages")
	}
	var onDelta func(rt.LLMDelta) error
	var st *streamer
	if target, ok := in["streamTo"].(map[string]any); ok && target != nil {
		st = &streamer{ctx: ctx, r: r, feature: str(target["feature"]), path: str(target["path"])}
		onDelta = st.delta
	}
	res, err := r.LLM(ctx, req, onDelta)
	if st != nil {
		st.flush()
	}
	if err != nil {
		var le *rt.LLMError
		var ke *kernel.Error
		switch {
		case errors.As(err, &le) && !le.Retryable, errors.As(err, &ke):
			return nil, err
		default:
			return nil, &retryable{err}
		}
	}
	if len(req.OutputSchema) == 0 {
		return map[string]any{"text": res.Text}, nil
	}
	out, err := fromJSON([]byte(res.Text))
	if err != nil {
		return nil, &retryable{fmt.Errorf("the model reply is not JSON (stop reason %s): %w", res.StopReason, err)}
	}
	return out, nil
}

// streamer forwards partial LLM output to an ephemeral field, at most
// five times per second (the gateway allows 30 ephemeral writes per second
// per feature). Failures stop streaming without failing the node.
type streamer struct {
	ctx     context.Context
	r       domain.Runtime
	feature string
	path    string
	text    strings.Builder
	last    time.Time
	sent    int
	off     bool
}

func (s *streamer) delta(d rt.LLMDelta) error {
	s.text.WriteString(d.Text)
	if time.Since(s.last) >= 200*time.Millisecond {
		s.publish(s.ctx)
	}
	return nil
}

func (s *streamer) flush() {
	if s.text.Len() != s.sent {
		s.publish(s.ctx)
	}
}

func (s *streamer) publish(ctx context.Context) {
	if s.off {
		return
	}
	s.last, s.sent = time.Now(), s.text.Len()
	if err := s.r.Ephemeral(ctx, s.feature, map[string]any{s.path: s.text.String()}); err != nil {
		s.off = true
		s.r.Log("llm.complete streamTo disabled", "err", err)
	}
}

// fromJSON decodes JSON into expression values: integral numbers become
// int64, others float64.
func fromJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the JSON document")
	}
	return numbers(v), nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
	}
	return v
}

// runCode runs a code node (spec 11.1). Worker crashes and unhandled
// exceptions are retryable when the node declares retry (spec 11.6).
func runCode(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
	out, err := r.RunCode(ctx, in)
	if err != nil {
		var ce *rt.CodeError
		if errors.As(err, &ce) {
			err = fmt.Errorf("code node: %w", ce)
			if ce.Retryable || ce.Unhandled {
				return nil, &retryable{err}
			}
		}
		return nil, err
	}
	return fromJSON(out)
}

// withSecrets replaces ${secret:name} in s after evaluation (ADR 0019);
// the run record keeps the references, never the values.
func withSecrets(s, host string, r domain.Runtime, escape bool) (string, error) {
	var err error
	out := domain.SecretRef.ReplaceAllStringFunc(s, func(m string) string {
		name := domain.SecretRef.FindStringSubmatch(m)[1]
		v, e := r.Secret(name, host)
		if e != nil && err == nil {
			err = e
		}
		if escape {
			return url.QueryEscape(v)
		}
		return v
	})
	return out, err
}

// secretURL substitutes secrets in the query string of raw; the host is
// fixed before substitution, and secrets elsewhere in the URL are refused.
func secretURL(raw string, r domain.Runtime) (string, error) {
	if !domain.SecretRef.MatchString(raw) {
		return raw, nil
	}
	base, query, _ := strings.Cut(raw, "?")
	if domain.SecretRef.MatchString(base) {
		return "", kernel.NewError(kernel.CodeValidationFailed, "secrets may only appear in the query string of a URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	q, err := withSecrets(query, u.Hostname(), r, true)
	if err != nil {
		return "", err
	}
	return base + "?" + q, nil
}

// MaxHTTPBody bounds an outbound request body of any kind (JSON, raw, or
// form), like the response body (ADR 0025).
const MaxHTTPBody = 1 << 20

// requestBody builds the body of an outbound request from exactly one of
// body (JSON), rawBody (text, sent byte for byte), or form (URL-encoded;
// values may hold secrets for host). It returns the default content type.
func requestBody(in map[string]any, host string, r domain.Runtime) ([]byte, string, error) {
	var set []string
	for _, k := range []string{"body", "rawBody", "form"} {
		if v, ok := in[k]; ok && v != nil {
			set = append(set, k)
		}
	}
	if len(set) > 1 {
		return nil, "", kernel.NewError(kernel.CodeValidationFailed, "http.request takes one of body, rawBody, or form, not %s", strings.Join(set, " and "))
	}
	if len(set) == 0 {
		return nil, "", nil
	}
	var raw []byte
	ctype := "application/json"
	switch set[0] {
	case "body":
		b, err := json.Marshal(toWire(in["body"]))
		if err != nil {
			return nil, "", err
		}
		raw = b
	case "rawBody":
		s, ok := in["rawBody"].(string)
		if !ok {
			return nil, "", kernel.NewError(kernel.CodeValidationFailed, "rawBody must be text")
		}
		raw, ctype = []byte(s), "text/plain; charset=utf-8"
	case "form":
		fields, ok := in["form"].(map[string]any)
		if !ok {
			return nil, "", kernel.NewError(kernel.CodeValidationFailed, "form must be an object of text values")
		}
		vals := url.Values{}
		for k, v := range fields {
			if v == nil {
				continue
			}
			switch v.(type) {
			case map[string]any, []any:
				return nil, "", kernel.NewError(kernel.CodeValidationFailed, "form field %s must be text, a number, or a boolean", k)
			}
			text, isStr := v.(string)
			if !isStr {
				text = fmt.Sprint(toWire(v)) // numbers, decimals, times, and booleans as JSON writes them
			}
			val, err := withSecrets(text, host, r, false)
			if err != nil {
				return nil, "", err
			}
			vals.Set(k, val)
		}
		raw, ctype = []byte(vals.Encode()), "application/x-www-form-urlencoded"
	}
	if len(raw) > MaxHTTPBody {
		return nil, "", kernel.NewError(kernel.CodeValidationFailed, "request body is %d bytes; the limit is %d", len(raw), MaxHTTPBody)
	}
	return raw, ctype, nil
}

// httpRequest serves http.request and code nodes' ctx.http (ADR 0019,
// ADR 0025).
func httpRequest(ctx context.Context, in map[string]any, r domain.Runtime) (any, error) {
	method := strings.ToUpper(str(in["method"]))
	if method == "" {
		method = http.MethodGet
	}
	target, err := secretURL(str(in["url"]), r)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil, kernel.NewError(kernel.CodeValidationFailed, "invalid url")
	}
	raw, ctype, err := requestBody(in, u.Hostname(), r)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if raw != nil {
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	if raw != nil {
		req.Header.Set("Content-Type", ctype)
	}
	if h, ok := in["headers"].(map[string]any); ok {
		for k, v := range h {
			val, err := withSecrets(fmt.Sprint(v), req.URL.Hostname(), r, false)
			if err != nil {
				return nil, err
			}
			req.Header.Set(k, val) // a Content-Type set by the flow wins
		}
	}
	res, err := r.HTTP(ctx, req)
	if err != nil {
		// url.Error repeats the URL, which may hold a secret.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = fmt.Errorf("%s %s: %w", method, req.URL.Host, ue.Err)
		}
		return nil, &retryable{err}
	}
	defer func() { _ = res.Body.Close() }()
	raw, err = io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, &retryable{err}
	}
	if res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests {
		return nil, &retryable{fmt.Errorf("HTTP %d", res.StatusCode)}
	}
	headers := map[string]any{}
	for k := range res.Header {
		headers[k] = res.Header.Get(k)
	}
	return map[string]any{"status": int64(res.StatusCode), "body": responseBody(res.Header.Get("Content-Type"), raw), "headers": headers}, nil
}

// responseBody decodes a response: JSON when the content type is JSON (or
// absent, or text/plain, as before), otherwise text decoded by its charset
// (UTF-8 by default). 4xx answers are returned the same way.
func responseBody(contentType string, raw []byte) any {
	media, params, _ := mime.ParseMediaType(contentType)
	jsonish := media == "" || media == "text/plain" || strings.HasSuffix(media, "/json") || strings.HasSuffix(media, "+json")
	if jsonish {
		var js any
		if json.Unmarshal(raw, &js) == nil {
			return js
		}
	}
	if cs := strings.ToLower(params["charset"]); cs != "" && cs != "utf-8" && cs != "utf8" {
		if enc, err := htmlindex.Get(cs); err == nil {
			if dec, err := enc.NewDecoder().Bytes(raw); err == nil {
				return string(dec)
			}
		}
	}
	return strings.ToValidUTF8(string(raw), "\uFFFD")
}

// attachment is an email attachment in deliver_email arguments.
type attachment struct {
	Blob string `json:"blob"`
	Name string `json:"name,omitempty"`
}

// attachments reads email.send attachments: blob values ({id, ...}) or
// {blob, name}.
func attachments(v any) ([]attachment, error) {
	if v == nil {
		return nil, nil
	}
	list, _ := v.([]any)
	var out []attachment
	for _, it := range list {
		a := attachment{}
		switch x := it.(type) {
		case map[string]any:
			if b, ok := x["blob"]; ok {
				a.Name = str(x["name"])
				x, _ = b.(map[string]any)
				if x == nil {
					if kb, ok := b.(kernel.Blob); ok {
						x = map[string]any{"id": kb.ID}
					}
				}
			}
			a.Blob = str(x["id"])
		case kernel.Blob:
			a.Blob = x.ID
		}
		if a.Blob == "" {
			return nil, kernel.NewError(kernel.CodeValidationFailed, "an attachment is a file value or {blob, name}")
		}
		out = append(out, a)
	}
	return out, nil
}
