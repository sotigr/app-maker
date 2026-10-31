package application

import "app-maker/internal/flow/domain"

// NodeInfo describes a node type for the visual editor's palette.
type NodeInfo struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	Description string         `json:"description"`
	Inputs      []domain.Param `json:"inputs"`
	Output      string         `json:"output"`
}

// FlowInfo describes a compiled flow.
type FlowInfo struct {
	Name       string             `json:"name"`
	Version    int                `json:"version"`
	Trigger    string             `json:"trigger"`
	On         string             `json:"on,omitempty"`
	File       string             `json:"file"`
	Definition *domain.Definition `json:"definition"`
}

// Catalog returns the flows and the node catalog (spec 10.13: built-in
// nodes are registered like any extension, so the editor lists them all).
func (e *Engine) Catalog() (flows []FlowInfo, nodes []NodeInfo) {
	flows, nodes = []FlowInfo{}, []NodeInfo{}
	if e.d.Nodes != nil {
		for _, n := range e.d.Nodes.All() {
			nodes = append(nodes, NodeInfo{Name: n.Name(), Kind: string(n.Kind()), Description: n.Description(),
				Inputs: n.Inputs(), Output: n.Output().String()})
		}
	}
	for _, f := range e.d.Flows.All() {
		flows = append(flows, FlowInfo{Name: f.Name, Version: f.Version, Trigger: string(f.Trigger), On: string(f.On),
			File: f.File, Definition: f.Def})
	}
	return flows, nodes
}
