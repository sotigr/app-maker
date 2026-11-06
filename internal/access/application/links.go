package application

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// ShareResult is the output of the built-in share action (ADR 0020). The
// token is shown once; only its hash is stored.
type ShareResult struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
	Actions   []string  `json:"actions"`
}

// Share creates a share link to feature id of a shareable type. The caller
// has been authorized for the share action. Input: expiresInDays (default
// 7, at most def.MaxDays), actions (a subset of def.Actions, default all),
// note.
func (s *AuthService) Share(ctx context.Context, actor accessapi.Principal, id kernel.ID, def *schemaapi.ShareDef, input map[string]any) (ShareResult, error) {
	if def == nil {
		return ShareResult{}, kernel.NewError(kernel.CodeNotFound, "this type cannot be shared")
	}
	if actor.IsLink() {
		return ShareResult{}, kernel.NewError(kernel.CodeForbidden, "share links cannot create share links")
	}
	days := min(7, def.MaxDays)
	if v, ok := input["expiresInDays"]; ok && v != nil {
		n, isNum := number(v)
		if !isNum || n != float64(int(n)) || n < 1 || int(n) > def.MaxDays {
			return ShareResult{}, kernel.NewValidationError(kernel.FieldError{Path: "/expiresInDays", Code: "maximum",
				Message: "expiresInDays must be a whole number from 1 to " + itoa(def.MaxDays)})
		}
		days = int(n)
	}
	actions := def.Actions
	if v, ok := input["actions"]; ok && v != nil {
		list, isList := v.([]any)
		if !isList {
			return ShareResult{}, kernel.NewValidationError(kernel.FieldError{Path: "/actions", Code: "type", Message: "a list of action names"})
		}
		actions = []string{}
		for _, a := range list {
			name, _ := a.(string)
			if !slices.Contains(def.Actions, name) {
				return ShareResult{}, kernel.NewValidationError(kernel.FieldError{Path: "/actions", Code: "enum",
					Message: "links of this type may offer only " + strings.Join(def.Actions, ", ")})
			}
			actions = append(actions, name)
		}
	}
	note, _ := input["note"].(string)
	expires := s.clock.Now().Add(time.Duration(days) * 24 * time.Hour).UTC().Truncate(time.Second)
	secret := randomToken("", 32)
	acts := make([]any, len(actions))
	for i, a := range actions {
		acts[i] = a
	}
	data := map[string]any{"object": id.String(), "actions": acts, "expiresAt": expires.Format(time.RFC3339),
		"tokenHash": HashToken(secret)}
	if !actor.ID.IsZero() {
		data["createdBy"] = actor.ID.String()
	}
	if note != "" {
		data["note"] = note
	}
	linkID, err := s.writer.Create(ctx, "shareLink", &id, data, nil)
	if err != nil {
		return ShareResult{}, err
	}
	s.audit(ctx, actor.ID, "share", linkID.String())
	tok := linkID.String() + "." + secret
	return ShareResult{ID: linkID.String(), Token: tok, URL: strings.TrimSuffix(s.cfg.PublicURL, "/") + "/s/" + tok,
		ExpiresAt: expires, Actions: actions}, nil
}

// AuthenticateLink resolves a share-link token ("<link id>.<secret>")
// into a link principal. Expired, revoked (deleted), and unknown links
// are unauthenticated.
func (s *AuthService) AuthenticateLink(ctx context.Context, token string) (accessapi.Principal, time.Time, error) {
	idPart, secret, ok := strings.Cut(token, ".")
	id, err := kernel.ParseID(idPart)
	if !ok || err != nil || secret == "" {
		return accessapi.Principal{}, time.Time{}, kernel.ErrUnauthenticated
	}
	f, err := s.reader.Get(ctx, id)
	if err != nil || f.Type.Base() != schemaapi.TypeShareLink {
		return accessapi.Principal{}, time.Time{}, kernel.ErrUnauthenticated
	}
	hash, _ := f.Data["tokenHash"].(string)
	if subtle.ConstantTimeCompare([]byte(hash), []byte(HashToken(secret))) != 1 {
		return accessapi.Principal{}, time.Time{}, kernel.ErrUnauthenticated
	}
	expires := asTime(f.Data["expiresAt"])
	if expires.IsZero() || !s.clock.Now().Before(expires) {
		return accessapi.Principal{}, time.Time{}, kernel.ErrUnauthenticated
	}
	p, err := linkPrincipal(id, f.Data)
	if err != nil {
		return accessapi.Principal{}, time.Time{}, kernel.ErrUnauthenticated
	}
	return p, expires, nil
}

// linkPrincipal builds the principal of a shareLink feature.
func linkPrincipal(id kernel.ID, data map[string]any) (accessapi.Principal, error) {
	object, err := kernel.ParseID(asString(data["object"]))
	if err != nil {
		return accessapi.Principal{}, kernel.ErrForbidden
	}
	scope := &accessapi.LinkScope{Object: object}
	if list, ok := data["actions"].([]any); ok {
		for _, a := range list {
			if name, ok := a.(string); ok {
				scope.Actions = append(scope.Actions, name)
			}
		}
	}
	return accessapi.Principal{ID: id, Kind: accessapi.KindLink, Link: scope}, nil
}

// LinkCSRF is the double-submit token of a share-link cookie: derived
// from the token, so it needs no storage.
func LinkCSRF(token string) string { return HashToken("csrf:" + token)[:32] }

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int64:
		return float64(n), true
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func itoa(n int) string { return strconv.Itoa(n) }

func asTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case string:
		p, _ := time.Parse(time.RFC3339, t)
		return p
	}
	return time.Time{}
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case kernel.ID:
		return x.String()
	case interface{ String() string }:
		return x.String()
	}
	return ""
}
