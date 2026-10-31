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

	adminui "app-maker/admin"
	accessapi "app-maker/internal/access/api"
	accessapp "app-maker/internal/access/application"
	accessdomain "app-maker/internal/access/domain"
	accesscrypto "app-maker/internal/access/infrastructure/crypto"
	accessmem "app-maker/internal/access/infrastructure/memory"
	"app-maker/internal/access/infrastructure/relindex"
	accesssqlite "app-maker/internal/access/infrastructure/sqlite"
	accesshttp "app-maker/internal/access/interfaces/http"
	changeapi "app-maker/internal/change/api"
	contentapp "app-maker/internal/content/application"
	contentdomain "app-maker/internal/content/domain"
	contentmem "app-maker/internal/content/infrastructure/memory"
	contentsqlite "app-maker/internal/content/infrastructure/sqlite"
	contenthttp "app-maker/internal/content/interfaces/http"
	flowapp "app-maker/internal/flow/application"
	flowhttp "app-maker/internal/flow/interfaces/http"
	integrationapp "app-maker/internal/integration/application"
	integrationdomain "app-maker/internal/integration/domain"
	integrationinfra "app-maker/internal/integration/infrastructure"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	querydomain "app-maker/internal/query/domain"
	schemaapi "app-maker/internal/schema/api"
	schemahttp "app-maker/internal/schema/interfaces/http"
	"app-maker/platform/config"
	"app-maker/platform/egress"
	"app-maker/platform/health"
	"app-maker/platform/httpserver"
	"app-maker/platform/jobs"
	"app-maker/platform/sqlitedb"
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
	// Email overrides the email sender (manifest tests record emails).
	Email integrationapp.EmailSender
	// Secrets overrides secret resolution (tests).
	Secrets func(ref string) (string, bool)
	// Deployer serves /v1/deploy when set (fwd, fw dev).
	Deployer *Deployer
}

// Runtime is a wired instance of the framework running one app.
type Runtime struct {
	Cfg        config.Config
	App        *Compiled
	Content    *contentapp.Service
	Auth       *accessapp.AuthService
	Authorizer *accessapp.Authorizer
	Jobs       *jobs.InProcess
	River      *jobs.RiverSQLite
	Enqueuer   kernel.JobEnqueuer
	JobKinds   *jobs.Registry
	Health     *health.Registry
	Handler    http.Handler
	Engine     *flowapp.Engine
	Deploys    DeployStore
	Log        *slog.Logger
	repos      *contentdomain.RepositoryRegistry
	dbs        map[string]*sqlitedb.DB
}

// authorizerRef breaks the Content <-> Access cycle: Content needs the
// Authorizer, which reads principals and grants through Content.
type authorizerRef struct{ accessapi.Authorizer }

