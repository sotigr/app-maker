package app

import (
	"strings"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
	manifestfs "organon/internal/manifest/infrastructure/fs"
)

func TestTrackerManifestCompiles(t *testing.T) {
	c, err := Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Flows.All()) != 7 {
		t.Fatalf("flows: %d", len(c.Flows.All()))
	}
	for _, w := range c.Warnings {
		t.Log(w.String())
	}
}

// Spec 12.5: migration files are checked by organon validate.
func TestMigrationValidation(t *testing.T) {
	for name, tc := range map[string]struct{ file, want string }{
		"unknown type":      {`{"id":"m1","type":"urn:app:feature:nope","from":1,"to":2,"set":{"/title":"'x'"}}`, "unknown type"},
		"skips a version":   {`{"id":"m1","type":"urn:app:feature:task","from":1,"to":3,"set":{"/title":"'x'"}}`, "one version at a time"},
		"beyond current":    {`{"id":"m1","type":"urn:app:feature:task","from":2,"to":3,"set":{"/title":"'x'"}}`, "at version 2"},
		"unknown field":     {`{"id":"m1","type":"urn:app:feature:task","from":1,"to":2,"set":{"/nope":"1"}}`, "has no field /nope"},
		"bad expression":    {`{"id":"m1","type":"urn:app:feature:task","from":1,"to":2,"set":{"/title":"feature.("}}`, "/set/title"},
		"set and flow":      {`{"id":"m1","type":"urn:app:feature:task","from":1,"to":2,"set":{"/title":"'x'"},"flow":"completeTask"}`, "exactly one"},
		"unprivileged flow": {`{"id":"m1","type":"urn:app:feature:task","from":1,"to":2,"flow":"completeTask"}`, "privileged"},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := manifestfs.Load("../../examples/tracker", "dev")
			if err != nil {
				t.Fatal(err)
			}
			m.Migrations = append(m.Migrations, manifestdomain.File{Path: "migrations/m1.json", Data: []byte(tc.file)})
			_, err = Compile(m)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
