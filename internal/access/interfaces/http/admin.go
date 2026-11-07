package http

import (
	"net/http"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	"organon/platform/httpserver"
)

// Account routes of the admin console (ADR 0027).

func (h *Handler) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/meta", h.adminMeta)
	mux.HandleFunc("GET /v1/auth/tokens", h.listTokens)
	mux.HandleFunc("DELETE /v1/auth/tokens/{prefix}", h.revokeToken)
	mux.HandleFunc("GET /v1/auth/users/{id}/credentials", h.credentials)
	mux.HandleFunc("POST /v1/auth/users/{id}/password", h.setPassword)
	mux.HandleFunc("DELETE /v1/auth/users/{id}/mfa", h.resetMFA)
	mux.HandleFunc("DELETE /v1/auth/users/{id}/sessions", h.signOutEverywhere)
}

// signedIn returns the caller, or writes 401 for anonymous callers.
func signedIn(w http.ResponseWriter, r *http.Request) (accessapi.Principal, bool) {
	p := accessapi.PrincipalFrom(r.Context())
	if p.IsAnonymous() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return p, false
	}
	return p, true
}

// principalParam reads the {id} path value, or the principal query
// parameter (default: the caller).
func principalParam(w http.ResponseWriter, r *http.Request, actor accessapi.Principal) (kernel.ID, bool) {
	raw := r.PathValue("id")
	if raw == "" {
		raw = r.URL.Query().Get("principal")
	}
	if raw == "" {
		return actor.ID, true
	}
	id, err := kernel.ParseID(raw)
	if err != nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return kernel.NilID, false
	}
	return id, true
}

func (h *Handler) adminMeta(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	m, err := h.auth.AdminMeta(actor)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, m)
}

func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	id, ok := principalParam(w, r, actor)
	if !ok {
		return
	}
	keys, err := h.auth.APIKeys(r.Context(), actor, id)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": keys})
}

func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	id, ok := principalParam(w, r, actor)
	if !ok {
		return
	}
	if err := h.auth.RevokeAPIKey(r.Context(), actor, id, r.PathValue("prefix")); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) credentials(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	id, ok := principalParam(w, r, actor)
	if !ok {
		return
	}
	c, err := h.auth.Credentials(r.Context(), actor, id)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, c)
}

func (h *Handler) setPassword(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	id, ok := principalParam(w, r, actor)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if err := h.auth.SetPassword(r.Context(), actor, id, body.Password); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) resetMFA(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	id, ok := principalParam(w, r, actor)
	if !ok {
		return
	}
	if err := h.auth.ResetMFA(r.Context(), actor, id); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) signOutEverywhere(w http.ResponseWriter, r *http.Request) {
	actor, ok := signedIn(w, r)
	if !ok {
		return
	}
	id, ok := principalParam(w, r, actor)
	if !ok {
		return
	}
	if err := h.auth.SignOutEverywhere(r.Context(), actor, id); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
