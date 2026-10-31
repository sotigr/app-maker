package application

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"sync"

	contentapi "app-maker/internal/content/api"
	"app-maker/internal/kernel"
)

// writeSet collects a run's staged writes and effects (spec 10.1: staged
// writes, one commit).
type writeSet struct {
	mu    sync.Mutex
	ops   []contentapi.WriteOp
	jobs  []kernel.JobRequest
	next  int
	byRef map[string]int // create ref -> op index
	byID  map[string]int // existing id -> update op index
}

func newWriteSet() *writeSet { return &writeSet{byRef: map[string]int{}, byID: map[string]int{}} }

// stage adds an operation. A feature is changed once per commit, so a
// second update of the same feature merges into the first, and updates of
// a feature created in this run merge into its create.
func (w *writeSet) stage(op contentapi.WriteOp) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch op.Op {
	case "create":
		w.next++
		ref := fmt.Sprintf("$n%d", w.next)
		op.Ref = ref
		if strings.HasPrefix(op.Parent, "$") {
			if _, ok := w.byRef[op.Parent]; !ok {
				return "", kernel.NewError(kernel.CodeValidationFailed, "unknown staged parent %s", op.Parent)
			}
		}
		w.byRef[ref] = len(w.ops)
		w.ops = append(w.ops, op)
		return ref, nil
	case "update":
		if i, ok := w.byRef[op.ID]; ok {
			w.ops[i].Data = mergeData(w.ops[i].Data, op.Data)
			return op.ID, nil
		}
		if i, ok := w.byID[op.ID]; ok && w.ops[i].Op == "update" {
			w.ops[i].Data = mergeData(w.ops[i].Data, op.Data)
			return op.ID, nil
		}
	}
	if _, ok := w.byID[op.ID]; ok {
		return "", kernel.NewError(kernel.CodeValidationFailed, "feature %s is changed twice in one run", op.ID)
	}
	if strings.HasPrefix(op.ID, "$") {
		return "", kernel.NewError(kernel.CodeValidationFailed, "a feature created in this run cannot be %sd in it", op.Op)
	}
	w.byID[op.ID] = len(w.ops)
	w.ops = append(w.ops, op)
	return op.ID, nil
}

func (w *writeSet) effect(j kernel.JobRequest) {
	w.mu.Lock()
	w.jobs = append(w.jobs, j)
	w.mu.Unlock()
}

// created returns a feature staged for creation, as reads see it.
func (w *writeSet) created(ref string) (contentapi.FlowFeature, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i, ok := w.byRef[ref]
	if !ok {
		return nil, false
	}
	op := w.ops[i]
	f := contentapi.FlowFeature{"id": ref, "type": op.Type, "version": int64(0), "parentId": op.Parent}
	maps.Copy(f, op.Data)
	return f, true
}

// overlay applies a staged update to a read.
func (w *writeSet) overlay(id string, f contentapi.FlowFeature) contentapi.FlowFeature {
	w.mu.Lock()
	defer w.mu.Unlock()
	i, ok := w.byID[id]
	if !ok {
		return f
	}
	op := w.ops[i]
	if op.Op == "delete" {
		return f
	}
	out := maps.Clone(f)
	for k, v := range op.Data {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

func mergeData(base, patch map[string]any) map[string]any {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for k, v := range patch {
		if pm, ok := v.(map[string]any); ok {
			if bm, ok := out[k].(map[string]any); ok {
				out[k] = mergeData(bm, pm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(toWire(v)) }
