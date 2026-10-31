package api

import (
	"context"
	"encoding/json"
	"time"
)

// CodeBundle is the code of a code node (spec 11.1): the files under the
// manifest's nodes/ directory and the requirements lock file.
type CodeBundle struct {
	Hash         string
	Files        map[string][]byte
	Requirements []byte
}

// CodeLimits bound one execution (spec 11.3).
type CodeLimits struct {
	TimeoutMs      int64    `json:"timeoutMs,omitempty"`
	MemoryMb       int64    `json:"memoryMb,omitempty"`
	CPU            float64  `json:"cpu,omitempty"`
	Network        []string `json:"network,omitempty"`
	MaxOutputBytes int64    `json:"maxOutputBytes,omitempty"`
}

// CodeRequest executes a code node.
type CodeRequest struct {
	RunID          string
	NodeID         string
	Entry          string // module path inside the bundle
	IdempotencyKey string // runID:nodeID, stable across retries (spec 11.6)
	Deadline       time.Time
	Input          json.RawMessage
	Bundle         CodeBundle
	Limits         CodeLimits
}

// CodeHost serves host calls made by code: feature.get, feature.query,
// llm.complete, log (spec 11.1). It runs as the run's principal.
type CodeHost func(ctx context.Context, method string, args json.RawMessage) (json.RawMessage, error)

// CodeRunner executes code nodes on runner processes.
type CodeRunner interface {
	Run(ctx context.Context, req CodeRequest, host CodeHost) (json.RawMessage, error)
}

// CodeError is a failed execution. Unhandled marks an exception raised by
// user code; Retryable marks failures worth retrying (worker crash).
type CodeError struct {
	Code      string
	Message   string
	Retryable bool
	Unhandled bool
}

func (e *CodeError) Error() string { return e.Code + ": " + e.Message }
