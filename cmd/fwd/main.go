// Command fwd is the framework server. It runs any combination of roles
// (spec 2.1) for one app manifest given as input:
//
//	fwd --manifest ./examples/tracker [--config fwd.yaml] [--roles api,worker]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"app-maker/internal/app"
	"app-maker/platform/config"
	"app-maker/platform/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fwd:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "", "YAML configuration file")
	manifest := flag.String("manifest", "", "app manifest directory (overrides FW_MANIFEST)")
	roles := flag.String("roles", "", "comma-separated roles: api,gateway,worker,runner")
	env := flag.String("env", "", "manifest environment (env/<env>.json)")
	dev := flag.Bool("dev", false, "development mode")
	flag.Parse()

	getenv := func(k string) string {
		switch {
		case k == "FW_MANIFEST" && *manifest != "":
			return *manifest
		case k == "FW_ROLES" && *roles != "":
			return *roles
		case k == "FW_ENV" && *env != "":
			return *env
		case k == "FW_DEV" && *dev:
			return "true"
		}
		return os.Getenv(k)
	}
	cfg, err := config.Load(*cfgPath, getenv)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	compiled, err := app.Load(cfg.Manifest, cfg.Env)
	if err != nil {
		return err
	}
	shutdown, err := observability.Setup(context.Background(), observability.Config{Endpoint: cfg.Observability.OTLPEndpoint,
		SampleRatio: cfg.Observability.SampleRatio, ServiceName: "fwd/" + compiled.Manifest.App.App, ServiceVersion: compiled.Manifest.App.Version})
	if err != nil {
		return fmt.Errorf("observability: %w", err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	for _, w := range compiled.Warnings {
		log.Warn(w.String())
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	srv := app.NewServer(context.Background(), cfg, app.Options{Log: log})
	if err := srv.Start(ctx, compiled); err != nil {
		return err
	}
	defer srv.Close()
	health := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt := srv.Current(); rt != nil {
			rt.Health.Handler().ServeHTTP(w, r)
			return
		}
		http.Error(w, "starting", http.StatusServiceUnavailable)
	})
	internal := &http.Server{Addr: cfg.HTTP.InternalAddr, Handler: health, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := internal.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("internal listener", "err", err)
		}
	}()
	var public *http.Server
	errc := make(chan error, 1)
	if cfg.HasRole(config.RoleAPI) || cfg.HasRole(config.RoleGateway) {
		public = &http.Server{Addr: cfg.HTTP.Addr, Handler: srv.Switch, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := public.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	log.Info("fwd started", "app", compiled.Manifest.App.App, "roles", strings.Join(cfg.Roles, ","),
		"addr", cfg.HTTP.Addr, "internal", cfg.HTTP.InternalAddr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	// Shutdown (spec 14.10): fail readiness, let the load balancer drain,
	// then finish in-flight requests.
	if rt := srv.Current(); rt != nil {
		rt.Health.MarkStopping()
	}
	if !cfg.Dev {
		time.Sleep(5 * time.Second)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if public != nil {
		_ = public.Shutdown(shutdownCtx)
	}
	_ = internal.Shutdown(shutdownCtx)
	log.Info("fwd stopped")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.Log.Level))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Log.Format == "text" {
		return slog.New(observability.LogHandler(slog.NewTextHandler(os.Stderr, opts)))
	}
	return slog.New(observability.LogHandler(slog.NewJSONHandler(os.Stderr, opts)))
}
