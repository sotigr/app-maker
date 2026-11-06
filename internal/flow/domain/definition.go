// Package domain is the Flow context's domain: flow definitions, the node
// catalog contracts, graph validation and compilation (spec 10.14), and
// cron schedules. Execution lives in the application layer, which owns
// goroutines (spec 15.6).
package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Definition is a flow document (spec 10.4). Every `in`, `when`, and
// `result` value is an expr expression; string literals are quoted inside
// the expression.
type Definition struct {
	Flow        string              `json:"flow"`
	Version     int                 `json:"version,omitempty"`
	Title       string              `json:"title,omitempty"`
	Description string              `json:"description,omitempty"`
	Trigger     Trigger             `json:"trigger"`
	RunAs       string              `json:"runAs,omitempty"` // trigger (default) or system
	Privileged  bool                `json:"privileged,omitempty"`
	Input       json.RawMessage     `json:"input,omitempty"`
	Output      json.RawMessage     `json:"output,omitempty"`
	TimeoutMs   int                 `json:"timeoutMs,omitempty"`
	Trace       bool                `json:"trace,omitempty"`
	Nodes       map[string]NodeSpec `json:"nodes"`
	Edges       [][2]string         `json:"edges,omitempty"`
	Result      map[string]any      `json:"result,omitempty"`
	// Layout holds node positions for the visual editor (ADR 0002); the
	// engine ignores it.
	Layout json.RawMessage `json:"x-layout,omitempty"`
}

// Trigger starts a flow (spec 10.2). Exactly one of Action, BeforeWrite,
// Event, Schedule, Webhook is set.
type Trigger struct {
	Action      string   `json:"action,omitempty"`
	BeforeWrite []string `json:"beforeWrite,omitempty"` // create, update, delete, move
	Event       []string `json:"event,omitempty"`       // created, updated, moved, deleted, purged
	Schedule    string   `json:"schedule,omitempty"`    // cron, UTC
	Webhook     string   `json:"webhook,omitempty"`
	On          string   `json:"on,omitempty"`     // type or trait ID
	When        string   `json:"when,omitempty"`   // condition for event and beforeWrite
	Secret      string   `json:"secret,omitempty"` // webhook signing secret, ${secret:name}
	Verify      *Verify  `json:"verify,omitempty"` // webhook signature scheme (ADR 0019)
}

// TriggerKind names a trigger kind.
type TriggerKind string

// Trigger kinds.
const (
	TriggerAction      TriggerKind = "action"
	TriggerBeforeWrite TriggerKind = "beforeWrite"
	TriggerEvent       TriggerKind = "event"
	TriggerSchedule    TriggerKind = "schedule"
	TriggerWebhook     TriggerKind = "webhook"
)

// Kind returns the trigger kind, or "" when zero or several are set.
func (t Trigger) Kind() TriggerKind {
	var kinds []TriggerKind
	if t.Action != "" {
		kinds = append(kinds, TriggerAction)
	}
	if t.BeforeWrite != nil {
		kinds = append(kinds, TriggerBeforeWrite)
	}
	if t.Event != nil {
		kinds = append(kinds, TriggerEvent)
	}
	if t.Schedule != "" {
		kinds = append(kinds, TriggerSchedule)
	}
	if t.Webhook != "" {
		kinds = append(kinds, TriggerWebhook)
	}
	if len(kinds) != 1 {
		return ""
	}
	return kinds[0]
}

// NodeSpec is one node of a flow.
type NodeSpec struct {
	Node     string         `json:"node"`
	In       map[string]any `json:"in,omitempty"`
	When     string         `json:"when,omitempty"`
	OnError  string         `json:"onError,omitempty"`
	Retry    *Retry         `json:"retry,omitempty"`
	MaxItems int            `json:"maxItems,omitempty"` // forEach
	Do       *SubGraph      `json:"do,omitempty"`       // forEach body
	// Code and LLM nodes declare their own schemas (spec 11).
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	Code         string          `json:"code,omitempty"`
	Requirements string          `json:"requirements,omitempty"`
	Limits       json.RawMessage `json:"limits,omitempty"`
	// Secrets code nodes may send with ctx.http (ADR 0019).
	Secrets []string `json:"secrets,omitempty"`
}

// Retry configures retries of External nodes (spec 10.10).
type Retry struct {
	Max     int    `json:"max"`
	Backoff string `json:"backoff,omitempty"` // exponential (default) or fixed
}

// SubGraph is the body of a forEach node.
type SubGraph struct {
	Nodes  map[string]NodeSpec `json:"nodes"`
	Edges  [][2]string         `json:"edges,omitempty"`
	Result any                 `json:"result,omitempty"`
}

// ParseDefinition decodes a flow document strictly.
func ParseDefinition(data []byte) (*Definition, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var d Definition
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("invalid flow: %w", err)
	}
	if d.Flow == "" {
		return nil, fmt.Errorf("invalid flow: \"flow\" (the name) is required")
	}
	if strings.ContainsAny(d.Flow, " /\\") {
		return nil, fmt.Errorf("invalid flow name %q", d.Flow)
	}
	return &d, nil
}

// Budgets per trigger kind (spec 10.11).
var (
	DefaultBudgetMs = map[TriggerKind]int{TriggerBeforeWrite: 2000, TriggerAction: 10_000,
		TriggerEvent: 60_000, TriggerSchedule: 60_000, TriggerWebhook: 60_000}
	MaxBudgetMs = map[TriggerKind]int{TriggerBeforeWrite: 5000, TriggerAction: 30_000,
		TriggerEvent: 900_000, TriggerSchedule: 900_000, TriggerWebhook: 900_000}
)

// forEach limits (spec 10.5).
const (
	DefaultMaxItems = 100
	HardMaxItems    = 1000
	MaxSubflowDepth = 4
)
