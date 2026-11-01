// Package http serves the end-user UI (ADR 0016): the bundle for the
// caller at /v1/ui, the component catalog, the app's styles and assets,
// and the embedded renderer at / with single-page-app fallback.
package http

import (
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	"organon/internal/ui/application"
	"organon/platform/httpserver"
)

// csp allows only same-origin scripts, styles, images, and connections.
const csp = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"

// Handler serves the UI routes.
type Handler struct {
	svc *application.Service
	// web is the built renderer (web/dist): index.html and assets/.
	web fs.FS
	// console is whether /admin is served (the root's fallback).
	console bool
}

// New returns a handler. web may be nil (no renderer built in).
func New(svc *application.Service, web fs.FS, console bool) *Handler {
	return &Handler{svc: svc, web: web, console: console}
}

// Register adds the routes. "GET /" catches every path no other route
// matches, so client-side routes load the renderer.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ui", h.bundle)
	mux.HandleFunc("GET /v1/ui/catalog", h.catalog)
	mux.HandleFunc("GET /assets/styles.css", h.styles)
	mux.HandleFunc("GET /assets/app/{path...}", h.asset)
	mux.HandleFunc("GET /assets/organon/{path...}", h.runtime)
	mux.HandleFunc("GET /", h.page)
}

func (h *Handler) bundle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	prefs := []string{r.URL.Query().Get("lang")}
	prefs = append(prefs, acceptLanguages(r.Header.Get("Accept-Language"))...)
	httpserver.WriteJSON(w, http.StatusOK, h.svc.Bundle(accessapi.PrincipalFrom(r.Context()), prefs))
}

// acceptLanguages returns the tags of an Accept-Language header, most
// preferred first.
func acceptLanguages(header string) []string {
	type tag struct {
		name string
		q    float64
	}
	var tags []tag
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				q = f
			}
		}
		if name != "" && name != "*" && q > 0 {
			tags = append(tags, tag{name, q})
		}
	}
	sort.SliceStable(tags, func(i, j int) bool { return tags[i].q > tags[j].q })
	out := make([]string, len(tags))
	for i, t := range tags {
		out[i] = t.name
	}
	return out
}

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	p := accessapi.PrincipalFrom(r.Context())
	switch {
	case p.IsAnonymous():
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
	case !p.IsSystem() && !p.HasRole("admin"):
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeForbidden, "admins only"))
	default:
		httpserver.WriteJSON(w, http.StatusOK, h.svc.Catalog())
	}
}

func static(w http.ResponseWriter, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=300")
	if contentType == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	}
	_, _ = w.Write(data)
}

func (h *Handler) styles(w http.ResponseWriter, r *http.Request) {
	css := h.svc.Styles()
	if css == nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	static(w, "text/css; charset=utf-8", css)
}

func (h *Handler) asset(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	data, ct, ok := h.svc.Asset(p)
	if !ok || strings.Contains(p, "..") {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	static(w, ct, data)
}

// runtime serves the renderer's hashed build assets.
func (h *Handler) runtime(w http.ResponseWriter, r *http.Request) {
	if h.web == nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	p := r.PathValue("path")
	if _, err := fs.Stat(h.web, p); err != nil || p == "" {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFileFS(w, r, h.web, p)
}

// page serves index.html for every client-side route of the app. Without
// a UI the root goes to the admin console, if it is served.
func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/admin" || strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/admin/") || strings.HasPrefix(p, "/assets/") {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	if !h.svc.Enabled() || h.web == nil {
		if p == "/" && h.console {
			http.Redirect(w, r, "/admin/", http.StatusFound)
			return
		}
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	index, err := fs.ReadFile(h.web, "index.html")
	if err != nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", csp)
	_, _ = w.Write(index)
}
