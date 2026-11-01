package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// Migration moves features of a type from one version to the next with
// expressions (spec 12.5): an expand, migrate, contract sequence where
// reads of old features are upcast in memory and migrate_batch jobs
// rewrite them.
type Migration struct {
	ID   string
	File string
	Type schemaapi.TypeID // base ID
	From int
	To   int
	// Set maps top-level paths to expressions over `feature` (the old
	// document); a nil program removes the field.
	Set map[string]rt.Program
	// Flow names a privileged system flow used instead of Set.
	Flow      string
	BatchSize int
}

// MigrationDoc is a migrations/*.json file.
type MigrationDoc struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	From      int               `json:"from"`
	To        int               `json:"to"`
	Set       map[string]string `json:"set,omitempty"`
	Flow      string            `json:"flow,omitempty"`
	BatchSize int               `json:"batchSize,omitempty"`
}

// Batch sizes of migrate_batch jobs.
const (
	DefaultMigrationBatch = 500
	MaxMigrationBatch     = 5000
)

// MigrationProblem is a validation error of a migration file.
type MigrationProblem struct {
	File, Pointer, Message string
}

// MigrationSource is one migration file.
type MigrationSource struct {
	File string
	Data []byte
}

var migrationID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)

// MigrationEnv types migration expressions: the old document is untyped
// because only the current schema is known.
func MigrationEnv() rt.Env {
	return rt.Env{Vars: map[string]*rt.Type{"feature": rt.OpenMap()}, Funcs: []string{rt.FnNow, rt.FnUUID}}
}

// CompileMigrations validates migration files against the current types
// (spec 12.5). flowOK checks that a named flow exists, is privileged, and
// runs as system.
func CompileMigrations(types schemaapi.TypeRegistry, exprs rt.Compiler, srcs []MigrationSource, flowOK func(string) error) ([]*Migration, []MigrationProblem) {
	var out []*Migration
	var probs []MigrationProblem
	ids := map[string]bool{}
	steps := map[string]bool{}
	for _, src := range srcs {
		errf := func(ptr, f string, a ...any) {
			probs = append(probs, MigrationProblem{File: src.File, Pointer: ptr, Message: fmt.Sprintf(f, a...)})
		}
		var doc MigrationDoc
		dec := json.NewDecoder(bytes.NewReader(src.Data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			errf("", "invalid migration: %v", err)
			continue
		}
		if !migrationID.MatchString(doc.ID) {
			errf("/id", "id must be lower-case letters, digits, dots, and dashes")
		} else if ids[doc.ID] {
			errf("/id", "duplicate migration id %q", doc.ID)
		}
		ids[doc.ID] = true
		ct, ok := types.Get(schemaapi.TypeID(doc.Type).Base())
		if !ok {
			errf("/type", "unknown type %q", doc.Type)
			continue
		}
		m := &Migration{ID: doc.ID, File: src.File, Type: ct.Base(), From: doc.From, To: doc.To, Flow: doc.Flow, BatchSize: doc.BatchSize}
		switch {
		case doc.From < 1 || doc.To != doc.From+1:
			errf("/to", "migrations move one version at a time (to = from + 1)")
		case doc.To > ct.Version:
			errf("/to", "type %s is at version %d", ct.Name, ct.Version)
		}
		step := fmt.Sprintf("%s@%d", m.Type, m.From)
		if steps[step] {
			errf("/from", "another migration already starts at version %d of %s", m.From, ct.Name)
		}
		steps[step] = true
		if m.BatchSize == 0 {
			m.BatchSize = DefaultMigrationBatch
		}
		if m.BatchSize < 1 || m.BatchSize > MaxMigrationBatch {
			errf("/batchSize", "batchSize must be between 1 and %d", MaxMigrationBatch)
		}
		switch {
		case (len(doc.Set) == 0) == (doc.Flow == ""):
			errf("", "a migration needs exactly one of set and flow")
		case doc.Flow != "":
			if err := flowOK(doc.Flow); err != nil {
				errf("/flow", "%v", err)
			}
		default:
			m.Set = map[string]rt.Program{}
			paths := make([]string, 0, len(doc.Set))
			for p := range doc.Set {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				src := doc.Set[p]
				ptr := "/set/" + strings.TrimPrefix(p, "/")
				if !strings.HasPrefix(p, "/") || strings.Count(p, "/") != 1 {
					errf(ptr, "paths are top-level JSON Pointers (/field)")
					continue
				}
				if strings.TrimSpace(src) == "nil" {
					m.Set[p] = nil // remove the old field
					continue
				}
				if f, ok := ct.Field(p); !ok || !f.Top {
					errf(ptr, "%s has no field %s (only nil may remove old fields)", ct.Name, p)
					continue
				}
				prog, err := exprs.Compile(src, MigrationEnv(), rt.Any, false)
				if err != nil {
					errf(ptr, "%v", err)
					continue
				}
				m.Set[p] = prog
			}
		}
		out = append(out, m)
	}
	return out, probs
}
