package domain_test

import (
	"strings"
	"testing"

	"app-maker/internal/schema/api"
	"app-maker/internal/schema/domain"
)

func compileOne(t *testing.T, version, props string, required ...string) *api.CompiledType {
	t.Helper()
	req := ""
	if len(required) > 0 {
		req = `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	doc := `{"$id":"urn:app:feature:x/v` + version + `","type":"object","properties":{` + props + `}` + req + `}`
	reg, probs := newCompiler().Compile(domain.Input{Types: []domain.Source{{File: "x.json", Data: []byte(doc)}}})
	if len(probs) > 0 {
		t.Fatalf("compile: %v", probs)
	}
	ct, _ := reg.ByName("x")
	return ct
}

// Milestone 1: the compatibility checker classifies every row of spec 4.8.
func TestCompatibilityTable(t *testing.T) {
	base := `"a":{"type":"string","maxLength":10},"n":{"type":"string","x-kind":"int64"},"e":{"enum":["x","y"]},"pub":{"type":"string"},"priv":{"type":"string","x-visibility":"private"},"plain":{"type":"string"}`
	rows := []struct {
		name    string
		props   string
		path    string
		kind    domain.ChangeKind
		storage domain.StorageImpact
		api     domain.APIImpact
		req     string
	}{
		{"add optional field", base + `,"b":{"type":"string"}`, "/b", domain.ChangeAddOptional, domain.StorageSafe, domain.APISafe, ""},
		{"add required field without default", base + `,"b":{"type":"string"}`, "/b", domain.ChangeAddRequired, domain.StorageNeedsMigration, domain.APIBreakingCreate, "b"},
		{"add required field with default", base + `,"b":{"type":"string","default":"z"}`, "/b", domain.ChangeAddRequiredDefault, domain.StorageBackfill, domain.APISafe, "b"},
		{"remove field", strings.Replace(base, `,"plain":{"type":"string"}`, "", 1), "/plain", domain.ChangeRemove, domain.StorageNeedsMigration, domain.APIBreaking, ""},
		{"rename field (remove side)", strings.Replace(base, `"plain":`, `"plain2":`, 1), "/plain", domain.ChangeRemove, domain.StorageNeedsMigration, domain.APIBreaking, ""},
		{"rename field (add side)", strings.Replace(base, `"plain":`, `"plain2":`, 1), "/plain2", domain.ChangeAddOptional, domain.StorageSafe, domain.APISafe, ""},
		{"widen int64 to decimal", strings.Replace(base, `"x-kind":"int64"`, `"x-kind":"decimal"`, 1), "/n", domain.ChangeWiden, domain.StorageSafe, domain.APIBreakingStrict, ""},
		{"widen: add enum value", strings.Replace(base, `["x","y"]`, `["x","y","z"]`, 1), "/e", domain.ChangeWiden, domain.StorageSafe, domain.APIBreakingStrict, ""},
		{"narrow: remove enum value", strings.Replace(base, `["x","y"]`, `["x"]`, 1), "/e", domain.ChangeNarrow, domain.StorageNeedsMigration, domain.APIBreaking, ""},
		{"tighten constraint", strings.Replace(base, `"maxLength":10`, `"maxLength":5`, 1), "/a", domain.ChangeNarrow, domain.StorageNeedsMigration, domain.APIBreaking, ""},
		{"visibility public to owner", strings.Replace(base, `"pub":{"type":"string"}`, `"pub":{"type":"string","x-visibility":"owner"}`, 1), "/pub", domain.ChangeVisibilityNarrowed, domain.StorageSafe, domain.APIBreakingAudience, ""},
		{"visibility private to public", strings.Replace(base, `,"x-visibility":"private"`, ``, 1), "/priv", domain.ChangeVisibilityWidened, domain.StorageSafe, domain.APIExposureForReview, ""},
		{"add x-indexed", strings.Replace(base, `"plain":{"type":"string"}`, `"plain":{"type":"string","x-indexed":true}`, 1), "/plain", domain.ChangeAddIndex, domain.StorageIndexBuild, domain.APISafe, ""},
	}
	prev := compileOne(t, "1", base)
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			var req []string
			if r.req != "" {
				req = []string{r.req}
			}
			next := compileOne(t, "2", r.props, req...)
			changes := domain.CheckCompatibility(prev, next)
			for _, c := range changes {
				if c.Path == r.path && c.Kind == r.kind {
					if c.Storage != r.storage || c.API != r.api {
						t.Fatalf("%s: storage %s api %s, want %s %s", r.path, c.Storage, c.API, r.storage, r.api)
					}
					return
				}
			}
			t.Fatalf("no %s change at %s in %+v", r.kind, r.path, changes)
		})
	}
	if changes := domain.CheckCompatibility(prev, compileOne(t, "2", base)); len(changes) != 0 {
		t.Fatalf("identical types report changes: %+v", changes)
	}
}

func TestBreaking(t *testing.T) {
	if (domain.Change{Storage: domain.StorageSafe, API: domain.APIBreakingStrict}).Breaking() {
		t.Error("widening is not breaking")
	}
	if !(domain.Change{Storage: domain.StorageNeedsMigration, API: domain.APISafe}).Breaking() {
		t.Error("needs migration is breaking")
	}
}
