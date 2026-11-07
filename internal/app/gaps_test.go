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

// Gap 52: decimals read inside a flow (node outputs) add, multiply and
// compare exactly, alone and mixed with the trigger feature's decimals.
func TestGap52DecimalsFromReads(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		m.Flows = append(m.Flows, manifestdomain.File{Path: "flows/sum.json", Data: []byte(`{
		  "flow": "sumEstimates", "trigger": { "action": "sumEstimates", "on": "urn:app:feature:project" },
		  "output": { "type": "object", "properties": { "total": { "type": "string" }, "big": { "type": "boolean" } } },
		  "nodes": {
		    "tasks": { "node": "feature.children", "in": { "id": "feature.id", "type": "'task'" } },
		    "one":   { "node": "feature.get", "in": { "id": "nodes.tasks.out[0].id" } } },
		  "result": { "total": "string(nodes.tasks.out[0].estimate + nodes.tasks.out[1].estimate * 2 - nodes.one.out.estimate)",
		              "big": "nodes.tasks.out[0].estimate + nodes.tasks.out[1].estimate > 3" } }`)})
		replaceFile(t, m.Policies, "policies/project.json", `"summarize":`, `"sumEstimates": "isOwner(feature)", "summarize":`)
	}, `[{"name": "sum", "given": {
	    "principals": { "ana": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t1", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "A", "estimate": "1.50" } },
	                  { "ref": "t2", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "B", "estimate": "2.25" } } ] },
	  "steps": [ { "as": "ana", "action": "sumEstimates", "on": "p", "expect": { "ok": true, "output": { "total": "4.50", "big": true } } } ] }]`)
}

// Gaps 38 and 53: update and delete rules read the feature's parent (it
// was always empty there, so every update was forbidden).
func TestGap38ParentInUpdateAndDeleteRules(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		replaceFile(t, m.Policies, "policies/comment.json", `"update": "isOwner(feature)"`, `"update": "isOwner(feature) && parent.status != 'done'"`)
		replaceFile(t, m.Policies, "policies/comment.json", `"delete": "isOwner(feature) ||`, `"delete": "isOwner(feature) && parent.status == 'todo' ||`)
	}, `[{"name": "parent", "given": {
	    "principals": { "ana": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "open", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "A" } },
	                  { "ref": "done", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "B", "status": "done" } },
	                  { "ref": "c1", "type": "comment", "parent": "open", "owner": "ana", "data": { "body": "x" } },
	                  { "ref": "c2", "type": "comment", "parent": "done", "owner": "ana", "data": { "body": "x" } } ] },
	  "steps": [ { "as": "ana", "update": "c1", "data": { "body": "y" }, "expect": { "ok": true } },
	             { "as": "ana", "update": "c2", "data": { "body": "y" }, "expect": { "error": "forbidden" } },
	             { "as": "ana", "delete": "c2", "expect": { "error": "forbidden" } },
	             { "as": "ana", "delete": "c1", "expect": { "ok": true } } ] }]`)
}

// Gap 29 (ADR 0024): member fields are read and filtered by signed-in
// callers who neither own nor administer the feature, never by share links.
func TestGap29MemberVisibility(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		replaceFile(t, m.Types, "types/task.json", `"properties": {`, `"properties": {
		"staffNote": { "type": "string", "x-visibility": "member", "x-indexed": true },`)
	}, `[{"name": "member", "given": {
	    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ana", "data": { "title": "T", "staffNote": "call back" } } ],
	    "grants": [ { "subject": "ben", "object": "p", "role": "viewer" } ] },
	  "steps": [ { "as": "ben", "read": "t", "expect": { "feature": { "title": "T", "staffNote": "call back" } } },
	             { "as": "ana", "read": "t", "expect": { "feature": { "staffNote": "call back" } } },
	             { "as": "link:t", "read": "t", "expect": { "feature": { "title": "T", "staffNote": null } } },
	             { "as": "ben", "query": { "type": "task", "where": { "field": "/staffNote", "op": "icontains", "value": "CALL" } }, "expect": { "count": 1 } } ] }]`)
}

// Gaps 35 and 45: x-write takes a list (any rule allows) and role:<name>
// (the role app-wide or through a grant on the feature; on create, on the
// parent). Several actions may own one field.
func TestGap35And45WriteRules(t *testing.T) {
	gapTest(t, func(m *manifestdomain.Manifest) {
		replaceFile(t, m.Types, "types/task.json", `"properties": {`, `"properties": {
		"approved": { "type": "boolean", "x-write": ["role:editor", "action:approve"] },
		"stock": { "type": "integer", "x-write": ["action:addStock", "action:takeStock"] },`)
		m.Flows = append(m.Flows,
			manifestdomain.File{Path: "flows/add-stock.json", Data: []byte(`{
			  "flow": "addStock", "trigger": { "action": "addStock", "on": "urn:app:feature:task" },
			  "nodes": { "set": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/stock": "(feature.stock ?? 0) + 5" } } } } }`)},
			manifestdomain.File{Path: "flows/take-stock.json", Data: []byte(`{
			  "flow": "takeStock", "trigger": { "action": "takeStock", "on": "urn:app:feature:task" },
			  "nodes": { "set": { "node": "feature.update", "in": { "id": "feature.id", "set": { "/stock": "feature.stock - 1" } } } } }`)})
		replaceFile(t, m.Policies, "policies/task.json", `"triage":`, `"addStock": "true", "takeStock": "true", "triage":`)
	}, `[{"name": "write rules", "given": {
	    "principals": { "ana": { "kind": "user" }, "ben": { "kind": "user" }, "eve": { "kind": "user" } },
	    "features": [ { "ref": "p", "type": "project", "owner": "ana", "data": { "title": "P" } },
	                  { "ref": "t", "type": "task", "parent": "p", "owner": "ben", "data": { "title": "T", "assignee": "ben" } } ],
	    "grants": [ { "subject": "ana", "object": "p", "role": "editor" }, { "subject": "eve", "object": "p", "role": "editor" },
	                { "subject": "ben", "object": "p", "role": "viewer" } ] },
	  "steps": [ { "as": "ben", "update": "t", "data": { "approved": true }, "expect": { "error": "validation_failed" } },
	             { "as": "ana", "update": "t", "data": { "approved": true }, "expect": { "ok": true, "feature": { "approved": true } } },
	             { "as": "ben", "update": "t", "data": { "stock": 3 }, "expect": { "error": "validation_failed" } },
	             { "as": "ben", "action": "addStock", "on": "t", "expect": { "ok": true, "feature": { "stock": 5 } } },
	             { "as": "ben", "action": "takeStock", "on": "t", "expect": { "ok": true, "feature": { "stock": 4 } } },
	             { "as": "eve", "create": "task", "parent": "p", "data": { "title": "U", "approved": true }, "expect": { "ok": true } }] }]`)
}
