package app

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"app-maker/internal/kernel"
	"app-maker/platform/config"
)

// copyTracker copies the example app and applies text replacements to one
// of its files.
func copyTracker(t *testing.T, file, old, new string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	if err := os.CopyFS(dir, os.DirFS("../../examples/tracker")); err != nil {
		t.Fatal(err)
	}
	if file != "" {
		p := filepath.Join(dir, file)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), old) {
			t.Fatalf("%s does not contain %q", file, old)
		}
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// Spec 12.4, 14.10: a manifest directory never deployed before (a new
// image) is deployed at startup with the breaking-change check; one seen
// before yields to the active deploy (made by fw apply).
func TestStartDeploysNewManifestDirectories(t *testing.T) {
	data := t.TempDir()
	start := func(dir string, allowBreaking bool) (*Server, error) {
		cfg := config.Default()
		cfg.Repositories = map[string]config.Repository{"default": {Driver: "sqlite", DSN: filepath.Join(data, "app.db")}}
		cfg.Credentials.Driver = "sqlite"
		cfg.Blobs.Dir = filepath.Join(data, "blobs")
		cfg.LLM.Provider = "fake"
		cfg.Bootstrap.AdminEmail, cfg.Bootstrap.AdminPassword = "admin@example.com", "admin-password-123"
		cfg.AllowBreaking = allowBreaking
		c, err := Load(dir, "dev")
		if err != nil {
			t.Fatal(err)
		}
		s := NewServer(context.Background(), cfg, Options{Log: slog.New(slog.DiscardHandler), FastHashing: true, Clock: kernel.SystemClock{}})
		if err := s.Start(context.Background(), c); err != nil {
			s.Close()
			return nil, err
		}
		return s, nil
	}
	active := func(s *Server) string {
		r, err := s.Current().Deploys.Active(context.Background())
		if err != nil || r == nil {
			t.Fatalf("active deploy: %v", err)
		}
		return r.Hash
	}
	running := func(s *Server) string { return s.Current().App.Manifest.Hash }

	v1 := copyTracker(t, "", "", "")
	v2 := copyTracker(t, "types/project.json", `"maxLength": 5000,`, `"maxLength": 5000, "description": "What the project is about",`)

	s, err := start(v1, false)
	if err != nil {
		t.Fatal(err)
	}
	h1 := running(s)
	s.Close()

	// A new release (v2) is deployed at startup.
	s, err = start(v2, false)
	if err != nil {
		t.Fatal(err)
	}
	h2 := running(s)
	if h2 == h1 || active(s) != h2 {
		t.Fatalf("new directory not deployed: running %s, active %s", h2, active(s))
	}
	s.Close()

	// Restarting with the older directory keeps the active deploy, as after
	// fw apply on a server whose image was not rebuilt.
	s, err = start(v1, false)
	if err != nil {
		t.Fatal(err)
	}
	if running(s) != h2 || active(s) != h2 {
		t.Fatalf("seen directory replaced the active deploy: running %s", running(s))
	}
	s.Close()

	// A breaking release without a migration stops startup unless allowed.
	broken := copyTracker(t, "types/project.json", `"maxLength": 5000,`, `"maxLength": 10,`)
	if _, err := start(broken, false); err == nil || !strings.Contains(err.Error(), "breaking") {
		t.Fatalf("breaking release: %v", err)
	}
	s, err = start(broken, true)
	if err != nil {
		t.Fatalf("allowed breaking release: %v", err)
	}
	if active(s) != running(s) || running(s) == h2 {
		t.Fatal("allowed breaking release not deployed")
	}
	s.Close()
}
