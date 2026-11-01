// Package llm holds the LLM provider adapters (spec 11.7): Anthropic
// (official SDK), OpenAI-compatible (also local servers), and a
// deterministic fake for tests and organon dev.
package llm

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	rt "organon/internal/runtime/api"
)

// Fake answers deterministically: plain text echoes the last user message;
// with an OutputSchema it returns the simplest document matching it.
type Fake struct{}

var _ rt.LLMProvider = Fake{}

// Free marks the fake as costing nothing, whatever model it stands in for.
func (Fake) Free() {}

// Complete implements rt.LLMProvider.
func (Fake) Complete(_ context.Context, req rt.LLMRequest) (rt.LLMResponse, error) {
	prompt := ""
	if n := len(req.Messages); n > 0 {
		prompt = req.Messages[n-1].Content
	}
	text := "fake reply: " + snippet(prompt, 200)
	if len(req.OutputSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.OutputSchema, &schema); err != nil {
			return rt.LLMResponse{}, &rt.LLMError{Code: "invalid_schema", Err: err}
		}
		b, err := json.Marshal(example(schema, snippet(prompt, 80)))
		if err != nil {
			return rt.LLMResponse{}, err
		}
		text = string(b)
	}
	in := words(req.System)
	for _, m := range req.Messages {
		in += words(m.Content)
	}
	return rt.LLMResponse{Model: req.Model, Text: text, StopReason: "end_turn",
		Usage: rt.LLMUsage{InputTokens: in, OutputTokens: words(text)}}, nil
}

// Stream implements rt.LLMProvider in chunks of 16 bytes.
func (f Fake) Stream(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, error) {
	res, err := f.Complete(ctx, req)
	if err != nil {
		return res, err
	}
	for s := res.Text; s != ""; {
		n := min(16, len(s))
		if err := onDelta(rt.LLMDelta{Text: s[:n]}); err != nil {
			return rt.LLMResponse{}, err
		}
		s = s[n:]
	}
	return res, nil
}

func words(s string) int64 { return int64(len(strings.Fields(s))) }

func snippet(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// example builds the simplest value of a JSON Schema; strings carry text.
func example(s map[string]any, text string) any {
	if v, ok := s["const"]; ok {
		return v
	}
	if e, ok := s["enum"].([]any); ok && len(e) > 0 {
		return e[0]
	}
	t, _ := s["type"].(string)
	if l, ok := s["type"].([]any); ok && len(l) > 0 {
		t, _ = l[0].(string)
	}
	switch t {
	case "object":
		props, _ := s["properties"].(map[string]any)
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := map[string]any{}
		for _, k := range keys {
			ps, _ := props[k].(map[string]any)
			out[k] = example(ps, text)
		}
		return out
	case "array":
		n, _ := s["minItems"].(float64)
		items, _ := s["items"].(map[string]any)
		out := make([]any, int(n))
		for i := range out {
			out[i] = example(items, text)
		}
		return out
	case "integer", "number":
		if m, ok := s["minimum"].(float64); ok {
			return m
		}
		return 0
	case "boolean":
		return false
	case "null":
		return nil
	case "string":
		if s["format"] == "date-time" {
			return "2026-01-01T00:00:00Z"
		}
		if text == "" {
			return "fake"
		}
		return text
	}
	return nil
}
