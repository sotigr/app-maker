package domain_test

import (
	"os"
	"path/filepath"
	"testing"

	"organon/internal/schema/domain"
)

// Milestone 1: fuzzing the compiler finds no panics (spec 15.7).
// Run with: go test ./internal/schema/domain -run '^$' -fuzz FuzzCompile -fuzztime 10m
func FuzzCompile(f *testing.F) {
	files, _ := filepath.Glob("../../../examples/tracker/types/*.json")
	for _, file := range files {
		if b, err := os.ReadFile(file); err == nil {
			f.Add(b)
		}
	}
	f.Add([]byte(`{"$id":"urn:app:feature:x/v1","type":"object","properties":{"f":{"oneOf":[{"properties":{"k":{"const":"a"}}},{"properties":{"k":{"const":"b"}}}]}}}`))
	f.Add([]byte(`{"$id":"urn:app:feature:x/v1","$defs":{"a":{"$ref":"#/$defs/b"},"b":{"type":"string"}},"properties":{"f":{"$ref":"#/$defs/a"}}}`))
	c := newCompiler()
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = c.Compile(domain.Input{Types: []domain.Source{{File: "fuzz.json", Data: data}}})
	})
}
