package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	rt "app-maker/internal/runtime/api"
)

// OpenAICompatible calls a server implementing the chat completions API
// (hosted vendors and local servers alike) over plain HTTP.
type OpenAICompatible struct {
	BaseURL string // e.g. http://localhost:11434/v1
	APIKey  string
	Client  *http.Client
}

var _ rt.LLMProvider = (*OpenAICompatible)(nil)

// NewOpenAICompatible returns the adapter.
func NewOpenAICompatible(baseURL, apiKey string) *OpenAICompatible {
	return &OpenAICompatible{BaseURL: strings.TrimSuffix(baseURL, "/"), APIKey: apiKey, Client: &http.Client{Timeout: 10 * time.Minute}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  map[string]bool `json:"stream_options,omitempty"`
	ResponseFormat map[string]any  `json:"response_format,omitempty"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		Delta        chatMessage `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

func (o *OpenAICompatible) body(req rt.LLMRequest, stream bool) ([]byte, error) {
	cr := chatRequest{Model: req.Model, MaxTokens: req.MaxTokens, Temperature: req.Temperature, Stream: stream}
	if cr.MaxTokens <= 0 {
		cr.MaxTokens = DefaultMaxTokens
	}
	if stream {
		cr.StreamOptions = map[string]bool{"include_usage": true}
	}
	if req.System != "" {
		cr.Messages = append(cr.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		cr.Messages = append(cr.Messages, chatMessage(m))
	}
	if len(req.OutputSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(req.OutputSchema, &schema); err != nil {
			return nil, &rt.LLMError{Code: "invalid_schema", Err: err}
		}
		cr.ResponseFormat = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "output", "strict": true, "schema": StrictSchema(schema)}}
	}
	return json.Marshal(cr)
}

func (o *OpenAICompatible) post(ctx context.Context, body []byte) (*http.Response, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		hr.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	res, err := o.Client.Do(hr)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &rt.LLMError{Retryable: true, Code: "network", Err: err}
	}
	if res.StatusCode >= 300 {
		defer func() { _ = res.Body.Close() }()
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		s := res.StatusCode
		return nil, &rt.LLMError{Retryable: s == 408 || s == 409 || s == 429 || s >= 500,
			Code: fmt.Sprintf("http_%d", s), Err: fmt.Errorf("%s", strings.TrimSpace(string(msg)))}
	}
	return res, nil
}

// Complete implements rt.LLMProvider.
func (o *OpenAICompatible) Complete(ctx context.Context, req rt.LLMRequest) (rt.LLMResponse, error) {
	body, err := o.body(req, false)
	if err != nil {
		return rt.LLMResponse{}, err
	}
	res, err := o.post(ctx, body)
	if err != nil {
		return rt.LLMResponse{}, err
	}
	defer func() { _ = res.Body.Close() }()
	var cr chatResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&cr); err != nil {
		return rt.LLMResponse{}, &rt.LLMError{Retryable: true, Code: "invalid_response", Err: err}
	}
	if len(cr.Choices) == 0 {
		return rt.LLMResponse{}, &rt.LLMError{Retryable: true, Code: "invalid_response", Err: errors.New("no choices")}
	}
	out := rt.LLMResponse{Model: cr.Model, Text: cr.Choices[0].Message.Content, StopReason: cr.Choices[0].FinishReason}
	if cr.Usage != nil {
		out.Usage = rt.LLMUsage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens}
	}
	return out, nil
}

// Stream implements rt.LLMProvider (server-sent events, "data: [DONE]").
func (o *OpenAICompatible) Stream(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, error) {
	body, err := o.body(req, true)
	if err != nil {
		return rt.LLMResponse{}, err
	}
	res, err := o.post(ctx, body)
	if err != nil {
		return rt.LLMResponse{}, err
	}
	defer func() { _ = res.Body.Close() }()
	out := rt.LLMResponse{Model: req.Model}
	var text strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var cr chatResponse
		if err := json.Unmarshal([]byte(data), &cr); err != nil {
			return rt.LLMResponse{}, &rt.LLMError{Retryable: true, Code: "invalid_response", Err: err}
		}
		if cr.Model != "" {
			out.Model = cr.Model
		}
		if cr.Usage != nil {
			out.Usage = rt.LLMUsage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens}
		}
		for _, c := range cr.Choices {
			if c.FinishReason != "" {
				out.StopReason = c.FinishReason
			}
			if c.Delta.Content == "" {
				continue
			}
			text.WriteString(c.Delta.Content)
			if err := onDelta(rt.LLMDelta{Text: c.Delta.Content}); err != nil {
				return rt.LLMResponse{}, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		return rt.LLMResponse{}, &rt.LLMError{Retryable: true, Code: "network", Err: err}
	}
	out.Text = text.String()
	return out, nil
}
