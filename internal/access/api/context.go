package api

import "context"

type principalKey struct{}

// WithPrincipal stores the authenticated principal in the context (spec
// 8.3: the result of authentication is placed in the request context).
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the request's principal, or Anonymous.
func PrincipalFrom(ctx context.Context) Principal {
	if p, ok := ctx.Value(principalKey{}).(Principal); ok {
		return p
	}
	return Anonymous()
}
