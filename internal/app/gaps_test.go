package app

import (
	"context"
	"strings"
	"testing"

	manifestdomain "organon/internal/manifest/domain"
	manifestfs "organon/internal/manifest/infrastructure/fs"
)

// gapTest runs one manifest test against the tracker after edit; the
// motoshop gap report (2026-11-06) numbers the regressions below.
func gapTest(t *testing.T, edit func(m *manifestdomain.Manifest), test string) {
	t.Helper()
	m, err := manifestfs.Load("../../examples/tracker", "dev")
	if err != nil {
		t.Fatal(err)
	}
	m.Tests = nil
	if edit != nil {
		edit(m)
	}
	m.Tests = append(m.Tests, manifestdomain.File{Path: "tests/gap.test.json", Data: []byte(test)})
	c, err := Compile(m)
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunManifestTests(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.Passed {
			t.Fatalf("%s: %s", r.Name, strings.Join(r.Failures, "; "))
		}
	}
}

// replaceFile replaces text in the manifest file at path.
func replaceFile(t *testing.T, files []manifestdomain.File, path, old, new string) {
	t.Helper()
	for i, f := range files {
		if f.Path == path {
			if !strings.Contains(string(f.Data), old) {
				t.Fatalf("%s does not contain %q", path, old)
			}
			files[i].Data = []byte(strings.Replace(string(f.Data), old, new, 1))
			return
		}
	}
	t.Fatalf("no file %s", path)
}

// Gap 1 (ADR 0023): a type's own action rule decides over the Ownable
// trait default, so "transfer": "false" denies even the owner.
func TestGap01TransferPolicy(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		replaceFile(t, m.Policies, "policies/comment.json", `"delete":`, `"actions": { "transfer": "false" }, "delete":`)
	}, `[{"name": "transfer denied", "given": {
	    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T" } },
	                  { "ref": "c", "type": "comment", "parent": "t", "owner": "ana", "data": { "body": "x" } } ] },
	  "steps": [ { "as": "ana", "action": "transfer", "on": "c", "input": { "to": "ben" }, "expect": { "error": "forbidden" } },
	             { "as": "ana", "action": "transfer", "on": "t", "input": { "to": "ben" }, "expect": { "ok": true } } ] }]`)
}

