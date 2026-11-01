package application

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	accessapi "organon/internal/access/api"
	changeapi "organon/internal/change/api"
	"organon/internal/content/domain"
	flowapi "organon/internal/flow/api"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// JobRecomputeAggregate recomputes x-aggregate fields of one parent
// (spec 2.5: unique per parent, so bursts coalesce).
const JobRecomputeAggregate = "recompute_aggregate"

type aggregateArgs struct {
	Parent kernel.ID `json:"parent"`
}

// AggregateHook enqueues recompute_aggregate jobs for commits that change
// children of types with x-aggregate fields.
type AggregateHook struct{ s *Service }

// AggregateHook returns the commit hook for aggregates.
func (s *Service) AggregateHook() *AggregateHook { return &AggregateHook{s: s} }

// Jobs implements CommitHook.
func (h *AggregateHook) Jobs(_ context.Context, events []changeapi.Event, _ flowapi.CausationChain) ([]kernel.JobRequest, error) {
	children := map[schemaapi.TypeID]bool{}
	for _, ct := range h.s.d.Types.All() {
		for _, a := range ct.Aggregates {
			children[a.Of] = true
		}
	}
	if len(children) == 0 {
		return nil, nil
	}
	seen := map[kernel.ID]bool{}
	var jobs []kernel.JobRequest
	add := func(parent *kernel.ID) error {
		if parent == nil || seen[*parent] {
			return nil
		}
		seen[*parent] = true
		args, err := json.Marshal(aggregateArgs{Parent: *parent})
		if err != nil {
			return err
		}
		jobs = append(jobs, kernel.JobRequest{Kind: JobRecomputeAggregate, Queue: kernel.QueueMaintenance, Args: args,
			UniqueKey: "agg:" + parent.String(), MaxAttempts: 10})
		return nil
	}
	for _, ev := range events {
		if !children[ev.TypeID.Base()] {
			continue
		}
		if err := add(ev.ParentID); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

// HandleRecomputeAggregate recomputes every aggregate of a parent from its
// current children and writes the result as the system (idempotent).
func (s *Service) HandleRecomputeAggregate(ctx context.Context, raw json.RawMessage) error {
	var a aggregateArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return err
	}
	for attempt := 0; attempt < 5; attempt++ {
		err := s.recomputeOnce(ctx, a.Parent)
		if !errors.Is(err, kernel.ErrVersionConflict) {
			if errors.Is(err, kernel.ErrNotFound) {
				return nil // parent deleted
			}
			return err
		}
	}
	return kernel.ErrVersionConflict
}

func (s *Service) recomputeOnce(ctx context.Context, parent kernel.ID) error {
	f, ct, err := s.load(ctx, parent)
	if err != nil {
		return err
	}
	if len(ct.Aggregates) == 0 {
		return nil
	}
	repo, err := s.repoFor(ct)
	if err != nil {
		return err
	}
	patch := map[string]any{}
	for _, agg := range ct.Aggregates {
		child, ok := s.d.Types.Get(agg.Of)
		if !ok {
			continue
		}
		v, err := s.aggregate(ctx, repo, parent, child, agg)
		if err != nil {
			return err
		}
		cur, _ := f.Field(agg.Path)
		fd, _ := ct.Field(agg.Path)
		nv, err := fd.Type.Coerce(agg.Path, v)
		if err != nil {
			return err
		}
		if cur == nil || !kernel.Equal(cur, nv) {
			patch[agg.Path[1:]] = fd.Type.Wire(nv)
		}
	}
	if len(patch) == 0 {
		return nil
	}
	_, err = s.Execute(ctx, WriteContext{Principal: accessapi.System(), Fixture: true, SkipBeforeWrite: true},
		[]Op{{Op: OpUpdate, Type: ct.Name, ID: parent.String(), Version: f.Version(), Data: patch}})
	return err
}

// aggregate computes one x-aggregate over the live children of parent.
func (s *Service) aggregate(ctx context.Context, repo domain.FeatureRepository, parent kernel.ID, child *schemaapi.CompiledType, agg schemaapi.AggregateDef) (any, error) {
	count := int64(0)
	var sum float64
	var dsum kernel.Decimal
	var best kernel.Value
	var after *queryapi.Cursor
	for {
		page, err := repo.Query(ctx, queryapi.Plan{Types: []schemaapi.TypeID{child.ID}, Limit: queryapi.MaxLimit, After: after,
			Scope: queryapi.Scope{Kind: queryapi.ScopeChildrenOf, Of: parent}, Sort: []queryapi.SortKey{{}}})
		if err != nil {
			return nil, err
		}
		for _, f := range page.Items {
			if agg.WhereProgram != nil {
				vars := map[string]any{rt.FnNow: s.d.Clock.Now}
				for k, v := range domain.ExprDocument(f.Snapshot()) {
					vars[k] = v
				}
				ok, err := agg.WhereProgram.Eval(vars)
				if err != nil || ok != true {
					continue
				}
			}
			if agg.Op == "count" {
				count++
				continue
			}
			v, ok := f.Field(agg.Field)
			if !ok {
				continue
			}
			if _, isNull := v.(kernel.Null); isNull {
				continue
			}
			count++
			switch x := v.(type) {
			case kernel.Int64:
				sum += float64(x)
			case kernel.Float:
				sum += float64(x)
			case kernel.DecimalValue:
				dsum, _ = dsum.Add(x.D)
			}
			if best == nil {
				best = v
			} else if c, ok := kernel.Compare(v, best); ok && ((agg.Op == "min" && c < 0) || (agg.Op == "max" && c > 0)) {
				best = v
			}
		}
		if page.Next == nil {
			break
		}
		after = page.Next
	}
	switch agg.Op {
	case "count":
		return count, nil
	case "min", "max":
		if best == nil {
			return nil, nil
		}
		return kernel.Native(best), nil
	case "sum":
		if child.Root != nil {
			if f, ok := child.Field(agg.Field); ok && f.Kind == kernel.KindDecimal {
				return dsum, nil
			}
			if f, ok := child.Field(agg.Field); ok && f.Kind == kernel.KindInt64 {
				return int64(sum), nil
			}
		}
		return sum, nil
	case "avg":
		if count == 0 {
			return nil, nil
		}
		return math.Round(sum/float64(count)*1e9) / 1e9, nil
	}
	return nil, nil
}
