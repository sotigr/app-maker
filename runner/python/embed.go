// Package python embeds the organon runtime so the runner can vendor it
// into every worker (spec 11.2).
package python

import "embed"

// FS holds organon/*.py.
//
//go:embed organon/*.py
var FS embed.FS
