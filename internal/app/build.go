package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	accessapi "app-maker/internal/access/api"
	accessapp "app-maker/internal/access/application"
	accesscrypto "app-maker/internal/access/infrastructure/crypto"
	accessmem "app-maker/internal/access/infrastructure/memory"
	"app-maker/internal/access/infrastructure/relindex"
	accesshttp "app-maker/internal/access/interfaces/http"
	changeapi "app-maker/internal/change/api"
	contentapp "app-maker/internal/content/application"
	contentdomain "app-maker/internal/content/domain"
	contentmem "app-maker/internal/content/infrastructure/memory"
	contenthttp "app-maker/internal/content/interfaces/http"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	querydomain "app-maker/internal/query/domain"
	schemaapi "app-maker/internal/schema/api"
	schemahttp "app-maker/internal/schema/interfaces/http"
	"app-maker/platform/config"
	"app-maker/platform/health"
	"app-maker/platform/httpserver"
	"app-maker/platform/jobs"
)

// Options tune a Runtime beyond the server configuration (tests, fw test).
type Options struct {
	Clock kernel.Clock
	IDs   kernel.IDGenerator
	Log   *slog.Logger
	// FastHashing uses cheap Argon2 parameters (tests only).
	FastHashing bool
	// JobBackoff overrides the first retry delay of in-process jobs.
	JobBackoff time.Duration
}

// Runtime is a wired instance of the framework running one app.
type Runtime struct {
	Cfg        config.Config
	App        *Compiled
	Content    *contentapp.Service
	Auth       *accessapp.AuthService
	Authorizer *accessapp.Authorizer
	Jobs       *jobs.InProcess
	JobKinds   *jobs.Registry
	Health     *health.Registry
	Handler    http.Handler
	Log        *slog.Logger
	repos      *contentdomain.RepositoryRegistry
}

// authorizerRef breaks the Content <-> Access cycle: Content needs the
// Authorizer, which reads principals and grants through Content.
type authorizerRef struct{ accessapi.Authorizer }

