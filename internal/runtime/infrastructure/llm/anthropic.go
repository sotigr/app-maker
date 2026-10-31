package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	rt "app-maker/internal/runtime/api"
)

// DefaultMaxTokens applies when a request sets no maxTokens.
const DefaultMaxTokens = 16000

// Anthropic calls the Claude Messages API through the official SDK. It
// opts into server-side refusal fallbacks, sends the node's output schema
// as the structured output format, and never sends sampling parameters
// (the API rejects temperature since 2026-10-21; Temperature is ignored).
type Anthropic struct{ client anthropic.Client }

var _ rt.LLMProvider = (*Anthropic)(nil)

// NewAnthropic returns the adapter. An empty apiKey uses the SDK's own
// credential resolution (ANTHROPIC_API_KEY, profiles).
func NewAnthropic(apiKey, baseURL string) *Anthropic {
	var opts []option.RequestOption
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{client: anthropic.NewClient(opts...)}
}

func (a *Anthropic) params(req rt.LLMRequest) (anthropic.BetaMessageNewParams, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}
	p := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(maxTokens),
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
	}
	if req.System != "" {
		p.System = []anthropic.BetaTextBlockParam{{Text: req.System}}
	}
	for _, m := range req.Messages {
		role := anthropic.BetaMessageParamRoleUser
		if m.Role == "assistant" {
			role = anthropic.BetaMessageParamRoleAssistant
		}
		p.Messages = append(p.Messages, anthropic.BetaMessageParam{Role: role,
			Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(m.Content)}})
	}
	if len(req.OutputSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.OutputSchema, &schema); err != nil {
			return p, &rt.LLMError{Code: "invalid_schema", Err: err}
		}
		p.OutputConfig = anthropic.BetaOutputConfigParam{Format: anthropic.BetaJSONOutputFormatParam{Schema: StrictSchema(schema)}}
	}
	return p, nil
}

// Complete implements rt.LLMProvider.
func (a *Anthropic) Complete(ctx context.Context, req rt.LLMRequest) (rt.LLMResponse, error) {
	p, err := a.params(req)
	if err != nil {
		return rt.LLMResponse{}, err
	}
	msg, err := a.client.Beta.Messages.New(ctx, p)
	if err != nil {
		return rt.LLMResponse{}, classify(err)
	}
	return response(msg)
}

// Stream implements rt.LLMProvider.
func (a *Anthropic) Stream(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, error) {
	p, err := a.params(req)
	if err != nil {
		return rt.LLMResponse{}, err
	}
	stream := a.client.Beta.Messages.NewStreaming(ctx, p)
	defer func() { _ = stream.Close() }()
	msg := anthropic.BetaMessage{}
	for stream.Next() {
		ev := stream.Current()
		if err := msg.Accumulate(ev); err != nil {
			return rt.LLMResponse{}, err
		}
		if d, ok := ev.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if t, ok := d.Delta.AsAny().(anthropic.BetaTextDelta); ok && t.Text != "" {
				if err := onDelta(rt.LLMDelta{Text: t.Text}); err != nil {
					return rt.LLMResponse{}, err
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return rt.LLMResponse{}, classify(err)
	}
	return response(&msg)
}

func response(msg *anthropic.BetaMessage) (rt.LLMResponse, error) {
	if msg.StopReason == anthropic.BetaStopReasonRefusal {
		return rt.LLMResponse{}, &rt.LLMError{Code: "refusal",
			Err: fmt.Errorf("the model declined the request (category %q)", msg.StopDetails.Category)}
	}
	var b strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return rt.LLMResponse{Model: string(msg.Model), Text: b.String(), StopReason: string(msg.StopReason),
		Usage: rt.LLMUsage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens}}, nil
}

// classify marks rate limits, overload, server errors, and network
// failures retryable; other API errors are permanent.
func classify(err error) error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		s := apiErr.StatusCode
		return &rt.LLMError{Retryable: s == 408 || s == 409 || s == 429 || s >= 500, Code: fmt.Sprintf("http_%d", s), Err: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &rt.LLMError{Retryable: true, Code: "network", Err: err}
}

// unsupported lists JSON Schema keywords structured outputs reject; the
// engine still validates the full schema on the response.
var unsupported = []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties",
	"$schema", "$id"}

// StrictSchema copies a schema for structured outputs: every object gets
// additionalProperties false, unsupported constraints and x- keywords are
// removed.
func StrictSchema(s map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range s {
		if strings.HasPrefix(k, "x-") || slices.Contains(unsupported, k) {
			continue
		}
		switch x := v.(type) {
		case map[string]any:
			if k == "properties" || k == "$defs" || k == "definitions" {
				m := map[string]any{}
				for name, sub := range x {
					if sm, ok := sub.(map[string]any); ok {
						m[name] = StrictSchema(sm)
					}
				}
				out[k] = m
			} else {
				out[k] = StrictSchema(x)
			}
		case []any:
			l := make([]any, len(x))
			for i, it := range x {
				if m, ok := it.(map[string]any); ok && (k == "anyOf" || k == "allOf" || k == "oneOf" || k == "prefixItems") {
					l[i] = StrictSchema(m)
				} else {
					l[i] = it
				}
			}
			out[k] = l
		default:
			out[k] = v
		}
	}
	if out["type"] == "object" || out["properties"] != nil {
		out["additionalProperties"] = false
	}
	return out
}
