// Package http serves the /v1/auth endpoints and the authentication
// middleware (spec 8.3).
package http

import (
	"net"
	"net/http"
	"strings"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/access/application"
	"app-maker/internal/kernel"
	"app-maker/platform/httpserver"
)

// Cookie and header names.
const (
	SessionCookie = "fw_session"
	CSRFCookie    = "fw_csrf"
	CSRFHeader    = "X-CSRF-Token"
)

// Handler serves authentication endpoints.
type Handler struct {
	auth   *application.AuthService
	secure bool
}

// New returns a handler; secure sets the Secure flag on cookies.
func New(auth *application.AuthService, secure bool) *Handler {
	return &Handler{auth: auth, secure: secure}
}

// Middleware resolves the caller into a Principal (spec 8.3): a bearer
// API key, or a session cookie with a double-submit CSRF check on
// state-changing requests. No credentials means anonymous; invalid
// credentials are 401.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := application.WithRequestCache(r.Context())
		p := accessapi.Anonymous()
		authz := r.Header.Get("Authorization")
		if authz == "" {
			// WebSocket clients outside browsers cannot set headers; they
			// pass the bearer token as a subprotocol "fw.bearer.<token>".
			for _, proto := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
				if tok, ok := strings.CutPrefix(strings.TrimSpace(proto), "fw.bearer."); ok {
					authz = "Bearer " + tok
				}
			}
		}
		if authz != "" {
			token, ok := strings.CutPrefix(authz, "Bearer ")
			if !ok {
				httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
				return
			}
			var err error
			if strings.HasPrefix(token, "fws_") {
				p, _, err = h.auth.AuthenticateSession(ctx, token)
			} else {
				p, err = h.auth.AuthenticateAPIKey(ctx, token)
			}
			if err != nil {
				httpserver.WriteError(w, r, err)
				return
			}
		} else if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
			sp, csrf, err := h.auth.AuthenticateSession(ctx, c.Value)
			if err != nil {
				h.clearCookies(w)
				httpserver.WriteError(w, r, err)
				return
			}
			if !safeMethod(r.Method) && r.Header.Get(CSRFHeader) != csrf {
				httpserver.WriteError(w, r, kernel.NewError(kernel.CodeForbidden, "missing or invalid CSRF token"))
				return
			}
			p = sp
		}
		next.ServeHTTP(w, r.WithContext(accessapi.WithPrincipal(ctx, p)))
	})
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// Register adds the routes under /v1/auth.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/auth/password", h.login)
	mux.HandleFunc("POST /v1/auth/logout", h.logout)
	mux.HandleFunc("GET /v1/auth/session", h.session)
	mux.HandleFunc("POST /v1/auth/users", h.createUser)
	mux.HandleFunc("POST /v1/auth/tokens", h.createToken)
}

type principalDTO struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Roles  []string `json:"roles"`
	Groups []string `json:"groups"`
}

func toDTO(p accessapi.Principal) principalDTO {
	d := principalDTO{Kind: string(p.Kind), Roles: append([]string{}, p.Roles...), Groups: []string{}}
	if !p.ID.IsZero() {
		d.ID = p.ID.String()
	}
	for _, g := range p.Groups {
		d.Groups = append(d.Groups, g.String())
	}
	return d
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Login    string `json:"login"`
		Password string `json:"password"`
		// CLI asks for the session token in the body instead of cookies;
		// browsers never receive it outside the HttpOnly cookie.
		CLI bool `json:"cli"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	res, err := h.auth.Login(r.Context(), body.Login, body.Password, ip)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if body.CLI {
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"principal": toDTO(res.Principal), "token": res.Token})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookie, Value: res.Token, Path: "/", HttpOnly: true, Secure: h.secure,
		SameSite: http.SameSiteLaxMode, Expires: res.ExpiresAt})
	http.SetCookie(w, &http.Cookie{Name: CSRFCookie, Value: res.CSRF, Path: "/", Secure: h.secure,
		SameSite: http.SameSiteLaxMode, Expires: res.ExpiresAt})
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"principal": toDTO(res.Principal), "csrf": res.CSRF})
}

func (h *Handler) clearCookies(w http.ResponseWriter) {
	for _, n := range []string{SessionCookie, CSRFCookie} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1, HttpOnly: n == SessionCookie, Secure: h.secure})
	}
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = h.auth.Logout(r.Context(), c.Value)
	}
	if t, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && strings.HasPrefix(t, "fws_") {
		_ = h.auth.Logout(r.Context(), t)
	}
	h.clearCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) session(w http.ResponseWriter, r *http.Request) {
	p := accessapi.PrincipalFrom(r.Context())
	if p.IsAnonymous() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"principal": toDTO(p)})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email       string   `json:"email"`
		DisplayName string   `json:"displayName"`
		Password    string   `json:"password"`
		Roles       []string `json:"roles"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	id, err := h.auth.CreateUser(r.Context(), accessapi.PrincipalFrom(r.Context()), application.NewUser{
		Email: body.Email, DisplayName: body.DisplayName, Password: body.Password, Roles: body.Roles})
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{"id": id.String()})
}

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Principal string `json:"principal"`
		Name      string `json:"name"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	actor := accessapi.PrincipalFrom(r.Context())
	if actor.IsAnonymous() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return
	}
	target := actor.ID
	if body.Principal != "" {
		id, err := kernel.ParseID(body.Principal)
		if err != nil {
			httpserver.WriteError(w, r, kernel.NewError(kernel.CodeBadRequest, "invalid principal id"))
			return
		}
		target = id
	}
	key, err := h.auth.CreateAPIKey(r.Context(), actor, target, body.Name)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{"key": key, "principal": target.String()})
}
