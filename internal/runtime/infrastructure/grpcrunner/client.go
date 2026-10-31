// Package grpcrunner is the engine's client of runner processes over the
// code-node protocol (spec 11.1): a Unix domain socket, or TCP for remote
// runners.
package grpcrunner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	codenodev1 "app-maker/api/proto/codenode/v1"
	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
)

// Client implements rt.CodeRunner.
type Client struct {
	conn *grpc.ClientConn
	c    codenodev1.RunnerClient

	mu     sync.Mutex
	loaded map[string]bool
}

var _ rt.CodeRunner = (*Client)(nil)

// Dial connects to a runner: "unix:///path/runner.sock" or "host:port".
func Dial(target string) (*Client, error) {
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(32<<20), grpc.MaxCallSendMsgSize(64<<20)))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, c: codenodev1.NewRunnerClient(conn), loaded: map[string]bool{}}, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Stats returns the runner's pool statistics.
func (c *Client) Stats(ctx context.Context) (*codenodev1.StatsResponse, error) {
	return c.c.Stats(ctx, &codenodev1.StatsRequest{})
}

func (c *Client) load(ctx context.Context, req rt.CodeRequest) error {
	c.mu.Lock()
	ok := c.loaded[req.Bundle.Hash]
	c.mu.Unlock()
	if ok {
		return nil
	}
	if _, err := c.c.Load(ctx, &codenodev1.LoadRequest{NodeId: req.NodeID, BundleHash: req.Bundle.Hash,
		Files: req.Bundle.Files, Requirements: req.Bundle.Requirements}); err != nil {
		return &rt.CodeError{Code: "load_failed", Message: err.Error()}
	}
	c.mu.Lock()
	c.loaded[req.Bundle.Hash] = true
	c.mu.Unlock()
	return nil
}

// Run implements rt.CodeRunner. A runner that lost the bundle (restart)
// is sent it again once.
func (c *Client) Run(ctx context.Context, req rt.CodeRequest, host rt.CodeHost) (json.RawMessage, error) {
	for attempt := 0; ; attempt++ {
		if err := c.load(ctx, req); err != nil {
			return nil, err
		}
		out, err := c.run(ctx, req, host)
		var ce *rt.CodeError
		if attempt == 0 && errors.As(err, &ce) && ce.Code == "not_loaded" {
			c.mu.Lock()
			delete(c.loaded, req.Bundle.Hash)
			c.mu.Unlock()
			continue
		}
		return out, err
	}
}

func (c *Client) run(ctx context.Context, req rt.CodeRequest, host rt.CodeHost) (json.RawMessage, error) {
	stream, err := c.c.Run(ctx)
	if err != nil {
		return nil, unavailable(ctx, err)
	}
	ex := &codenodev1.Execute{RunId: req.RunID, NodeId: req.NodeID, BundleHash: req.Bundle.Hash, Entry: req.Entry,
		IdempotencyKey: req.IdempotencyKey, InputJson: req.Input,
		Limits: &codenodev1.Limits{TimeoutMs: req.Limits.TimeoutMs, MemoryMb: req.Limits.MemoryMb, Cpu: req.Limits.CPU,
			Network: req.Limits.Network, MaxOutputBytes: req.Limits.MaxOutputBytes}}
	if !req.Deadline.IsZero() {
		ex.DeadlineUnixMs = req.Deadline.UnixMilli()
	}
	if err := stream.Send(&codenodev1.EngineMessage{Msg: &codenodev1.EngineMessage_Execute{Execute: ex}}); err != nil {
		return nil, unavailable(ctx, err)
	}
	for {
		m, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				err = errors.New("the runner closed the stream without a result")
			}
			return nil, unavailable(ctx, err)
		}
		if cb := m.GetCallback(); cb != nil {
			res := &codenodev1.CallbackResult{Id: cb.GetId()}
			out, herr := host(ctx, cb.GetMethod(), cb.GetArgsJson())
			if herr != nil {
				res.Error = &codenodev1.Error{Code: kernel.CodeOf(herr), Message: herr.Error()}
			} else {
				res.ResultJson = out
			}
			if err := stream.Send(&codenodev1.EngineMessage{Msg: &codenodev1.EngineMessage_CallbackResult{CallbackResult: res}}); err != nil {
				return nil, unavailable(ctx, err)
			}
			continue
		}
		r := m.GetResult()
		if r == nil {
			continue
		}
		_ = stream.CloseSend()
		if e := r.GetError(); e != nil {
			switch e.GetCode() {
			case "deadline_exceeded":
				return nil, context.DeadlineExceeded
			case "cancelled":
				return nil, context.Canceled
			}
			return nil, &rt.CodeError{Code: e.GetCode(), Message: e.GetMessage(), Retryable: e.GetRetryable(), Unhandled: e.GetCode() == "code_error"}
		}
		return r.GetOutputJson(), nil
	}
}

func unavailable(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &rt.CodeError{Code: "runner_unavailable", Message: err.Error(), Retryable: true}
}
