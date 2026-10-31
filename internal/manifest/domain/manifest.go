// Package domain is the Manifest context's domain: the app manifest, its
// app.json, environment substitution, and the manifest hash (spec 12).
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// File is one manifest file, with its path relative to the manifest root.
type File struct {
	Path string
	Data []byte
}

// Manifest is a loaded app manifest (spec 12.1).
type Manifest struct {
	Dir        string
	Env        string
	App        AppConfig
	Types      []File
	Traits     []File
	Policies   []File
	Flows      []File
	Tests      []File
	Migrations []File
	Nodes      []File
	Templates  []File
	Seeds      map[string]File // by environment name
	Hash       string
}

// AppConfig is app.json (spec 12.2).
type AppConfig struct {
	App          string              `json:"app"`
	Version      string              `json:"version"`
	Framework    string              `json:"framework,omitempty"`
	Repositories map[string]string   `json:"repositories,omitempty"`
	Auth         AuthConfig          `json:"auth"`
	Roles        map[string][]string `json:"roles,omitempty"`
	Limits       Limits              `json:"limits"`
	LLM          json.RawMessage     `json:"llm,omitempty"`
	Python       json.RawMessage     `json:"python,omitempty"`
	Settings     map[string]any      `json:"settings,omitempty"`
}

// AuthConfig is the auth section of app.json.
type AuthConfig struct {
	Password struct {
		Enabled   bool `json:"enabled"`
		MinLength int  `json:"minLength,omitempty"`
	} `json:"password"`
	OIDC []json.RawMessage `json:"oidc,omitempty"`
	MFA  json.RawMessage   `json:"mfa,omitempty"`
}

// Limits may only lower the server defaults (spec 14.1).
type Limits struct {
	RequestsPerMinute int `json:"requestsPerMinute,omitempty"`
	MaxDocumentBytes  int `json:"maxDocumentBytes,omitempty"`
}

var envRef = regexp.MustCompile(`\$\{env\.([A-Za-z0-9_.]+)\}`)

// Substitute replaces ${env.<path>} references with values from the
// environment file. ${secret:<name>} references are left for the secret
// store and never resolved into the manifest (spec 12.2).
func Substitute(data []byte, env map[string]any) ([]byte, error) {
	var missing []string
	out := envRef.ReplaceAllFunc(data, func(m []byte) []byte {
		path := string(envRef.FindSubmatch(m)[1])
		v, ok := lookup(env, strings.Split(path, "."))
		if !ok {
			missing = append(missing, path)
			return m
		}
		switch x := v.(type) {
		case string:
			b, _ := json.Marshal(x)
			return b[1 : len(b)-1] // inside an existing JSON string
		default:
			b, _ := json.Marshal(x)
			return b
		}
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("undefined environment values: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

func lookup(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = mm[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// ComputeHash hashes every file path and content (the deploy identity,
// spec 12.4).
func (m *Manifest) ComputeHash(appJSON []byte) {
	h := sha256.New()
	write := func(path string, data []byte) {
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	write("app.json", appJSON)
	var all []File
	for _, group := range [][]File{m.Types, m.Traits, m.Policies, m.Flows, m.Migrations, m.Nodes, m.Templates} {
		all = append(all, group...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	for _, f := range all {
		write(f.Path, f.Data)
	}
	m.Hash = hex.EncodeToString(h.Sum(nil))
}
