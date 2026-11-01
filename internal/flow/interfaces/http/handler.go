// Package http serves actions (spec 6.2 POST /actions), inbound webhooks
// (POST /hooks/{name}), and the flow catalog used by the visual editor.
package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	accessapi "organon/internal/access/api"
	"organon/internal/flow/application"
	"organon/internal/kernel"
	"organon/platform/httpserver"
)

// Handler serves flow endpoints.
type Handler struct {
	engine *application.Engine
}

// New returns a handler.
func New(engine *application.Engine) *Handler {
	return &Handler{engine: engine}
}

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/actions/{type}/{id}/{action}", h.action)
	mux.HandleFunc("POST /v1/hooks/{name}", h.hook)
	mux.HandleFunc("GET /v1/flows", h.list)
	mux.HandleFunc("POST /v1/flows/runs/{id}/retry", h.retry)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	if accessapi.PrincipalFrom(r.Context()).IsAnonymous() {
		switch kernel.CodeOf(err) {
		case kernel.CodeNotFound, kernel.CodeForbidden:
			err = kernel.ErrUnauthenticated
		}
	}
	httpserver.WriteError(w, r, err)
}

func (h *Handler) action(w http.ResponseWriter, r *http.Request) {
	id, err := kernel.ParseID(r.PathValue("id"))
	if err != nil {
		fail(w, r, kernel.ErrNotFound)
		return
	}
	var input map[string]any
	body, err := httpserver.ReadBody(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&input); err != nil {
			fail(w, r, kernel.NewError(kernel.CodeBadRequest, "action input must be a JSON object"))
			return
		}
	}
	res, err := h.engine.InvokeAction(r.Context(), accessapi.PrincipalFrom(r.Context()), r.PathValue("type"), id, r.PathValue("action"), input)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, res)
}

// hook accepts an inbound webhook; the run happens in a worker (spec 10.2).
func (h *Handler) hook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeBadRequest, "unreadable body"))
		return
	}
	if err := h.engine.Webhook(r.Context(), r.PathValue("name"), body, r.Header.Get("X-Organon-Signature")); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// list returns the flows and the node catalog (admins only).
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	p := accessapi.PrincipalFrom(r.Context())
	if !p.IsSystem() && !p.HasRole("admin") {
		fail(w, r, kernel.ErrForbidden)
		return
	}
	flows, nodes := h.engine.Catalog()
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"flows": flows, "nodes": nodes})
}

// retry re-enqueues a dead-lettered run (admins; spec 10.10).
func (h *Handler) retry(w http.ResponseWriter, r *http.Request) {
	id, err := kernel.ParseID(r.PathValue("id"))
	if err != nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	if err := h.engine.RetryRun(r.Context(), accessapi.PrincipalFrom(r.Context()), id); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
