package grpcrunner

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	codenodev1 "app-maker/api/proto/codenode/v1"
	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
	"app-maker/internal/runtime/application"
	"app-maker/internal/runtime/infrastructure/process"
	grpcserver "app-maker/internal/runtime/interfaces/grpc"
)

// Spec 11.1: the engine talks to the runner over gRPC on a Unix socket;
// callbacks run in the engine; a restarted runner gets the bundle again.
func TestGRPCRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	dir := t.TempDir()
	sock := filepath.Join(dir, "runner.sock")
	serve := func() (*grpc.Server, *process.Envs) {
		envs := &process.Envs{Root: filepath.Join(dir, "cache")}
		r := application.NewRunner(application.RunnerConfig{}, envs, &process.Starter{Envs: envs}, nil)
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := grpc.NewServer()
		codenodev1.RegisterRunnerServer(srv, grpcserver.New(r))
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { srv.Stop(); r.Close(); _ = envs.RemoveAll() })
		return srv, envs
	}
	srv, _ := serve()
	c, err := Dial("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	req := rt.CodeRequest{RunID: "r", NodeID: "n", Entry: "main.py", IdempotencyKey: "r:n", Deadline: time.Now().Add(20 * time.Second),
		Input: json.RawMessage(`{"id":"f1"}`), Bundle: rt.CodeBundle{Hash: "b1", Files: map[string][]byte{"main.py": []byte(`
from fwnode import HostError
def run(input, ctx):
    f = ctx.get_feature(input["id"])
    try:
        ctx.get_feature("gone")
    except HostError as e:
        code = e.code
    return {"title": f["title"], "code": code}
`)}}}
	host := func(_ context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
		if string(args) == `{"id":"gone"}` {
			return nil, kernel.ErrNotFound
		}
		return json.RawMessage(`{"title":"hello"}`), nil
	}
	out, err := c.Run(context.Background(), req, host)
	if err != nil || string(out) != `{"title":"hello","code":"not_found"}` {
		t.Fatalf("run: %s %v", out, err)
	}
	stats, err := c.Stats(context.Background())
	if err != nil || len(stats.GetPools()) != 1 || stats.GetPools()[0].GetWarm() != 1 {
		t.Fatalf("stats %v %v", stats, err)
	}

	// A deadline is reported as such.
	loop := req
	loop.Bundle.Hash, loop.Bundle.Files = "b2", map[string][]byte{"main.py": []byte("def run(i, ctx):\n    while True: pass\n")}
	loop.Deadline = time.Now().Add(300 * time.Millisecond)
	if _, err := c.Run(context.Background(), loop, host); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}

	// Restart the runner: the client reloads the bundle transparently.
	srv.Stop()
	serve()
	out, err = c.Run(context.Background(), req, host)
	if err != nil || string(out) != `{"title":"hello","code":"not_found"}` {
		t.Fatalf("after restart: %s %v", out, err)
	}
}
