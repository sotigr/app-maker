package api

import (
	"context"
	"encoding/json"
)

// LLMMessage is one conversation turn (role "user" or "assistant").
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// LLMRequest is a completion request (spec 11.7). Model is the resolved
// provider model ID when sent to a provider, and an alias (default, fast,
// large) when sent to an LLMClient.
type LLMRequest struct {
	Model        string
	System       string
	Messages     []LLMMessage
	OutputSchema json.RawMessage // structured output contract
	MaxTokens    int
	Temperature  *float64
}

// LLMUsage counts tokens of one call.
type LLMUsage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// LLMResponse is a completed call. Text holds the JSON document when the
// request had an OutputSchema.
type LLMResponse struct {
	Model      string   `json:"model"`
	Text       string   `json:"text"`
	StopReason string   `json:"stopReason"`
	Usage      LLMUsage `json:"usage"`
}

// LLMDelta is a piece of streamed output.
type LLMDelta struct {
	Text string
}

// LLMProvider is a model vendor adapter (spec 11.7): Anthropic,
// OpenAI-compatible, or the deterministic fake.
type LLMProvider interface {
	Complete(ctx context.Context, req LLMRequest) (LLMResponse, error)
	Stream(ctx context.Context, req LLMRequest, onDelta func(LLMDelta) error) (LLMResponse, error)
}

// LLMCall is the record of one call (spec 11.7: model, tokens, latency,
// cost estimate).
type LLMCall struct {
	Alias     string   `json:"alias"`
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	Usage     LLMUsage `json:"usage"`
	LatencyMs int64    `json:"latencyMs"`
	CostUSD   float64  `json:"costUsd"`
}

// LLMClient resolves model aliases, enforces the monthly budget, and
// records every call. onDelta may be nil (no streaming).
type LLMClient interface {
	Complete(ctx context.Context, req LLMRequest, onDelta func(LLMDelta) error) (LLMResponse, LLMCall, error)
}

// LLMError is a provider failure. Retryable marks transient failures (rate
// limits, overload, network); others (bad request, auth, refusal) are not
// retried.
type LLMError struct {
	Retryable bool
	Code      string // e.g. refusal, http_400
	Err       error
}

func (e *LLMError) Error() string { return e.Code + ": " + e.Err.Error() }
func (e *LLMError) Unwrap() error { return e.Err }
