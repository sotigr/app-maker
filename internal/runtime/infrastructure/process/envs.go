// Package process runs code-node workers as separate OS processes
// (spec 11.3 isolation level "process": development and single-replica
// deployments whose code is written by the app's own developers).
package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	rt "app-maker/internal/runtime/api"
	"app-maker/internal/runtime/application"
	fwpython "app-maker/runner/python"
)

// Envs writes bundles and fwnode under Root and builds virtual
// environments with uv, keyed by the lock file hash (spec 11.5).
type Envs struct {
	Root string
	// Python is the interpreter for bundles without requirements;
	// default python3 on PATH.
	Python string

	mu     sync.Mutex
	fwnode string
}

var _ application.Environments = (*Envs)(nil)

// FwnodeDir writes the embedded fwnode package once and returns the
// directory to put on sys.path.
func (e *Envs) FwnodeDir() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fwnode != "" {
		return e.fwnode, nil
	}
	h := sha256.New()
	files := map[string][]byte{}
	err := fs.WalkDir(fwpython.FS, "fwnode", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fwpython.FS.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = data
		h.Write([]byte(path))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	dir := filepath.Join(e.Root, "fwnode", hex.EncodeToString(h.Sum(nil))[:16])
	if err := writeTree(dir, files); err != nil {
		return "", err
	}
	e.fwnode = dir
	return dir, nil
}

// Prepare implements application.Environments.
func (e *Envs) Prepare(ctx context.Context, b rt.CodeBundle) (application.Env, bool, error) {
	dir := filepath.Join(e.Root, "bundles", b.Hash)
	cached := exists(filepath.Join(dir, ".ready"))
	if !cached {
		if err := writeTree(dir, b.Files); err != nil {
			return application.Env{}, false, err
		}
	}
	python, err := e.interpreter(ctx, b.Requirements)
	if err != nil {
		return application.Env{}, false, err
	}
	return application.Env{Dir: dir, Python: python}, cached, nil
}

func (e *Envs) interpreter(ctx context.Context, requirements []byte) (string, error) {
	if len(bytes.TrimSpace(requirements)) == 0 {
		if e.Python != "" {
			return e.Python, nil
		}
		return exec.LookPath("python3")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		return "", fmt.Errorf("requirements.lock needs uv on the runner's PATH to build the environment (spec 11.5)")
	}
	sum := sha256.Sum256(requirements)
	venv := filepath.Join(e.Root, "venvs", hex.EncodeToString(sum[:])[:16])
	python := filepath.Join(venv, "bin", "python")
	if exists(filepath.Join(venv, ".ready")) {
		return python, nil
	}
	e.mu.Lock() // one build at a time
	defer e.mu.Unlock()
	if exists(filepath.Join(venv, ".ready")) {
		return python, nil
	}
	_ = os.RemoveAll(venv)
	base := e.Python
	if base == "" {
		base = "python3"
	}
	if out, err := exec.CommandContext(ctx, uv, "venv", "--quiet", "--python", base, venv).CombinedOutput(); err != nil {
		return "", fmt.Errorf("uv venv: %v: %s", err, strings.TrimSpace(string(out)))
	}
	lock := filepath.Join(venv, "requirements.lock")
	if err := os.WriteFile(lock, requirements, 0o600); err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, uv, "pip", "sync", "--quiet", "--require-hashes", "--python", python, lock).CombinedOutput(); err != nil {
		return "", fmt.Errorf("uv pip sync: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.WriteFile(filepath.Join(venv, ".ready"), nil, 0o600); err != nil {
		return "", err
	}
	return python, nil
}

// writeTree writes files into dir atomically, read-only (spec 11.3: a
// read-only code directory).
func writeTree(dir string, files map[string][]byte) error {
	if exists(filepath.Join(dir, ".ready")) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".tmp-")
	if err != nil {
		return err
	}
	for name, data := range files {
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("bundle path %q escapes the bundle", name)
		}
		p := filepath.Join(tmp, clean)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o444); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, ".ready"), nil, 0o444); err != nil {
		return err
	}
	_ = filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o555)
		}
		return nil
	})
	if err := os.Rename(tmp, dir); err != nil {
		_ = makeWritable(tmp)
		_ = os.RemoveAll(tmp)
		if exists(filepath.Join(dir, ".ready")) {
			return nil // another process won the race
		}
		return err
	}
	return nil
}

func makeWritable(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// RemoveAll deletes Root, including read-only bundle directories.
func (e *Envs) RemoveAll() error {
	_ = makeWritable(e.Root)
	return os.RemoveAll(e.Root)
}
