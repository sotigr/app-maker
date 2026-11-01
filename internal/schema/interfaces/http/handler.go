// Package http serves /v1/schema (spec 6.2).
package http

import (
	"net/http"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
	"organon/internal/schema/application"
	"organon/platform/httpserver"
)

// Handler serves schema documents for the caller's audience.
type Handler struct {
	types   schemaapi.TypeRegistry
	app     string
	version string
}

// New returns a handler.
func New(types schemaapi.TypeRegistry, app, version string) *Handler {
	return &Handler{types: types, app: app, version: version}
}

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/schema", h.openapi)
	mux.HandleFunc("GET /v1/schema/types/{type}", h.describe)
	mux.HandleFunc("GET /v1/schema/types", h.list)
}

// audience is the document audience: what any feature of a type would
// show this caller at most, excluding owner-only grants.
func audience(p accessapi.Principal) schemaapi.Audience {
	switch {
	case p.IsSystem():
		return schemaapi.AudSystem
	case p.HasRole("admin"):
		return schemaapi.AudAdmin
	case !p.IsAnonymous():
		return schemaapi.AudOwner
	}
	return schemaapi.AudPublic
}

func (h *Handler) openapi(w http.ResponseWriter, r *http.Request) {
	a := audience(accessapi.PrincipalFrom(r.Context()))
	httpserver.WriteJSON(w, http.StatusOK, application.OpenAPI(h.types, a, h.app, h.version))
}

func (h *Handler) describe(w http.ResponseWriter, r *http.Request) {
	ct, ok := h.types.ByName(r.PathValue("type"))
	if !ok {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, application.Describe(ct, audience(accessapi.PrincipalFrom(r.Context()))))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	a := audience(accessapi.PrincipalFrom(r.Context()))
	out := []application.TypeDescription{}
	for _, ct := range h.types.All() {
		out = append(out, application.Describe(ct, a))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"types": out})
}
