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

// Load reads the manifest in dir for environment env. The env file
// env/<env>.json is optional unless app.json references ${env.*}.
func Load(dir, env string) (*domain.Manifest, error) {
	m := &domain.Manifest{Dir: dir, Env: env, Seeds: map[string]domain.File{}}
	envValues := map[string]any{}
	if env != "" {
		b, err := os.ReadFile(filepath.Join(dir, "env", env+".json"))
		switch {
		case err == nil:
			if err := json.Unmarshal(b, &envValues); err != nil {
				return nil, fmt.Errorf("env/%s.json: %w", env, err)
			}
		case !errors.Is(err, fs.ErrNotExist):
			return nil, err
		}
	}
	appJSON, err := os.ReadFile(filepath.Join(dir, "app.json"))
	if err != nil {
		return nil, fmt.Errorf("manifest %s: app.json: %w", dir, err)
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
	roots := []string{dir}
	modules, _ := filepath.Glob(filepath.Join(dir, "modules", "*"))
	sort.Strings(modules)
	roots = append(roots, modules...)
	for _, root := range roots {
		for _, g := range []struct {
			sub string
			ext string
			dst *[]domain.File
		}{
			{"types", ".json", &m.Types}, {"traits", ".json", &m.Traits}, {"policies", ".json", &m.Policies},
			{"flows", ".json", &m.Flows}, {"nodes", "", &m.Nodes},
		} {
			files, err := readDir(dir, filepath.Join(root, g.sub), g.ext)
			if err != nil {
				return nil, err
			}
			*g.dst = append(*g.dst, files...)
		}
	}
	for _, g := range []struct {
		sub string
		ext string
		dst *[]domain.File
	}{{"tests", ".json", &m.Tests}, {"migrations", ".json", &m.Migrations}, {"templates", "", &m.Templates}} {
		files, err := readDir(dir, filepath.Join(dir, g.sub), g.ext)
		if err != nil {
			return nil, err
		}
		*g.dst = files
	}
	seeds, err := readDir(dir, filepath.Join(dir, "seed"), ".json")
	if err != nil {
		return nil, err
	}
	for _, s := range seeds {
		m.Seeds[strings.TrimSuffix(filepath.Base(s.Path), ".json")] = s
	}
	m.ComputeHash(appJSON)
	return m, nil
}

// readDir reads files under sub recursively; ext filters by extension.
func readDir(base, sub, ext string) ([]domain.File, error) {
	var out []domain.File
	err := filepath.WalkDir(sub, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return err
		}
		if d.IsDir() || (ext != "" && filepath.Ext(path) != ext) || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, path)
		out = append(out, domain.File{Path: filepath.ToSlash(rel), Data: data})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}
