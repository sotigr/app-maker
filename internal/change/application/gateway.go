// Package application holds the Change context's use cases: publishing
// events to the change log (spec 9.2) and the streaming gateway (9.4–9.11).
package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	accessapi "app-maker/internal/access/api"
	changeapi "app-maker/internal/change/api"
	"app-maker/internal/change/domain"
	contentapi "app-maker/internal/content/api"
	"app-maker/internal/kernel"
	queryapi "app-maker/internal/query/api"
	schemaapi "app-maker/internal/schema/api"
)

// Limits of the gateway (spec 9.8, 14.1).
const (
	QueueSize        = 256
	MaxSubsPerConn   = 200
	MaxConnsPerUser  = 20 // streaming connections per principal (spec 14.1)
	MaxChildren      = 1000
	EphemeralPerSec  = 30
	HeartbeatSeconds = 25
)

// Message is one protocol message (spec 9.10): t, sid, sseq, cursor, ...
type Message map[string]any

// Target is a subscription request.
type Target struct {
	SID    string             `json:"sid"`
	Target string             `json:"target"` // feature, children, query
	ID     string             `json:"id,omitempty"`
	Parent string             `json:"parent,omitempty"`
	Type   string             `json:"type,omitempty"`
	Query  *queryapi.Document `json:"query,omitempty"`
	Paths  []string           `json:"paths,omitempty"`
	From   string             `json:"from,omitempty"`
}

// Gateway matches change log events to subscriptions and delivers
// per-subscriber projections (spec 9).
type Gateway struct {
	log     domain.ChangeLog
	reader  contentapi.StreamReader
	authz   accessapi.Authorizer
	types   schemaapi.TypeRegistry
	planner queryapi.Planner
	logger  *slog.Logger

	mu    sync.Mutex
	conns map[*Conn]struct{}
	// last values of ephemeral fields for new subscribers (spec 9.7)
	lastValues map[kernel.ID]map[string]any
	ephemeral  map[kernel.ID][]time.Time
}

// NewGateway returns a gateway; call Run to start dispatching.
func NewGateway(log domain.ChangeLog, reader contentapi.StreamReader, authz accessapi.Authorizer, types schemaapi.TypeRegistry,
	planner queryapi.Planner, logger *slog.Logger) *Gateway {
	if logger == nil {
		logger = slog.Default()
	}
	return &Gateway{log: log, reader: reader, authz: authz, types: types, planner: planner, logger: logger,
		conns: map[*Conn]struct{}{}, lastValues: map[kernel.ID]map[string]any{}, ephemeral: map[kernel.ID][]time.Time{}}
}

// Run dispatches live events until ctx ends.
func (g *Gateway) Run(ctx context.Context) error {
	head, err := g.log.Head(ctx)
	if err != nil {
		return err
	}
	stream, err := g.log.Subscribe(ctx, head)
	if err != nil {
		return err
	}
	defer stream.Close()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-stream.C():
			if !ok {
				return nil
			}
			g.dispatch(ctx, ev)
		}
	}
}

func (g *Gateway) dispatch(ctx context.Context, ev changeapi.Event) {
	if ev.Kind == changeapi.EventEphemeral {
		g.mu.Lock()
		lv := g.lastValues[ev.FeatureID]
		if lv == nil {
			lv = map[string]any{}
			g.lastValues[ev.FeatureID] = lv
		}
		for k, v := range ev.After {
			lv[k] = v
		}
		g.mu.Unlock()
	}
	g.mu.Lock()
	conns := make([]*Conn, 0, len(g.conns))
	for c := range g.conns {
		conns = append(conns, c)
	}
	g.mu.Unlock()
	accessChange := isAccessEvent(ev)
	for _, c := range conns {
		c.onEvent(ctx, ev)
		if accessChange {
			c.recheck(ctx)
		}
	}
}

