package statemachine

import (
	"sync"
	"time"
)

// Clock is the engine's tick source.  Production code uses RealClock; tests
// drive the engine deterministically with ManualClock.
type Clock interface {
	// Ticks delivers one value per engine tick.
	Ticks() <-chan time.Time
	// TickDone is called by the engine after all work for a tick completes.
	TickDone()
	// Stop releases the clock's resources and unblocks anything waiting on it.
	Stop()
}

// ── Real clock ────────────────────────────────────────────────────────────────

// RealClock ticks off a time.Ticker at the configured rate.
type RealClock struct {
	ticker *time.Ticker
	once   sync.Once
}

// NewRealClock creates a clock ticking at rateHz (values <= 0 default to 100 Hz).
func NewRealClock(rateHz int) *RealClock {
	if rateHz <= 0 {
		rateHz = defaultTickHz
	}
	return &RealClock{ticker: time.NewTicker(time.Second / time.Duration(rateHz))}
}

// Ticks implements Clock.
func (c *RealClock) Ticks() <-chan time.Time { return c.ticker.C }

// TickDone implements Clock.
func (c *RealClock) TickDone() {}

// Stop implements Clock.
func (c *RealClock) Stop() { c.once.Do(c.ticker.Stop) }

// ── Manual clock ──────────────────────────────────────────────────────────────

// ManualClock advances the engine one tick at a time.  Tick blocks until the
// engine has finished processing that tick, so a test can assert on engine
// state immediately afterwards without sleeping.
type ManualClock struct {
	ticks chan time.Time
	ack   chan struct{}
	stop  chan struct{}
	once  sync.Once
	now   time.Time
	mu    sync.Mutex
}

// NewManualClock creates a manually driven clock.
func NewManualClock() *ManualClock {
	return &ManualClock{
		ticks: make(chan time.Time),
		ack:   make(chan struct{}),
		stop:  make(chan struct{}),
		now:   time.Unix(0, 0),
	}
}

// Ticks implements Clock.
func (c *ManualClock) Ticks() <-chan time.Time { return c.ticks }

// TickDone implements Clock.
func (c *ManualClock) TickDone() {
	select {
	case c.ack <- struct{}{}:
	case <-c.stop:
	}
}

// Stop implements Clock.
func (c *ManualClock) Stop() { c.once.Do(func() { close(c.stop) }) }

// Tick advances the engine by exactly one tick and returns once the engine has
// completed that tick's work.  It returns immediately if the clock is stopped.
func (c *ManualClock) Tick() {
	c.mu.Lock()
	c.now = c.now.Add(time.Millisecond)
	now := c.now
	c.mu.Unlock()

	select {
	case c.ticks <- now:
	case <-c.stop:
		return
	}
	select {
	case <-c.ack:
	case <-c.stop:
	}
}

// TickN advances the engine by n ticks.
func (c *ManualClock) TickN(n int) {
	for i := 0; i < n; i++ {
		c.Tick()
	}
}
