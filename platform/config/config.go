// Package config loads the server configuration: one YAML file plus FW_
// environment overrides, validated into a typed Config (spec 14.9).
package config

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Topology declares how replicas run (spec 14.10).
type Topology string

// Topologies.
const (
	TopologySingle     Topology = "single"
	TopologyReplicated Topology = "replicated"
)

// Roles (spec 2.1).
const (
	RoleAPI     = "api"
	RoleGateway = "gateway"
	RoleWorker  = "worker"
	RoleRunner  = "runner"
)

// Repository configures one feature repository.
type Repository struct {
	Driver string `yaml:"driver"` // memory | sqlite | postgres
	DSN    string `yaml:"dsn"`
}

// Config is the server configuration.
type Config struct {
	Roles    []string `yaml:"roles"`
	Topology Topology `yaml:"topology"`
	// Dev enables development behavior: the dev-only manifest editing
	// endpoint (ADR 0002), relaxed cookies, verbose errors in logs.
	Dev bool `yaml:"dev"`
	// Manifest is the app manifest directory; Env selects env/<env>.json.
	Manifest string `yaml:"manifest"`
	Env      string `yaml:"env"`

	HTTP struct {
		Addr          string `yaml:"addr"`
		InternalAddr  string `yaml:"internalAddr"`
		SecureCookies bool   `yaml:"secureCookies"`
		MaxBodyBytes  int64  `yaml:"maxBodyBytes"`
		// TrustProxyHeaders takes the client IP from X-Forwarded-For (only
		// behind a trusted reverse proxy).
		TrustProxyHeaders bool `yaml:"trustProxyHeaders"`
	} `yaml:"http"`

	Repositories map[string]Repository `yaml:"repositories"`
	Credentials  struct {
		Driver string `yaml:"driver"` // memory | sqlite | postgres
		DSN    string `yaml:"dsn"`
	} `yaml:"credentials"`
	Jobs struct {
		Driver      string         `yaml:"driver"` // inprocess | river
		Concurrency map[string]int `yaml:"concurrency"`
	} `yaml:"jobs"`
	Secrets struct {
		// CursorKey signs pagination cursors; shared by all replicas.
		CursorKey string `yaml:"cursorKey"`
	} `yaml:"secrets"`
	Bootstrap struct {
		AdminEmail    string `yaml:"adminEmail"`
		AdminPassword string `yaml:"adminPassword"`
	} `yaml:"bootstrap"`
	Log struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"` // json | text
	} `yaml:"log"`
	// Limits are the operator's request rates (spec 14.1); app.json may
	// only lower requestsPerMinute.
	Limits struct {
		RequestsPerMinute  int `yaml:"requestsPerMinute"`
		AnonymousPerMinute int `yaml:"anonymousPerMinute"`
	} `yaml:"limits"`
	Blobs struct {
		Driver string `yaml:"driver"` // fs (s3 later)
		// Dir holds fs blobs; empty uses a directory in the temp directory.
		Dir string `yaml:"dir"`
	} `yaml:"blobs"`
	Runner struct {
		// Isolation of code-node workers (spec 11.3): process | container |
		// microvm. Only process is implemented; it is for development and
		// single-replica deployments.
		Isolation string `yaml:"isolation"`
		// Listen is where the runner role serves: a Unix socket path or
		// host:port; empty uses a private socket in the temp directory.
		Listen string `yaml:"listen"`
		// Addr is the runner the engine calls ("unix:///path" or host:port);
		// empty uses the runner role of this process.
		Addr       string `yaml:"addr"`
		CacheDir   string `yaml:"cacheDir"`
		MaxWorkers int    `yaml:"maxWorkers"`
	} `yaml:"runner"`
	LLM struct {
		// Provider, when set, serves every model alias (fw dev: fake).
		Provider string `yaml:"provider"`
		// Providers are named model vendors that app.json aliases refer to
		// as "<name>:<model>". "fake" always exists; "anthropic" exists by
		// default and uses the SDK's credential resolution.
		Providers map[string]LLMProvider `yaml:"providers"`
	} `yaml:"llm"`
}

