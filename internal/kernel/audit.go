package kernel

import "time"

// AuditRecord is one audit log entry (spec 14.3): a mutation, written in
// its commit transaction, or an authentication event. Field values are
// never recorded, only the changed paths.
type AuditRecord struct {
	ID        ID        `json:"id"`
	At        time.Time `json:"at"`
	Actor     ID        `json:"actor"`
	ActorKind string    `json:"actorKind,omitempty"`
	// Action is created, updated, moved, deleted, purged, restored, or an
	// auth event (login, login_failed, logout, mfa_verified, ...).
	Action  string   `json:"action"`
	Target  *ID      `json:"target,omitempty"`
	Type    string   `json:"type,omitempty"`
	Version Version  `json:"version,omitempty"`
	Changed []string `json:"changed,omitempty"`
	// Run is the flow run that caused the mutation, if any.
	Run    string `json:"run,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// AuditFilter selects audit records, newest first.
type AuditFilter struct {
	Actor  *ID
	Target *ID
	Action string
	Since  time.Time
	Before *ID // records strictly older than this ID (pagination)
	Limit  int
}
