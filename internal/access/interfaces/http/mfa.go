package http

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	"organon/platform/httpserver"
)

// Cookies of the OIDC and MFA steps.
const (
	OIDCStateCookie = "organon_oidc"
	MFATicketCookie = "organon_mfa"
)

// ticket reads an MFA ticket from the body or, after an OIDC redirect,
// from the short-lived cookie.
func ticket(r *http.Request, body string) string {
	if body != "" {
		return body
	}
	if c, err := r.Cookie(MFATicketCookie); err == nil {
		return c.Value
	}
	return ""
}

func (h *Handler) clearMFACookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: MFATicketCookie, Value: "", Path: "/v1/auth/mfa", MaxAge: -1, HttpOnly: true, Secure: h.secure})
}

func (h *Handler) mfaVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
		CLI    bool   `json:"cli"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	res, err := h.auth.VerifyMFA(r.Context(), ticket(r, body.Ticket), body.Code)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	h.clearMFACookie(w)
	h.writeLogin(w, res, body.CLI)
}

// totpEnroll starts enrollment for the signed-in principal, or during a
// login that requires MFA (with the enroll ticket).
func (h *Handler) totpEnroll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ticket string `json:"ticket"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if t := ticket(r, body.Ticket); t != "" {
		e, err := h.auth.BeginTOTPWithTicket(r.Context(), t)
		if err != nil {
			httpserver.WriteError(w, r, err)
			return
		}
		h.clearMFACookie(w)
		httpserver.WriteJSON(w, http.StatusOK, e)
		return
	}
	p := accessapi.PrincipalFrom(r.Context())
	if p.ID.IsZero() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return
	}
	e, err := h.auth.BeginTOTP(r.Context(), p.ID)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, e)
}

func (h *Handler) totpConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ticket string `json:"ticket"`
		Code   string `json:"code"`
		CLI    bool   `json:"cli"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	if body.Ticket != "" {
		res, err := h.auth.ConfirmTOTPWithTicket(r.Context(), body.Ticket, body.Code)
		if err != nil {
			httpserver.WriteError(w, r, err)
			return
		}
		h.writeLogin(w, res, body.CLI)
		return
	}
	p := accessapi.PrincipalFrom(r.Context())
	if p.ID.IsZero() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return
	}
	if err := h.auth.ConfirmTOTP(r.Context(), p.ID, body.Code); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) totpDisable(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"code"`
	}
	if err := httpserver.DecodeJSON(r, &body); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	p := accessapi.PrincipalFrom(r.Context())
	if p.ID.IsZero() {
		httpserver.WriteError(w, r, kernel.ErrUnauthenticated)
		return
	}
	if err := h.auth.DisableTOTP(r.Context(), p, body.Code); err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) oidcProviders(w http.ResponseWriter, _ *http.Request) {
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"providers": h.auth.OIDCProviders()})
}

func (h *Handler) callbackURL(r *http.Request, provider string) string {
	base := h.PublicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimSuffix(base, "/") + "/v1/auth/oidc/" + url.PathEscape(provider) + "/callback"
}

// safeRedirect allows only same-origin paths (no open redirects).
func safeRedirect(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.ContainsAny(p, "\r\n") {
		return "/"
	}
	return p
}

func (h *Handler) oidcStart(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	q := r.URL.Query()
	u, state, err := h.auth.OIDCStart(r.Context(), provider, h.callbackURL(r, provider), safeRedirect(q.Get("redirect")), q.Get("invite"))
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: OIDCStateCookie, Value: state, Path: "/v1/auth/oidc/", HttpOnly: true, Secure: h.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: 600})
	http.Redirect(w, r, u, http.StatusFound)
}

func (h *Handler) oidcCallback(w http.ResponseWriter, r *http.Request) {
	provider := r.PathValue("provider")
	c, err := r.Cookie(OIDCStateCookie)
	if err != nil {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeUnauthenticated, "the login expired; start again"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: OIDCStateCookie, Value: "", Path: "/v1/auth/oidc/", MaxAge: -1, HttpOnly: true, Secure: h.secure})
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		httpserver.WriteError(w, r, kernel.NewError(kernel.CodeUnauthenticated, "the identity provider refused the login: %s", e))
		return
	}
	res, after, err := h.auth.OIDCCallback(r.Context(), provider, c.Value, q.Get("code"), q.Get("state"), h.callbackURL(r, provider))
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	after = safeRedirect(after)
	if res.MFA != "" {
		http.SetCookie(w, &http.Cookie{Name: MFATicketCookie, Value: res.Ticket, Path: "/v1/auth/mfa", HttpOnly: true, Secure: h.secure,
			SameSite: http.SameSiteStrictMode, Expires: time.Now().Add(10 * time.Minute)})
		sep := "?"
		if strings.Contains(after, "?") {
			sep = "&"
		}
		http.Redirect(w, r, after+sep+"mfa="+res.MFA, http.StatusFound)
		return
	}
	h.setSession(w, res)
	http.Redirect(w, r, after, http.StatusFound)
}

func (h *Handler) oidcInvite(w http.ResponseWriter, r *http.Request) {
	id, err := kernel.ParseID(r.PathValue("id"))
	if err != nil {
		httpserver.WriteError(w, r, kernel.ErrNotFound)
		return
	}
	tok, exp, err := h.auth.CreateOIDCInvite(r.Context(), accessapi.PrincipalFrom(r.Context()), id)
	if err != nil {
		httpserver.WriteError(w, r, err)
		return
	}
	links := map[string]string{}
	for _, p := range h.auth.OIDCProviders() {
		links[p] = "/v1/auth/oidc/" + url.PathEscape(p) + "/start?invite=" + url.QueryEscape(tok)
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{"invite": tok, "expiresAt": exp, "links": links})
}
