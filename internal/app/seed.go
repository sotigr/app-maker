package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"

	accessapi "app-maker/internal/access/api"
	accessapp "app-maker/internal/access/application"
	contentapp "app-maker/internal/content/application"
	"app-maker/internal/kernel"
)

// Seed is seed/<env>.json (spec 12.1): users, features, and grants.
type Seed struct {
	Users []struct {
		Ref      string   `json:"ref"`
		Email    string   `json:"email"`
		Password string   `json:"password"`
		Name     string   `json:"name"`
		Roles    []string `json:"roles"`
	} `json:"users"`
	Features []TestFeature `json:"features"`
	Grants   []TestGrant   `json:"grants"`
}

// ApplySeed loads seed data in-process as the system (fw dev).
func ApplySeed(ctx context.Context, rt *Runtime, data []byte) error {
	var s Seed
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	refs := map[string]string{}
	resolve := func(r string) string {
		if id, ok := refs[r]; ok {
			return id
		}
		return r
	}
	for _, u := range s.Users {
		id, err := rt.Auth.CreateUser(ctx, accessapi.System(), accessapp.NewUser{Email: u.Email, DisplayName: u.Name, Password: u.Password, Roles: u.Roles})
		if err != nil {
			return fmt.Errorf("seed user %s: %w", u.Email, err)
		}
		refs[u.Ref] = id.String()
	}
	for _, f := range s.Features {
		var owner *kernel.ID
		if f.Owner != "" {
			id, err := kernel.ParseID(resolve(f.Owner))
			if err != nil {
				return fmt.Errorf("seed feature %s: unknown owner %s", f.Ref, f.Owner)
			}
			owner = &id
		}
		data := map[string]any{}
		for k, v := range f.Data {
			if str, ok := v.(string); ok {
				v = resolve(str)
			}
			data[k] = v
		}
		r, err := rt.Content.Execute(ctx, contentapp.WriteContext{Principal: accessapi.System(), Fixture: true, Owner: owner},
			[]contentapp.Op{{Op: contentapp.OpCreate, Type: f.Type, Ref: "$s", Parent: resolve(f.Parent), Data: data}})
		if err != nil {
			return fmt.Errorf("seed feature %s: %w", f.Ref, err)
		}
		refs[f.Ref] = r.Refs["$s"]
	}
	for _, g := range s.Grants {
		_, err := rt.Content.Execute(ctx, contentapp.WriteContext{Principal: accessapi.System(), Fixture: true},
			[]contentapp.Op{{Op: contentapp.OpCreate, Type: "grant", Parent: resolve(g.Object),
				Data: map[string]any{"subject": resolve(g.Subject), "role": g.Role}}})
		if err != nil {
			return fmt.Errorf("seed grant: %w", err)
		}
	}
	return nil
}

// Switch is an http.Handler whose target can be replaced atomically (hot
// reload in fw dev, manifest activation).
type Switch struct{ h atomic.Pointer[http.Handler] }

// Set replaces the handler.
func (s *Switch) Set(h http.Handler) { s.h.Store(&h) }

// ServeHTTP implements http.Handler.
func (s *Switch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := s.h.Load()
	if h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	(*h).ServeHTTP(w, r)
}
