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

	adminui "organon/admin"
	accessapi "organon/internal/access/api"
	accessapp "organon/internal/access/application"
	accessdomain "organon/internal/access/domain"
	accesscrypto "organon/internal/access/infrastructure/crypto"
	accessmem "organon/internal/access/infrastructure/memory"
	"organon/internal/access/infrastructure/relindex"
	accesssqlite "organon/internal/access/infrastructure/sqlite"
	accesshttp "organon/internal/access/interfaces/http"
	changeapi "organon/internal/change/api"
	changeapp "organon/internal/change/application"
	changemem "organon/internal/change/infrastructure/memory"
	changehttp "organon/internal/change/interfaces/http"
	contentapp "organon/internal/content/application"
	contentdomain "organon/internal/content/domain"
	contentmem "organon/internal/content/infrastructure/memory"
	contentsqlite "organon/internal/content/infrastructure/sqlite"
	contenthttp "organon/internal/content/interfaces/http"
	flowapp "organon/internal/flow/application"
	flowdomain "organon/internal/flow/domain"
	flowhttp "organon/internal/flow/interfaces/http"
	integrationapp "organon/internal/integration/application"
	integrationdomain "organon/internal/integration/domain"
	integrationinfra "organon/internal/integration/infrastructure"
	integrationhttp "organon/internal/integration/interfaces/http"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	querydomain "organon/internal/query/domain"
	bleveidx "organon/internal/query/infrastructure/bleve"
	runtimeapp "organon/internal/runtime/application"
	schemaapi "organon/internal/schema/api"
	schemahttp "organon/internal/schema/interfaces/http"
	uiapp "organon/internal/ui/application"
	uihttp "organon/internal/ui/interfaces/http"
	"organon/platform/config"
	"organon/platform/egress"
	"organon/platform/health"
	"organon/platform/httpserver"
	"organon/platform/jobs"
	"organon/platform/observability"
	"organon/platform/sqlitedb"
	webui "organon/web"
)

