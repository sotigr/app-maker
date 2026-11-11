// Package buildinfo names the framework release a binary was built from, so
// users can tell which documented features an image supports.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Version is the framework release (semantic versioning). Bump it with each
// published image; features in the guide name the version that added them.
const Version = "1.1.1"

// Commit is the source revision, set at build time with
// -ldflags "-X organon/platform/buildinfo.Commit=<rev>"; local builds fall
// back to the revision Go records from the repository.
var Commit = ""

// String is the one-line version text: "1.1.0 (3fa2af8)".
func String() string {
	c := Commit
	if c == "" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			dirty := false
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					c = s.Value
				case "vcs.modified":
					dirty = s.Value == "true"
				}
			}
			if len(c) > 7 {
				c = c[:7]
			}
			if c != "" && dirty {
				c += "-dirty"
			}
		}
	}
	if c == "" {
		return Version
	}
	return fmt.Sprintf("%s (%s)", Version, c)
}
