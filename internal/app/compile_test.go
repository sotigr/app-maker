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
	if len(c.Flows.All()) != 8 {
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

// ADR 0016: plans list ui/ changes and pages that become visible without
// logging in.
func TestPlanUI(t *testing.T) {
	m, err := manifestfs.Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	ui := m.UI
	m.UI = nil
	cur, err := Compile(m)
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := manifestfs.Load("../../examples/tracker", "dev")
	for i, f := range m2.UI {
		if f.Path == "ui/app.json" {
			m2.UI[i].Data = []byte(strings.Replace(string(f.Data), `"requireLogin": true`, `"requireLogin": false`, 1))
		}
	}
	next, err := Compile(m2)
	if err != nil {
		t.Fatal(err)
	}
	p := MakePlan(cur, next)
	if len(p.UI) != len(ui) || p.UI[0].Status != "added" {
		t.Fatalf("ui diff: %+v", p.UI)
	}
	// projects, project, and task have no visible rule; myTasks needs a user.
	if strings.Join(p.PublicPages, ",") != "project,projects,task" {
		t.Fatalf("public pages: %v", p.PublicPages)
	}
}

// ADR 0020: every action of x-shareable is a transition or a flow action.
func TestShareableActionsExist(t *testing.T) {
	m, err := manifestfs.Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range m.Types {
		if strings.HasSuffix(f.Path, "task.json") {
			m.Types[i].Data = []byte(strings.Replace(string(f.Data), `"actions": ["start"]`, `"actions": ["start", "fly"]`, 1))
		}
	}
	_, err = Compile(m)
	if err == nil || !strings.Contains(err.Error(), "/x-shareable/actions/1") || !strings.Contains(err.Error(), `no action "fly"`) {
		t.Fatalf("want an error for the unknown action, got %v", err)
	}
}