// Options tune a Runtime beyond the server configuration (tests, organon test).
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
	// Deployer serves /v1/deploy when set (organond, organon dev).
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
	Gateway    *changeapp.Gateway
	LLM        *runtimeapp.Router
	ChangeLog  *changemem.Log
	Deploys    DeployStore
	UI         *uiapp.Service
	Log        *slog.Logger
	repos      *contentdomain.RepositoryRegistry
	dbs        map[string]*sqlitedb.DB
	closers    []func()
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
	// Change log (spec 9.3): in-memory until the Valkey and PostgreSQL
	// adapters (phase 10); refused in the replicated topology.
	rt.ChangeLog = changemem.New(24*time.Hour, 0)
	rt.JobKinds.Register(changeapi.JobPublishEvents, changeapp.Publisher{Log: rt.ChangeLog}.Handle, 0)

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
	// Search index (spec 7.5): embedded Bleve, in memory; rebuilt by
	// index_features jobs. PostgreSQL full-text search arrives with phase 10.
	search, err := bleveidx.NewMemory()
	if err != nil {
		return nil, err
	}
	authz := &authorizerRef{}
	limits := contentapp.Limits{MaxDocumentBytes: c.Manifest.App.Limits.MaxDocumentBytes}
	if limits.MaxDocumentBytes > 256<<10 {
		limits.MaxDocumentBytes = 256 << 10 // apps may only lower limits (spec 14.1)
	}
	rt.Content = contentapp.NewService(contentapp.Deps{
		Repos: rt.repos, Types: c.Types, Authorizer: authz, Clock: opt.Clock, IDs: opt.IDs,
		Planner: querydomain.NewPlanner(cursorKey), Limits: limits, Search: search,
	})
	sys := rt.Content.SystemPorts()

	// Access (spec 8).
	index := relindex.New(sys)
	rt.Authorizer = accessapp.NewAuthorizer(c.Policies, c.Roles, index, exprCompiler(), opt.Clock)
	rt.Authorizer.SetParentLoader(rt.Content.RefByID)
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
	authCfg, err := authConfig(cfg, c, cursorKey, secrets, out, opt.Clock)
	if err != nil {
		return nil, err
	}
	authCfg.Audit = rt.Content.AppendAudit
	authCfg.NoAdmins = cfg.Admin.DisableAccounts
	authCfg.PublicURL = cfg.HTTP.PublicURL
	authCfg.Roles = c.Manifest.App.Roles
	rt.Auth = accessapp.NewAuthService(store, hasher, accessmem.NewLimiter(10, 15*time.Minute, opt.Clock), sys, sys, index, opt.Clock, authCfg)
	settings := c.Manifest.App.Settings
	if rt.LLM, err = llmRouter(cfg, c, secrets, opt); err != nil {
		return nil, err
	}
	code, err := rt.codeRunner(cfg, opt)
	if err != nil {
		return nil, err
	}
	var blobSvc *integrationapp.Blobs // set below; used at run time by documents and attachments
	rt.Engine = flowapp.NewEngine(flowapp.Deps{
		Flows: c.Flows, Content: rt.Content.FlowAccess(), Authorizer: rt.Authorizer, Types: c.Types,
		Clock: opt.Clock, IDs: opt.IDs, Jobs: rt.Enqueuer, HTTP: out, Templates: templates, Settings: settings,
		Resolve: rt.Auth.Resolve, Recorder: runRecorder{w: sys}, Log: opt.Log, Secrets: secrets, Nodes: c.Nodes,
		SecretHosts: c.Manifest.App.SecretHosts(), Document: renderDocument(c, opt.Clock, func() *integrationapp.Blobs { return blobSvc }), Share: func(ctx context.Context, p accessapi.Principal, id kernel.ID,
			def *schemaapi.ShareDef, input map[string]any) (any, error) {
			return rt.Auth.Share(ctx, p, id, def, input)
		}, LLM: rt.LLM, Ephemeral: rt.ephemeral, Code: code, Bundle: newBundles(c).get,
	})
	rt.Content.SetBeforeWrite(&beforeWriteAdapter{engine: rt.Engine})
	rt.Content.SetMigrations(c.Migrations, rt.Engine.RunForFeature)
	rt.JobKinds.Register(contentapp.JobMigrateBatch, rt.Content.HandleMigrateBatch, 10*time.Minute)
	rt.Content.AddHook(rt.Engine.EventHook())
	rt.Content.AddHook(rt.Content.AggregateHook())
	rt.Content.AddHook(rt.Content.IndexHook())
	rt.JobKinds.Register(contentapp.JobIndexFeatures, rt.Content.HandleIndexFeatures, time.Minute)
	rt.JobKinds.Register(contentapp.JobRecomputeAggregate, rt.Content.HandleRecomputeAggregate, time.Minute)
	rt.JobKinds.Register(flowapp.JobRunFlow, rt.Engine.HandleRunFlow, 15*time.Minute)
	rt.JobKinds.Register(flowapp.JobEmitEvent, rt.Engine.HandleEmitEvent, time.Minute)
	email := opt.Email
	if email == nil && cfg.Email.Driver == "smtp" {
		pw := ""
		if cfg.Email.Password != "" {
			v, ok := secrets(cfg.Email.Password)
			if !ok {
				return nil, fmt.Errorf("email.password: secret %s is not set", cfg.Email.Password)
			}
			pw = v
		}
		email = integrationinfra.SMTPSender{Addr: cfg.Email.Addr, From: cfg.Email.From, Username: cfg.Email.Username, Password: pw, TLS: cfg.Email.TLS}
	}
	if email == nil {
		email = integrationinfra.LogSender{Log: opt.Log}
	}
	delivery := &integrationapp.Delivery{Email: email, HTTP: out, Templates: templates, Secrets: secrets, Clock: opt.Clock,
		Blob: func(ctx context.Context, id string, max int64) ([]byte, string, error) {
			return blobSvc.Bytes(ctx, id, max)
		}}
	rt.JobKinds.Register(flowapp.JobDeliverEmail, delivery.HandleEmail, time.Minute)
	rt.JobKinds.Register(flowapp.JobDeliverWebhook, delivery.HandleWebhook, time.Minute)
	blobs, err := rt.blobService(cfg, cursorKey, opt)
	blobSvc = blobs
	if err != nil {
		return nil, err
	}
	schedules, err := rt.Engine.Schedules()
	if err != nil {
		return nil, err
	}
	retention := time.Duration(cfg.Audit.RetentionDays) * 24 * time.Hour
	rt.JobKinds.Register(contentapp.JobExpireAudit, rt.Content.ExpireAudit(retention), 5*time.Minute)
	schedules = append(schedules, flowapp.Schedule{Name: contentapp.JobExpireAudit,
		Next: func(t time.Time) time.Time { return t.Truncate(24 * time.Hour).Add(24*time.Hour + 3*time.Hour) },
		Job:  kernel.JobRequest{Kind: contentapp.JobExpireAudit, Queue: kernel.QueueMaintenance, Args: []byte("{}"), MaxAttempts: 3}})
	schedules = append(schedules, flowapp.Schedule{Name: integrationapp.JobCleanupBlobs,
		Next: func(t time.Time) time.Time { return t.Truncate(time.Hour).Add(time.Hour) },
		Job:  kernel.JobRequest{Kind: integrationapp.JobCleanupBlobs, Queue: kernel.QueueMaintenance, Args: []byte("{}"), MaxAttempts: 3}})
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
	accessH.Signed = []string{"/v1/blobs/", "/s/"}
	accessH.PublicURL = cfg.HTTP.PublicURL
	accessH.Register(mux)
	integrationhttp.New(blobs).Register(mux)
	contenthttp.New(rt.Content).Register(mux)
	flowhttp.New(rt.Engine).Register(mux)
	rt.Gateway = changeapp.NewGateway(rt.ChangeLog, rt.Content.StreamReader(), rt.Authorizer, c.Types, querydomain.NewPlanner(cursorKey), opt.Log)
	changehttp.New(rt.Gateway).Register(mux)
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
	if !cfg.Admin.DisableConsole {
		mux.Handle("GET /admin/", adminui.Handler())
		mux.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusFound))
	}
	rt.UI = uiapp.New(uiapp.Deps{UI: c.UI, Types: c.Types, Clock: opt.Clock, Version: c.Manifest.Hash[:12],
		FlowActions: func(ct *schemaapi.CompiledType) map[string]json.RawMessage {
			out := map[string]json.RawMessage{}
			for _, f := range c.Flows.ByTrigger(flowdomain.TriggerAction) {
				if f.Matches(ct) {
					out[f.Def.Trigger.Action] = f.Def.Input
				}
			}
			return out
		}})
	(&uiEditor{rt: rt}).register(mux)
	uihttp.New(rt.UI, webui.FS(), !cfg.Admin.DisableConsole).Register(mux)
	maxBody := cfg.HTTP.MaxBodyBytes
	if maxBody == 0 {
		maxBody = 1 << 20
	}
	rt.Handler = httpserver.Chain(observability.Route(mux),
		httpserver.WithRequestID,
		httpserver.Recover,
		httpserver.AccessLog(opt.Log),
		httpserver.LimitBody(maxBody, "/v1/blobs/"),
		accessH.Middleware,
		httpserver.RateLimit(httpserver.NewMemoryLimiter(), rateConfig(cfg, c)),
		httpserver.Idempotency(idem, func(r *http.Request) string {
			return accessapi.PrincipalFrom(r.Context()).ID.String()
		}),
	)
	rt.Handler = observability.HTTP(rt.Handler)

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
		// The embedded search index lives in memory: rebuild it from the
		// repositories (spec 7.5).
		if err := rt.Content.StartMigrations(ctx); err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
		go func() {
			if n, err := rt.Content.ReindexAll(ctx); err != nil {
				rt.Log.Error("search reindex failed", "err", err)
			} else if n > 0 {
				rt.Log.Info("search index rebuilt", "features", n)
			}
		}()
		rt.Jobs.Start(ctx)
		if rt.River != nil {
			if err := rt.River.Start(ctx); err != nil {
				return fmt.Errorf("river: %w", err)
			}
		}
	}
	if rt.Cfg.HasRole(config.RoleGateway) || rt.Cfg.HasRole(config.RoleAPI) {
		go func() {
			if err := rt.Gateway.Run(ctx); err != nil {
				rt.Log.Error("gateway stopped", "err", err)
			}
		}()
	}
	go rt.Health.RunWatchdog(ctx)
	return nil
}

