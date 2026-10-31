package application

import (
	"context"
	"testing"
	"time"

	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
	"app-maker/internal/runtime/domain/llm"
)

type priced struct{}

func (priced) Complete(context.Context, rt.LLMRequest) (rt.LLMResponse, error) {
	return rt.LLMResponse{Text: "ok", Usage: rt.LLMUsage{InputTokens: 1_000_000}}, nil
}

func (p priced) Stream(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, error) {
	res, _ := p.Complete(ctx, req)
	return res, onDelta(rt.LLMDelta{Text: res.Text})
}

// Spec 11.7: every call is recorded and subject to the monthly budget.
func TestRouterBudget(t *testing.T) {
	cfg, _ := llm.ParseConfig([]byte(`{"monthlyBudgetUsd":10}`))
	clock := kernel.NewFakeClock(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	r := NewRouter(cfg, map[string]rt.LLMProvider{"anthropic": priced{}}, "", clock, nil)
	req := rt.LLMRequest{Model: "default", Messages: []rt.LLMMessage{{Role: "user", Content: "x"}}}
	for i := 0; i < 3; i++ { // 4 USD per call at claude-opus-5-5 input prices
		_, call, err := r.Complete(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if call.Model != "claude-opus-5-5" || call.CostUSD != 4 {
			t.Fatalf("call record %+v", call)
		}
	}
	if _, _, err := r.Complete(context.Background(), req, nil); kernel.CodeOf(err) != kernel.CodeRateLimited {
		t.Fatalf("over budget: %v", err)
	}
	clock.Advance(31 * 24 * time.Hour) // a new month resets the budget
	if _, _, err := r.Complete(context.Background(), req, nil); err != nil {
		t.Fatalf("new month: %v", err)
	}
	// Forcing a provider (fw dev) and unknown providers.
	f := NewRouter(cfg, map[string]rt.LLMProvider{"anthropic": priced{}}, "fake", clock, nil)
	if _, _, err := f.Complete(context.Background(), req, nil); kernel.CodeOf(err) != kernel.CodeUnsupportedCapability {
		t.Fatalf("unknown provider: %v", err)
	}
}