// LLMProvider configures one model vendor (spec 11.7).
type LLMProvider struct {
	Kind    string `yaml:"kind"` // anthropic | openai-compatible | fake
	BaseURL string `yaml:"baseURL"`
	// APIKey is a ${secret:<name>} reference; literal keys are rejected.
	APIKey string `yaml:"apiKey"`
}

// Default returns the development defaults: all roles, single topology,
// memory storage.
func Default() Config {
	var c Config
	c.Roles = []string{RoleAPI, RoleGateway, RoleWorker, RoleRunner}
	c.Topology = TopologySingle
	c.Env = "dev"
	c.HTTP.Addr = ":8080"
	c.HTTP.InternalAddr = ":9090"
	c.HTTP.MaxBodyBytes = 1 << 20
	c.Repositories = map[string]Repository{"default": {Driver: "memory"}}
	c.Credentials.Driver = "memory"
	c.Jobs.Driver = "inprocess"
	c.Log.Level = "info"
	c.Log.Format = "json"
	c.Runner.Isolation = "process"
	c.Blobs.Driver = "fs"
	c.Limits.RequestsPerMinute = 600
	c.Limits.AnonymousPerMinute = 60
	return c
}

// Load reads path (optional; "" skips the file), applies FW_ overrides
// from env, and validates.
func Load(path string, env func(string) string) (Config, error) {
	c := Default()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return c, fmt.Errorf("config: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil {
			return c, fmt.Errorf("config %s: %w", path, err)
		}
	}
	if err := c.applyEnv(env); err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c *Config) applyEnv(env func(string) string) error {
	str := func(key string, dst *string) {
		if v := env(key); v != "" {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) error {
		if v := env(key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("config: %s: %w", key, err)
			}
			*dst = b
		}
		return nil
	}
	if v := env("FW_ROLES"); v != "" {
		c.Roles = strings.Split(v, ",")
	}
	if v := env("FW_TOPOLOGY"); v != "" {
		c.Topology = Topology(v)
	}
	if err := boolean("FW_DEV", &c.Dev); err != nil {
		return err
	}
	str("FW_MANIFEST", &c.Manifest)
	str("FW_ENV", &c.Env)
	str("FW_HTTP_ADDR", &c.HTTP.Addr)
	str("FW_INTERNAL_ADDR", &c.HTTP.InternalAddr)
	if err := boolean("FW_SECURE_COOKIES", &c.HTTP.SecureCookies); err != nil {
		return err
	}
	if v := env("FW_REPOSITORY_DEFAULT_DRIVER"); v != "" {
		r := c.Repositories["default"]
		r.Driver = v
		c.Repositories["default"] = r
	}
	if v := env("FW_REPOSITORY_DEFAULT_DSN"); v != "" {
		r := c.Repositories["default"]
		r.DSN = v
		c.Repositories["default"] = r
	}
	str("FW_CREDENTIALS_DRIVER", &c.Credentials.Driver)
	str("FW_CREDENTIALS_DSN", &c.Credentials.DSN)
	str("FW_JOBS_DRIVER", &c.Jobs.Driver)
	str("FW_CURSOR_KEY", &c.Secrets.CursorKey)
	str("FW_BOOTSTRAP_ADMIN_EMAIL", &c.Bootstrap.AdminEmail)
	str("FW_BOOTSTRAP_ADMIN_PASSWORD", &c.Bootstrap.AdminPassword)
	str("FW_LOG_LEVEL", &c.Log.Level)
	str("FW_LOG_FORMAT", &c.Log.Format)
	str("FW_LLM_PROVIDER", &c.LLM.Provider)
	str("FW_RUNNER_ISOLATION", &c.Runner.Isolation)
	str("FW_BLOBS_DRIVER", &c.Blobs.Driver)
	str("FW_BLOBS_DIR", &c.Blobs.Dir)
	str("FW_RUNNER_LISTEN", &c.Runner.Listen)
	str("FW_RUNNER_ADDR", &c.Runner.Addr)
	return nil
}

// HasRole reports whether the process runs role.
func (c Config) HasRole(role string) bool { return slices.Contains(c.Roles, role) }

