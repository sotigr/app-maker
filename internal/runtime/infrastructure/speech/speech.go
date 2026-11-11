// Package speech holds Transcriber adapters (ADR 0028): an OpenAI-compatible
// /audio/transcriptions client (OpenAI Whisper, Groq, self-hosted whisper
// servers) and a deterministic fake for development and tests.
package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

// OpenAICompatible posts audio to {BaseURL}/audio/transcriptions.
type OpenAICompatible struct {
	BaseURL string // e.g. https://api.openai.com/v1
	APIKey  string
	Model   string // e.g. whisper-1
	Client  *http.Client
}

var _ rt.Transcriber = (*OpenAICompatible)(nil)

// NewOpenAICompatible returns the adapter; model defaults to whisper-1.
func NewOpenAICompatible(baseURL, apiKey, model string) *OpenAICompatible {
	if model == "" {
		model = "whisper-1"
	}
	return &OpenAICompatible{BaseURL: strings.TrimSuffix(baseURL, "/"), APIKey: apiKey, Model: model, Client: &http.Client{Timeout: 2 * time.Minute}}
}

var extensions = map[string]string{"audio/webm": "webm", "audio/ogg": "ogg", "audio/mp4": "mp4", "audio/mpeg": "mp3", "audio/wav": "wav", "audio/x-wav": "wav"}

// Transcribe implements rt.Transcriber.
func (o *OpenAICompatible) Transcribe(ctx context.Context, audio []byte, contentType, lang string) (rt.Transcript, error) {
	media := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	ext := extensions[media]
	if ext == "" {
		ext = "webm"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="speech.%s"`, ext))
	h.Set("Content-Type", media)
	part, err := w.CreatePart(h)
	if err != nil {
		return rt.Transcript{}, err
	}
	if _, err := part.Write(audio); err != nil {
		return rt.Transcript{}, err
	}
	_ = w.WriteField("model", o.Model)
	_ = w.WriteField("response_format", "verbose_json")
	if lang != "" {
		_ = w.WriteField("language", lang)
	}
	if err := w.Close(); err != nil {
		return rt.Transcript{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+"/audio/transcriptions", &body)
	if err != nil {
		return rt.Transcript{}, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	res, err := o.Client.Do(req)
	if err != nil {
		return rt.Transcript{}, &kernel.Error{Code: "speech_failed", Message: "transcription service unreachable", Status: 502}
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		// The provider's message may echo request details; keep only the status.
		return rt.Transcript{}, &kernel.Error{Code: "speech_failed", Message: fmt.Sprintf("transcription service answered HTTP %d", res.StatusCode), Status: 502}
	}
	var out struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return rt.Transcript{}, &kernel.Error{Code: "speech_failed", Message: "transcription service sent an invalid answer", Status: 502}
	}
	return rt.Transcript{Text: strings.TrimSpace(out.Text), Seconds: out.Duration}, nil
}

// Fake answers every request with the same text; organon dev and tests.
type Fake struct{}

var _ rt.Transcriber = Fake{}

// FakeText is what the fake transcriber hears.
const FakeText = "dictated text"

// Transcribe implements rt.Transcriber.
func (Fake) Transcribe(_ context.Context, audio []byte, _, _ string) (rt.Transcript, error) {
	if len(audio) == 0 {
		return rt.Transcript{}, kernel.NewError(kernel.CodeValidationFailed, "no audio")
	}
	return rt.Transcript{Text: FakeText}, nil
}
