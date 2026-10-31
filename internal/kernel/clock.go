package kernel

import (
	"sync"
	"time"
)

// Version is a feature's optimistic concurrency version; it increases by
// exactly one per committed change (spec 3.1, 3.5).
type Version int64

// Clock is the source of time for all contexts (spec 15.6).
type Clock interface {
	Now() time.Time
}

// SystemClock reads the system time in UTC.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FakeClock is a settable clock for tests.
type FakeClock struct {
	mu sync.Mutex
	t  time.Time
}

// NewFakeClock returns a FakeClock set to t.
func NewFakeClock(t time.Time) *FakeClock { return &FakeClock{t: t.UTC()} }

// Now returns the clock's current time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// Advance moves the clock forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// Set sets the clock to t.
func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t.UTC()
	c.mu.Unlock()
}
