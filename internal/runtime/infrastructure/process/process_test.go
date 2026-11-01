package process

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	rt "organon/internal/runtime/api"
	"organon/internal/runtime/application"
)

const nodeCode = `
import os, urllib.request
from helpers import shout

def run(input, ctx):
    mode = input.get("mode")
    if mode == "boom":
        raise ValueError("bad input")
    if mode == "loop":
        while True:
            pass
    if mode == "set":
        return {"x": {1, 2}}
    if mode == "net":
        try:
            urllib.request.urlopen("http://127.0.0.1:9/", timeout=5)
            return {"net": "open"}
        except Exception as e:
            return {"net": type(e).__name__}
    if mode == "env":
        return {"keys": sorted(k for k in os.environ if not k.lower().endswith("_proxy"))}
    print("this goes to the log, not the protocol")
    task = ctx.get_feature(input["taskId"])
    ctx.log("classified", title=task["title"])
    try:
        ctx.get_feature("missing")
    except Exception as e:
        missing = e.code
    return {"label": shout(task["title"]), "key": ctx.idempotency_key, "missing": missing,
            "budget": ctx.remaining_ms() > 0}
`

func newRunner(t *testing.T) (*application.Runner, rt.CodeBundle) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	envs := &Envs{Root: t.TempDir()}
	t.Cleanup(func() { _ = envs.RemoveAll() })
	r := application.NewRunner(application.RunnerConfig{MaxWorkers: 2}, envs, &Starter{Envs: envs}, nil)
	t.Cleanup(r.Close)
	b := rt.CodeBundle{Hash: "h1", Files: map[string][]byte{
		"classify.py": []byte(nodeCode),
		"helpers.py":  []byte("def shout(s):\n    return s.upper()\n"),
	}}
	if _, err := r.Load(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	return r, rt.CodeBundle{Hash: "h1"}
}

func host(t *testing.T, logged *[]string) rt.CodeHost {
	return func(_ context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
		switch method {
		case "feature.get":
			var a struct{ ID string }
			_ = json.Unmarshal(args, &a)
			if a.ID == "missing" {
				return nil, errors.New("not found")
			}
			return json.RawMessage(`{"id":"` + a.ID + `","title":"fix the build"}`), nil
		case "log":
			*logged = append(*logged, string(args))
			return json.RawMessage("null"), nil
		}
		t.Errorf("unexpected host call %s", method)
		return nil, errors.New("unexpected")
	}
}

func req(input string, timeout time.Duration, b rt.CodeBundle) rt.CodeRequest {
	return rt.CodeRequest{RunID: "r1", NodeID: "classify", Entry: "classify.py", IdempotencyKey: "r1:classify",
		Deadline: time.Now().Add(timeout), Input: json.RawMessage(input), Bundle: b}
}

// Spec 11.1-11.4: execution, callbacks, errors, limits, and pooling.
func TestProcessRunner(t *testing.T) {
	r, b := newRunner(t)
	ctx := context.Background()
	var logged []string
	out, err := r.Execute(ctx, req(`{"taskId":"t1"}`, 20*time.Second, b), host(t, &logged))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["label"] != "FIX THE BUILD" || got["key"] != "r1:classify" || got["missing"] != "host_error" || got["budget"] != true {
		t.Fatalf("output %s", out)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], `"title":"fix the build"`) {
		t.Fatalf("log callbacks %v", logged)
	}
	// The worker is reused.
	if _, err := r.Execute(ctx, req(`{"taskId":"t2"}`, 20*time.Second, b), host(t, &logged)); err != nil {
		t.Fatal(err)
	}
	if s := r.Stats(); len(s) != 1 || s[0].Warm != 1 || s[0].Busy != 0 {
		t.Fatalf("stats %+v", s)
	}

	// An unhandled exception is reported and recycles the worker.
	var ce *rt.CodeError
	_, err = r.Execute(ctx, req(`{"mode":"boom"}`, 20*time.Second, b), host(t, &logged))
	if !errors.As(err, &ce) || !ce.Unhandled || !strings.Contains(ce.Message, "ValueError: bad input") {
		t.Fatalf("exception: %v", err)
	}
	if s := r.Stats(); s[0].Warm != 0 {
		t.Fatalf("worker not recycled after an exception: %+v", s)
	}

	// Non-JSON output.
	if _, err := r.Execute(ctx, req(`{"mode":"set"}`, 20*time.Second, b), host(t, &logged)); !errors.As(err, &ce) || ce.Code != "invalid_output" {
		t.Fatalf("set output: %v", err)
	}

	// The deadline kills a busy worker.
	start := time.Now()
	_, err = r.Execute(ctx, req(`{"mode":"loop"}`, 500*time.Millisecond, b), host(t, &logged))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("deadline: %v after %v", err, time.Since(start))
	}

	// Network goes through the proxy, which denies everything not allowlisted.
	out, err = r.Execute(ctx, req(`{"mode":"net"}`, 20*time.Second, b), host(t, &logged))
	if err != nil || !strings.Contains(string(out), "HTTPError") {
		t.Fatalf("network: %s %v", out, err)
	}
	// No credentials in the environment.
	out, err = r.Execute(ctx, req(`{"mode":"env"}`, 20*time.Second, b), host(t, &logged))
	if err != nil || string(out) != `{"keys":["HOME","LANG","PATH","PWD","PYTHONDONTWRITEBYTECODE","PYTHONUNBUFFERED","SHLVL","TMPDIR"]}` {
		t.Fatalf("environment: %s %v", out, err)
	}

	// Unknown bundles ask the engine to load them.
	if _, err := r.Execute(ctx, req(`{}`, time.Second, rt.CodeBundle{Hash: "nope"}), host(t, &logged)); !errors.Is(err, application.ErrNotLoaded) {
		t.Fatalf("not loaded: %v", err)
	}
}

func TestRequirementsNeedUV(t *testing.T) {
	if _, err := exec.LookPath("uv"); err == nil {
		t.Skip("uv is installed")
	}
	envs := &Envs{Root: t.TempDir()}
	t.Cleanup(func() { _ = envs.RemoveAll() })
	_, _, err := envs.Prepare(context.Background(), rt.CodeBundle{Hash: "x", Requirements: []byte("httpx==0.27.0 --hash=sha256:abc\n")})
	if err == nil || !strings.Contains(err.Error(), "uv") {
		t.Fatalf("err %v", err)
	}
}
