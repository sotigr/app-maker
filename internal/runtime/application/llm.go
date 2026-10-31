// Package application holds the Runtime context's services: the LLM
// router behind the llm.complete node and ctx.llm (spec 11.7).
package application

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"app-maker/internal/kernel"
	rt "app-maker/internal/runtime/api"
	"app-maker/internal/runtime/domain/llm"
)

// Router resolves aliases to providers, enforces the monthly budget, and
// records every call (spec 11.7). Spend is kept in memory per calendar
// month (UTC) and restarts at zero with the process.
type Router struct {
	cfg       llm.Config
	providers map[string]rt.LLMProvider
	clock     kernel.Clock
	log       *slog.Logger
	// force sends every alias to one provider (fw dev uses the fake).
	force string

	mu    sync.Mutex
	month string
	spent float64
}

var _ rt.LLMClient = (*Router)(nil)

// NewRouter returns a router. force, when set, names the provider that
// serves every alias.
func NewRouter(cfg llm.Config, providers map[string]rt.LLMProvider, force string, clock kernel.Clock, log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{cfg: cfg, providers: providers, force: force, clock: clock, log: log}
}

// Complete implements rt.LLMClient.
func (r *Router) Complete(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, rt.LLMCall, error) {
	alias := req.Model
	target, err := r.cfg.Resolve(alias)
	if err != nil {
		return rt.LLMResponse{}, rt.LLMCall{}, kernel.NewError(kernel.CodeBadRequest, "%v", err)
	}
	if r.force != "" {
		target.Provider = r.force
	}
	p, ok := r.providers[target.Provider]
	if !ok {
		return rt.LLMResponse{}, rt.LLMCall{}, kernel.NewError(kernel.CodeUnsupportedCapability, "LLM provider %q is not configured", target.Provider)
	}
	if err := r.checkBudget(); err != nil {
		return rt.LLMResponse{}, rt.LLMCall{}, err
	}
	req.Model = target.Model
	start := r.clock.Now()
	var res rt.LLMResponse
	if onDelta != nil {
		res, err = p.Stream(ctx, req, onDelta)
	} else {
		res, err = p.Complete(ctx, req)
	}
	call := rt.LLMCall{Alias: alias, Provider: target.Provider, Model: target.Model, Usage: res.Usage,
		LatencyMs: r.clock.Now().Sub(start).Milliseconds(), CostUSD: llm.Cost(target.Model, res.Usage.InputTokens, res.Usage.OutputTokens)}
	r.charge(call.CostUSD)
	r.log.Info("llm call", "alias", alias, "provider", call.Provider, "model", call.Model,
		"inputTokens", call.Usage.InputTokens, "outputTokens", call.Usage.OutputTokens,
		"latencyMs", call.LatencyMs, "costUsd", call.CostUSD, "err", err)
	if err != nil {
		return rt.LLMResponse{}, call, fmt.Errorf("llm %s: %w", target.Provider, err)
	}
	return res, call, nil
}

func (r *Router) monthKey() string { return r.clock.Now().UTC().Format("2006-01") }

func (r *Router) checkBudget() error {
	if r.cfg.MonthlyBudgetUSD <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.monthKey(); m != r.month {
		r.month, r.spent = m, 0
	}
	if r.spent >= r.cfg.MonthlyBudgetUSD {
		return kernel.NewError(kernel.CodeRateLimited, "the monthly LLM budget of %.2f USD is exhausted", r.cfg.MonthlyBudgetUSD)
	}
	return nil
}

func (r *Router) charge(usd float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if m := r.monthKey(); m != r.month {
		r.month, r.spent = m, 0
	}
	r.spent += usd
}

// Spent returns this month's estimated spend.
func (r *Router) Spent() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.month != r.monthKey() {
		return 0
	}
	return r.spent
}
