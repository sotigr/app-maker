package application

import (
	"context"

	accessapi "organon/internal/access/api"
	contentapi "organon/internal/content/api"
	"organon/internal/content/domain"
	"organon/internal/kernel"
	schemaapi "organon/internal/schema/api"
)

// FlowPorts adapts the service to contentapi.FlowAccess.
type FlowPorts struct{ s *Service }

// FlowAccess returns the port used by the Flow context.
func (s *Service) FlowAccess() *FlowPorts { return &FlowPorts{s: s} }

var _ contentapi.FlowAccess = (*FlowPorts)(nil)

// flowFeature converts a projected view into expression form.
func (x *FlowPorts) flowFeature(view contentapi.FeatureView) contentapi.FlowFeature {
	out := contentapi.FlowFeature{"id": view.ID, "type": view.Type, "version": int64(view.Version), "parentId": ""}
	if view.Parent != nil {
		out["parentId"] = *view.Parent
	}
	ct, ok := x.s.d.Types.ByName(view.Type)
	for k, wire := range view.Data {
		if !ok {
			out[k] = wire
			continue
		}
		f, found := ct.Field("/" + k)
		if !found {
			continue
		}
		v, err := f.Type.Coerce("/"+k, wire)
		if err != nil {
			out[k] = wire
			continue
		}
		out[k] = kernel.Native(v)
	}
	return out
}

// Read implements contentapi.FlowAccess.
func (x *FlowPorts) Read(ctx context.Context, p accessapi.Principal, id kernel.ID) (contentapi.FlowFeature, error) {
	v, err := x.s.GetFeature(ctx, p, "", id)
	if err != nil {
		return nil, err
	}
	return x.flowFeature(v), nil
}

// Query implements contentapi.FlowAccess.
func (x *FlowPorts) Query(ctx context.Context, p accessapi.Principal, doc []byte) ([]contentapi.FlowFeature, error) {
	res, err := x.s.RunQuery(ctx, p, doc)
	if err != nil {
		return nil, err
	}
	out := make([]contentapi.FlowFeature, len(res.Items))
	for i, v := range res.Items {
		out[i] = x.flowFeature(v)
	}
	return out, nil
}

// Children implements contentapi.FlowAccess.
func (x *FlowPorts) Children(ctx context.Context, p accessapi.Principal, parent kernel.ID, typeName string, limit int) ([]contentapi.FlowFeature, error) {
	page, err := x.s.ListChildren(ctx, p, parent, typeName, "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]contentapi.FlowFeature, len(page.Items))
	for i, v := range page.Items {
		out[i] = x.flowFeature(v)
	}
	return out, nil
}

// Storage implements contentapi.FlowAccess.
func (x *FlowPorts) Storage(ctx context.Context, id kernel.ID) (contentapi.FlowFeature, error) {
	f, ct, err := x.s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	return x.storageOf(f.Snapshot(), ct), nil
}

func (x *FlowPorts) storageOf(snap domain.Snapshot, ct *schemaapi.CompiledType) contentapi.FlowFeature {
	out := contentapi.FlowFeature(domain.ExprDocument(snap))
	out["type"] = ct.Name
	return out
}

// Commit implements contentapi.FlowAccess.
func (x *FlowPorts) Commit(ctx context.Context, req contentapi.CommitRequest) (contentapi.CommitResult, error) {
	if len(req.Ops) == 0 && len(req.Jobs) > 0 {
		// Effects without writes are still committed atomically as jobs.
		if err := x.s.enqueueJobs(ctx, req.Jobs); err != nil {
			return contentapi.CommitResult{}, err
		}
		return contentapi.CommitResult{Refs: map[string]string{}, Versions: map[string]kernel.Version{}}, nil
	}
	if len(req.Ops) == 0 {
		return contentapi.CommitResult{Refs: map[string]string{}, Versions: map[string]kernel.Version{}}, nil
	}
	ops := make([]Op, len(req.Ops))
	for i, o := range req.Ops {
		ops[i] = Op{Op: OpKind(o.Op), Type: o.Type, Ref: o.Ref, ID: o.ID, Parent: o.Parent, Version: o.Version, Data: o.Data}
	}
	// Flow writes run beforeWrite flows too (ADR 0023); the staged writes
	// of those flows do not, so they cannot trigger themselves.
	wc := WriteContext{Principal: req.Principal, Action: req.Action, Causation: req.Causation, ExtraJobs: req.Jobs}
	if id, err := kernel.ParseID(req.ActionFeature); err == nil && req.Action != "" {
		wc.ActionFeature = id
	}
	res, err := x.s.Execute(ctx, wc, ops)
	if err != nil {
		return contentapi.CommitResult{}, err
	}
	return contentapi.CommitResult{Refs: res.Refs, Versions: res.Versions, Features: res.Features}, nil
}

// AuthorizeAction implements contentapi.FlowAccess.
func (x *FlowPorts) AuthorizeAction(ctx context.Context, p accessapi.Principal, id kernel.ID, action string, input map[string]any) (contentapi.FlowFeature, error) {
	f, ct, err := x.s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	ref := x.s.Ref(f, ct)
	if _, err := x.s.canRead(ctx, p, ref); err != nil {
		return nil, err
	}
	d, err := x.s.d.Authorizer.Check(ctx, p, accessapi.CheckRequest{Op: accessapi.ActionOp(action), Type: ct, Feature: ref, Input: input})
	if err != nil {
		return nil, err
	}
	if !d.Allowed {
		return nil, kernel.NewError(kernel.CodeForbidden, "action %s is not allowed", action)
	}
	return x.storageOf(f.Snapshot(), ct), nil
}

// StreamPorts adapts the service to contentapi.StreamReader.
type StreamPorts struct{ s *Service }

// StreamReader returns the snapshot port used by the gateway.
func (s *Service) StreamReader() *StreamPorts { return &StreamPorts{s: s} }

var _ contentapi.StreamReader = (*StreamPorts)(nil)

// Get implements contentapi.StreamReader.
func (x *StreamPorts) Get(ctx context.Context, p accessapi.Principal, id kernel.ID) (contentapi.FeatureView, error) {
	return x.s.GetFeature(ctx, p, "", id)
}

// Children implements contentapi.StreamReader (every page up to limit).
func (x *StreamPorts) Children(ctx context.Context, p accessapi.Principal, parent kernel.ID, typeName string, limit int) ([]contentapi.FeatureView, error) {
	var out []contentapi.FeatureView
	cursor := ""
	for len(out) < limit {
		page, err := x.s.ListChildren(ctx, p, parent, typeName, cursor, 500)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	return out, nil
}

// Query implements contentapi.StreamReader.
func (x *StreamPorts) Query(ctx context.Context, p accessapi.Principal, doc []byte) ([]contentapi.FeatureView, error) {
	res, err := x.s.RunQuery(ctx, p, doc)
	if err != nil {
		return nil, err
	}
	return res.Items, nil
}