// isAccessEvent: grants, memberships, and principal changes may change
// who reads what (spec 9.9).
func isAccessEvent(ev changeapi.Event) bool {
	b := ev.TypeID.Base()
	return b == schemaapi.TypeGrant || b == schemaapi.TypeTeam || b == schemaapi.TypeUser || b == schemaapi.TypeServiceAccount
}

// Connect opens a connection for a principal. Messages arrive on Out.
func (g *Gateway) Connect(ctx context.Context, p accessapi.Principal) (*Conn, error) {
	g.mu.Lock()
	if !p.ID.IsZero() {
		n := 0
		for c := range g.conns {
			if c.principal.ID == p.ID {
				n++
			}
		}
		if n >= MaxConnsPerUser {
			g.mu.Unlock()
			return nil, kernel.NewError(kernel.CodeRateLimited, "at most %d streaming connections per principal", MaxConnsPerUser)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &Conn{g: g, ctx: ctx, cancel: cancel, principal: p, out: make(chan Message, QueueSize), subs: map[string]*Sub{}}
	g.conns[c] = struct{}{}
	g.mu.Unlock()
	go func() {
		<-ctx.Done()
		g.mu.Lock()
		delete(g.conns, c)
		g.mu.Unlock()
	}()
	go c.flushLoop()
	return c, nil
}

// Conn is one client connection with its subscriptions.
type Conn struct {
	g         *Gateway
	ctx       context.Context
	cancel    context.CancelFunc
	principal accessapi.Principal
	out       chan Message
	mu        sync.Mutex
	subs      map[string]*Sub
}

// Out returns the delivery channel; it is never closed while subscribed.
func (c *Conn) Out() <-chan Message { return c.out }

// Done is closed when the connection ends.
func (c *Conn) Done() <-chan struct{} { return c.ctx.Done() }

// Close ends the connection and its subscriptions (spec 9.12).
func (c *Conn) Close() { c.cancel() }

// Sub is one subscription.
type Sub struct {
	t        Target
	typeCT   *schemaapi.CompiledType
	pred     queryapi.Predicate
	sseq     int64
	versions map[kernel.ID]kernel.Version
	members  map[kernel.ID]bool
	paths    map[string]bool // requested top-level paths; nil = all
	buffer   []changeapi.Event
	ready    bool
	state    bool // state mode: coalesce when slow (spec 9.7)
	minGap   time.Duration
	lastSend map[kernel.ID]time.Time
	pending  map[kernel.ID]Message
}

func (c *Conn) send(m Message) bool {
	select {
	case c.out <- m:
		return true
	default:
		return false
	}
}

// offer delivers a delta or applies backpressure (spec 9.8): state-mode
// values are coalesced; append-mode overflow resets the subscription.
func (c *Conn) offer(s *Sub, id kernel.ID, m Message) {
	if s.state && s.minGap > 0 && m["t"] == "delta" && time.Since(s.lastSend[id]) < s.minGap {
		c.coalesce(s, id, m)
		return
	}
	s.sseq++
	m["sseq"] = s.sseq
	if c.send(m) {
		s.lastSend[id] = time.Now()
		return
	}
	s.sseq--
	if s.state {
		c.coalesce(s, id, m)
		return
	}
	delete(c.subs, s.t.SID)
	c.send(Message{"t": "reset", "sid": s.t.SID, "reason": "overflow", "cursor": m["cursor"]})
}

func (c *Conn) coalesce(s *Sub, id kernel.ID, m Message) {
	if p, ok := s.pending[id]; ok {
		pset, pok := p["set"].(map[string]any)
		mset, mok := m["set"].(map[string]any)
		if pok && mok {
			for k, v := range mset {
				pset[k] = v
			}
			p["version"], p["cursor"] = m["version"], m["cursor"]
			return
		}
	}
	s.pending[id] = m // a leave (or a delta after a leave) replaces what was pending
}

// flushLoop sends coalesced state values when the client keeps up.
func (c *Conn) flushLoop() {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			c.mu.Lock()
			for _, s := range c.subs {
				for id, m := range s.pending {
					if s.minGap > 0 && time.Since(s.lastSend[id]) < s.minGap {
						continue
					}
					s.sseq++
					m["sseq"] = s.sseq
					if !c.send(m) {
						s.sseq--
						break
					}
					s.lastSend[id] = time.Now()
					delete(s.pending, id)
				}
			}
			c.mu.Unlock()
		}
	}
}

