// Package admin embeds the admin console and visual flow editor (spec
// 13.3, ADR 0002). Build the UI with `make ui`; the built assets in dist/
// are committed so the server stays a single self-contained binary.
package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the console under /admin with single-page-app fallback.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embedded at build time
	}
	files := http.FileServerFS(sub)
	return http.StripPrefix("/admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			r.URL.Path = "/" // client-side routes fall back to index.html
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		files.ServeHTTP(w, r)
	}))
}
