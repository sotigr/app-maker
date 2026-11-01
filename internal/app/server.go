package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	manifestfs "organon/internal/manifest/infrastructure/fs"
	"organon/platform/config"
)

// Server runs one app and swaps its runtime when a new manifest is
// activated (organon apply, organon dev hot reload).
type Server struct {
	Cfg    config.Config
	Opts   Options
	Switch *Switch
	// OnActivate runs after each activation (organon dev applies seed data).
	OnActivate func(ctx context.Context, rt *Runtime, first bool) error

	mu     sync.Mutex
	cur    *Runtime
	cancel context.CancelFunc
	base   context.Context
	first  bool
}

// NewServer returns a server; the deployer is wired to it.
func NewServer(ctx context.Context, cfg config.Config, opts Options) *Server {
	s := &Server{Cfg: cfg, Opts: opts, Switch: &Switch{}, base: ctx, first: true}
	s.Opts.Deployer = &Deployer{Env: cfg.Env, Current: s.Current, Activate: s.Activate}
	return s
}

// Current returns the running runtime.
func (s *Server) Current() *Runtime {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Activate builds a runtime for c and swaps it in. The previous runtime is
// closed first: SQLite allows one opener per process.
func (s *Server) Activate(_ context.Context, c *Compiled) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		s.cancel()
		s.cur.Close(context.Background())
		s.cur = nil
	}
	ctx, cancel := context.WithCancel(s.base)
	rt, err := Build(ctx, s.Cfg, c, s.Opts)
	if err != nil {
		cancel()
		return err
	}
	if err := rt.Start(ctx); err != nil {
		cancel()
		rt.Close(context.Background())
		return err
	}
	if err := rt.Bootstrap(ctx); err != nil {
		cancel()
		rt.Close(context.Background())
		return err
	}
	if s.OnActivate != nil {
		if err := s.OnActivate(ctx, rt, s.first); err != nil {
			rt.Log.Error("activation hook failed", "err", err)
		}
	}
	s.cur, s.cancel, s.first = rt, cancel, false
	s.Switch.Set(rt.Handler)
	rt.Health.MarkStarted()
	return nil
}

// Start activates the manifest directory or, outside development, the
// active deploy (spec 14.10: replicas load the active manifest from the
// database). A directory manifest that was never deployed is a new release
// (a new image or volume) and is deployed now, with the same breaking-change
// check as organon apply; one deployed before yields to later organon apply deploys.
func (s *Server) Start(ctx context.Context, compiled *Compiled) error {
	if err := s.Activate(ctx, compiled); err != nil {
		return err
	}
	if s.Cfg.Dev {
		return nil
	}
	rt := s.Current()
	active, err := rt.Deploys.Active(ctx)
	if err != nil {
		return err
	}
	if active == nil {
		return rt.Deploys.Save(ctx, DeployRecord{Hash: compiled.Manifest.Hash, Actor: "bootstrap", At: time.Now().UTC(), Files: compiled.Manifest.Files})
	}
	if active.Hash == compiled.Manifest.Hash {
		return nil
	}
	m, err := manifestfs.LoadFiles("deployed", active.Files, s.Cfg.Env)
	if err != nil {
		return fmt.Errorf("active deploy %s: %w", active.Hash[:12], err)
	}
	c, err := Compile(m)
	if err != nil {
		return fmt.Errorf("active deploy %s: %w", active.Hash[:12], err)
	}
	history, err := rt.Deploys.List(ctx)
	if err != nil {
		return err
	}
	seen := false
	for _, r := range history {
		seen = seen || r.Hash == compiled.Manifest.Hash
	}
	if !seen {
		plan := MakePlan(c, compiled)
		if plan.Breaking && len(plan.Migrations) == 0 && !s.Cfg.AllowBreaking {
			return fmt.Errorf("the manifest directory has breaking changes against the active deploy %s; add a migration or set ORGANON_ALLOW_BREAKING=true",
				active.Hash[:12])
		}
		rt.Log.Info("deploying the new manifest directory", "from", active.Hash[:12], "to", compiled.Manifest.Hash[:12])
		return rt.Deploys.Save(ctx, DeployRecord{Hash: compiled.Manifest.Hash, Actor: "startup", At: time.Now().UTC(), Plan: &plan,
			Files: compiled.Manifest.Files})
	}
	rt.Log.Info("running the applied manifest instead of the directory", "active", active.Hash[:12], "dir", compiled.Manifest.Hash[:12])
	return s.Activate(ctx, c)
}

// Close stops the runtime.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		s.cancel()
		s.cur.Close(context.Background())
		s.cur = nil
	}
}
