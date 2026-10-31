// Package http serves streaming (spec 9.10): SSE at /v1/stream, WebSocket
// at /v1/ws, and ephemeral writes at /v1/ephemeral/{type}/{id}.
package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/change/application"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	"app-maker/platform/httpserver"
)

// Handler serves streaming endpoints.
type Handler struct {
	g *application.Gateway
}

// New returns a handler.
func New(g *application.Gateway) *Handler { return &Handler{g: g} }

// Register adds the routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/stream", h.sse)
	mux.HandleFunc("GET /v1/ws", h.ws)
	mux.HandleFunc("POST /v1/ephemeral/{type}/{id}", h.ephemeral)
}

func requireAuth(w http.ResponseWriter, r *http.Request) (accessapi.Principal, bool) {
	p := accessapi.PrincipalFrom(r.Context())
	if p.IsAnonymous() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return p, false
	}
	return p, true
}

// targetFromQuery reads ?sub=<base64url JSON> or plain parameters.
func targetFromQuery(r *http.Request) (application.Target, error) {
	q := r.URL.Query()
	var t application.Target
	if s := q.Get("sub"); s != "" {
		raw, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			return t, kernel.NewError(kernel.CodeBadRequest, "sub must be base64url JSON")
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return t, kernel.NewError(kernel.CodeBadRequest, "invalid subscription: %v", err)
		}
	} else {
		t = application.Target{Target: q.Get("target"), ID: q.Get("id"), Parent: q.Get("parent"), Type: q.Get("type")}
		if p := q.Get("paths"); p != "" {
			t.Paths = strings.Split(p, ",")
		}
		if qs := q.Get("query"); qs != "" {
			t.Query = &queryapi.Document{}
			if err := json.Unmarshal([]byte(qs), t.Query); err != nil {
				return t, kernel.NewError(kernel.CodeBadRequest, "invalid query: %v", err)
			}
		}
	}
	if t.SID == "" {
		t.SID = "s1"
	}
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		t.From = id
	}
	return t, nil
}

// sse serves one subscription per connection; the event ID is the resume
// cursor, so browsers resume with Last-Event-ID (spec 9.10).
func (h *Handler) sse(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAuth(w, r)
	if !ok {
		return
	}
	t, err := targetFromQuery(r)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpserver.WriteError(w, r, fmt.Errorf("streaming unsupported"))
		return
	}
	conn := h.g.Connect(r.Context(), p)
	defer conn.Close()
	if err := conn.Subscribe(r.Context(), t); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(application.HeartbeatSeconds * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-conn.Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case m := <-conn.Out():
			b, _ := json.Marshal(m)
			if c, ok := m["cursor"].(string); ok && c != "" {
				_, _ = fmt.Fprintf(w, "id: %s\n", c)
			}
			_, _ = fmt.Fprintf(w, "event: %v\ndata: %s\n\n", m["t"], b)
			flusher.Flush()
			if m["t"] == "error" || m["t"] == "reset" {
				return
			}
		}
	}
}

type wsIn struct {
	application.Target
	T   string         `json:"t"`
	Set map[string]any `json:"set,omitempty"`
}

// ws multiplexes subscriptions over one WebSocket (spec 9.10).
func (h *Handler) ws(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAuth(w, r)
	if !ok {
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn := h.g.Connect(ctx, p)
	defer conn.Close()
	go func() {
		defer cancel()
		for {
			var in wsIn
			if err := wsjson.Read(ctx, c, &in); err != nil {
				return
			}
			switch in.T {
			case "subscribe":
				if err := conn.Subscribe(ctx, in.Target); err != nil {
					_ = wsjson.Write(ctx, c, application.Message{"t": "error", "sid": in.SID, "code": kernel.CodeOf(err), "detail": err.Error()})
				}
			case "unsubscribe":
				conn.Unsubscribe(in.SID)
			case "publish":
				id, perr := kernel.ParseID(in.ID)
				if perr != nil {
					continue
				}
				if err := h.g.Ephemeral(ctx, p, in.Type, id, in.Set); err != nil {
					_ = wsjson.Write(ctx, c, application.Message{"t": "error", "sid": in.SID, "code": kernel.CodeOf(err)})
				}
			}
		}
	}()
	ping := time.NewTicker(application.HeartbeatSeconds * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.Close(websocket.StatusNormalClosure, "")
			return
		case <-conn.Done():
			_ = c.Close(websocket.StatusPolicyViolation, "closed")
			return
		case <-ping.C:
			if err := c.Ping(ctx); err != nil {
				return
			}
		case m := <-conn.Out():
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := wsjson.Write(wctx, c, m)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

func (h *Handler) ephemeral(w http.ResponseWriter, r *http.Request) {
	p, ok := requireAuth(w, r)
	if !ok {
		return
	}
	id, err := kernel.ParseID(r.PathValue("id"))
	if err != nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	var values map[string]any
	if err := httpserver.DecodeJSON(r, &values); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if err := h.g.Ephemeral(r.Context(), p, r.PathValue("type"), id, values); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