// Gap 22 (ADR 0023): an update that changes nothing succeeds without
// update permission (read is enough), and the run's email is still sent.
func TestGap22NoOpUpdate(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/noop.json", Data: []byte(`{
		  "flow": "noop", "trigger": { "action": "noop", "on": "urn:app:feature:task" },
		  "nodes": {
		    "same": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/title": "feature.title" } } },
		    "mail": { "node": "email.send", "in": { "to": "'a@example.com'", "template": "'assigned'", "vars": { "title": "feature.title" } } } } }`)})
		replaceFile(t, m.Policies, "policies/task.json", `"triage":`, `"noop": "hasRole(feature, 'viewer')", "triage":`)
	}, `[{"name": "no-op", "given": {
	    "principals": { "ana": { "kind": "user" }, "vic": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T" } } ],
	    "grants": [ { "subject": "vic", "object": "p", "role": "viewer" } ] },
	  "steps": [ { "as": "vic", "action": "noop", "on": "t", "expect": { "ok": true, "effects": [ { "node": "email.send", "count": 1 } ] } },
	             { "as": "vic", "update": "t", "data": { "title": "T" }, "expect": { "ok": true } },
	             { "as": "vic", "update": "t", "data": { "title": "changed" }, "expect": { "error": "forbidden" } } ] }]`)
}

// Gap 32 (ADR 0023): event flows on "deleted" run, with the deleted
// feature's last state as feature and before/after in nodes.
func TestGap32DeletedEvents(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/gone.json", Data: []byte(`{
		  "flow": "gone", "trigger": { "event": ["deleted"], "on": "urn:app:feature:comment" },
		  "nodes": { "mail": { "node": "email.send", "when": "feature.body == 'bye' && before.body == 'bye'",
		    "in": { "to": "'a@example.com'", "template": "'assigned'", "vars": { "title": "feature.body" } } } } }`)})
	}, `[{"name": "deleted", "given": {
	    "principals": { "ana": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T" } },
	                  { "ref": "c", "type": "comment", "parent": "t", "owner": "ana", "data": { "body": "bye" } } ] },
	  "steps": [ { "as": "ana", "delete": "c", "expect": { "ok": true, "effects": [ { "node": "email.send", "count": 1 } ] } } ] }]`)
}

// Gap 33 (ADR 0023): a trigger condition that fails at run time records a
// failed run (code when_failed) instead of silently skipping the flow.
func TestGap33WhenErrorRecorded(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/guard.json", Data: []byte(`{
		  "flow": "guard", "trigger": { "event": ["created"], "on": "urn:app:feature:comment", "when": "int(after.body) > 0" },
		  "nodes": { "mail": { "node": "email.send", "in": { "to": "'a@example.com'", "template": "'assigned'", "vars": {} } } } }`)})
	}, `[{"name": "when error", "given": {
	    "principals": { "ana": { "kind": "user" }, "adm": { "kind": "user", "roles": ["admin"] } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T" } } ],
	    "grants": [ { "subject": "ana", "object": "p", "role": "editor" } ] },
	  "steps": [ { "as": "ana", "create": "comment", "parent": "t", "data": { "body": "not a number" },
	               "expect": { "ok": true, "effects": [ { "node": "email.send", "count": 0 } ] } },
	             { "as": "adm", "query": { "type": "flowRun", "where": { "field": "/status", "op": "eq", "value": "failed" } },
	               "expect": { "count": 1 } } ] }]`)
}

// Gaps 13 and 30 (ADR 0023): beforeWrite flows also see transitions and
// writes made by flows (here: the reject-spam flow on a comment created by
// completeTask, and a guard on the start transition).
func TestGap13BeforeWriteOnTransitionsAndFlowWrites(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/no-start.json", Data: []byte(`{
		  "flow": "noStart", "trigger": { "beforeWrite": ["update"], "on": "urn:app:feature:task",
		    "when": "before.status == 'todo' && after.status == 'in_progress' && after.title == 'frozen'" },
		  "nodes": { "r": { "node": "reject", "in": { "code": "'frozen'", "status": "409" } } } }`)})
	}, `[{"name": "beforeWrite everywhere", "given": {
	    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "frozen" } },
	                  { "ref": "u", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "U", "status": "in_progress", "assignee": "ben" } } ],
	    "grants": [ { "subject": "ana", "object": "p", "role": "editor" }, { "subject": "ben", "object": "p", "role": "editor" } ] },
	  "steps": [ { "as": "ana", "action": "start", "on": "t", "expect": { "error": "frozen" } },
	             { "as": "ben", "action": "complete", "on": "u", "input": { "note": "BUY NOW cheap" }, "expect": { "error": "spam" } } ] }]`)
}

// Gaps 31 and 6 (ADR 0023): a beforeWrite flow with runAs: system stages
// writes as system, and may set fields of the feature being created.
func TestGap31BeforeWriteRunAsAndOwnFields(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/stamp.json", Data: []byte(`{
		  "flow": "stamp", "trigger": { "beforeWrite": ["create"], "on": "urn:app:feature:comment" },
		  "runAs": "system", "privileged": true,
		  "nodes": {
		    "shout": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/body": "upper(feature.body)" } } },
		    "touch": { "node": "feature.update", "in": { "id": "feature.parentId", "set": { "/title": "'commented'" } } } } }`)})
	}, `[{"name": "beforeWrite as system", "given": {
	    "principals": { "ana": { "kind": "user" }, "vic": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T" } } ],
	    "grants": [ { "subject": "vic", "object": "p", "role": "viewer" } ] },
	  "steps": [ { "as": "vic", "create": "comment", "parent": "t", "data": { "body": "hello" }, "ref": "c",
	               "expect": { "ok": true, "feature": { "body": "HELLO" } } },
	             { "as": "ana", "read": "t", "expect": { "feature": { "title": "commented" } } } ] }]`)
}

// Gap 17 (ADR 0023): an edge from a skipped node only orders; the node
// after a reject that did not fire still runs.
func TestGap17EdgesDoNotPropagateSkips(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/guarded.json", Data: []byte(`{
		  "flow": "guarded", "trigger": { "action": "rename", "on": "urn:app:feature:task" },
		  "nodes": {
		    "guard": { "node": "reject", "when": "feature.title == 'locked'", "in": { "code": "'locked'" } },
		    "write": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/title": "'renamed'" } } } },
		  "edges": [["guard", "write"]] }`)})
		replaceFile(t, m.Policies, "policies/task.json", `"triage":`, `"rename": "hasRole(feature, 'editor')", "triage":`)
	}, `[{"name": "edge after a skipped reject", "given": {
	    "principals": { "ana": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T" } } ],
	    "grants": [ { "subject": "ana", "object": "p", "role": "editor" } ] },
	  "steps": [ { "as": "ana", "action": "rename", "on": "t", "expect": { "ok": true, "feature": { "title": "renamed" } } } ] }]`)
}
