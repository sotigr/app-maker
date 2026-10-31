// Package memory is the in-memory change log (development and tests; it
// is refused in the replicated topology, spec 9.3).
package memory

import (
	"context"
	"strconv"
	"sync"
	"time"

	changeapi "app-maker/internal/change/api"
	"app-maker/internal/change/domain"
)

// Log keeps events for the retention period and fans them out.
type Log struct {
	mu        sync.Mutex
	events    []changeapi.Event // ordered by seq
	first     domain.Cursor     // seq of events[0]
	next      domain.Cursor
	retention time.Duration
	maxEvents int
	subs      map[*sub]struct{}
}

// New returns a log keeping events for retention (default 24 h, spec 9.3)
// and at most maxEvents.
func New(retention time.Duration, maxEvents int) *Log {
	if retention == 0 {
		retention = 24 * time.Hour
	}
	if maxEvents == 0 {
		maxEvents = 100_000
	}
	return &Log{retention: retention, maxEvents: maxEvents, first: 1, next: 1, subs: map[*sub]struct{}{}}
}

var _ domain.ChangeLog = (*Log)(nil)

type sub struct {
	ch     chan changeapi.Event
	once   sync.Once
	log    *Log
	closed chan struct{}
}

func (s *sub) C() <-chan changeapi.Event { return s.ch }

func (s *sub) Close() {
	s.once.Do(func() {
		s.log.mu.Lock()
		delete(s.log.subs, s)
		s.log.mu.Unlock()
		close(s.closed)
	})
}

// Publish implements domain.ChangeLog: it assigns sequence numbers.
func (l *Log) Publish(_ context.Context, events []changeapi.Event) error {
	l.mu.Lock()
	now := time.Now()
	out := make([]changeapi.Event, len(events))
	for i, e := range events {
		e.Seq = domain.Cursor(l.next).String()
		l.next++
		out[i] = e
		l.events = append(l.events, e)
	}
	l.trim(now)
	subs := make([]*sub, 0, len(l.subs))
	for s := range l.subs {
		subs = append(subs, s)
	}
	l.mu.Unlock()
	for _, s := range subs {
		for _, e := range out {
			select {
			case s.ch <- e:
			case <-s.closed:
			case <-time.After(5 * time.Second):
				// A stuck consumer must not stall publishing; it will
				// detect the gap by version and refetch.
			}
		}
	}
	return nil
}

func (l *Log) trim(now time.Time) {
	cut := 0
	for cut < len(l.events) && (len(l.events)-cut > l.maxEvents || now.Sub(l.events[cut].At) > l.retention) {
		cut++
	}
	if cut > 0 {
		l.events = append([]changeapi.Event(nil), l.events[cut:]...)
		l.first += domain.Cursor(cut)
	}
}

// Subscribe implements domain.ChangeLog.
func (l *Log) Subscribe(ctx context.Context, from domain.Cursor) (domain.Stream, error) {
	l.mu.Lock()
	if from+1 < l.first && from != 0 {
		l.mu.Unlock()
		return nil, domain.ErrCursorExpired
	}
	var backlog []changeapi.Event
	for _, e := range l.events {
		if seqOf(e) > from {
			backlog = append(backlog, e)
		}
	}
	s := &sub{ch: make(chan changeapi.Event, 1024), log: l, closed: make(chan struct{})}
	l.subs[s] = struct{}{}
	l.mu.Unlock()
	if len(backlog) > 0 {
		go func() {
			for _, e := range backlog {
				select {
				case s.ch <- e:
				case <-s.closed:
					return
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		select {
		case <-ctx.Done():
			s.Close()
		case <-s.closed:
		}
	}()
	return s, nil
}

func seqOf(e changeapi.Event) domain.Cursor {
	c, _ := domain.ParseCursor(e.Seq)
	return c
}

// Oldest implements domain.ChangeLog.
func (l *Log) Oldest(context.Context) (domain.Cursor, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.first - 1, nil
}

// Head implements domain.ChangeLog.
func (l *Log) Head(context.Context) (domain.Cursor, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.next - 1, nil
}

// String is for debugging.
func (l *Log) String() string {
	return "memory change log @" + strconv.FormatUint(uint64(l.next-1), 10)
}
