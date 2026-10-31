// Package http translates the generic feature API (spec 6.2) to Content
// use cases. Handlers contain no business rules.
package http

import (
	"net/http"
	"strconv"

	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	"app-maker/internal/content/application"
	"app-maker/internal/kernel"
	"app-maker/platform/httpserver"
)

// Handler serves /v1/features, /v1/query, and /v1/transactions.
type Handler struct {
	svc *application.Service
}

// New returns a handler.
func New(svc *application.Service) *Handler { return &Handler{svc: svc} }

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/features/{type}", h.create)
	mux.HandleFunc("GET /v1/features/{type}/{id}", h.get)
	mux.HandleFunc("PATCH /v1/features/{type}/{id}", h.update)
	mux.HandleFunc("DELETE /v1/features/{type}/{id}", h.delete)
	mux.HandleFunc("GET /v1/features/{id}/children", h.children)
	mux.HandleFunc("POST /v1/features/{id}/move", h.move)
	mux.HandleFunc("POST /v1/query", h.query)
	mux.HandleFunc("POST /v1/transactions", h.transaction)
}

// fail writes err. Anonymous callers get 401 instead of 403/404, so they
// learn to authenticate without learning whether anything exists.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	if accessapi.PrincipalFrom(r.Context()).IsAnonymous() {
		switch kernel.CodeOf(err) {
		case kernel.CodeNotFound, kernel.CodeForbidden:
			err = kernel.ErrUnauthenticated
		}
	}
	httpserver.WriteError(w, r, err)
}

func wc(r *http.Request) application.WriteContext {
	return application.WriteContext{Principal: accessapi.PrincipalFrom(r.Context())}
}

func pathID(r *http.Request, name string) (kernel.ID, error) {
	id, err := kernel.ParseID(r.PathValue(name))
	if err != nil {
		return kernel.NilID, kernel.ErrNotFound
	}
	return id, nil
}

func writeFeature(w http.ResponseWriter, status int, f contentapi.FeatureView) {
	w.Header().Set("ETag", httpserver.ETag(int64(f.Version)))
	httpserver.WriteJSON(w, status, f)
}

// requireVersion reads If-Match; missing is 428 (spec 6.1).
func requireVersion(r *http.Request) (kernel.Version, error) {
	v, ok, err := httpserver.IfMatch(r)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, kernel.NewError(kernel.CodeVersionRequired, "If-Match is required")
	}
	return kernel.Version(v), nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var data map[string]any
	if err := httpserver.DecodeJSON(r, &data); err != nil {
		fail(w, r, err)
		return
	}
	op := application.Op{Op: application.OpCreate, Type: r.PathValue("type"), Data: data, Parent: r.URL.Query().Get("parent")}
	res, err := h.svc.Execute(r.Context(), wc(r), []application.Op{op})
	if err != nil {
		fail(w, r, err)
		return
	}
	f := res.Features[0]
	w.Header().Set("Location", "/v1/features/"+f.Type+"/"+f.ID)
	writeFeature(w, http.StatusCreated, f)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	typeName := r.PathValue("type")
	if typeName == "_" {
		typeName = "" // any type: clients that only know the ID (parent links)
	}
	f, err := h.svc.GetFeature(r.Context(), accessapi.PrincipalFrom(r.Context()), typeName, id)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeFeature(w, http.StatusOK, f)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := requireVersion(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var data map[string]any
	if err := httpserver.DecodeJSON(r, &data); err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.svc.Execute(r.Context(), wc(r), []application.Op{{Op: application.OpUpdate, Type: r.PathValue("type"), ID: id.String(), Version: v, Data: data}})
	if err != nil {
		fail(w, r, err)
		return
	}
	writeFeature(w, http.StatusOK, res.Features[0])
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	v, err := requireVersion(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	if _, err := h.svc.Execute(r.Context(), wc(r), []application.Op{{Op: application.OpDelete, Type: r.PathValue("type"), ID: id.String(), Version: v}}); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) children(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	q := r.URL.Query()
	limit := 0
	if l := q.Get("limit"); l != "" {
		if limit, err = strconv.Atoi(l); err != nil || limit < 1 || limit > 500 {
			fail(w, r, kernel.NewError(kernel.CodeBadRequest, "limit must be between 1 and 500"))
			return
		}
	}
	page, err := h.svc.ListChildren(r.Context(), accessapi.PrincipalFrom(r.Context()), id, q.Get("type"), q.Get("cursor"), limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, page)
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		fail(w, r, err)
		return
	}
	var body struct {
		Parent  string         `json:"parent"`
		Version kernel.Version `json:"version"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.Version == 0 {
		if v, ok, _ := httpserver.IfMatch(r); ok {
			body.Version = kernel.Version(v)
		}
	}
	f, err := h.svc.MoveFeature(r.Context(), wc(r), id, body.Parent, body.Version)
	if err != nil {
		fail(w, r, err)
		return
	}
	writeFeature(w, http.StatusOK, f)
}

func (h *Handler) query(w http.ResponseWriter, r *http.Request) {
	body, err := httpserver.ReadBody(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.svc.RunQuery(r.Context(), accessapi.PrincipalFrom(r.Context()), body)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) transaction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ops []application.Op `json:"ops"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	res, err := h.svc.Execute(r.Context(), wc(r), body.Ops)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, res)
}
