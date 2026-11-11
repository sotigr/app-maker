package speech

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ADR 0028: the OpenAI-compatible transcriber sends the audio as a file
// with the model and the language, and the key as a bearer token.
func TestOpenAICompatibleTranscribe(t *testing.T) {
	var got struct{ model, lang, file, ctype, auth, name string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			w.WriteHeader(404)
			return
		}
		got.auth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		got.model, got.lang = r.FormValue("model"), r.FormValue("language")
		f, h, err := r.FormFile("file")
		if err == nil {
			b, _ := io.ReadAll(f)
			got.file, got.ctype, got.name = string(b), h.Header.Get("Content-Type"), h.Filename
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"text": " Καλημέρα σας. ", "duration": 1.5})
	}))
	defer srv.Close()
	tr := NewOpenAICompatible(srv.URL+"/v1/", "sk-test", "")
	out, err := tr.Transcribe(context.Background(), []byte("OPUS"), "audio/webm;codecs=opus", "el")
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Καλημέρα σας." || out.Seconds != 1.5 {
		t.Fatalf("transcript: %+v", out)
	}
	if got.model != "whisper-1" || got.lang != "el" || got.file != "OPUS" || got.ctype != "audio/webm" || got.name != "speech.webm" || got.auth != "Bearer sk-test" {
		t.Fatalf("request: %+v", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"message":"bad audio sk-test"}}`))
	}))
	defer bad.Close()
	if _, err := NewOpenAICompatible(bad.URL, "sk-test", "m").Transcribe(context.Background(), []byte("x"), "audio/ogg", ""); err == nil || err.Error() == "" {
		t.Fatal("want an error")
	} else if contains(err.Error(), "sk-test") {
		t.Fatalf("error leaks provider text: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