// Build wires the contexts for cfg and a compiled manifest.
func Build(ctx context.Context, cfg config.Config, c *Compiled, opt Options) (rt *Runtime, err error) {
	if opt.Clock == nil {
		opt.Clock = kernel.SystemClock{}
	}
	if opt.IDs == nil {
		opt.IDs = kernel.UUIDv7Generator{}
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	rt = &Runtime{Cfg: cfg, App: c, Health: health.NewRegistry(), Log: opt.Log, dbs: map[string]*sqlitedb.DB{}}
	partial := rt // error returns set rt to nil; clean up what was opened
	defer func() {
		if err != nil {
			partial.Close(context.Background())
		}
	}()

	// Jobs (spec 2.5): River on SQLite, or the in-process adapter for the
	// memory repository.
	rt.JobKinds = jobs.NewRegistry()
	rt.Jobs = jobs.NewInProcess(rt.JobKinds, jobs.InProcessConfig{Concurrency: cfg.Jobs.Concurrency, BaseBackoff: opt.JobBackoff}, opt.Log)
	rt.Enqueuer = rt.Jobs
	rt.JobKinds.Register(changeapi.JobPublishEvents, func(context.Context, json.RawMessage) error {
		return nil // the change log arrives in phase 8; events are dropped until then
	}, 0)

	// Content repositories (spec 5.4).
	rt.repos = contentdomain.NewRepositoryRegistry()
	for name, rc := range cfg.Repositories {
		switch rc.Driver {
		case "memory":
			rt.repos.Register(name, contentmem.New(rt.Jobs))
		case "sqlite":
			db, err := rt.sqlite(ctx, rc.DSN)
			if err != nil {
				return nil, fmt.Errorf("repositories.%s: %w", name, err)
			}
			repo, err := contentsqlite.New(ctx, db.DB, rt.River)
			if err != nil {
				return nil, fmt.Errorf("repositories.%s: %w", name, err)
			}
			var types []*schemaapi.CompiledType
			for _, ct := range c.Types.All() {
				if ct.Repository == name {
					types = append(types, ct)
				}
			}
			if err := repo.EnsureIndexes(ctx, types); err != nil {
				return nil, fmt.Errorf("repositories.%s: %w", name, err)
			}
			rt.repos.Register(name, repo)
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
	var store accessdomain.CredentialStore
	switch cfg.Credentials.Driver {
	case "memory":
		store = accessmem.NewStore()
	case "sqlite":
		dsn := cfg.Credentials.DSN
		if dsn == "" {
			dsn = cfg.Repositories["default"].DSN
		}
		db, err := rt.sqlite(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("credentials: %w", err)
		}
		if store, err = accesssqlite.New(ctx, db.DB); err != nil {
			return nil, fmt.Errorf("credentials: %w", err)
		}
	default:
		return nil, fmt.Errorf("credentials.driver: %q is not available yet", cfg.Credentials.Driver)
	}
	authCfg := accessapp.AuthConfig{PasswordEnabled: c.Manifest.App.Auth.Password.Enabled, MinPasswordLength: c.Manifest.App.Auth.Password.MinLength}
	rt.Auth = accessapp.NewAuthService(store, hasher, accessmem.NewLimiter(10, 15*time.Minute, opt.Clock), sys, sys, index, opt.Clock, authCfg)

	// Flows (spec 10) and effects (spec 14.5).
	rt.Content.SetJobs(rt.Enqueuer)
	secrets := opt.Secrets
	if secrets == nil {
		secrets = envSecrets
	}
	var tfiles []struct{ Path, Data string }
	for _, f := range c.Manifest.Templates {
		tfiles = append(tfiles, struct{ Path, Data string }{f.Path, string(f.Data)})
	}
	templates, err := integrationdomain.NewTemplates(templateSources(tfiles))
	if err != nil {
		return nil, err
	}
	out := egress.New(egress.Config{AllowPrivate: cfg.Dev})
	settings := c.Manifest.App.Settings
	rt.Engine = flowapp.NewEngine(flowapp.Deps{
		Flows: c.Flows, Content: rt.Content.FlowAccess(), Authorizer: rt.Authorizer, Types: c.Types,
		Clock: opt.Clock, IDs: opt.IDs, Jobs: rt.Enqueuer, HTTP: out, Templates: templates, Settings: settings,
		Resolve: rt.Auth.Resolve, Recorder: runRecorder{w: sys}, Log: opt.Log, Secrets: secrets, Nodes: c.Nodes,
	})
	rt.Content.SetBeforeWrite(&beforeWriteAdapter{engine: rt.Engine})
	rt.Content.AddHook(rt.Engine.EventHook())
	rt.JobKinds.Register(flowapp.JobRunFlow, rt.Engine.HandleRunFlow, 15*time.Minute)
	email := opt.Email
	if email == nil {
		email = integrationinfra.LogSender{Log: opt.Log}
	}
	delivery := &integrationapp.Delivery{Email: email, HTTP: out, Templates: templates, Secrets: secrets, Clock: opt.Clock}
	rt.JobKinds.Register(flowapp.JobDeliverEmail, delivery.HandleEmail, time.Minute)
	rt.JobKinds.Register(flowapp.JobDeliverWebhook, delivery.HandleWebhook, time.Minute)
	schedules, err := rt.Engine.Schedules()
	if err != nil {
		return nil, err
	}
	for _, sc := range schedules {
		if rt.River != nil {
			err = rt.River.AddPeriodic(sc.Name, sc.Next, sc.Job)
		} else {
			err = rt.Jobs.AddPeriodic(sc.Name, sc.Next, sc.Job)
		}
		if err != nil {
			return nil, err
		}
	}

	// HTTP (spec 6).
	mux := http.NewServeMux()
	accessH := accesshttp.New(rt.Auth, cfg.HTTP.SecureCookies)
	accessH.Register(mux)
	contenthttp.New(rt.Content).Register(mux)
	flowhttp.New(rt.Engine).Register(mux)
	schemahttp.New(c.Types, c.Manifest.App.App, c.Manifest.App.Version).Register(mux)
	var idem httpserver.IdempotencyStore = httpserver.NewMemoryIdempotency()
	rt.Deploys = &memoryDeploys{}
	if db, ok := rt.dbs[cfg.Repositories["default"].DSN]; ok {
		if idem, err = httpserver.NewSQLIdempotency(ctx, db.DB); err != nil {
			return nil, err
		}
		if rt.Deploys, err = newSQLDeploys(ctx, db.DB); err != nil {
			return nil, err
		}
	}
	if opt.Deployer != nil {
		opt.Deployer.register(mux)
	}
	(&editorHandler{rt: rt}).register(mux)
	mux.Handle("GET /admin/", adminui.Handler())
	mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusFound))
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

// sqlite opens (once per DSN) a SQLite database and its River client.
func (rt *Runtime) sqlite(ctx context.Context, dsn string) (*sqlitedb.DB, error) {
	if db, ok := rt.dbs[dsn]; ok {
		return db, nil
	}
	db, err := sqlitedb.Open(dsn)
	if err != nil {
		return nil, err
	}
	rt.dbs[dsn] = db
	if rt.River == nil {
		rt.River, err = jobs.NewRiverSQLite(ctx, db.DB, rt.JobKinds, jobs.RiverConfig{
			Concurrency: rt.Cfg.Jobs.Concurrency, InsertOnly: !rt.Cfg.HasRole(config.RoleWorker)}, rt.Log)
		if err != nil {
			return nil, err
		}
		rt.Enqueuer = rt.River
		rt.Health.Register(health.Check{Name: "jobs", Required: true, Fn: func(ctx context.Context) error {
			return db.PingContext(ctx)
		}})
	}
	return db, nil
}

// Start runs background workers until ctx ends.
func (rt *Runtime) Start(ctx context.Context) error {
	if rt.Cfg.HasRole(config.RoleWorker) {
		rt.Jobs.Start(ctx)
		if rt.River != nil {
			if err := rt.River.Start(ctx); err != nil {
				return fmt.Errorf("river: %w", err)
			}
		}
	}
	go rt.Health.RunWatchdog(ctx)
	return nil
}

// Close stops River and closes databases.
func (rt *Runtime) Close(ctx context.Context) {
	if rt.River != nil && rt.Cfg.HasRole(config.RoleWorker) {
		_ = rt.River.Stop(ctx)
	}
	for _, db := range rt.dbs {
		_ = db.Close()
	}
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
