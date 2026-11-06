package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	accessapi "organon/internal/access/api"
	contentapi "organon/internal/content/api"
	"organon/internal/content/domain"
	flowapi "organon/internal/flow/api"
	"organon/internal/kernel"
	queryapi "organon/internal/query/api"
	rt "organon/internal/runtime/api"
	schemaapi "organon/internal/schema/api"
)

// OpKind is a transaction operation (spec 6.3).
type OpKind string

// Operation kinds.
const (
	OpCreate OpKind = "create"
	OpUpdate OpKind = "update"
	OpDelete OpKind = "delete"
	OpMove   OpKind = "move"
)

// Op is one operation of a transaction. IDs and parents may be temporary
// references ("$p") to features created earlier in the same transaction.
type Op struct {
	Op      OpKind         `json:"op"`
	Type    string         `json:"type"`
	Ref     string         `json:"ref,omitempty"`
	ID      string         `json:"id,omitempty"`
	Parent  string         `json:"parent,omitempty"`
	Version kernel.Version `json:"version,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
	// AsSystem marks a write staged by a beforeWrite flow that runs as
	// system; it is prepared as the system principal (ADR 0023).
	AsSystem bool `json:"-"`
}

// WriteContext carries who writes and why.
type WriteContext struct {
	Principal accessapi.Principal
	// Action names the action performing the write; it unlocks fields with
	// x-write: action:<name> (spec 10.3). Empty for generic writes and
	// transactions.
	Action string
	// ActionFeature is the feature Action runs on: its updates were
	// authorized by the action's rule, so the update rule does not apply
	// (ADR 0023). Zero for writes outside actions.
	ActionFeature kernel.ID
	// Causation is the chain of flow runs that led to this write.
	Causation flowapi.CausationChain
	// Fixture bypasses write rules and policies for seed data and manifest
	// test fixtures; only the system principal may use it.
	Fixture bool
	// Owner overrides the owner of created Ownable features (fixtures).
	Owner *kernel.ID
	// SkipBeforeWrite is set when a before-write flow's staged writes are
	// committed, so flows do not re-trigger themselves.
	SkipBeforeWrite bool
	// ExtraJobs are committed with the batch (effects staged by flows).
	ExtraJobs []kernel.JobRequest
	// KeepVersion writes maintained fields (aggregate recomputes) without
	// a version check or bump (ADR 0023).
	KeepVersion bool
}

// TxResult is the outcome of a committed transaction.
type TxResult struct {
	Refs     map[string]string         `json:"refs"`
	Versions map[string]kernel.Version `json:"versions"`
	Features []contentapi.FeatureView  `json:"features"`
}

// work is one operation being prepared.
type work struct {
	index   int
	op      Op
	ct      *schemaapi.CompiledType
	id      kernel.ID
	feature *domain.Feature
	before  *domain.Snapshot // state before the change, for updates
	parent  *domain.Feature
	aud     schemaapi.Audience
	unique  string
	deleted bool
	extra   []*work // cascaded deletes and setNull updates
	ref     *accessapi.FeatureRef
	tx      *txState
	// seqs are x-sequence fields to number in the commit (ADR 0021).
	seqs []pendingSeq
}

// pendingSeq is a sequence number drawn when the batch commits.
type pendingSeq struct {
	def    schemaapi.SequenceDef
	series string
}

// Execute runs operations through the write pipeline and commits them in
// one batch (spec 6.4).
func (s *Service) Execute(ctx context.Context, wc WriteContext, ops []Op) (TxResult, error) {
	if len(ops) == 0 {
		return TxResult{}, kernel.NewError(kernel.CodeBadRequest, "no operations")
	}
	if len(ops) > s.d.Limits.MaxTransactionOps {
		return TxResult{}, kernel.NewError(kernel.CodeValidationFailed, "at most %d operations per transaction", s.d.Limits.MaxTransactionOps)
	}
	if wc.Fixture && !wc.Principal.IsSystem() {
		return TxResult{}, kernel.ErrForbidden
	}
	now := s.d.Clock.Now()
	works, refs, err := s.resolve(ops)
	if err != nil {
		return TxResult{}, err
	}
	// One repository per batch (spec 5.2).
	repoName := works[0].ct.Repository
	for _, w := range works {
		if w.ct.Repository != repoName {
			return TxResult{}, kernel.ErrCrossRepository
		}
	}
	repo, err := s.d.Repos.Get(repoName)
	if err != nil {
		return TxResult{}, err
	}
	if err := s.loadAll(ctx, repo, works); err != nil {
		return TxResult{}, err
	}
	tx := &txState{}
	for _, w := range works {
		w.tx = tx
	}
	if err := s.prepareAll(ctx, wc, repo, works, now); err != nil {
		return TxResult{}, err
	}
	// Writes staged by before-write flows join the batch and go through
	// the same pipeline (spec 10.6 step 3).
	if len(tx.staged) > 0 {
		byID := map[kernel.ID]*work{}
		for _, w := range works {
			byID[w.id] = w
		}
		inner := wc
		inner.SkipBeforeWrite = true
		system := inner
		system.Principal = accessapi.System()
		// An update of a feature of this write (even one being created)
		// merges into it, write rules checked again (ADR 0023).
		var staged []Op
		var origin []int
		for i, op := range tx.staged {
			iwc := inner
			if op.AsSystem {
				iwc = system
			}
			id, err := kernel.ParseID(op.ID)
			orig := byID[id]
			if err != nil || orig == nil {
				staged, origin = append(staged, op), append(origin, tx.origin[i])
				continue
			}
			if op.Op != OpUpdate || orig.deleted {
				return TxResult{}, opErr(orig.index, kernel.NewError(kernel.CodeValidationFailed, "a before-write flow may only update a feature of the write itself"))
			}
			merged := &work{index: orig.index, op: op, ct: orig.ct, id: orig.id, feature: orig.feature, before: orig.before, tx: orig.tx}
			if err := s.prepareUpdate(ctx, iwc, merged, now); err != nil {
				return TxResult{}, opErr(orig.index, err)
			}
			orig.unique, orig.seqs = merged.unique, append(orig.seqs, merged.seqs...)
		}
		extra, extraRefs, err := s.resolve(staged)
		if err != nil {
			return TxResult{}, err
		}
		var own, sys []*work
		for i, w := range extra {
			if w.ct.Repository != repoName {
				return TxResult{}, kernel.ErrCrossRepository
			}
			if byID[w.id] != nil {
				return TxResult{}, kernel.NewError(kernel.CodeValidationFailed, "a before-write flow changed a feature of the write itself")
			}
			w.index = origin[i]
			if staged[i].AsSystem {
				sys = append(sys, w)
			} else {
				own = append(own, w)
			}
		}
		extra = append(own, sys...)
		if err := s.loadAll(ctx, repo, extra); err != nil {
			return TxResult{}, err
		}
		if err := s.prepareAll(ctx, inner, repo, own, now); err != nil {
			return TxResult{}, err
		}
		if err := s.prepareAll(ctx, system, repo, sys, now); err != nil {
			return TxResult{}, err
		}
		works = append(works, extra...)
		for k, v := range extraRefs {
			refs[k] = v
		}
	}
	return s.commit(ctx, wc, repo, works, refs)
}

// txState is shared by the operations of one Execute call.
type txState struct {
	staged []Op // writes staged by before-write flows
	origin []int
}

func (s *Service) prepareAll(ctx context.Context, wc WriteContext, repo domain.FeatureRepository, works []*work, now timeT) error {
	created := map[kernel.ID]*work{}
	for _, w := range works {
		var err error
		switch w.op.Op {
		case OpCreate:
			if err = s.prepareCreate(ctx, wc, w, created, now); err == nil {
				err = s.verifyBlobs(ctx, wc, w, nil)
			}
			created[w.id] = w
		case OpUpdate:
			old := blobIDs(w.ct, w.feature.Fields())
			if err = s.prepareUpdate(ctx, wc, w, now); err == nil {
				err = s.verifyBlobs(ctx, wc, w, old)
			}
		case OpDelete:
			err = s.prepareDelete(ctx, wc, repo, w, works, now)
		case OpMove:
			err = s.prepareMove(ctx, wc, w, created, now)
		}
		if err != nil {
			return opErr(w.index, err)
		}
	}
	return nil
}

// MoveFeature changes a feature's parent (spec 6.2 POST /features/{id}/move).
func (s *Service) MoveFeature(ctx context.Context, wc WriteContext, id kernel.ID, parent string, version kernel.Version) (contentapi.FeatureView, error) {
	_, ct, err := s.load(ctx, id)
	if err != nil {
		return contentapi.FeatureView{}, err
	}
	res, err := s.Execute(ctx, wc, []Op{{Op: OpMove, Type: ct.Name, ID: id.String(), Version: version, Parent: parent}})
	if err != nil {
		return contentapi.FeatureView{}, err
	}
	return res.Features[0], nil
}

// resolve assigns IDs, resolves types and temporary references in order.
func (s *Service) resolve(ops []Op) ([]*work, map[string]kernel.ID, error) {
	refs := map[string]kernel.ID{}
	touched := map[kernel.ID]bool{}
	var works []*work
	resolveID := func(i int, v, what string) (kernel.ID, error) {
		if strings.HasPrefix(v, "$") {
			id, ok := refs[v]
			if !ok {
				return kernel.NilID, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "%s %s is used before the operation that creates it", what, v))
			}
			return id, nil
		}
		id, err := kernel.ParseID(v)
		if err != nil {
			return kernel.NilID, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "invalid %s %q", what, v))
		}
		return id, nil
	}
	for i, op := range ops {
		ct, err := s.typeByName(op.Type)
		if err != nil {
			return nil, nil, opErr(i, err)
		}
		w := &work{index: i, op: op, ct: ct}
		switch op.Op {
		case OpCreate:
			w.id = s.d.IDs.NewID()
			if op.Ref != "" {
				if !strings.HasPrefix(op.Ref, "$") {
					return nil, nil, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "temporary references start with $"))
				}
				if _, dup := refs[op.Ref]; dup {
					return nil, nil, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "duplicate reference %s", op.Ref))
				}
				refs[op.Ref] = w.id
			}
		case OpUpdate, OpDelete, OpMove:
			id, err := resolveID(i, op.ID, "id")
			if err != nil {
				return nil, nil, err
			}
			if op.Version <= 0 {
				return nil, nil, opErr(i, kernel.NewError(kernel.CodeVersionRequired, "version is required"))
			}
			w.id = id
		default:
			return nil, nil, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "unknown op %q", op.Op))
		}
		if touched[w.id] {
			return nil, nil, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "a transaction may change each feature once"))
		}
		touched[w.id] = true
		if op.Op == OpCreate || op.Op == OpMove {
			parent := op.Parent
			if parent == "" {
				if op.Op == OpMove {
					return nil, nil, opErr(i, kernel.NewError(kernel.CodeValidationFailed, "parent is required"))
				}
				parent = contentapi.RootID.String()
			}
			pid, err := resolveID(i, parent, "parent")
			if err != nil {
				return nil, nil, err
			}
			w.op.Parent = pid.String()
		}
		works = append(works, w)
	}
	return works, refs, nil
}

// loadAll reads every existing feature and parent with one GetMany call.
func (s *Service) loadAll(ctx context.Context, repo domain.FeatureRepository, works []*work) error {
	var ids []kernel.ID
	for _, w := range works {
		if w.op.Op != OpCreate {
			ids = append(ids, w.id)
		}
		if w.op.Parent != "" {
			ids = append(ids, kernel.MustParseID(w.op.Parent))
		}
	}
	found := map[kernel.ID]*domain.Feature{}
	if len(ids) > 0 {
		fs, err := repo.GetMany(ctx, ids)
		if err != nil {
			return err
		}
		for _, f := range fs {
			found[f.ID()] = f
		}
	}
	for _, w := range works {
		if w.op.Op != OpCreate {
			f, ok := found[w.id]
			if !ok {
				return opErr(w.index, kernel.ErrNotFound)
			}
			if f.TypeID().Base() != w.ct.Base() {
				return opErr(w.index, kernel.ErrNotFound)
			}
			if f.Version() != w.op.Version {
				return opErr(w.index, &kernel.VersionConflictError{Conflicts: []kernel.VersionConflict{{ID: f.ID(), Current: f.Version()}}})
			}
			snap := f.Snapshot()
			w.before = &snap
			w.feature = f
		}
		if w.op.Parent != "" {
			w.parent = found[kernel.MustParseID(w.op.Parent)]
		}
	}
	return nil
}

func (s *Service) parentInfo(w *work, created map[kernel.ID]*work) (*domain.ParentInfo, *accessapi.FeatureRef, error) {
	pid := kernel.MustParseID(w.op.Parent)
	if cw, ok := created[pid]; ok {
		return &domain.ParentInfo{ID: pid, Type: cw.ct, Ancestors: cw.feature.Ancestors()}, s.Ref(cw.feature, cw.ct), nil
	}
	if w.parent == nil {
		return nil, nil, kernel.NewError(kernel.CodeNotFound, "parent not found")
	}
	pct, err := s.typeOf(w.parent)
	if err != nil {
		return nil, nil, err
	}
	return &domain.ParentInfo{ID: pid, Type: pct, Ancestors: w.parent.Ancestors()}, s.Ref(w.parent, pct), nil
}

func (s *Service) authorize(ctx context.Context, wc WriteContext, req accessapi.CheckRequest) error {
	if wc.Fixture {
		return nil
	}
	req.InAction = wc.Action
	d, err := s.d.Authorizer.Check(ctx, wc.Principal, req)
	if err != nil {
		return err
	}
	if !d.Allowed {
		// Hide existence when the caller cannot even read the feature (or,
		// for create, the parent): never distinguish missing and hidden.
		target := req.Feature
		if req.Op == accessapi.OpCreate {
			target = req.Parent
		}
		if target != nil {
			if rd, err := s.d.Authorizer.Check(ctx, wc.Principal, accessapi.CheckRequest{Op: accessapi.OpRead, Type: target.Type, Feature: target}); err == nil && !rd.Allowed {
				return kernel.ErrNotFound
			}
		}
		return kernel.NewError(kernel.CodeForbidden, "%s is not allowed", req.Op)
	}
	return nil
}

func (s *Service) prepareCreate(ctx context.Context, wc WriteContext, w *work, created map[kernel.ID]*work, now timeT) error {
	ct := w.ct
	if ct.Base() == schemaapi.TypeRoot {
		return kernel.NewError(kernel.CodeForbidden, "the root feature is created at bootstrap")
	}
	pinfo, pref, err := s.parentInfo(w, created)
	if err != nil {
		return err
	}
	in, err := s.coerceInput(ct, w.op.Data)
	if err != nil {
		return err
	}
	patch := in.vals
	if ct.Base() == schemaapi.TypeGrant {
		// A grant's object is its parent (spec 8.6).
		obj := kernel.Ref{ID: pinfo.ID}
		if v, ok := patch["/object"]; ok && v != nil {
			if r, isRef := v.(kernel.Ref); !isRef || r.ID != pinfo.ID {
				return kernel.NewValidationError(kernel.FieldError{Path: "/object", Code: "const", Message: "a grant's object must be its parent"})
			}
		}
		patch["/object"] = obj
	}
	if err := s.authorize(ctx, wc, accessapi.CheckRequest{Op: accessapi.OpCreate, Type: ct, Parent: pref, Patch: nativePatch(patch)}); err != nil {
		return err
	}
	w.aud = s.createAudience(wc, ct)
	if err := s.checkWritable(wc, ct, w.aud, schemaapi.DirCreate, in, nil); err != nil {
		return err
	}
	if err := s.validateView(ct, w.aud, schemaapi.DirCreate, in, true); err != nil {
		return err
	}
	fields := map[string]kernel.Value{}
	for _, f := range ct.TopFields() {
		if f.Default != nil && f.Persist {
			v, err := f.Type.Coerce(f.Path, f.Default)
			if err == nil {
				fields[f.Path] = v
			}
		}
	}
	for p, v := range patch {
		if v != nil {
			fields[p] = v
		}
	}
	var owner *kernel.ID
	if ct.HasTrait(schemaapi.TraitOwnable) {
		o := wc.Principal.ID
		if wc.Owner != nil {
			o = *wc.Owner
		}
		if o.IsZero() {
			return kernel.ErrUnauthenticated
		}
		owner = &o
		fields["/owner"] = kernel.Ref{ID: o, Target: string(schemaapi.TraitPrincipal)}
	}
	if kind, ok := principalKinds[ct.Base()]; ok {
		fields["/kind"] = kernel.Enum(kind)
	}
	s.stamp(ct, fields, now, true)
	if err := s.compute(ct, fields, w.id, pinfo.ID, 1); err != nil {
		return err
	}
	f, err := domain.NewFeature(w.id, ct, pinfo, owner, fields, now, wc.Principal.ID)
	if err != nil {
		return err
	}
	if err := s.validateStorage(ct, w.aud, f.Fields()); err != nil {
		return err
	}
	w.feature = f
	if err := s.pendingSequences(ct, w, "", pinfo.ID); err != nil {
		return err
	}
	w.unique = uniqueKey(ct, fields)
	if err := s.beforeWrite(ctx, wc, w); err != nil {
		return err
	}
	return nil
}

var principalKinds = map[schemaapi.TypeID]string{
	schemaapi.TypeUser: "user", schemaapi.TypeOrganization: "organization",
	schemaapi.TypeTeam: "team", schemaapi.TypeServiceAccount: "service",
}

// createAudience picks the write view for a create: the creator owns
// Ownable features.
func (s *Service) createAudience(wc WriteContext, ct *schemaapi.CompiledType) schemaapi.Audience {
	switch {
	case wc.Principal.IsSystem() || wc.Fixture:
		return schemaapi.AudSystem
	case wc.Principal.HasRole("admin"):
		return schemaapi.AudAdmin
	case ct.HasTrait(schemaapi.TraitOwnable):
		return schemaapi.AudOwner
	}
	return schemaapi.AudPublic
}

func (s *Service) prepareUpdate(ctx context.Context, wc WriteContext, w *work, now timeT) error {
	ct := w.ct
	ref := s.Ref(w.feature, ct)
	w.ref = ref
	in, err := s.mergePatch(ct, w.feature, w.op.Data)
	if err != nil {
		return err
	}
	// Unchanged values are not writes (ADR 0023): setting a field to its
	// current value needs no permission and changes nothing.
	for p, v := range in.vals {
		cur, had := w.feature.Field(p)
		if (v == nil && !had) || (v != nil && had && kernel.Equal(cur, v)) {
			delete(in.vals, p)
		}
	}
	merged := in.vals
	op := accessapi.OpUpdate
	switch {
	case len(merged) == 0 && len(in.failed) == 0:
		op = accessapi.OpRead // nothing to write; the caller must still see it
	case wc.Action != "" && wc.ActionFeature == w.id:
		op = "" // the action's rule allowed this action on this feature
	}
	if op != "" {
		if err := s.authorize(ctx, wc, accessapi.CheckRequest{Op: op, Type: ct, Feature: ref, Patch: nativePatch(merged)}); err != nil {
			return err
		}
	}
	if wc.Fixture || wc.Principal.IsSystem() {
		w.aud = schemaapi.AudSystem
	} else if w.aud, err = s.d.Authorizer.Audience(ctx, wc.Principal, ref); err != nil {
		return err
	}
	if err := s.checkWritable(wc, ct, w.aud, schemaapi.DirUpdate, in, w.feature); err != nil {
		return err
	}
	if err := s.validateView(ct, w.aud, schemaapi.DirUpdate, in, false); err != nil {
		return err
	}
	if err := s.checkTransitions(ctx, wc, ct, w.feature, merged, ref); err != nil {
		return err
	}
	fields := w.feature.Fields()
	for p, v := range merged {
		if v == nil {
			delete(fields, p)
		} else {
			fields[p] = v
		}
	}
	parent := kernel.NilID
	if pid := w.feature.ParentID(); pid != nil {
		parent = *pid
	}
	if err := s.compute(ct, fields, w.id, parent, w.feature.Version()+1); err != nil {
		return err
	}
	patch := domain.Patch{}
	for p, v := range merged {
		patch[p] = v
	}
	for _, c := range ct.Computed {
		patch[c.Path] = fields[c.Path] // nil removes
	}
	if _, err := w.feature.Apply(patch, now, wc.Principal.ID); err != nil {
		return err
	}
	if w.feature.Changed() {
		stamp := map[string]kernel.Value{}
		s.stamp(ct, stamp, now, false)
		if len(stamp) > 0 {
			if _, err := w.feature.Apply(domain.Patch(stamp), now, wc.Principal.ID); err != nil {
				return err
			}
		}
	}
	if err := s.validateStorage(ct, w.aud, w.feature.Fields()); err != nil {
		return err
	}
	if wc.Action != "" {
		if err := s.pendingSequences(ct, w, wc.Action, parent); err != nil {
			return err
		}
	}
	w.unique = uniqueKey(ct, w.feature.Fields())
	return s.beforeWrite(ctx, wc, w)
}

func (s *Service) prepareMove(ctx context.Context, wc WriteContext, w *work, created map[kernel.ID]*work, now timeT) error {
	pinfo, pref, err := s.parentInfo(w, created)
	if err != nil {
		return err
	}
	w.ref = s.Ref(w.feature, w.ct)
	if err := s.authorize(ctx, wc, accessapi.CheckRequest{Op: accessapi.OpMove, Type: w.ct, Feature: w.ref, Parent: pref}); err != nil {
		return err
	}
	if err := w.feature.MoveTo(*pinfo, w.ct, now, wc.Principal.ID); err != nil {
		return err
	}
	w.unique = uniqueKey(w.ct, w.feature.Fields())
	return s.beforeWrite(ctx, wc, w)
}

func (s *Service) prepareDelete(ctx context.Context, wc WriteContext, repo domain.FeatureRepository, w *work, all []*work, now timeT) error {
	w.ref = s.Ref(w.feature, w.ct)
	if err := s.authorize(ctx, wc, accessapi.CheckRequest{Op: accessapi.OpDelete, Type: w.ct, Feature: w.ref}); err != nil {
		return err
	}
	if err := s.beforeWrite(ctx, wc, w); err != nil {
		return err
	}
	if err := w.feature.MarkDeleted(now, wc.Principal.ID); err != nil {
		return err
	}
	w.deleted = true
	// Children are soft-deleted in the same transaction (spec 3.6).
	desc, err := repo.Descendants(ctx, w.id, domain.MaxMoveSize)
	if err != nil {
		return err
	}
	deleting := map[kernel.ID]bool{w.id: true}
	targets := []*work{w}
	for _, d := range desc {
		for _, other := range all {
			if other.id == d.ID() && other.op.Op != OpDelete {
				return kernel.NewError(kernel.CodeValidationFailed, "feature %s is changed and deleted in one transaction", d.ID())
			}
		}
		dct, err := s.typeOf(d)
		if err != nil {
			return err
		}
		if err := d.MarkDeleted(now, wc.Principal.ID); err != nil {
			return err
		}
		cw := &work{index: w.index, op: Op{Op: OpDelete}, ct: dct, id: d.ID(), feature: d, deleted: true}
		w.extra = append(w.extra, cw)
		targets = append(targets, cw)
		deleting[d.ID()] = true
	}
	return s.applyOnDelete(ctx, wc, repo, w, targets, deleting, now)
}

// applyOnDelete enforces x-onDelete for references to deleted features
// (spec 3.3): restrict blocks, setNull clears.
func (s *Service) applyOnDelete(ctx context.Context, wc WriteContext, repo domain.FeatureRepository, w *work, targets []*work, deleting map[kernel.ID]bool, now timeT) error {
	for _, ref := range s.d.Types.All() {
		if ref.Repository != w.ct.Repository {
			continue // cross-repository references always dangle
		}
		for _, f := range ref.TopFields() {
			target := f.RefTarget
			isList := f.Kind == kernel.KindList && f.Type.Elem.Kind == kernel.KindRef
			if isList {
				target = f.Type.Elem.RefTarget
			}
			if (f.Kind != kernel.KindRef && !isList) || f.OnDelete == schemaapi.OnDeleteDangle {
				continue
			}
			for _, t := range targets {
				if target != "" && !t.ct.Is(target) {
					continue
				}
				op := queryapi.OpEq
				if isList {
					op = queryapi.OpContains
				}
				page, err := repo.Query(ctx, queryapi.Plan{
					Types: []schemaapi.TypeID{ref.ID}, Limit: queryapi.MaxLimit,
					Where: queryapi.Compare{Path: f.Path, Op: op, Value: kernel.Ref{ID: t.id}},
				})
				if err != nil {
					return err
				}
				for _, rf := range page.Items {
					if deleting[rf.ID()] {
						continue
					}
					if f.OnDelete == schemaapi.OnDeleteRestrict {
						return &kernel.ConstraintError{Path: f.Path, Code: "restrict", ID: rf.ID()}
					}
					if isList || slices.ContainsFunc(w.extra, func(x *work) bool { return x.id == rf.ID() }) {
						continue // setNull on lists is not defined; leave dangling
					}
					if _, err := rf.Apply(domain.Patch{f.Path: kernel.Null{}}, now, wc.Principal.ID); err != nil {
						return err
					}
					w.extra = append(w.extra, &work{index: w.index, op: Op{Op: OpUpdate}, ct: ref, id: rf.ID(), feature: rf})
				}
			}
		}
	}
	return nil
}

// commit builds the batch, plans jobs, applies it, and projects results.
func (s *Service) commit(ctx context.Context, wc WriteContext, repo domain.FeatureRepository, works []*work, refs map[string]kernel.ID) (TxResult, error) {
	var all []*work
	var seqs []*work
	for _, w := range works {
		all = append(all, w)
		all = append(all, w.extra...)
		if len(w.seqs) > 0 {
			seqs = append(seqs, w)
		}
	}
	var b domain.Batch
	var err error
	if len(seqs) == 0 {
		if b, err = s.batch(ctx, wc, all); err != nil {
			return TxResult{}, err
		}
	} else {
		// Numbers are drawn inside the repository's transaction, and the
		// batch (events, audit, jobs) is built after, so it carries them.
		now := s.d.Clock.Now()
		b.Finalize = func(next domain.NextFunc) (domain.Batch, error) {
			for _, w := range seqs {
				for _, p := range w.seqs {
					n, err := next(p.series)
					if err != nil {
						return domain.Batch{}, err
					}
					v := schemaapi.FormatSequence(p.def.Format, n, p.series, now)
					w.feature.AssignSequence(p.def.Path, kernel.String(v), now, wc.Principal.ID)
				}
			}
			return s.batch(ctx, wc, all)
		}
	}
	res := TxResult{Refs: map[string]string{}, Versions: map[string]kernel.Version{}}
	if len(b.Mutations) > 0 || b.Finalize != nil {
		if !repo.Capabilities().Transactions && (len(b.Mutations) > 1 || b.Finalize != nil) {
			return TxResult{}, &kernel.UnsupportedError{Capability: "Transactions"}
		}
		cr, err := repo.Apply(ctx, b)
		if err != nil {
			return TxResult{}, err
		}
		for id, v := range cr.Versions {
			res.Versions[id.String()] = v
		}
	} else if len(b.Jobs) > 0 {
		// Nothing changed, but the run's effects are still due.
		if err := s.enqueueJobs(ctx, b.Jobs); err != nil {
			return TxResult{}, err
		}
	}
	return s.finishCommit(res, works, refs), nil
}

// batch builds the repository batch of the prepared works.
func (s *Service) batch(ctx context.Context, wc WriteContext, all []*work) (domain.Batch, error) {
	var b domain.Batch
	b.Causation = wc.Causation
	for _, w := range all {
		f := w.feature
		if !f.Changed() {
			continue
		}
		kind := domain.MutUpdate
		switch {
		case w.op.Op == OpCreate:
			kind = domain.MutCreate
		case w.deleted:
			kind = domain.MutDelete
		case w.op.Op == OpMove:
			kind = domain.MutMove
		}
		m := domain.Mutation{Kind: kind, Feature: f.Snapshot(), Expected: f.Version(), UniqueKey: w.unique}
		events := f.DrainEvents()
		switch {
		case wc.KeepVersion && kind == domain.MutUpdate:
			// Maintained fields only: merged, version unchanged; events
			// carry version 0 so clients apply them without a bump.
			m.KeepVersion, m.Feature.Version, m.Patch = true, f.Version(), map[string]kernel.Value{}
			for i := range events {
				for _, p := range events[i].Changed {
					m.Patch[p] = m.Feature.Fields[p]
				}
				events[i].Version = 0
			}
		case kind == domain.MutUpdate || kind == domain.MutMove:
			for _, a := range w.ct.Aggregates {
				m.Preserve = append(m.Preserve, a.Path)
			}
		}
		b.Mutations = append(b.Mutations, m)
		b.Events = append(b.Events, events...)
	}
	b.Audit = s.auditRecords(wc, b.Events)
	events := s.toChangeEvents(b.Events, wc.Causation)
	if len(events) > 0 {
		jobs, err := publishJobs(events)
		if err != nil {
			return domain.Batch{}, err
		}
		b.Jobs = append(b.Jobs, jobs...)
		for _, h := range s.d.Hooks {
			hj, err := h.Jobs(ctx, events, wc.Causation)
			if err != nil {
				return domain.Batch{}, err
			}
			b.Jobs = append(b.Jobs, hj...)
		}
	}
	b.Jobs = append(b.Jobs, wc.ExtraJobs...)
	return b, nil
}

// finishCommit adds refs and the committed features to the result.
func (s *Service) finishCommit(res TxResult, works []*work, refs map[string]kernel.ID) TxResult {
	for r, id := range refs {
		res.Refs[r] = id.String()
	}
	for _, w := range works {
		if w.deleted {
			continue
		}
		snap := w.feature.Snapshot()
		res.Features = append(res.Features, s.link(w.ct, domain.Project(w.ct, snap, w.aud, nil)))
	}
	return res
}

// input is a request body converted to typed values. Fields that failed
// conversion keep their raw value so that schema validation can report
// every problem at once (spec 4.9).
type input struct {
	vals   map[string]kernel.Value // nil value = removal (merge patch null)
	failed map[string]any
	errs   []kernel.FieldError
}

// keys returns every top-level path in the body.
func (in input) keys() []string {
	out := make([]string, 0, len(in.vals)+len(in.failed))
	for p := range in.vals {
		out = append(out, p)
	}
	for p := range in.failed {
		out = append(out, p)
	}
	return out
}

// coerceInput converts a body into typed values without failing; unknown
// fields and conversion problems are collected in errs.
func (s *Service) coerceInput(ct *schemaapi.CompiledType, data map[string]any) (input, error) {
	in := input{vals: map[string]kernel.Value{}, failed: map[string]any{}}
	if err := checkShape("", data, 0); err != nil {
		return in, err
	}
	for k, raw := range data {
		path := "/" + strings.TrimPrefix(k, "/")
		f, ok := ct.Field(path)
		if !ok || !f.Top {
			in.errs = append(in.errs, kernel.FieldError{Path: path, Code: "additionalProperties", Message: "unknown property"})
			continue
		}
		if raw == nil {
			in.vals[path] = nil
			continue
		}
		v, err := f.Type.Coerce(path, raw)
		if err != nil {
			var ve *kernel.ValidationError
			if !errors.As(err, &ve) {
				return input{}, err
			}
			in.failed[path] = raw
			in.errs = append(in.errs, ve.Errors...)
			continue
		}
		in.vals[path] = v
	}
	return in, nil
}

// mergePatch applies RFC 7396 to each top-level field: objects merge
// recursively, null removes (spec 4.9). The result maps paths to new
// values, nil meaning removal.
func (s *Service) mergePatch(ct *schemaapi.CompiledType, f *domain.Feature, data map[string]any) (input, error) {
	adjusted := map[string]any{}
	for k, raw := range data {
		path := "/" + strings.TrimPrefix(k, "/")
		fd, ok := ct.Field(path)
		if ok && fd.Kind == kernel.KindObject && fd.Type.Union == nil {
			if pm, isMap := raw.(map[string]any); isMap {
				cur, _ := f.Field(path)
				base, _ := fd.Type.Wire(cur).(map[string]any)
				raw = mergeJSON(base, pm)
			}
		}
		adjusted[k] = raw
	}
	return s.coerceInput(ct, adjusted)
}

func mergeJSON(base, patch map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		if v == nil {
			delete(out, k)
			continue
		}
		if pm, ok := v.(map[string]any); ok {
			bm, _ := out[k].(map[string]any)
			out[k] = mergeJSON(bm, pm)
			continue
		}
		out[k] = v
	}
	return out
}

// checkWritable enforces x-write and the caller's write view (spec 4.2).
func (s *Service) checkWritable(wc WriteContext, ct *schemaapi.CompiledType, aud schemaapi.Audience, dir schemaapi.Direction, in input, f *domain.Feature) error {
	if wc.Fixture {
		return nil
	}
	view := ct.View(aud, dir)
	var errs []kernel.FieldError
	for _, path := range in.keys() {
		if view.Writable[path] || s.actionMayWrite(wc, ct, path) {
			continue
		}
		fd, _ := ct.Field(path)
		code, msg := "not_writable", "field is not writable"
		if fd != nil && !ct.View(aud, schemaapi.DirRead).Readable[path] {
			code, msg = "additionalProperties", "unknown property" // never reveal hidden fields
		}
		if fd != nil && fd.Immutable && f != nil {
			code, msg = "immutable", "field is writable on create only"
		}
		errs = append(errs, kernel.FieldError{Path: path, Code: code, Message: msg})
	}
	if len(errs) > 0 {
		return kernel.NewValidationError(errs...)
	}
	return nil
}

// actionMayWrite reports whether the running action unlocks the field.
func (s *Service) actionMayWrite(wc WriteContext, ct *schemaapi.CompiledType, path string) bool {
	if wc.Action == "" {
		return false
	}
	fd, ok := ct.Field(path)
	if !ok || fd.Write.Kind != schemaapi.WriteAction {
		return false
	}
	if fd.Write.Action == wc.Action {
		return true
	}
	if sm, ok := ct.StateMachines[path]; ok && fd.Write.Action == "transition" {
		for _, t := range sm.Transitions {
			if t.Action == wc.Action {
				return true
			}
		}
	}
	return false
}

// checkTransitions enforces x-stateMachine on changed enum fields (spec 4.10).
func (s *Service) checkTransitions(ctx context.Context, wc WriteContext, ct *schemaapi.CompiledType, f *domain.Feature, patch map[string]kernel.Value, ref *accessapi.FeatureRef) error {
	if wc.Fixture {
		return nil
	}
	for path, sm := range ct.StateMachines {
		nv, changing := patch[path]
		if !changing {
			continue
		}
		cur, _ := f.Field(path)
		from, _ := kernel.Native(cur).(string)
		to, _ := kernel.Native(nv).(string)
		if from == to {
			continue
		}
		var tr *schemaapi.Transition
		if wc.Action != "" && wc.Action != "transition" {
			t, ok := sm.Find(from, wc.Action)
			if !ok || t.To != to {
				return kernel.NewError(kernel.CodeInvalidTransition, "action %s cannot change %s from %q to %q", wc.Action, path, from, to)
			}
			tr = t
		} else {
			t, ok := sm.Allowed(from, to)
			if !ok {
				return kernel.NewError(kernel.CodeInvalidTransition, "%s cannot change from %q to %q", path, from, to)
			}
			tr = t
		}
		if tr.GuardProgram != nil {
			ok, err := evalGuard(ctx, tr.GuardProgram, wc, ref, s)
			if err != nil {
				return err
			}
			if !ok {
				return kernel.NewError(kernel.CodePreconditionFailed, "guard of %s failed", tr.Action)
			}
		}
	}
	return nil
}

func evalGuard(ctx context.Context, p rt.Program, wc WriteContext, ref *accessapi.FeatureRef, s *Service) (bool, error) {
	vars := s.d.Authorizer.Bindings(ctx, wc.Principal)
	vars["principal"] = wc.Principal.ExprValue()
	vars["feature"] = ref.Data
	vars["input"] = map[string]any{}
	vars[rt.FnNow] = s.d.Clock.Now
	out, err := p.Eval(vars)
	if err != nil {
		return false, err
	}
	b, _ := out.(bool)
	return b, nil
}

// validateView validates the body (non-null values, wire form) against the
// caller's create or update view and merges conversion errors, so every
// problem is reported at once.
func (s *Service) validateView(ct *schemaapi.CompiledType, aud schemaapi.Audience, dir schemaapi.Direction, in input, create bool) error {
	view := ct.View(aud, dir)
	doc := map[string]any{}
	for path, v := range in.vals {
		if v == nil || !view.Writable[path] {
			continue // action or fixture writes are checked by storage validation
		}
		f, _ := ct.Field(path)
		doc[path[1:]] = f.Type.Wire(v)
	}
	for path, raw := range in.failed {
		if view.Writable[path] {
			doc[path[1:]] = raw
		}
	}
	var out []kernel.FieldError
	reported := map[string]bool{}
	for _, e := range view.Validator.Validate(doc) {
		if create && e.Code == "required" {
			// Fields with defaults satisfy "required".
			if f, ok := ct.Field(e.Path); ok && f.Default != nil {
				continue
			}
		}
		out = append(out, e)
		reported[e.Path] = true
	}
	for _, e := range in.errs {
		if !reported[e.Path] {
			out = append(out, e)
		}
	}
	if len(out) > 0 {
		return kernel.NewValidationError(out...)
	}
	return nil
}

// validateStorage validates the merged document against the storage view.
// Errors on fields the caller cannot read become invalid_state (spec 4.9).
func (s *Service) validateStorage(ct *schemaapi.CompiledType, aud schemaapi.Audience, fields map[string]kernel.Value) error {
	doc := domain.WireDocument(ct, fields)
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(b) > s.d.Limits.MaxDocumentBytes {
		return kernel.NewValidationError(kernel.FieldError{Path: "", Code: "maxBytes", Message: "document too large"})
	}
	errs := ct.Storage.Validator.Validate(doc)
	if len(errs) == 0 {
		return nil
	}
	readable := ct.View(aud, schemaapi.DirRead).Readable
	var out []kernel.FieldError
	hidden := false
	for _, e := range errs {
		top := "/" + strings.SplitN(strings.TrimPrefix(e.Path, "/"), "/", 2)[0]
		if readable[top] {
			out = append(out, e)
		} else {
			hidden = true
		}
	}
	if hidden {
		out = append(out, kernel.FieldError{Path: "", Code: kernel.CodeInvalidState, Message: "the feature is in an invalid state"})
	}
	return kernel.NewValidationError(out...)
}

// stamp maintains the Timestamped trait's fields.
func (s *Service) stamp(ct *schemaapi.CompiledType, fields map[string]kernel.Value, now timeT, create bool) {
	if !ct.HasTrait(schemaapi.TraitTimestamped) {
		return
	}
	if create {
		fields["/createdAt"] = kernel.NewTime(now)
	}
	fields["/updatedAt"] = kernel.NewTime(now)
}

// compute evaluates x-computed fields over the merged document (spec 4.4).
func (s *Service) compute(ct *schemaapi.CompiledType, fields map[string]kernel.Value, id, parent kernel.ID, version kernel.Version) error {
	if len(ct.Computed) == 0 {
		return nil
	}
	for _, c := range ct.Computed {
		snap := domain.Snapshot{ID: id, Fields: fields, Version: version}
		if !parent.IsZero() {
			snap.ParentID = &parent
		}
		doc := domain.ExprDocument(snap)
		vars := map[string]any{"feature": doc, rt.FnNow: s.d.Clock.Now}
		for _, f := range ct.TopFields() {
			vars[f.Path[1:]] = doc[f.Path[1:]]
		}
		out, err := c.Program.Eval(vars)
		if err != nil {
			return kernel.NewValidationError(kernel.FieldError{Path: c.Path, Code: "computed", Message: err.Error()})
		}
		if out == nil {
			delete(fields, c.Path)
			continue
		}
		v, err := c.Type.Coerce(c.Path, out)
		if err != nil {
			return err
		}
		fields[c.Path] = v
	}
	return nil
}

func uniqueKey(ct *schemaapi.CompiledType, fields map[string]kernel.Value) string {
	if ct.UniqueName == "" {
		return ""
	}
	v, ok := fields[ct.UniqueName]
	if !ok {
		return ""
	}
	s, _ := kernel.Native(v).(string)
	if s == "" {
		return ""
	}
	return string(ct.Base()) + "\x00" + s
}

func nativePatch(p map[string]kernel.Value) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k[1:]] = kernel.Native(v)
	}
	return out
}

// Document shape limits (spec 14.1).
const (
	MaxNestingDepth = 8
	MaxListItems    = 1000
)

// checkShape bounds object nesting and list lengths of an input document
// before coercion.
func checkShape(path string, v any, depth int) error {
	switch x := v.(type) {
	case map[string]any:
		if depth > MaxNestingDepth {
			return kernel.NewValidationError(kernel.FieldError{Path: path, Code: "maxDepth", Message: fmt.Sprintf("objects nest deeper than %d levels", MaxNestingDepth)})
		}
		for k, e := range x {
			if err := checkShape(path+"/"+k, e, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if len(x) > MaxListItems {
			return kernel.NewValidationError(kernel.FieldError{Path: path, Code: "maxItems", Message: fmt.Sprintf("lists hold at most %d items", MaxListItems)})
		}
		if depth > MaxNestingDepth {
			return kernel.NewValidationError(kernel.FieldError{Path: path, Code: "maxDepth", Message: fmt.Sprintf("values nest deeper than %d levels", MaxNestingDepth)})
		}
		for i, e := range x {
			if err := checkShape(fmt.Sprintf("%s/%d", path, i), e, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// pendingSequences records the x-sequence fields of ct to number when w
// commits: on create (action "") or when their action runs, once.
func (s *Service) pendingSequences(ct *schemaapi.CompiledType, w *work, action string, parent kernel.ID) error {
	for _, d := range ct.Sequences {
		if d.On != action {
			continue
		}
		if v, ok := w.feature.Field(d.Path); ok && v != nil {
			if _, isNull := v.(kernel.Null); !isNull {
				continue // numbered already
			}
		}
		series := d.SeriesConst
		if d.Series != nil {
			snap := domain.Snapshot{ID: w.id, Fields: w.feature.Fields(), Version: w.feature.Version()}
			if !parent.IsZero() {
				snap.ParentID = &parent
			}
			doc := domain.ExprDocument(snap)
			vars := map[string]any{"feature": doc, rt.FnNow: s.d.Clock.Now}
			for _, f := range ct.TopFields() {
				vars[f.Path[1:]] = doc[f.Path[1:]]
			}
			out, err := d.Series.Eval(vars)
			str, _ := out.(string)
			if err != nil || str == "" {
				return kernel.NewValidationError(kernel.FieldError{Path: d.Path, Code: "sequence",
					Message: "the sequence series must be a non-empty string"})
			}
			series = str
		}
		w.seqs = append(w.seqs, pendingSeq{def: d, series: series})
	}
	return nil
}