// Validate checks the configuration; errors name the key (spec 14.9).
func (c Config) Validate() error {
	var errs []error
	for _, r := range c.Roles {
		if !slices.Contains([]string{RoleAPI, RoleGateway, RoleWorker, RoleRunner}, r) {
			errs = append(errs, fmt.Errorf("roles: unknown role %q", r))
		}
	}
	switch c.Blobs.Driver {
	case "fs":
		if c.Topology == TopologyReplicated {
			errs = append(errs, fmt.Errorf("blobs.driver: fs keeps blobs on one machine; the replicated topology needs s3 (spec 14.10)"))
		}
	case "s3":
		errs = append(errs, fmt.Errorf("blobs.driver: s3 is not implemented yet"))
	default:
		errs = append(errs, fmt.Errorf("blobs.driver: must be fs or s3"))
	}
	switch c.Runner.Isolation {
	case "process":
		if c.Topology == TopologyReplicated && c.HasRole(RoleRunner) {
			errs = append(errs, fmt.Errorf("runner.isolation: process isolation is only for development and single-replica deployments (spec 11.3)"))
		}
	case "container", "microvm":
		if c.HasRole(RoleRunner) {
			errs = append(errs, fmt.Errorf("runner.isolation: %s isolation is not implemented yet", c.Runner.Isolation))
		}
	default:
		errs = append(errs, fmt.Errorf("runner.isolation: must be process, container, or microvm"))
	}
	for name, p := range c.LLM.Providers {
		if !slices.Contains([]string{"anthropic", "openai-compatible", "fake"}, p.Kind) {
			errs = append(errs, fmt.Errorf("llm.providers.%s.kind: unknown kind %q", name, p.Kind))
		}
		if p.Kind == "openai-compatible" && p.BaseURL == "" {
			errs = append(errs, fmt.Errorf("llm.providers.%s.baseURL: required for openai-compatible", name))
		}
		if p.APIKey != "" && !strings.HasPrefix(p.APIKey, "${secret:") {
			errs = append(errs, fmt.Errorf("llm.providers.%s.apiKey: must be a ${secret:<name>} reference", name))
		}
	}
	if c.Topology != TopologySingle && c.Topology != TopologyReplicated {
		errs = append(errs, fmt.Errorf("topology: must be single or replicated"))
	}
	if _, ok := c.Repositories["default"]; !ok {
		errs = append(errs, fmt.Errorf("repositories.default: required"))
	}
	for name, r := range c.Repositories {
		if !slices.Contains([]string{"memory", "sqlite", "postgres"}, r.Driver) {
			errs = append(errs, fmt.Errorf("repositories.%s.driver: unknown driver %q", name, r.Driver))
		}
		if r.Driver == "sqlite" && r.DSN == "" {
			errs = append(errs, fmt.Errorf("repositories.%s.dsn: required for sqlite", name))
		}
	}
	// The replicated topology refuses adapters that keep state in one
	// replica (spec 14.10).
	if c.Topology == TopologyReplicated {
		for name, r := range c.Repositories {
			if r.Driver == "memory" || r.Driver == "sqlite" {
				errs = append(errs, fmt.Errorf("repositories.%s.driver: %s is refused in the replicated topology", name, r.Driver))
			}
		}
		if c.Credentials.Driver == "memory" || c.Credentials.Driver == "sqlite" {
			errs = append(errs, fmt.Errorf("credentials.driver: %s is refused in the replicated topology", c.Credentials.Driver))
		}
		if c.Jobs.Driver == "inprocess" {
			errs = append(errs, fmt.Errorf("jobs.driver: inprocess is refused in the replicated topology"))
		}
		if c.Secrets.CursorKey == "" {
			errs = append(errs, fmt.Errorf("secrets.cursorKey: required in the replicated topology"))
		}
		if c.Dev {
			errs = append(errs, fmt.Errorf("dev: not allowed in the replicated topology"))
		}
	}
	if c.Manifest == "" {
		errs = append(errs, fmt.Errorf("manifest: required (directory of the app manifest)"))
	}
	return errors.Join(errs...)
}
