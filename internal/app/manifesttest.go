package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	accessapi "organon/internal/access/api"
	accessapp "organon/internal/access/application"
	contentapp "organon/internal/content/application"
	flowapp "organon/internal/flow/application"
	"organon/internal/kernel"
	"organon/platform/config"
)

// ManifestTest is a test file of the manifest (spec 12.6).
type ManifestTest struct {
	Name  string     `json:"name"`
	Given TestGiven  `json:"given"`
	Steps []TestStep `json:"steps"`
	// HTTP fixtures for External nodes; unmatched requests fail.
	HTTP []HTTPFixture `json:"http,omitempty"`
}

// TestGiven declares fixtures. Principal names are local labels.
type TestGiven struct {
	Principals map[string]TestPrincipal `json:"principals"`
	Features   []TestFeature            `json:"features"`
	Grants     []TestGrant              `json:"grants"`
}

// TestPrincipal is a fixture principal.
type TestPrincipal struct {
	Kind  string   `json:"kind"`
	Roles []string `json:"roles,omitempty"`
}

// TestFeature is a fixture feature, created as the system.
type TestFeature struct {
	Ref    string         `json:"ref"`
	Type   string         `json:"type"`
	Parent string         `json:"parent,omitempty"`
	Owner  string         `json:"owner,omitempty"`
	Data   map[string]any `json:"data"`
}

// TestGrant is a fixture grant.
type TestGrant struct {
	Subject string `json:"subject"`
	Object  string `json:"object"`
	Role    string `json:"role"`
}

