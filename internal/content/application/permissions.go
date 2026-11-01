package application

import (
	"context"
	"errors"
	"strings"

	accessapi "organon/internal/access/api"
	"organon/internal/kernel"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// MaxPermissionChecks bounds one permissions request.
const MaxPermissionChecks = 100

// Permission is one question of the end-user UI's can(f, op) (ADR 0016):
// op is update, delete, action:<name>, or create:<child type>.
type Permission struct {
	ID string `json:"id"`
	Op string `json:"op"`
}

// Can answers permission questions for p. A feature p cannot read gives
// false, so the answer never reveals that it exists. Actions that are
// state-machine transitions also need the current state and the guard.
func (s *Service) Can(ctx context.Context, p accessapi.Principal, checks []Permission) ([]bool, error) {
	if len(checks) > MaxPermissionChecks {
		return nil, kernel.NewValidationError(kernel.FieldError{Path: "/checks", Code: "maxItems",
			Message: "at most 100 checks per request"})
	}
	out := make([]bool, len(checks))
	for i, c := range checks {
		ok, err := s.can(ctx, p, c)
		if err != nil {
			return nil, err
		}
		out[i] = ok
	}
	return out, nil
}

func (s *Service) can(ctx context.Context, p accessapi.Principal, c Permission) (bool, error) {
	id, err := kernel.ParseID(c.ID)
	if err != nil {
		return false, nil
	}
	f, ct, err := s.load(ctx, id)
	if errors.Is(err, kernel.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ref := s.Ref(f, ct)
	if _, err := s.canRead(ctx, p, ref); err != nil {
		if errors.Is(err, kernel.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	allowed := func(req accessapi.CheckRequest) (bool, error) {
		d, err := s.d.Authorizer.Check(ctx, p, req)
		return err == nil && d.Allowed, err
	}
	switch op := c.Op; {
	case op == "update":
		return allowed(accessapi.CheckRequest{Op: accessapi.OpUpdate, Type: ct, Feature: ref, Patch: map[string]any{}})
	case op == "delete":
		return allowed(accessapi.CheckRequest{Op: accessapi.OpDelete, Type: ct, Feature: ref})
	case strings.HasPrefix(op, "create:"):
		child, ok := s.d.Types.ByName(op[len("create:"):])
		if !ok || !ct.AllowsChild(child) {
			return false, nil
		}
		return allowed(accessapi.CheckRequest{Op: accessapi.OpCreate, Type: child, Parent: ref})
	case strings.HasPrefix(op, "action:"):
		return s.canAction(ctx, p, ct, ref, op[len("action:"):], allowed)
	}
	return false, nil
}

func (s *Service) canAction(ctx context.Context, p accessapi.Principal, ct *schemaapi.CompiledType, ref *accessapi.FeatureRef,
	name string, allowed func(accessapi.CheckRequest) (bool, error)) (bool, error) {
	if name == "transition" { // any transition available now
		for _, sm := range ct.StateMachines {
			from, _ := ref.Data[sm.Path[1:]].(string)
			for _, t := range sm.Transitions {
				if t.From != from {
					continue
				}
				if ok, err := s.canAction(ctx, p, ct, ref, t.Action, allowed); ok || err != nil {
					return ok, err
				}
			}
		}
		return false, nil
	}
	ok, err := allowed(accessapi.CheckRequest{Op: accessapi.ActionOp(name), Type: ct, Feature: ref, Input: map[string]any{}})
	if !ok || err != nil {
		return false, err
	}
	for _, sm := range ct.StateMachines {
		declared := false
		for _, t := range sm.Transitions {
			declared = declared || t.Action == name
		}
		if !declared {
			continue
		}
		from, _ := ref.Data[sm.Path[1:]].(string)
		t, found := sm.Find(from, name)
		if !found {
			return false, nil
		}
		if t.GuardProgram != nil {
			vars := s.d.Authorizer.Bindings(ctx, p)
			vars["principal"] = p.ExprValue()
			vars["feature"] = ref.Data
			vars["input"] = map[string]any{}
			vars[rt.FnNow] = s.d.Clock.Now
			out, err := t.GuardProgram.Eval(vars)
			if b, _ := out.(bool); err != nil || !b {
				return false, nil
			}
		}
	}
	return true, nil
}
