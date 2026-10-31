// Package api is the Change context's published language: change events
// as carried by publish_events jobs and the change log (spec 9.1).
package api

import (
	"time"

	flowapi "app-maker/internal/flow/api"
	"app-maker/internal/kernel"
	schemaapi "app-maker/internal/schema/api"
)

// EventKind is the kind of a change event.
type EventKind string

// Event kinds (spec 9.1, 9.9, 14.10).
const (
	EventCreated           EventKind = "created"
	EventUpdated           EventKind = "updated"
	EventMoved             EventKind = "moved"
	EventDeleted           EventKind = "deleted"
	EventPurged            EventKind = "purged"
	EventEphemeral         EventKind = "ephemeral"
	EventAccessChanged     EventKind = "access-changed"
	EventTypeRegistered    EventKind = "type-registered"
	EventManifestActivated EventKind = "manifest-activated"
)

// Event is one change, carrying the full storage document after the
// change. Values are in wire JSON form so events serialize losslessly.
type Event struct {
	Seq       string                 `json:"seq,omitempty"` // change log position, assigned on publish
	Kind      EventKind              `json:"kind"`
	FeatureID kernel.ID              `json:"id"`
	TypeID    schemaapi.TypeID       `json:"type"`
	ParentID  *kernel.ID             `json:"parent,omitempty"`
	OwnerID   *kernel.ID             `json:"owner,omitempty"`
	Ancestors []kernel.ID            `json:"ancestors,omitempty"`
	Version   kernel.Version         `json:"version"`
	Changed   []string               `json:"changed,omitempty"`
	Before    map[string]any         `json:"before,omitempty"`
	After     map[string]any         `json:"after,omitempty"`
	Actor     kernel.ID              `json:"actor"`
	Causation flowapi.CausationChain `json:"causation,omitempty"`
	At        time.Time              `json:"at"`
	// Principals and Features name what an access-changed event affects.
	Principals []kernel.ID `json:"principals,omitempty"`
	Features   []kernel.ID `json:"features,omitempty"`
}