// TestStep is one operation and its expectation.
type TestStep struct {
	As     string         `json:"as"`
	Action string         `json:"action,omitempty"`
	On     string         `json:"on,omitempty"`
	Input  map[string]any `json:"input,omitempty"`
	Create string         `json:"create,omitempty"`
	Parent string         `json:"parent,omitempty"`
	Ref    string         `json:"ref,omitempty"`
	Update string         `json:"update,omitempty"`
	Delete string         `json:"delete,omitempty"`
	Read   string         `json:"read,omitempty"`
	Query  map[string]any `json:"query,omitempty"`
	// Page checks whether the principal is offered a UI page (ADR 0016).
	Page   string         `json:"page,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
	Expect TestExpect     `json:"expect"`
}

// TestExpect is a step's expectation.
type TestExpect struct {
	OK      *bool          `json:"ok,omitempty"`
	Error   string         `json:"error,omitempty"`
	Feature map[string]any `json:"feature,omitempty"`
	Output  map[string]any `json:"output,omitempty"`
	Count   *int           `json:"count,omitempty"`
	Effects []TestEffect   `json:"effects,omitempty"`
	// Visible is the expectation of a page step.
	Visible *bool `json:"visible,omitempty"`
}

// TestEffect counts effects staged by a step, by node type.
type TestEffect struct {
	Node  string `json:"node"`
	Count int    `json:"count"`
}

// HTTPFixture answers an outbound request of an External node.
type HTTPFixture struct {
	Method string `json:"method,omitempty"`
	URL    string `json:"url"`
	Status int    `json:"status"`
	Body   any    `json:"body,omitempty"`
}

// TestResult is the outcome of one manifest test.
type TestResult struct {
	File     string   `json:"file"`
	Name     string   `json:"name"`
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
}

// effectRecorder counts effect jobs by kind; effects are recorded, not
// delivered (spec 12.6).
type effectRecorder struct {
	mu     sync.Mutex
	counts map[string]int
}

func (r *effectRecorder) handler(kind string) func(context.Context, json.RawMessage) error {
	return func(context.Context, json.RawMessage) error {
		r.mu.Lock()
		r.counts[kind]++
		r.mu.Unlock()
		return nil
	}
}

func (r *effectRecorder) take() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.counts
	r.counts = map[string]int{}
	return out
}

var effectKinds = map[string]string{"email.send": flowapp.JobDeliverEmail, "webhook.call": flowapp.JobDeliverWebhook}

type fixtureHTTP struct{ fixtures []HTTPFixture }

func (f fixtureHTTP) Do(req *http.Request) (*http.Response, error) {
	for _, fx := range f.fixtures {
		if fx.URL == req.URL.String() && (fx.Method == "" || strings.EqualFold(fx.Method, req.Method)) {
			b, _ := json.Marshal(fx.Body)
			return &http.Response{StatusCode: fx.Status, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
		}
	}
	return nil, fmt.Errorf("no HTTP fixture for %s %s", req.Method, req.URL)
}

// RunManifestTests runs every test of the compiled manifest against a fresh
// in-memory instance each (spec 12.3 organon test).
func RunManifestTests(ctx context.Context, c *Compiled) ([]TestResult, error) {
	var results []TestResult
	for _, f := range c.Manifest.Tests {
		var tests []ManifestTest
		trimmed := bytes.TrimSpace(f.Data)
		if len(trimmed) > 0 && trimmed[0] == '[' {
			if err := json.Unmarshal(f.Data, &tests); err != nil {
				return nil, fmt.Errorf("%s: %w", f.Path, err)
			}
		} else {
			var one ManifestTest
			if err := json.Unmarshal(f.Data, &one); err != nil {
				return nil, fmt.Errorf("%s: %w", f.Path, err)
			}
			tests = []ManifestTest{one}
		}
		for _, t := range tests {
			res := runManifestTest(ctx, c, t)
			res.File = f.Path
			results = append(results, res)
		}
	}
	return results, nil
}

func runManifestTest(ctx context.Context, c *Compiled, t ManifestTest) (res TestResult) {
	res = TestResult{Name: t.Name, Passed: true}
	fail := func(format string, args ...any) {
		res.Passed = false
		res.Failures = append(res.Failures, fmt.Sprintf(format, args...))
	}
	cfg := config.Default()
	cfg.LLM.Provider = "fake" // manifest tests never call real models
	cfg.Manifest = c.Manifest.Dir
	cfg.Bootstrap.AdminEmail, cfg.Bootstrap.AdminPassword = "admin@test.local", "test-admin-password"
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	effects := &effectRecorder{counts: map[string]int{}}
	rt, err := Build(ctx, cfg, c, Options{Clock: kernel.SystemClock{}, FastHashing: true, Log: slog.New(slog.DiscardHandler),
		JobBackoff: time.Millisecond, Secrets: func(string) (string, bool) { return "test-secret", true }})
	if err != nil {
		fail("build: %v", err)
		return res
	}
	defer rt.Close(context.Background())
	rt.JobKinds.Register(flowapp.JobDeliverEmail, effects.handler(flowapp.JobDeliverEmail), 0)
	rt.JobKinds.Register(flowapp.JobDeliverWebhook, effects.handler(flowapp.JobDeliverWebhook), 0)
	rt.Engine.SetHTTP(fixtureHTTP{fixtures: t.HTTP})
	if err := rt.Start(ctx); err != nil {
		fail("start: %v", err)
		return res
	}
	if err := rt.Bootstrap(ctx); err != nil {
		fail("bootstrap: %v", err)
		return res
	}
	refs := map[string]string{} // fixture ref -> feature ID
	names := make([]string, 0, len(t.Given.Principals))
	for n := range t.Given.Principals {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		p := t.Given.Principals[name]
		if p.Kind != "" && p.Kind != "user" {
			fail("principal %s: only user principals are supported in tests", name)
			return res
		}
		id, err := rt.Auth.CreateUser(ctx, accessapi.System(), accessapp.NewUser{Email: name + "@test.local", DisplayName: name,
			Password: "test-password-" + name, Roles: p.Roles})
		if err != nil {
			fail("principal %s: %v", name, err)
			return res
		}
		refs[name] = id.String()
	}
	resolve := func(ref string) string {
		if id, ok := refs[ref]; ok {
			return id
		}
		return ref
	}
	resolveData := func(data map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range data {
			if s, ok := v.(string); ok {
				out[k] = resolve(s)
			} else {
				out[k] = v
			}
		}
		return out
	}
	sys := accessapi.System()
	for i, f := range t.Given.Features {
		var owner *kernel.ID
		if f.Owner != "" {
			id, err := kernel.ParseID(resolve(f.Owner))
			if err != nil {
				fail("feature %d: unknown owner %q", i, f.Owner)
				return res
			}
			owner = &id
		}
		r, err := rt.Content.Execute(ctx, contentapp.WriteContext{Principal: sys, Fixture: true, Owner: owner}, []contentapp.Op{
			{Op: contentapp.OpCreate, Type: f.Type, Ref: "$f", Parent: resolve(f.Parent), Data: resolveData(f.Data)}})
		if err != nil {
			fail("feature %s: %v", f.Ref, err)
			return res
		}
		refs[f.Ref] = r.Refs["$f"]
	}
	for _, g := range t.Given.Grants {
		_, err := rt.Content.Execute(ctx, contentapp.WriteContext{Principal: sys, Fixture: true}, []contentapp.Op{
			{Op: contentapp.OpCreate, Type: "grant", Parent: resolve(g.Object), Data: map[string]any{"subject": resolve(g.Subject), "role": g.Role}}})
		if err != nil {
			fail("grant %s/%s: %v", g.Subject, g.Object, err)
			return res
		}
	}
	for i, st := range t.Steps {
		label := fmt.Sprintf("step %d", i+1)
		p := accessapi.Anonymous()
		if st.As != "" {
			id, err := kernel.ParseID(resolve(st.As))
			if err != nil {
				fail("%s: unknown principal %q", label, st.As)
				continue
			}
			if p, err = rt.Auth.Resolve(ctx, id); err != nil {
				fail("%s: %v", label, err)
				continue
			}
		}
		_ = effects.take()
		var featureID string
		var output any
		count := -1
		var err error
		switch {
		case st.Action != "":
			var id kernel.ID
			if id, err = kernel.ParseID(resolve(st.On)); err == nil {
				var ar flowapp.ActionResult
				typeName := ""
				if f, _, lerr := rt.Content.Load(ctx, id); lerr == nil {
					if ct, ok := c.Types.Get(f.TypeID().Base()); ok {
						typeName = ct.Name
					}
				}
				ar, err = rt.Engine.InvokeAction(ctx, p, typeName, id, st.Action, resolveData(st.Input))
				output, featureID = ar.Output, resolve(st.On)
			}
		case st.Create != "":
			var r contentapp.TxResult
			r, err = rt.Content.Execute(ctx, contentapp.WriteContext{Principal: p}, []contentapp.Op{
				{Op: contentapp.OpCreate, Type: st.Create, Ref: "$s", Parent: resolve(st.Parent), Data: resolveData(st.Data)}})
			if err == nil {
				featureID = r.Refs["$s"]
				if st.Ref != "" {
					refs[st.Ref] = featureID
				}
			}
		case st.Update != "":
			featureID = resolve(st.Update)
			err = updateAs(ctx, rt, p, featureID, resolveData(st.Data))
		case st.Delete != "":
			err = deleteAs(ctx, rt, p, resolve(st.Delete))
		case st.Read != "":
			featureID = resolve(st.Read)
			var id kernel.ID
			if id, err = kernel.ParseID(featureID); err == nil {
				_, err = rt.Content.GetFeature(ctx, p, "", id)
			}
		case st.Page != "":
			checkPage(rt, p, st, label, fail)
			continue
		case st.Query != nil:
			raw, _ := json.Marshal(st.Query)
			var qr contentapp.QueryResult
			if qr, err = rt.Content.RunQuery(ctx, p, raw); err == nil {
				count = len(qr.Items)
			}
		default:
			fail("%s: no operation", label)
			continue
		}
		dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
		_ = rt.Jobs.Drain(dctx)
		dcancel()
		got := effects.take()
		ex := st.Expect
		switch {
		case ex.Error != "":
			if err == nil {
				fail("%s: want error %s, got success", label, ex.Error)
			} else if code := kernel.CodeOf(err); code != ex.Error {
				fail("%s: want error %s, got %s (%v)", label, ex.Error, code, err)
			}
			continue
		case err != nil:
			fail("%s: unexpected error %s: %v", label, kernel.CodeOf(err), err)
			continue
		}
		if ex.OK != nil && !*ex.OK {
			fail("%s: want failure, got success", label)
		}
		if ex.Count != nil && count != *ex.Count {
			fail("%s: want %d results, got %d", label, *ex.Count, count)
		}
		if ex.Feature != nil {
			id, perr := kernel.ParseID(featureID)
			if perr != nil {
				fail("%s: no feature to check", label)
			} else if v, gerr := rt.Content.GetFeature(ctx, sys, "", id); gerr != nil {
				fail("%s: reading the feature: %v", label, gerr)
			} else {
				for k, want := range ex.Feature {
					if !sameJSON(v.Data[k], want) {
						fail("%s: feature.%s = %v, want %v", label, k, v.Data[k], want)
					}
				}
			}
		}
		if ex.Output != nil {
			m, _ := output.(map[string]any)
			for k, want := range ex.Output {
				if !sameJSON(m[k], want) {
					fail("%s: output.%s = %v, want %v", label, k, m[k], want)
				}
			}
		}
		for _, e := range ex.Effects {
			kind, ok := effectKinds[e.Node]
			if !ok {
				fail("%s: unknown effect node %s", label, e.Node)
				continue
			}
			if got[kind] != e.Count {
				fail("%s: %s ran %d times, want %d", label, e.Node, got[kind], e.Count)
			}
		}
	}
	return res
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func updateAs(ctx context.Context, rt *Runtime, p accessapi.Principal, id string, data map[string]any) error {
	fid, err := kernel.ParseID(id)
	if err != nil {
		return kernel.ErrNotFound
	}
	f, ct, err := rt.Content.Load(ctx, fid)
	if err != nil {
		return err
	}
	_, err = rt.Content.Execute(ctx, contentapp.WriteContext{Principal: p}, []contentapp.Op{
		{Op: contentapp.OpUpdate, Type: ct.Name, ID: id, Version: f.Version(), Data: data}})
	return err
}

func deleteAs(ctx context.Context, rt *Runtime, p accessapi.Principal, id string) error {
	fid, err := kernel.ParseID(id)
	if err != nil {
		return kernel.ErrNotFound
	}
	f, ct, err := rt.Content.Load(ctx, fid)
	if err != nil {
		return err
	}
	_, err = rt.Content.Execute(ctx, contentapp.WriteContext{Principal: p}, []contentapp.Op{
		{Op: contentapp.OpDelete, Type: ct.Name, ID: id, Version: f.Version()}})
	return err
}

// checkPage runs a page step: is the page in the principal's UI bundle?
func checkPage(rt *Runtime, p accessapi.Principal, st TestStep, label string, fail func(string, ...any)) {
	known := false
	if ui := rt.App.UI; ui != nil {
		for _, pg := range ui.Pages {
			known = known || pg.Name == st.Page
		}
	}
	if !known {
		fail("%s: unknown page %q", label, st.Page)
		return
	}
	if st.Expect.Visible == nil {
		fail("%s: a page step expects visible", label)
		return
	}
	visible := false
	for _, pg := range rt.UI.Bundle(p).Pages {
		visible = visible || pg.Name == st.Page
	}
	if visible != *st.Expect.Visible {
		fail("%s: page %s visible = %v, want %v", label, st.Page, visible, *st.Expect.Visible)
	}
}
