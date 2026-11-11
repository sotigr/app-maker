package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
	"organon/platform/config"
)

// ADR 0028: dictation from UI text fields is transcribed on the server in
// the caller's UI language, for signed-in callers only, without storing the
// audio; the page may use the microphone itself only.
func TestSpeechTranscription(t *testing.T) {
	var mu sync.Mutex
	var langs []string
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		mu.Lock()
		langs = append(langs, r.FormValue("language"))
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "Αλλαγή τακακιών", "duration": 2})
	}))
	defer stt.Close()
	speechOn := func(m *manifestdomain.Manifest) {
		for i, f := range m.UI {
			if f.Path == "ui/app.json" {
				// The tracker turns dictation on; a shorter limit shows it is read.
				m.UI[i].Data = bytes.Replace(f.Data, []byte(`"maxSeconds": 60`), []byte(`"maxSeconds": 30`), 1)
			}
		}
	}
	s := startFull(t, "memory", nil, speechOn, func(c *config.Config) {
		c.Speech.Kind, c.Speech.BaseURL, c.Speech.APIKey = "openai-compatible", stt.URL, "${secret:pay}"
	})
	admin := s.admin()
	post := func(c *client, query, ctype string, audio []byte) resp {
		req, _ := http.NewRequest("POST", s.url+"/v1/ui/transcribe"+query, bytes.NewReader(audio))
		req.Header.Set("Content-Type", ctype)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, _ := io.ReadAll(res.Body)
		return resp{Status: res.StatusCode, Raw: string(raw)}
	}
	last := func() string { mu.Lock(); defer mu.Unlock(); return langs[len(langs)-1] }

	// The bundle tells the renderer that a server fallback exists.
	b := mustOK(t, admin.do("GET", "/v1/ui", nil), 200)
	if !strings.Contains(b.Raw, `"speech":{"maxSeconds":30,"mode":"auto","position":"end","server":true}`) {
		t.Fatalf("bundle speech: %s", b.Raw)
	}

	// An explicit language is reduced to its primary subtag.
	r := mustOK(t, post(admin, "?lang=el-GR", "audio/webm;codecs=opus", []byte("OPUS")), 200)
	if !strings.Contains(r.Raw, "Αλλαγή τακακιών") || last() != "el" {
		t.Fatalf("transcribe: %s lang %q", r.Raw, last())
	}
	// Without one, the user's saved UI language is used.
	me := mustOK(t, admin.do("GET", "/v1/features/user/"+admin.id, nil), 200)
	mustOK(t, admin.do("PATCH", "/v1/features/user/"+admin.id, map[string]any{"locale": "el"}, "If-Match", me.Header.Get("ETag")), 200)
	admin = s.admin()
	mustOK(t, post(admin, "", "audio/ogg", []byte("OGG")), 200)
	if last() != "el" {
		t.Fatalf("default language: %q", last())
	}

	if r := post(s.anon(), "", "audio/webm", []byte("x")); r.Status != 401 {
		t.Fatalf("anonymous: %d %s", r.Status, r.Raw)
	}
	if r := post(admin, "", "text/plain", []byte("x")); r.Status != 400 {
		t.Fatalf("bad type: %d %s", r.Status, r.Raw)
	}
	if r := post(admin, "", "audio/webm", nil); r.Status != 400 {
		t.Fatalf("empty: %d %s", r.Status, r.Raw)
	}
	if r := post(admin, "", "audio/webm", bytes.Repeat([]byte("x"), 1<<20+1)); r.Status != 413 {
		t.Fatalf("too large: %d %s", r.Status, r.Raw)
	}

	// The renderer page may use the microphone itself only.
	res, err := http.Get(s.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if pp := res.Header.Get("Permissions-Policy"); !strings.Contains(pp, "microphone=(self)") {
		t.Fatalf("Permissions-Policy: %q", pp)
	}

	// Without a server transcriber, the endpoint says so and the bundle
	// leaves dictation to the browser.
	s2 := startFull(t, "memory", nil, speechOn, func(c *config.Config) { c.Speech.Kind = "none" })
	a2 := s2.admin()
	if b := mustOK(t, a2.do("GET", "/v1/ui", nil), 200); !strings.Contains(b.Raw, `"server":false`) {
		t.Fatalf("bundle without transcriber: %s", b.Raw)
	}
	req, _ := http.NewRequest("POST", s2.url+"/v1/ui/transcribe", bytes.NewReader([]byte("x")))
	req.Header.Set("Content-Type", "audio/webm")
	req.Header.Set("Authorization", "Bearer "+a2.token)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res2.Body.Close()
	if res2.StatusCode != 503 {
		t.Fatalf("no transcriber: %d", res2.StatusCode)
	}
}
