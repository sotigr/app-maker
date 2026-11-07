package kernel

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// FoldText returns s for case- and accent-insensitive matching (the
// icontains and iprefix query operators): lower case, accents and other
// combining marks removed, final sigma as sigma. "Παπαδόπουλος" and
// "ΠΑΠΑΔΟΠΟΥΛΟΣ" fold alike, as do "Émile" and "emile".
func FoldText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		r = unicode.ToLower(r)
		if r == 'ς' {
			r = 'σ'
		}
		b.WriteRune(r)
	}
	return b.String()
}
