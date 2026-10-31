// Package python embeds the fwnode runtime so the runner can vendor it
// into every worker (spec 11.2).
package python

import "embed"

// FS holds fwnode/*.py.
//
//go:embed fwnode/*.py
var FS embed.FS
