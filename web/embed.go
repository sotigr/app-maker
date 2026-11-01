// Package web embeds the end-user UI renderer (ADR 0016): a small app that
// mounts @organon/ui and interprets the manifest's ui/ pages. Build it
// with `make web`; dist/ is committed so organond stays one binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the built renderer: index.html and assets/.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // embedded at build time
	}
	return sub
}
