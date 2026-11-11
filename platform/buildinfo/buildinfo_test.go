package buildinfo

import (
	"regexp"
	"strings"
	"testing"
)

// The version is semantic and leads the version text.
func TestString(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("Version %q is not semantic", Version)
	}
	old := Commit
	defer func() { Commit = old }()
	Commit = "abc1234"
	if got := String(); got != Version+" (abc1234)" {
		t.Fatalf("String() = %q", got)
	}
	Commit = ""
	if !strings.HasPrefix(String(), Version) {
		t.Fatalf("String() = %q", String())
	}
}