// Build wires the contexts for cfg and a compiled manifest.
func Build(cfg config.Config, c *Compiled, opt Options) (*Runtime, error) {
	if opt.Clock == nil {
		opt.Clock = kernel.SystemClock{}
	}
	if opt.IDs == nil {
		opt.IDs = kernel.UUIDv7Generator{}
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	rt := &Runtime{Cfg: cfg, App: c, Health: health.NewRegistry(), Log: opt.Log}

	// Jobs (spec 2.5). The River adapter arrives with the SQLite repository.
	rt.JobKinds = jobs.NewRegistry()
	rt.Jobs = jobs.NewInProcess(rt.JobKinds, jobs.InProcessConfig{Concurrency: cfg.Jobs.Concurrency, BaseBackoff: opt.JobBackoff}, opt.Log)
	rt.JobKinds.Register(changeapi.JobPublishEvents, func(context.Context, json.RawMessage) error {
		return nil // the change log arrives in phase 8; events are dropped until then
	}, 0)

	// Content repositories (spec 5.4).
	rt.repos = contentdomain.NewRepositoryRegistry()
	for name, rc := range cfg.Repositories {
		switch rc.Driver {
		case "memory":
			rt.repos.Register(name, contentmem.New(rt.Jobs))
		default:
			return nil, fmt.Errorf("repositories.%s.driver: %q is not available yet", name, rc.Driver)
		}
	}
	for _, ct := range c.Types.All() {
		if _, err := rt.repos.Get(ct.Repository); err != nil {
			return nil, fmt.Errorf("type %s: %w", ct.ID, err)
		}
	}

	cursorKey := []byte(cfg.Secrets.CursorKey)
	if len(cursorKey) == 0 {
		cursorKey = make([]byte, 32) // single topology: per-process key
		_, _ = rand.Read(cursorKey)
	} else {
		sum := sha256.Sum256(cursorKey)
		cursorKey = sum[:]
	}
	authz := &authorizerRef{}
	limits := contentapp.Limits{MaxDocumentBytes: c.Manifest.App.Limits.MaxDocumentBytes}
	if limits.MaxDocumentBytes > 256<<10 {
		limits.MaxDocumentBytes = 256 << 10 // apps may only lower limits (spec 14.1)
	}
	rt.Content = contentapp.NewService(contentapp.Deps{
		Repos: rt.repos, Types: c.Types, Authorizer: authz, Clock: opt.Clock, IDs: opt.IDs,
		Planner: querydomain.NewPlanner(cursorKey), Limits: limits,
	})
	sys := rt.Content.SystemPorts()

	// Access (spec 8).
	index := relindex.New(sys)
	rt.Authorizer = accessapp.NewAuthorizer(c.Policies, c.Roles, index, exprCompiler(), opt.Clock)
	authz.Authorizer = rt.Authorizer
	hasher := accesscrypto.DefaultArgon2id
	if opt.FastHashing {
		hasher = accesscrypto.Argon2id{Time: 1, Memory: 1024, Threads: 1, KeyLen: 32}
	}
	var store *accessmem.Store
	switch cfg.Credentials.Driver {
	case "memory":
		store = accessmem.NewStore()
	default:
		return nil, fmt.Errorf("credentials.driver: %q is not available yet", cfg.Credentials.Driver)
	}
	authCfg := accessapp.AuthConfig{PasswordEnabled: c.Manifest.App.Auth.Password.Enabled, MinPasswordLength: c.Manifest.App.Auth.Password.MinLength}
	rt.Auth = accessapp.NewAuthService(store, hasher, accessmem.NewLimiter(10, 15*time.Minute, opt.Clock), sys, sys, index, opt.Clock, authCfg)

	// HTTP (spec 6).
	mux := http.NewServeMux()
	accessH := accesshttp.New(rt.Auth, cfg.HTTP.SecureCookies)
	accessH.Register(mux)
	contenthttp.New(rt.Content).Register(mux)
	schemahttp.New(c.Types, c.Manifest.App.App, c.Manifest.App.Version).Register(mux)
	idem := httpserver.NewMemoryIdempotency()
	maxBody := cfg.HTTP.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 1 << 20
	}
	rt.Handler = httpserver.Chain(mux,
		httpserver.WithRequestID,
		httpserver.Recover,
		httpserver.AccessLog(opt.Log),
		httpserver.LimitBody(maxBody),
		accessH.Middleware,
		httpserver.Idempotency(idem, func(r *http.Request) string {
			return accessapi.PrincipalFrom(r.Context()).ID.String()
		}),
	)

	// Health (spec 14.10).
	for _, name := range rt.repos.Names() {
		repo, _ := rt.repos.Get(name)
		rt.Health.Register(health.Check{Name: "repository:" + name, Required: true, Fn: func(ctx context.Context) error {
			_, err := repo.Children(ctx, kernel.NilID, contentdomain.PageRequest{Limit: 1})
			return err
		}})
	}
	return rt, nil
}

// Start runs background workers until ctx ends.
func (rt *Runtime) Start(ctx context.Context) {
	if rt.Cfg.HasRole(config.RoleWorker) {
		rt.Jobs.Start(ctx)
	}
	go rt.Health.RunWatchdog(ctx)
}

// Bootstrap creates the root feature and the initial admin (spec 8.4).
func (rt *Runtime) Bootstrap(ctx context.Context) error {
	if err := rt.Content.EnsureRoot(ctx, rt.App.Manifest.App.App); err != nil {
		return fmt.Errorf("bootstrap root: %w", err)
	}
	created, err := rt.Auth.BootstrapAdmin(ctx, rt.Cfg.Bootstrap.AdminEmail, rt.Cfg.Bootstrap.AdminPassword)
	if err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		rt.Log.Info("bootstrap admin created", "login", rt.Cfg.Bootstrap.AdminEmail)
	}
	return nil
}

// Type returns a compiled type by short name (tests and CLI helpers).
func (rt *Runtime) Type(name string) (*schemaapi.CompiledType, bool) {
	return rt.App.Types.ByName(name)
}

// keep the query api import used for documentation of the planner port.
var _ queryapi.Planner = (*querydomain.Planner)(nil)
