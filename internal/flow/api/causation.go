// Package api is the Flow context's published contract: the causation
// chain and the ports other contexts use to run flows (spec 10).
package api

import "organon/internal/kernel"

// CausationLink records one flow run that led to a write (spec 10.9).
type CausationLink struct {
	FlowID    string    `json:"flow"`
	RunID     kernel.ID `json:"run"`
	FeatureID kernel.ID `json:"feature"`
}

// CausationChain lists the flow runs that led to a batch.
type CausationChain []CausationLink

// MaxCausationDepth is the longest chain a run may start with (spec 10.9).
const MaxCausationDepth = 8

// Contains reports whether flow already ran for feature in the chain.
func (c CausationChain) Contains(flow string, feature kernel.ID) bool {
	for _, l := range c {
		if l.FlowID == flow && l.FeatureID == feature {
			return true
		}
	}
	return false
}

// With returns a copy of c with link appended.
func (c CausationChain) With(link CausationLink) CausationChain {
	out := make(CausationChain, len(c), len(c)+1)
	copy(out, c)
	return append(out, link)
}
