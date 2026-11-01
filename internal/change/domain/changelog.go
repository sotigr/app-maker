// Package domain is the Change context's domain: the change log port,
// cursors, subscription targets, and the delivery rules of spec 9.
package domain

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	changeapi "organon/internal/change/api"
)

// Cursor is a change log position ("c:<seq>").
type Cursor uint64

// String formats the cursor.
func (c Cursor) String() string { return "c:" + strconv.FormatUint(uint64(c), 10) }

// ParseCursor parses "c:<seq>".
func ParseCursor(s string) (Cursor, error) {
	n, err := strconv.ParseUint(strings.TrimPrefix(s, "c:"), 10, 64)
	if err != nil || !strings.HasPrefix(s, "c:") {
		return 0, fmt.Errorf("invalid cursor %q", s)
	}
	return Cursor(n), nil
}

// Stream delivers events in log order.
type Stream interface {
	C() <-chan changeapi.Event
	Close()
}

// ChangeLog is the retained stream of events used for real-time fan-out
// (spec 9.3). Delivery is at least once and not ordered per feature;
// consumers are version-aware.
type ChangeLog interface {
	Publish(ctx context.Context, events []changeapi.Event) error
	// Subscribe delivers every event after the cursor, then live events.
	// ErrCursorExpired reports a cursor older than the retention.
	Subscribe(ctx context.Context, from Cursor) (Stream, error)
	// Oldest returns the earliest cursor still retained.
	Oldest(ctx context.Context) (Cursor, error)
	// Head returns the cursor of the latest event.
	Head(ctx context.Context) (Cursor, error)
}

// ErrCursorExpired means the client must reset and refetch (spec 9.11).
var ErrCursorExpired = fmt.Errorf("cursor expired")
