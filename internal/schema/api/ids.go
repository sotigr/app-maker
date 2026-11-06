// Package api is the Schema context's published language (spec 2.3): type
// IDs, the compiled type, views, and the TypeRegistry port. Other contexts
// consume compiled types only through this package.
package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// TypeID identifies a feature type or trait, e.g. "urn:app:feature:task/v2"
// or "urn:sys:trait:ownable". The base ID has no version suffix.
type TypeID string

var typeIDPattern = regexp.MustCompile(`^urn:([a-z][a-zA-Z0-9]*)(:[a-z][a-zA-Z0-9]*)*:(feature|trait):([a-z][a-zA-Z0-9]*)(/v([1-9][0-9]*))?$`)

// ParseTypeID validates s.
func ParseTypeID(s string) (TypeID, error) {
	if !typeIDPattern.MatchString(s) {
		return "", fmt.Errorf("invalid type id %q (want urn:<ns>:feature|trait:<name>[/v<N>])", s)
	}
	return TypeID(s), nil
}

// MustTypeID parses s and panics on error; for static definitions.
func MustTypeID(s string) TypeID {
	t, err := ParseTypeID(s)
	if err != nil {
		panic(err)
	}
	return t
}

func (t TypeID) String() string { return string(t) }

// Base returns the ID without its version suffix.
func (t TypeID) Base() TypeID {
	if i := strings.LastIndex(string(t), "/v"); i >= 0 {
		return t[:i]
	}
	return t
}

// Version returns the version suffix number, or 0 when absent.
func (t TypeID) Version() int {
	if i := strings.LastIndex(string(t), "/v"); i >= 0 {
		n, err := strconv.Atoi(string(t[i+2:]))
		if err == nil {
			return n
		}
	}
	return 0
}

// WithVersion returns the base ID with version v.
func (t TypeID) WithVersion(v int) TypeID {
	return TypeID(fmt.Sprintf("%s/v%d", t.Base(), v))
}

// Name returns the short name ("task").
func (t TypeID) Name() string {
	b := string(t.Base())
	return b[strings.LastIndex(b, ":")+1:]
}

// IsTrait reports whether t names a trait.
func (t TypeID) IsTrait() bool { return strings.Contains(string(t), ":trait:") }

// IsSystem reports whether t is in the reserved urn:sys: namespace.
func (t TypeID) IsSystem() bool { return strings.HasPrefix(string(t), "urn:sys:") }

// Module returns the manifest module of an app type ("crm" for
// urn:app:crm:feature:contact), or "" (spec 12.1).
func (t TypeID) Module() string {
	parts := strings.Split(string(t.Base()), ":")
	if len(parts) == 5 && parts[1] == "app" {
		return parts[2]
	}
	return ""
}

// System type and trait IDs (spec 3.7, 4.7).
const (
	TypeRoot           TypeID = "urn:sys:feature:root"
	TypeUser           TypeID = "urn:sys:feature:user"
	TypeOrganization   TypeID = "urn:sys:feature:organization"
	TypeTeam           TypeID = "urn:sys:feature:team"
	TypeServiceAccount TypeID = "urn:sys:feature:serviceAccount"
	TypeFeatureType    TypeID = "urn:sys:feature:featureType"
	TypeFlow           TypeID = "urn:sys:feature:flow"
	TypeFlowRun        TypeID = "urn:sys:feature:flowRun"
	TypeGrant          TypeID = "urn:sys:feature:grant"
	TypeBlob           TypeID = "urn:sys:feature:blob"
	TypeMigration      TypeID = "urn:sys:feature:migration"
	TypeShareLink      TypeID = "urn:sys:feature:shareLink"

	TraitPrincipal   TypeID = "urn:sys:trait:principal"
	TraitOwnable     TypeID = "urn:sys:trait:ownable"
	TraitTimestamped TypeID = "urn:sys:trait:timestamped"
	TraitTaggable    TypeID = "urn:sys:trait:taggable"
)
