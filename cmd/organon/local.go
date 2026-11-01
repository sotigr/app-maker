package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"organon/internal/app"
	"organon/platform/config"
	"organon/platform/observability"
)

func printProblems(err error) error {
	var ve *app.ValidationError
	if errors.As(err, &ve) {
		for _, p := range ve.Problems {
			fmt.Println(p.String())
		}
		return fmt.Errorf("manifest is invalid")
	}
	return err
}

func validateCmd() *cobra.Command {
	var env string
	c := &cobra.Command{
		Use:   "validate [dir]",
		Short: "Check schemas, policies, flows, and UI pages; type-check every expression",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := app.Load(dirArg(args), env)
			if err != nil {
				return printProblems(err)
			}
			for _, w := range c.Warnings {
				fmt.Println(w.String())
			}
			fmt.Printf("ok: %d types, %d policies, %d flows, %d ui pages, %d tests (manifest %s)\n", len(c.Types.All()),
				len(c.Policies.Targets()), len(c.Flows.All()), len(c.UI.Pages), len(c.Manifest.Tests), c.Manifest.Hash[:12])
			return nil
		},
	}
	c.Flags().StringVar(&env, "env", "dev", "environment file env/<env>.json")
	return c
}

func testCmd() *cobra.Command {
	var env string
	c := &cobra.Command{
		Use:   "test [dir]",
		Short: "Run manifest tests against in-memory instances",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			c, err := app.Load(dirArg(args), env)
			if err != nil {
				return printProblems(err)
			}
			results, err := app.RunManifestTests(context.Background(), c)
			if err != nil {
				return err
			}
			failed := 0
			for _, r := range results {
				if r.Passed {
					fmt.Printf("PASS %s: %s\n", r.File, r.Name)
					continue
				}
				failed++
				fmt.Printf("FAIL %s: %s\n", r.File, r.Name)
				for _, f := range r.Failures {
					fmt.Printf("     %s\n", f)
				}
			}
			fmt.Printf("%d passed, %d failed\n", len(results)-failed, failed)
			if failed > 0 {
				return fmt.Errorf("%d manifest tests failed", failed)
			}
			return nil
		},
	}
	c.Flags().StringVar(&env, "env", "dev", "environment file env/<env>.json")
	return c
}

// devCmd runs all roles locally on SQLite with River, in-process jobs and
// schedules, seed data, and hot reload on manifest changes (spec 12.3).
func devCmd() *cobra.Command {
	var addr, env, llmProvider string
	c := &cobra.Command{
		Use:   "dev [dir]",
		Short: "Run the app locally with SQLite, River, seed data, and hot reload",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := dirArg(args)
			log := slog.New(observability.LogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
			shutdown, err := observability.Setup(context.Background(), observability.Config{ServiceName: "organon-dev"})
			if err != nil {
				return err
			}
			defer func() { _ = shutdown(context.Background()) }()
			cfg := config.Default()
			cfg.Dev, cfg.Manifest, cfg.Env = true, dir, env
			cfg.HTTP.Addr = addr
			cfg.LLM.Provider = llmProvider
			dbPath := filepath.Join(dir, ".organon", "dev.db")
			cfg.Repositories = map[string]config.Repository{"default": {Driver: "sqlite", DSN: dbPath}}
			cfg.Credentials.Driver = "sqlite"
			cfg.Blobs.Dir = filepath.Join(dir, ".organon", "blobs")
			cfg.Secrets.DataKeyFile, cfg.Secrets.CreateDataKey = filepath.Join(dir, ".organon", "data.key"), true
			cfg.Bootstrap.AdminEmail = envOr("ORGANON_BOOTSTRAP_ADMIN_EMAIL", "admin@localhost")
			cfg.Bootstrap.AdminPassword = envOr("ORGANON_BOOTSTRAP_ADMIN_PASSWORD", "admin-password-dev")
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			server := app.NewServer(ctx, cfg, app.Options{Log: log})
			server.OnActivate = func(ctx context.Context, rt *app.Runtime, first bool) error {
				if !first {
					log.Info("manifest reloaded", "hash", rt.App.Manifest.Hash[:12])
					return nil
				}
				// Seed once, on a fresh database (the admin was just created).
				users, err := rt.Content.SystemPorts().Find(ctx, "urn:sys:feature:user", nil, 2)
				if err != nil || len(users) > 1 {
					return err
				}
				log.Info("admin", "login", cfg.Bootstrap.AdminEmail, "password", cfg.Bootstrap.AdminPassword)
				if s, ok := rt.App.Manifest.Seeds[env]; ok {
					if err := app.ApplySeed(ctx, rt, s.Data); err != nil {
						return err
					}
					log.Info("seed applied", "file", s.Path)
				}
				return nil
			}
			compiled, err := app.Load(dir, env)
			if err != nil {
				return printProblems(err)
			}
			if err := server.Start(ctx, compiled); err != nil {
				return err
			}
			defer server.Close()
			srv := &http.Server{Addr: cfg.HTTP.Addr, Handler: server.Switch, ReadHeaderTimeout: 10 * time.Second}
			go func() {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("listener", "err", err)
					stop()
				}
			}()
			log.Info("organon dev running", "url", "http://localhost"+strings.TrimPrefix(cfg.HTTP.Addr, "0.0.0.0"), "db", dbPath)
			last := manifestStamp(dir)
			tick := time.NewTicker(time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					_ = srv.Shutdown(shutdown)
					cancel()
					return nil
				case <-tick.C:
					s := manifestStamp(dir)
					if s == last {
						continue
					}
					last = s
					c, err := app.Load(dir, env)
					if err != nil {
						log.Error("reload failed; still serving the previous manifest", "err", printProblems(err))
						continue
					}
					if err := server.Activate(ctx, c); err != nil {
						log.Error("reload failed", "err", err)
					}
				}
			}
		},
	}
	c.Flags().StringVar(&addr, "addr", ":8080", "listen address")
	c.Flags().StringVar(&llmProvider, "llm", "fake", "LLM provider serving every model alias (fake, anthropic, or a configured name; empty uses app.json aliases)")
	c.Flags().StringVar(&env, "env", "dev", "environment")
	return c
}

// manifestStamp fingerprints the manifest files' names, sizes, and times.
func manifestStamp(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".organon" || strings.HasPrefix(d.Name(), ".") && path != dir) {
			return filepath.SkipDir
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			fmt.Fprintf(&b, "%s:%d:%d;", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold a manifest with an example type, policy, flow, and test",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if _, err := os.Stat(name); err == nil {
				return fmt.Errorf("%s already exists", name)
			}
			for path, content := range scaffold(name) {
				full := filepath.Join(name, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
					return err
				}
			}
			fmt.Printf("created %s; run: organon validate %s && organon test %s && organon dev %s\n", name, name, name, name)
			return nil
		},
	}
}
