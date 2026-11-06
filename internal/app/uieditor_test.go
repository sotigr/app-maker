package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"organon/internal/kernel"
	"organon/platform/config"
)

// ADR 0016: the UI builder reads ui/ files as written, validates unsaved
// edits, and in development writes pages, the shell, styles, and assets
// back into the manifest; changes with errors are refused with problems.
func TestUIEditorSaves(t *testing.T) {
	dir := copyTracker(t, "", "", "")
	cfg := config.Default()
	cfg.Dev = true
	cfg.Blobs.Dir = filepath.Join(t.TempDir(), "blobs")
	cfg.LLM.Provider = "fake"
	cfg.Bootstrap.AdminEmail, cfg.Bootstrap.AdminPassword = "admin@example.com", "admin-password-123"
	ctx := context.Background()
	s := NewServer(ctx, cfg, Options{Log: slog.New(slog.DiscardHandler), FastHashing: true, Clock: kernel.SystemClock{}})
	t.Cleanup(s.Close)
	reload := func() {
		t.Helper()
		c, err := Load(dir, "dev")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Activate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	reload()
	do := func(token, method, path string, body any) (int, map[string]any) {
		t.Helper()
		var rd *bytes.Reader
		if b, ok := body.([]byte); ok {
			rd = bytes.NewReader(b)
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.Switch.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	var admin string
	signIn := func() {
		t.Helper()
		_, login := do("", "POST", "/v1/auth/password", map[string]any{"login": "admin@example.com", "password": "admin-password-123", "cli": true})
		if admin, _ = login["token"].(string); admin == "" {
			t.Fatalf("login: %v", login)
		}
	}
	signIn()
	// Activation rebuilds the runtime, and the memory credential store
	// with it, as organon dev's hot reload does.
	reloadAndSignIn := func() {
		t.Helper()
		reload()
		signIn()
	}
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}

	code, src := do(admin, "GET", "/v1/ui/source", nil)
	if code != 200 || src["dev"] != true || len(src["pages"].([]any)) != 5 || src["app"] == nil || src["styles"] == nil {
		t.Fatalf("source: %d %v", code, src)
	}
	if _, ok := src["types"].(map[string]any)["task"]; !ok {
		t.Fatal("source carries type metadata")
	}

	about := map[string]any{"page": "about", "route": "/about", "body": map[string]any{"component": "text", "text": "Hi"}}
	broken := map[string]any{"page": "about", "route": "/about", "body": map[string]any{"component": "text"}}
	_, v := do(admin, "POST", "/v1/ui/validate", map[string]any{"files": map[string]any{"ui/pages/about.json": mustJSON(broken)}})
	if v["valid"] != false || !strings.Contains(mustJSON(v), `"pointer":"/body"`) {
		t.Fatalf("validate: %v", v)
	}
	if code, out := do(admin, "PUT", "/v1/dev/manifest/ui/pages/about", map[string]any{"document": broken}); code != 422 || exists("ui/pages/about.json") {
		t.Fatalf("broken page saved: %d %v", code, out)
	}
	if code, out := do(admin, "PUT", "/v1/dev/manifest/ui/pages/about", map[string]any{"document": about}); code != 200 || !exists("ui/pages/about.json") {
		t.Fatalf("save page: %d %v", code, out)
	}
	reloadAndSignIn()
	about["page"] = "aboutUs"
	if code, out := do(admin, "PUT", "/v1/dev/manifest/ui/pages/aboutUs", map[string]any{"document": about, "from": "about"}); code != 200 ||
		exists("ui/pages/about.json") || !exists("ui/pages/aboutUs.json") {
		t.Fatalf("rename: %d %v", code, out)
	}
	reloadAndSignIn()
	// The task page links to the project page, so it cannot be deleted.
	if code, out := do(admin, "DELETE", "/v1/dev/manifest/ui/pages/project", nil); code != 422 || !exists("ui/pages/project.json") {
		t.Fatalf("delete a linked page: %d %v", code, out)
	}
	if code, _ := do(admin, "DELETE", "/v1/dev/manifest/ui/pages/aboutUs", nil); code != 200 || exists("ui/pages/aboutUs.json") {
		t.Fatalf("delete page: %d", code)
	}
	if code, _ := do(admin, "PUT", "/v1/dev/manifest/ui/styles", map[string]any{"css": ":root { --o-accent: red; }\n"}); code != 200 {
		t.Fatalf("styles: %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "ui/styles.css")); !strings.Contains(string(b), "red") {
		t.Fatalf("styles file: %s", b)
	}
	if code, _ := do(admin, "PUT", "/v1/dev/manifest/ui/assets/img/logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)); code != 200 || !exists("ui/assets/img/logo.svg") {
		t.Fatalf("asset: %d", code)
	}
	if code, _ := do(admin, "PUT", "/v1/dev/manifest/ui/assets/run.js", []byte(`alert(1)`)); code != 422 || exists("ui/assets/run.js") {
		t.Fatalf("script asset: %d", code)
	}
	if code, _ := do(admin, "PUT", "/v1/dev/manifest/ui/assets/..%2F..%2Fapp.json", []byte(`{}`)); code == 200 {
		t.Fatalf("traversal: %d", code)
	}
	reloadAndSignIn()
	app := map[string]any{"title": "T", "home": "projects", "nav": []any{map[string]any{"label": "X", "page": "aboutUs"}}}
	if code, out := do(admin, "PUT", "/v1/dev/manifest/ui/app", map[string]any{"document": app}); code != 422 {
		t.Fatalf("app with a missing page: %d %v", code, out)
	}

	// Non-admins get nothing; outside dev there are no save routes.
	if code, _ := do("", "GET", "/v1/ui/source", nil); code != 401 {
		t.Fatalf("anonymous source: %d", code)
	}
	s.Current().Cfg.Dev = false
	h := &uiEditor{rt: s.Current()}
	mux := http.NewServeMux()
	h.register(mux)
	if _, pattern := mux.Handler(httptest.NewRequest("PUT", "/v1/dev/manifest/ui/pages/x", nil)); pattern != "" {
		t.Fatalf("save routes outside dev: %q", pattern)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
