package application

import (
	"context"
	"time"

	accessapi "app-maker/internal/access/api"
	"app-maker/internal/content/domain"
	schemaapi "app-maker/internal/schema/api"
)

type timeT = time.Time

// BeforeWriteRequest describes one operation about to be written.
type BeforeWriteRequest struct {
	Op        OpKind
	Type      *schemaapi.CompiledType
	Principal accessapi.Principal
	// Before is the feature before the change (nil for create), After the
	// state after it (nil for delete), both in expression form.
	Before map[string]any
	After  map[string]any
}

// BeforeWriteHook runs before-write flows (spec 10.2). It may reject the
// write by returning an error; staged writes join the batch through
// ExtraOps. It is implemented by the Flow context and installed at wiring.
type BeforeWriteHook interface {
	BeforeWrite(ctx context.Context, req BeforeWriteRequest) (BeforeWriteResult, error)
}

// BeforeWriteResult carries before-write flows' staged writes; they join
// the triggering batch. Temporary references must be unique per call.
type BeforeWriteResult struct {
	Ops []Op
}

func (s *Service) beforeWrite(ctx context.Context, wc WriteContext, w *work) error {
	if s.d.BeforeWrite == nil || wc.SkipBeforeWrite || wc.Fixture {
		return nil
	}
	req := BeforeWriteRequest{Op: w.op.Op, Type: w.ct, Principal: wc.Principal}
	if w.before != nil {
		req.Before = domain.ExprDocument(*w.before)
	}
	if w.op.Op != OpDelete && w.feature != nil {
		req.After = domain.ExprDocument(w.feature.Snapshot())
	}
	res, err := s.d.BeforeWrite.BeforeWrite(ctx, req)
	if err != nil {
		return err
	}
	if len(res.Ops) > 0 && w.tx != nil {
		w.tx.staged = append(w.tx.staged, res.Ops...)
		for range res.Ops {
			w.tx.origin = append(w.tx.origin, w.index)
		}
	}
	return nil
}