// Close stops River and the runner and closes databases.
func (rt *Runtime) Close(ctx context.Context) {
	if rt.River != nil && rt.Cfg.HasRole(config.RoleWorker) {
		_ = rt.River.Stop(ctx)
	}
	for i := len(rt.closers) - 1; i >= 0; i-- {
		rt.closers[i]()
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
	if rt.Cfg.Admin.DisableAccounts {
		rt.Log.Info("admin accounts are disabled; admins cannot sign in")
	}
	return nil
}

// Type returns a compiled type by short name (tests and CLI helpers).
func (rt *Runtime) Type(name string) (*schemaapi.CompiledType, bool) {
	return rt.App.Types.ByName(name)
}

// keep the query api import used for documentation of the planner port.
var _ queryapi.Planner = (*querydomain.Planner)(nil)

// rateConfig applies the operator's request rates, lowered by app.json
// (spec 14.1).
func rateConfig(cfg config.Config, c *Compiled) httpserver.RateConfig {
	per := cfg.Limits.RequestsPerMinute
	if app := c.Manifest.App.Limits.RequestsPerMinute; app > 0 && (per <= 0 || app < per) {
		per = app
	}
	return httpserver.RateConfig{PerPrincipal: per, PerIP: cfg.Limits.AnonymousPerMinute, TrustProxy: cfg.HTTP.TrustProxyHeaders,
		Exempt: []string{"/v1/blobs/", "/assets/"},
		Key: func(r *http.Request) string {
			p := accessapi.PrincipalFrom(r.Context())
			if p.ID.IsZero() {
				return ""
			}
			return p.ID.String()
		}}
}