// Subscribe creates a subscription: authorized with the read policy, with
// a gap-free snapshot followed by deltas (spec 9.4, 9.5).
func (c *Conn) Subscribe(ctx context.Context, t Target) error {
	c.mu.Lock()
	if len(c.subs) >= MaxSubsPerConn {
		c.mu.Unlock()
		return kernel.NewError(kernel.CodeRateLimited, "too many subscriptions on this connection")
	}
	if _, dup := c.subs[t.SID]; dup || t.SID == "" {
		c.mu.Unlock()
		return kernel.NewError(kernel.CodeBadRequest, "subscription id missing or in use")
	}
	s := &Sub{t: t, versions: map[kernel.ID]kernel.Version{}, members: map[kernel.ID]bool{}, state: true,
		lastSend: map[kernel.ID]time.Time{}, pending: map[kernel.ID]Message{}}
	if len(t.Paths) > 0 {
		s.paths = map[string]bool{}
		for _, p := range t.Paths {
			s.paths["/"+strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)[0]] = true
		}
	}
	// 1. Register first so events are buffered while the snapshot is read.
	c.subs[t.SID] = s
	c.mu.Unlock()
	views, err := c.snapshot(ctx, s)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		delete(c.subs, t.SID)
		return err
	}
	if s.typeCT != nil {
		c.applyStreamSpec(s)
	}
	head, _ := c.g.log.Head(ctx)
	if t.From != "" {
		// Resume (spec 9.11): an expired or invalid cursor resets; either
		// way the client gets a fresh snapshot and treats both paths alike.
		from, perr := domain.ParseCursor(t.From)
		oldest, _ := c.g.log.Oldest(ctx)
		if perr != nil || from < oldest {
			c.out <- Message{"t": "reset", "sid": t.SID, "reason": "cursor_expired"}
		}
	}
	feats := make([]any, 0, len(views))
	for _, v := range views {
		id := kernel.MustParseID(v.ID)
		s.versions[id] = v.Version
		s.members[id] = true
		data := filter(v.Data, s.paths)
		c.g.mu.Lock()
		for k, lv := range c.g.lastValues[id] {
			if s.paths == nil || s.paths["/"+k] {
				data[k] = lv
			}
		}
		c.g.mu.Unlock()
		feats = append(feats, Message{"id": v.ID, "type": v.Type, "parent": v.Parent, "version": v.Version, "data": data})
	}
	s.sseq++
	c.out <- Message{"t": "snapshot", "sid": t.SID, "sseq": s.sseq, "cursor": head.String(), "features": feats}
	// 3. Deliver buffered events newer than the snapshot.
	s.ready = true
	buffered := s.buffer
	s.buffer = nil
	for _, ev := range buffered {
		c.deliver(ctx, s, ev)
	}
	return nil
}

// applyStreamSpec sets the delivery mode from the requested fields' x-stream.
func (c *Conn) applyStreamSpec(s *Sub) {
	for _, f := range s.typeCT.TopFields() {
		if f.Stream == nil || (s.paths != nil && !s.paths[f.Path]) {
			continue
		}
		if f.Stream.Mode == schemaapi.StreamAppend {
			s.state = false
		}
		if f.Stream.MaxRate > 0 {
			gap := time.Duration(float64(time.Second) / f.Stream.MaxRate)
			if gap > s.minGap {
				s.minGap = gap
			}
		}
	}
}

