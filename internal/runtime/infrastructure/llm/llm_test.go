package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	rt "organon/internal/runtime/api"
)

var schema = json.RawMessage(`{"type":"object","properties":{"label":{"type":"string","maxLength":20},"score":{"type":"number","minimum":0},"tags":{"type":"array","items":{"type":"object","properties":{"n":{"type":"string"}}}}},"required":["label"]}`)

// Spec 11.7: the fake is deterministic and honours the output schema.
func TestFake(t *testing.T) {
	res, err := Fake{}.Complete(context.Background(), rt.LLMRequest{Model: "echo", OutputSchema: schema,
		Messages: []rt.LLMMessage{{Role: "user", Content: "Classify: broken build"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"label":"Classify: broken build","score":0,"tags":[]}` {
		t.Errorf("text %s", res.Text)
	}
	var streamed strings.Builder
	res2, err := Fake{}.Stream(context.Background(), rt.LLMRequest{Messages: []rt.LLMMessage{{Role: "user", Content: "hello there, this is a longer prompt"}}},
		func(d rt.LLMDelta) error { streamed.WriteString(d.Text); return nil })
	if err != nil || streamed.String() != res2.Text || res2.Usage.InputTokens != 7 {
		t.Errorf("stream %q vs %q, usage %+v, err %v", streamed.String(), res2.Text, res2.Usage, err)
	}
}

func TestStrictSchema(t *testing.T) {
	var s map[string]any
	_ = json.Unmarshal(schema, &s)
	got, _ := json.Marshal(StrictSchema(s))
	want := `{"additionalProperties":false,"properties":{"label":{"type":"string"},"score":{"type":"number"},"tags":{"items":{"additionalProperties":false,"properties":{"n":{"type":"string"}},"type":"object"},"type":"array"}},"required":["label"],"type":"object"}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// The Anthropic adapter sends the schema as output_config.format, opts
// into refusal fallbacks, and never sends temperature.
func TestAnthropicRequest(t *testing.T) {
	var body map[string]any
	var beta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"end_turn",
			"content":[{"type":"text","text":"{\"label\":\"bug\"}"}],"usage":{"input_tokens":12,"output_tokens":5}}`)
	}))
	defer srv.Close()
	temp := 0.2
	a := NewAnthropic("test-key", srv.URL)
	res, err := a.Complete(context.Background(), rt.LLMRequest{Model: "claude-opus-5-5", System: "sys", OutputSchema: schema, Temperature: &temp,
		Messages: []rt.LLMMessage{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != `{"label":"bug"}` || res.Usage.InputTokens != 12 || res.Usage.OutputTokens != 5 {
		t.Errorf("response %+v", res)
	}
	if !strings.Contains(beta, "server-side-fallback-2026-07-01") || body["fallbacks"] != "default" {
		t.Errorf("fallbacks not requested: beta %q body %v", beta, body["fallbacks"])
	}
	if _, ok := body["temperature"]; ok {
		t.Error("temperature sent")
	}
	oc, _ := body["output_config"].(map[string]any)
	format, _ := oc["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"].(map[string]any)["additionalProperties"] != false {
		t.Errorf("output_config %v", oc)
	}
	if body["max_tokens"] != float64(DefaultMaxTokens) || body["model"] != "claude-opus-5-5" {
		t.Errorf("body %v", body)
	}
}

func TestAnthropicRefusalAndErrors(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","stop_reason":"refusal",
			"stop_details":{"type":"refusal","category":"cyber","explanation":"x"},"content":[],"usage":{"input_tokens":1,"output_tokens":0}}`)
	}))
	defer srv.Close()
	a := NewAnthropic("k", srv.URL)
	req := rt.LLMRequest{Model: "claude-opus-5-5", Messages: []rt.LLMMessage{{Role: "user", Content: "x"}}}
	var le *rt.LLMError
	if _, err := a.Complete(context.Background(), req); !errors.As(err, &le) || le.Code != "refusal" || le.Retryable {
		t.Errorf("refusal: %v", err)
	}
	status = http.StatusBadRequest
	if _, err := a.Complete(context.Background(), req); !errors.As(err, &le) || le.Code != "http_400" || le.Retryable {
		t.Errorf("400: %v", err)
	}
}

func TestOpenAICompatible(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad path", http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			for _, c := range []string{"Hel", "lo"} {
				_, _ = io.WriteString(w, `data: {"model":"m","choices":[{"delta":{"content":"`+c+`"}}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`+"\n\ndata: [DONE]\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"{\"label\":\"x\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`)
	}))
	defer srv.Close()
	o := NewOpenAICompatible(srv.URL+"/v1/", "k")
	res, err := o.Complete(context.Background(), rt.LLMRequest{Model: "m", System: "s", OutputSchema: schema, Messages: []rt.LLMMessage{{Role: "user", Content: "u"}}})
	if err != nil || res.Text != `{"label":"x"}` || res.Usage.OutputTokens != 4 {
		t.Fatalf("complete %+v %v", res, err)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 || body["response_format"].(map[string]any)["type"] != "json_schema" {
		t.Errorf("body %v", body)
	}
	var got strings.Builder
	res, err = o.Stream(context.Background(), rt.LLMRequest{Model: "m", Messages: []rt.LLMMessage{{Role: "user", Content: "u"}}},
		func(d rt.LLMDelta) error { got.WriteString(d.Text); return nil })
	if err != nil || res.Text != "Hello" || got.String() != "Hello" || res.Usage.InputTokens != 3 || res.StopReason != "stop" {
		t.Errorf("stream %+v %q %v", res, got.String(), err)
	}
	o.BaseURL = srv.URL + "/nope"
	var le *rt.LLMError
	if _, err := o.Complete(context.Background(), rt.LLMRequest{Model: "m"}); !errors.As(err, &le) || le.Retryable || le.Code != "http_404" {
		t.Errorf("404: %v", err)
	}
}
