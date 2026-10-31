package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/grpc"

	codenodev1 "app-maker/api/proto/codenode/v1"
	rtapi "app-maker/internal/runtime/api"
	runtimeapp "app-maker/internal/runtime/application"
	"app-maker/internal/runtime/infrastructure/grpcrunner"
	"app-maker/internal/runtime/infrastructure/process"
	grpcserver "app-maker/internal/runtime/interfaces/grpc"
	"app-maker/platform/config"
)

// codeRunner starts the runner role when configured and connects the
// engine to a runner (spec 11.1). It returns nil when no runner is
// reachable; code nodes then fail with unsupported_capability.
func (rt *Runtime) codeRunner(cfg config.Config, opt Options) (rtapi.CodeRunner, error) {
	target := cfg.Runner.Addr
	if cfg.HasRole(config.RoleRunner) {
		root := cfg.Runner.CacheDir
		if root == "" {
			root = filepath.Join(os.TempDir(), "fw-runner-"+strconv.Itoa(os.Getuid()))
		}
		envs := &process.Envs{Root: root}
		runner := runtimeapp.NewRunner(runtimeapp.RunnerConfig{MaxWorkers: cfg.Runner.MaxWorkers}, envs,
			&process.Starter{Envs: envs, Log: opt.Log, AllowPrivate: cfg.Dev}, opt.Log)
		network, addr := "unix", cfg.Runner.Listen
		if addr == "" {
			dir, err := os.MkdirTemp("", "fw-runner-")
			if err != nil {
				return nil, err
			}
			addr = filepath.Join(dir, "runner.sock")
			rt.closers = append(rt.closers, func() { _ = os.RemoveAll(dir) })
		} else if !strings.Contains(addr, "/") {
			network = "tcp"
		}
		ln, err := net.Listen(network, addr)
		if err != nil {
			return nil, fmt.Errorf("runner: listen %s: %w", addr, err)
		}
		srv := grpc.NewServer(grpc.MaxRecvMsgSize(64 << 20))
		codenodev1.RegisterRunnerServer(srv, grpcserver.New(runner))
		go func() { _ = srv.Serve(ln) }()
		rt.closers = append(rt.closers, func() { srv.Stop(); runner.Close() })
		if target == "" {
			target = network + "://" + addr
			if network == "tcp" {
				target = addr
			}
		}
	}
	if target == "" {
		return nil, nil
	}
	client, err := grpcrunner.Dial(target)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	rt.closers = append(rt.closers, func() { _ = client.Close() })
	return client, nil
}

// bundles builds code bundles from the manifest: every file under the
// nodes/ directory that holds the code file (spec 11.1), plus the lock file.
type bundles struct {
	files map[string][]byte
	mu    sync.Mutex
	cache map[[2]string]rtapi.CodeBundle
}

func newBundles(c *Compiled) *bundles {
	b := &bundles{files: map[string][]byte{}, cache: map[[2]string]rtapi.CodeBundle{}}
	for _, f := range c.Manifest.Nodes {
		b.files[f.Path] = f.Data
	}
	return b
}

func (b *bundles) get(code, requirements string) (rtapi.CodeBundle, string, error) {
	i := strings.LastIndex("/"+code, "/nodes/")
	if i < 0 {
		return rtapi.CodeBundle{}, "", fmt.Errorf("code %s is not under nodes/", code)
	}
	root := code[:i] + "nodes/" // "nodes/" or "modules/<m>/nodes/"
	entry := strings.TrimPrefix(code, root)
	b.mu.Lock()
	defer b.mu.Unlock()
	key := [2]string{root, requirements}
	if cached, ok := b.cache[key]; ok {
		return cached, entry, nil
	}
	out := rtapi.CodeBundle{Files: map[string][]byte{}}
	if requirements != "" {
		out.Requirements = b.files[requirements]
	}
	var names []string
	for p, data := range b.files {
		if rel, ok := strings.CutPrefix(p, root); ok && p != requirements {
			out.Files[rel] = data
			names = append(names, rel)
		}
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n))
		h.Write([]byte{0})
		h.Write(out.Files[n])
		h.Write([]byte{0})
	}
	h.Write([]byte("requirements\x00"))
	h.Write(out.Requirements)
	out.Hash = hex.EncodeToString(h.Sum(nil))
	b.cache[key] = out
	return out, entry, nil
}