func filter(data map[string]any, paths map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range data {
		if paths == nil || paths["/"+k] {
			out[k] = v
		}
	}
	return out
}

// snapshot reads the subscription's current features as the principal.
func (c *Conn) snapshot(ctx context.Context, s *Sub) ([]contentapi.FeatureView, error) {
	p := c.principal
	switch s.t.Target {
	case "feature":
		id, err := kernel.ParseID(s.t.ID)
		if err != nil {
			return nil, kernel.ErrNotFound
		}
		v, err := c.g.reader.Get(ctx, p, id)
		if err != nil {
			return nil, err
		}
		s.typeCT, _ = c.g.types.ByName(v.Type)
		return []contentapi.FeatureView{v}, nil
	case "children":
		id, err := kernel.ParseID(s.t.Parent)
		if err != nil {
			return nil, kernel.ErrNotFound
		}
		if s.t.Type != "" {
			ct, ok := c.g.types.ByName(s.t.Type)
			if !ok {
				return nil, kernel.NewError(kernel.CodeBadRequest, "unknown type %q", s.t.Type)
			}
			s.typeCT = ct
		}
		if _, err := c.g.reader.Get(ctx, p, id); err != nil {
			return nil, err
		}
		return c.g.reader.Children(ctx, p, id, s.t.Type, MaxChildren)
	case "query":
		return c.querySnapshot(ctx, s)
	}
	return nil, kernel.NewError(kernel.CodeBadRequest, "target must be feature, children, or query")
}

// querySnapshot: live queries are restricted to one type, a scope, and
// eq/in predicates on indexed fields, with no sort windows (spec 9.4).
func (c *Conn) querySnapshot(ctx context.Context, s *Sub) ([]contentapi.FeatureView, error) {
	q := s.t.Query
	if q == nil {
		return nil, kernel.NewError(kernel.CodeInvalidQuery, "query target needs a query")
	}
	if len(q.Sort) > 0 || q.Cursor != "" {
		return nil, kernel.NewError(kernel.CodeInvalidQuery, "live queries cannot sort or page")
	}
	ct, ok := c.g.types.ByName(q.Type)
	if !ok {
		return nil, kernel.NewError(kernel.CodeInvalidQuery, "live queries need one type")
	}
	if !liveSafe(q.Where) {
		return nil, kernel.NewError(kernel.CodeInvalidQuery, "live queries allow only eq and in predicates combined with and")
	}
	s.typeCT = ct
	raw, _ := json.Marshal(q)
	plan, err := c.g.planner.Plan(ctx, queryapi.PlanRequest{Doc: q, Types: []*schemaapi.CompiledType{ct}, Audience: queryAudience(c.principal),
		Scope: queryapi.True{}, ScopeExact: true, Caps: queryapi.RepoCaps{RangeFilters: true, Sort: true, SubtreeQueries: true}})
	if err != nil {
		return nil, err
	}
	s.pred = plan.Where
	if q.Scope.ChildrenOf != "" || q.Scope.DescendantsOf != "" {
		var terms []queryapi.Predicate
		if s.pred != nil {
			terms = append(terms, s.pred)
		}
		if q.Scope.ChildrenOf != "" {
			s.t.Parent = q.Scope.ChildrenOf
		}
		if q.Scope.DescendantsOf != "" {
			id, _ := kernel.ParseID(q.Scope.DescendantsOf)
			terms = append(terms, queryapi.InScope{Field: queryapi.ScopeAncestor, IDs: []kernel.ID{id}})
		}
		s.pred = queryapi.And{Terms: terms}
	}
	return c.g.reader.Query(ctx, c.principal, raw)
}

func liveSafe(n *queryapi.Node) bool {
	if n == nil {
		return true
	}
	if n.And != nil {
		for _, t := range n.And {
			if !liveSafe(t) {
				return false
			}
		}
		return true
	}
	return n.Or == nil && n.Not == nil && (n.Op == queryapi.OpEq || n.Op == queryapi.OpIn)
}

