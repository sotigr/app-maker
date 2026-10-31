// Package grpc serves the runner role's code-node protocol (spec 11.1).
package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	codenodev1 "app-maker/api/proto/codenode/v1"
	rt "app-maker/internal/runtime/api"
	"app-maker/internal/runtime/application"
)

// Server implements codenodev1.RunnerServer on top of the Runner.
type Server struct {
	codenodev1.UnimplementedRunnerServer
	r *application.Runner
}

// New returns the server.
func New(r *application.Runner) *Server { return &Server{r: r} }

// Load implements codenodev1.RunnerServer.
func (s *Server) Load(ctx context.Context, req *codenodev1.LoadRequest) (*codenodev1.LoadResponse, error) {
	if req.GetBundleHash() == "" {
		return nil, status.Error(codes.InvalidArgument, "bundle_hash is required")
	}
	cached, err := s.r.Load(ctx, rt.CodeBundle{Hash: req.GetBundleHash(), Files: req.GetFiles(), Requirements: req.GetRequirements()})
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &codenodev1.LoadResponse{Cached: cached}, nil
}

// hostErr carries the engine's error code to the worker.
type hostErr struct{ code, msg string }

func (e *hostErr) Error() string     { return e.msg }
func (e *hostErr) ErrorCode() string { return e.code }

// Run implements codenodev1.RunnerServer: Execute, then callbacks relayed
// to the engine, then Result. Cancel or a closed stream kills the worker.
func (s *Server) Run(stream codenodev1.Runner_RunServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	ex := first.GetExecute()
	if ex == nil {
		return status.Error(codes.InvalidArgument, "the first message must be Execute")
	}
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	results := make(chan *codenodev1.CallbackResult, 1)
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				cancel()
				return
			}
			switch {
			case m.GetCallbackResult() != nil:
				select {
				case results <- m.GetCallbackResult():
				case <-ctx.Done():
					return
				}
			case m.GetCancel() != nil:
				cancel()
				return
			}
		}
	}()
	var next atomic.Uint64
	host := func(hctx context.Context, method string, args json.RawMessage) (json.RawMessage, error) {
		id := next.Add(1)
		if err := stream.Send(&codenodev1.RunnerMessage{Msg: &codenodev1.RunnerMessage_Callback{
			Callback: &codenodev1.Callback{Id: id, Method: method, ArgsJson: args}}}); err != nil {
			return nil, err
		}
		select {
		case res := <-results:
			if res.GetId() != id {
				return nil, errors.New("callback results out of order")
			}
			if e := res.GetError(); e != nil {
				return nil, &hostErr{code: e.GetCode(), msg: e.GetMessage()}
			}
			return res.GetResultJson(), nil
		case <-hctx.Done():
			return nil, hctx.Err()
		}
	}
	req := rt.CodeRequest{RunID: ex.GetRunId(), NodeID: ex.GetNodeId(), Entry: ex.GetEntry(), IdempotencyKey: ex.GetIdempotencyKey(),
		Input: ex.GetInputJson(), Bundle: rt.CodeBundle{Hash: ex.GetBundleHash()}}
	if ms := ex.GetDeadlineUnixMs(); ms > 0 {
		req.Deadline = time.UnixMilli(ms)
	}
	if l := ex.GetLimits(); l != nil {
		req.Limits = rt.CodeLimits{TimeoutMs: l.GetTimeoutMs(), MemoryMb: l.GetMemoryMb(), CPU: l.GetCpu(), Network: l.GetNetwork(), MaxOutputBytes: l.GetMaxOutputBytes()}
	}
	out, err := s.r.Execute(ctx, req, host)
	res := &codenodev1.Result{OutputJson: out}
	if err != nil {
		res.Error = toProto(err)
	}
	return stream.Send(&codenodev1.RunnerMessage{Msg: &codenodev1.RunnerMessage_Result{Result: res}})
}

func toProto(err error) *codenodev1.Error {
	var ce *rt.CodeError
	switch {
	case errors.As(err, &ce):
		return &codenodev1.Error{Code: ce.Code, Message: ce.Message, Retryable: ce.Retryable}
	case errors.Is(err, context.DeadlineExceeded):
		return &codenodev1.Error{Code: "deadline_exceeded", Message: "the node exceeded its deadline"}
	case errors.Is(err, context.Canceled):
		return &codenodev1.Error{Code: "cancelled", Message: "the execution was cancelled"}
	}
	return &codenodev1.Error{Code: "runner_error", Message: err.Error(), Retryable: true}
}

// Stats implements codenodev1.RunnerServer.
func (s *Server) Stats(context.Context, *codenodev1.StatsRequest) (*codenodev1.StatsResponse, error) {
	out := &codenodev1.StatsResponse{}
	for _, p := range s.r.Stats() {
		out.Pools = append(out.Pools, &codenodev1.PoolStats{NodeId: p.NodeID, BundleHash: p.BundleHash,
			Warm: int32(p.Warm), Busy: int32(p.Busy), Queued: int32(p.Queued), ColdStartMs: p.ColdStartMs})
	}
	return out, nil
}
