package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	accessapi "organon/internal/access/api"
	contentapi "organon/internal/content/api"
	flowapi "organon/internal/flow/api"
	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
)

// NodeKind controls where a node may appear and how the engine treats it
// (spec 10.5).
type NodeKind string

// Node kinds.
const (
	KindPure      NodeKind = "Pure"
	KindDataRead  NodeKind = "DataRead"
	KindDataWrite NodeKind = "DataWrite"
	KindEffect    NodeKind = "Effect"
	KindExternal  NodeKind = "External"
	KindCode      NodeKind = "Code"
	KindControl   NodeKind = "Control"
)

// AllowedIn reports whether nodes of kind k may appear in flows with
// trigger t: beforeWrite flows stay limited to Pure, DataRead, DataWrite,
// and Control nodes (spec 10.5, decision table 16.2).
func (k NodeKind) AllowedIn(t TriggerKind) bool {
	if t != TriggerBeforeWrite {
		return true
	}
	return k == KindPure || k == KindDataRead || k == KindDataWrite || k == KindControl
}

// Param is one input of a node type.
type Param struct {
	Name     string   `json:"name"`
	Type     *rt.Type `json:"-"`
	TypeName string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	// Map params hold an object whose values are expressions (set, data,
	// vars); each value is compiled with type any.
	Map bool `json:"map,omitempty"`
	// Literal params must be plain JSON, not expressions (e.g. forEach
	// subgraph settings).
	Doc string `json:"doc,omitempty"`
}

// NodeType is a node implementation (spec 10.13). Built-in nodes are
// registered through the same NodeRegistry as any extension.
type NodeType interface {
	Name() string
	Kind() NodeKind
	Description() string
	Inputs() []Param
	// Output returns the static type of the node's output.
	Output() *rt.Type
	Execute(ctx context.Context, in map[string]any, r Runtime) (any, error)
}

// Runtime is what a node may use. It deliberately exposes no repository
// (spec 10.13): reads and writes go through Content as the run's principal
// and through the run's write set.
type Runtime interface {
	Principal() accessapi.Principal
	RunID() kernel.ID
	NodeID() string
	Now() time.Time
	NewID() kernel.ID
	// Read returns a feature, reading through the write set.
	Read(ctx context.Context, id string) (contentapi.FlowFeature, error)
	Query(ctx context.Context, doc map[string]any) ([]contentapi.FlowFeature, error)
	Children(ctx context.Context, id, typeName string, limit int) ([]contentapi.FlowFeature, error)
	// Stage adds a write to the write set and returns the feature's ID or
	// temporary reference.
	Stage(op contentapi.WriteOp) (string, error)
	// Effect stages a job, delivered after commit.
	Effect(job kernel.JobRequest)
	// HTTP performs an outbound request through the egress policy.
	HTTP(ctx context.Context, req *http.Request) (*http.Response, error)
	// Subflow runs another flow inline and returns its result.
	Subflow(ctx context.Context, name string, input map[string]any) (any, error)
	// Render renders a manifest template.
	Render(name string, vars map[string]any) (string, error)
	// LLM calls a model through the instance's router (spec 11.7); req.Model
	// is an alias. onDelta may be nil.
	LLM(ctx context.Context, req rt.LLMRequest, onDelta func(rt.LLMDelta) error) (rt.LLMResponse, error)
	// Ephemeral publishes x-persist:false field values of a feature as the
	// run's principal (spec 9.7; llm.complete streamTo).
	Ephemeral(ctx context.Context, featureID string, values map[string]any) error
	// OutputSchema is the node's declared output schema, if any.
	OutputSchema() json.RawMessage
	// Causation is the run's causation chain, ending with this run.
	Causation() flowapi.CausationChain
	// RunCode executes the node's code on a runner (spec 11.1); host calls
	// run as the run's principal through the write set.
	RunCode(ctx context.Context, input map[string]any) (json.RawMessage, error)
	Log(msg string, kv ...any)
}

// RejectError aborts a beforeWrite or action flow with a declared error
// (spec 10.5 reject).
type RejectError struct {
	Code    string
	Message string
	Status  int
}

func (e *RejectError) Error() string { return e.Code + ": " + e.Message }

// FailError fails a run without a declared code (spec 10.10 flow_failed).
type FailError struct{ Message string }

func (e *FailError) Error() string { return "flow failed: " + e.Message }

// NodeRegistry holds node types by name (spec 10.13).
type NodeRegistry struct {
	mu    sync.RWMutex
	types map[string]NodeType
}

// NewNodeRegistry returns an empty registry.
func NewNodeRegistry() *NodeRegistry { return &NodeRegistry{types: map[string]NodeType{}} }

// Register adds a node type; names are unique.
func (r *NodeRegistry) Register(t NodeType) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.types[t.Name()]; dup {
		return fmt.Errorf("node type %q is already registered", t.Name())
	}
	r.types[t.Name()] = t
	return nil
}

// Lookup returns a node type.
func (r *NodeRegistry) Lookup(name string) (NodeType, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.types[name]
	return t, ok
}

// All returns every node type sorted by name (for the editor palette).
func (r *NodeRegistry) All() []NodeType {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeType, 0, len(r.types))
	for _, t := range r.types {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