func queryAudience(p accessapi.Principal) schemaapi.Audience {
	switch {
	case p.IsSystem():
		return schemaapi.AudSystem
	case p.HasRole("admin"):
		return schemaapi.AudAdmin
	}
	return schemaapi.AudPublic
}

// Unsubscribe ends one subscription.
func (c *Conn) Unsubscribe(sid string) {
	c.mu.Lock()
	delete(c.subs, sid)
	c.mu.Unlock()
}

func (c *Conn) onEvent(ctx context.Context, ev changeapi.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ev.Kind == changeapi.EventUpdated && ev.FeatureID == c.principal.ID {
		if st, _ := ev.After["status"].(string); st != "" && st != "active" {
			c.cancel() // suspension closes all connections (spec 8.8, 9.9)
			return
		}
	}
	for _, s := range c.subs {
		if !s.ready {
			s.buffer = append(s.buffer, ev)
			continue
		}
		c.deliver(ctx, s, ev)
	}
}

// relevant decides whether an event concerns a subscription and whether
// the feature is (still) a member.
func (s *Sub) relevant(ev changeapi.Event, doc queryapi.Doc) (concerns, member bool) {
	gone := ev.Kind == changeapi.EventDeleted || ev.Kind == changeapi.EventPurged
	switch s.t.Target {
	case "feature":
		return ev.FeatureID.String() == s.t.ID, !gone
	case "children":
		inParent := ev.ParentID != nil && ev.ParentID.String() == s.t.Parent
		typeOK := s.typeCT == nil || ev.TypeID.Base() == s.typeCT.Base()
		return (inParent && typeOK) || s.members[ev.FeatureID], inParent && typeOK && !gone
	case "query":
		if s.typeCT == nil || ev.TypeID.Base() != s.typeCT.Base() {
			return false, false
		}
		matches := !gone && queryapi.Match(s.pred, doc)
		if s.t.Parent != "" {
			matches = matches && ev.ParentID != nil && ev.ParentID.String() == s.t.Parent
		}
		return matches || s.members[ev.FeatureID], matches
	}
	return false, false
}

