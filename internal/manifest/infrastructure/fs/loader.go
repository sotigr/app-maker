// Package fs loads app manifests from a directory (spec 12.1).
package fs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"app-maker/internal/manifest/domain"
)

// Load reads the manifest in dir for environment env.
func Load(dir, env string) (*domain.Manifest, error) {
	files, err := ReadDir(dir)
	if err != nil {
		return nil, err
	}
	return LoadFiles(dir, files, env)
}

// ReadDir collects the manifest files under dir (hidden entries skipped).
func ReadDir(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && path != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		files[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", dir, err)
	}
	return files, nil
}

// LoadFiles builds a manifest from files keyed by path relative to the
// manifest root (a directory or an uploaded bundle, spec 12.4). The env
// file env/<env>.json is optional unless app.json references ${env.*}.
func LoadFiles(dir string, files map[string][]byte, env string) (*domain.Manifest, error) {
	m := &domain.Manifest{Dir: dir, Env: env, Seeds: map[string]domain.File{}}
	envValues := map[string]any{}
	if b, ok := files["env/"+env+".json"]; ok && env != "" {
		if err := json.Unmarshal(b, &envValues); err != nil {
			return nil, fmt.Errorf("env/%s.json: %w", env, err)
		}
	}
	appJSON, ok := files["app.json"]
	if !ok {
		return nil, fmt.Errorf("manifest %s: app.json is missing", dir)
	}
	resolved, err := domain.Substitute(appJSON, envValues)
	if err != nil {
		return nil, fmt.Errorf("app.json: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(resolved))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m.App); err != nil {
		return nil, fmt.Errorf("app.json: %w", err)
	}
	if m.App.App == "" {
		return nil, errors.New("app.json: app is required")
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		f := domain.File{Path: p, Data: files[p]}
		parts := strings.Split(p, "/")
		if len(parts) >= 3 && parts[0] == "modules" {
			parts = parts[2:] // modules/<name>/<dir>/... (spec 12.1)
		}
		if len(parts) < 2 {
			continue
		}
		ext := filepath.Ext(p)
		switch parts[0] {
		case "types":
			if ext == ".json" {
				m.Types = append(m.Types, f)
			}
		case "traits":
			if ext == ".json" {
				m.Traits = append(m.Traits, f)
			}
		case "policies":
			if ext == ".json" {
				m.Policies = append(m.Policies, f)
			}
		case "flows":
			if ext == ".json" {
				m.Flows = append(m.Flows, f)
			}
		case "nodes":
			m.Nodes = append(m.Nodes, f)
		case "tests":
			if ext == ".json" && !strings.HasPrefix(p, "modules/") {
				m.Tests = append(m.Tests, f)
			}
		case "migrations":
			if ext == ".json" {
				m.Migrations = append(m.Migrations, f)
			}
		case "templates":
			m.Templates = append(m.Templates, f)
		case "seed":
			if ext == ".json" {
				m.Seeds[strings.TrimSuffix(parts[len(parts)-1], ".json")] = f
			}
		}
	}
	m.Files = files
	m.ComputeHash(appJSON)
	return m, nil
}
