// Package http serves uploads and signed blob URLs (spec 14.4).
package http

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/integration/application"
	"app-maker/internal/kernel"
	"app-maker/platform/httpserver"
)

// Handler serves POST /v1/uploads and GET/PUT /v1/blobs/{id}.
type Handler struct{ blobs *application.Blobs }

// New returns the handler.
func New(b *application.Blobs) *Handler { return &Handler{blobs: b} }

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/uploads", h.upload)
	mux.HandleFunc("PUT /v1/blobs/{id}", h.put)
	mux.HandleFunc("GET /v1/blobs/{id}", h.get)
}

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	p := accessapi.PrincipalFrom(r.Context())
	if p.IsAnonymous() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return
	}
	var req application.UploadRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	t, err := h.blobs.CreateUpload(r.Context(), p, req)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, t)
}

func (h *Handler) put(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.blobs.Upload(r.Context(), r.PathValue("id"), q.Get("exp"), q.Get("sig"), r.Header.Get("Content-Type"), r.Body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rc, info, err := h.blobs.Download(r.Context(), r.PathValue("id"), q.Get("exp"), q.Get("sig"))
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	defer func() { _ = rc.Close() }()
	hd := w.Header()
	hd.Set("Content-Type", info.ContentType)
	hd.Set("Content-Length", strconv.FormatInt(info.Size, 10))
	hd.Set("Cache-Control", "private, max-age=300")
	hd.Set("X-Content-Type-Options", "nosniff")
	// Uploaded content is never active in the app's origin.
	hd.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'")
	if strings.HasPrefix(info.ContentType, "image/") && info.ContentType != "image/svg+xml" {
		hd.Set("Content-Disposition", "inline")
	} else {
		hd.Set("Content-Disposition", "attachment")
	}
	_, _ = io.Copy(w, rc)
}