// deliver projects one event for one subscription (spec 9.6).
func (c *Conn) deliver(ctx context.Context, s *Sub, ev changeapi.Event) {
	ct, ok := c.g.types.Get(ev.TypeID)
	if !ok {
		ct, ok = c.g.types.Get(ev.TypeID.Base())
	}
	if !ok {
		return
	}
	doc := eventDoc{ev: ev, ct: ct}
	concerns, member := s.relevant(ev, doc)
	if !concerns {
		return
	}
	id := ev.FeatureID
	if ev.Kind != changeapi.EventEphemeral && ev.Version <= s.versions[id] && s.members[id] == member {
		return // version-aware consumer: duplicates and stale events dropped (spec 9.2)
	}
	if !member {
		if s.members[id] {
			delete(s.members, id)
			delete(s.versions, id)
			c.offer(s, id, Message{"t": "leave", "sid": s.t.SID, "cursor": ev.Seq, "id": id.String()})
		}
		return
	}
	ref := eventRef(ev, ct)
	if d, err := c.g.authz.Check(ctx, c.principal, accessapi.CheckRequest{Op: accessapi.OpRead, Type: ct, Feature: ref}); err != nil || !d.Allowed {
		if s.members[id] {
			delete(s.members, id)
			c.offer(s, id, Message{"t": "leave", "sid": s.t.SID, "cursor": ev.Seq, "id": id.String()})
		}
		return
	}
	aud, err := c.g.authz.Audience(ctx, c.principal, ref)
	if err != nil {
		return
	}
	readable := ct.View(aud, schemaapi.DirRead).Readable
	entering := !s.members[id]
	skipped := ev.Kind != changeapi.EventEphemeral && ev.Version > s.versions[id]+1
	var paths []string
	if entering || skipped || ev.Kind == changeapi.EventCreated || ev.Kind == changeapi.EventMoved {
		for k := range ev.After {
			paths = append(paths, "/"+k)
		}
	} else {
		paths = ev.Changed
	}
	set := map[string]any{}
	var unset []string
	for _, p := range paths {
		top := "/" + strings.SplitN(strings.TrimPrefix(p, "/"), "/", 2)[0]
		if !readable[top] || (s.paths != nil && !s.paths[top]) {
			continue
		}
		if v, ok := ev.After[top[1:]]; ok {
			set[top] = v
		} else {
			unset = append(unset, top)
		}
	}
	if ev.Kind != changeapi.EventEphemeral {
		s.versions[id] = ev.Version
	}
	s.members[id] = true
	if len(set) == 0 && len(unset) == 0 && !entering {
		return // nothing this subscriber may see changed (spec 9.6)
	}
	sort.Strings(unset)
	m := Message{"t": "delta", "sid": s.t.SID, "cursor": ev.Seq, "id": id.String(), "version": ev.Version, "set": set, "unset": orEmpty(unset)}
	if entering {
		m["enter"] = true
		m["type"] = ct.Name
		if ev.ParentID != nil {
			m["parent"] = ev.ParentID.String()
		}
	}
	if ev.Kind == changeapi.EventEphemeral {
		m["ephemeral"] = true
	}
	c.offer(s, id, m)
	if !ev.At.IsZero() {
		// Commit-to-delivery latency (spec 14.3).
		kernel.Tel.Record(ctx, "stream.delivery_latency_s", time.Since(ev.At).Seconds(), kernel.A("target", s.t.Target))
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// recheck re-evaluates read access after grant or membership changes
// (spec 9.9): lost access closes the subscription with forbidden.
func (c *Conn) recheck(ctx context.Context) {
	c.mu.Lock()
	subs := make([]*Sub, 0, len(c.subs))
	for _, s := range c.subs {
		subs = append(subs, s)
	}
	c.mu.Unlock()
	for _, s := range subs {
		var err error
		switch s.t.Target {
		case "feature":
			_, err = c.g.reader.Get(ctx, c.principal, kernel.MustParseID(s.t.ID))
		case "children":
			_, err = c.g.reader.Get(ctx, c.principal, kernel.MustParseID(s.t.Parent))
		}
		if err != nil && errors.Is(err, kernel.ErrNotFound) {
			c.mu.Lock()
			delete(c.subs, s.t.SID)
			c.mu.Unlock()
			c.send(Message{"t": "error", "sid": s.t.SID, "code": kernel.CodeForbidden})
		}
	}
}

// eventDoc adapts an event's After document to the query matcher.
type eventDoc struct {
	ev changeapi.Event
	ct *schemaapi.CompiledType
}

func (d eventDoc) FeatureID() kernel.ID     { return d.ev.FeatureID }
func (d eventDoc) Owner() *kernel.ID        { return d.ev.OwnerID }
func (d eventDoc) AncestorIDs() []kernel.ID { return d.ev.Ancestors }
func (d eventDoc) Value(path string) (kernel.Value, bool) {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	raw, ok := d.ev.After[segs[0]]
	if !ok {
		return nil, false
	}
	f, ok := d.ct.Field("/" + segs[0])
	if !ok {
		return nil, false
	}
	v, err := f.Type.Coerce("/"+segs[0], raw)
	if err != nil {
		return nil, false
	}
	for _, s := range segs[1:] {
		o, isObj := v.(kernel.Object)
		if !isObj {
			return nil, false
		}
		if v, ok = o.Get(s); !ok {
			return nil, false
		}
	}
	return v, true
}

// eventRef builds the Authorizer's view of the event's feature.
func eventRef(ev changeapi.Event, ct *schemaapi.CompiledType) *accessapi.FeatureRef {
	data := map[string]any{}
	for k, raw := range ev.After {
		if f, ok := ct.Field("/" + k); ok {
			if v, err := f.Type.Coerce("/"+k, raw); err == nil {
				data[k] = kernel.Native(v)
				continue
			}
		}
		data[k] = raw
	}
	data["id"] = ev.FeatureID.String()
	data["version"] = int64(ev.Version)
	data["parentId"] = ""
	if ev.ParentID != nil {
		data["parentId"] = ev.ParentID.String()
	}
	anc := make([]string, len(ev.Ancestors))
	for i, a := range ev.Ancestors {
		anc[i] = a.String()
	}
	data[accessapi.AncestorsKey] = anc
	return &accessapi.FeatureRef{ID: ev.FeatureID, Type: ct, ParentID: ev.ParentID, OwnerID: ev.OwnerID, Ancestors: ev.Ancestors, Data: data}
}

// Ephemeral publishes values of x-persist: false fields straight to the
// change log, skipping the repository (spec 9.7). It is authorized with
// the type's update policy and validated against the field schemas.
func (g *Gateway) Ephemeral(ctx context.Context, p accessapi.Principal, typeName string, id kernel.ID, values map[string]any) error {
	ct, ok := g.types.ByName(typeName)
	if !ok {
		return kernel.ErrNotFound
	}
	v, err := g.reader.Get(ctx, p, id)
	if err != nil {
		return err
	}
	if v.Type != ct.Name {
		return kernel.ErrNotFound
	}
	g.mu.Lock()
	now := time.Now()
	recent := slices.DeleteFunc(g.ephemeral[id], func(t time.Time) bool { return now.Sub(t) > time.Second })
	if len(recent) >= EphemeralPerSec {
		g.mu.Unlock()
		return kernel.NewError(kernel.CodeRateLimited, "too many ephemeral writes for this feature")
	}
	g.ephemeral[id] = append(recent, now)
	g.mu.Unlock()
	after := map[string]any{}
	var changed []string
	var errs []kernel.FieldError
	for k, raw := range values {
		path := "/" + strings.TrimPrefix(k, "/")
		f, ok := ct.Field(path)
		if !ok || !f.Top || f.Persist {
			errs = append(errs, kernel.FieldError{Path: path, Code: "not_ephemeral", Message: "only x-persist: false fields can be written here"})
			continue
		}
		val, err := f.Type.Coerce(path, raw)
		if err != nil {
			errs = append(errs, kernel.FieldError{Path: path, Code: "type", Message: err.Error()})
			continue
		}
		after[path[1:]] = f.Type.Wire(val)
		changed = append(changed, path)
	}
	if len(errs) > 0 {
		return kernel.NewValidationError(errs...)
	}
	parent := (*kernel.ID)(nil)
	if v.Parent != nil {
		pid := kernel.MustParseID(*v.Parent)
		parent = &pid
	}
	ev := changeapi.Event{Kind: changeapi.EventEphemeral, FeatureID: id, TypeID: ct.ID, ParentID: parent, Version: v.Version,
		Changed: changed, After: after, Actor: p.ID, At: time.Now().UTC()}
	ref := eventRef(changeapi.Event{FeatureID: id, TypeID: ct.ID, ParentID: parent, After: v.Data, Version: v.Version}, ct)
	d, err := g.authz.Check(ctx, p, accessapi.CheckRequest{Op: accessapi.OpUpdate, Type: ct, Feature: ref})
	if err != nil {
		return err
	}
	if !d.Allowed {
		return kernel.ErrForbidden
	}
	return g.log.Publish(ctx, []changeapi.Event{ev})
}

// Publisher is the publish_events job worker (spec 9.2).
type Publisher struct{ Log domain.ChangeLog }

// Handle appends a batch's events to the change log.
func (p Publisher) Handle(ctx context.Context, raw json.RawMessage) error {
	var a changeapi.PublishEventsArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("publish_events args: %w", err)
	}
	return p.Log.Publish(ctx, a.Events)
}
