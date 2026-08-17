// Package daqsim is a non-LabVIEW DAQ node: it speaks the control node's
// daqNode WebSocket protocol (docs/websocket-protocol.md, Part 2) well enough
// to develop, rehearse and CI-test the controlNode <-> daqNode lifecycle
// without hardware or the LabVIEW application.
//
// The control node DIALS each daqNode, so the simulator is the WebSocket
// SERVER: it accepts one connection at a time, answers the config_req
// handshake, streams data, applies commands to an in-memory channel model
// driven entirely by the config it receives (no hardcoded channel names), and
// runs `state_update` entry/exit sequences and abort_rule monitoring locally,
// exactly as the protocol describes the real node doing.
package daqsim

import (
	"sync"
	"time"
)

// Clock is the simulator's time source for sequence timing: the spacing
// between entry_sequence / exit_sequence steps and the granularity of
// abort_rule scanning. Production (the standalone binary) uses RealClock so a
// burn takes as long as it really would; tests use FakeClock so a multi-second
// burn runs instantly while still recording the correct relative timing in the
// applied-set-point log (t_ms comes from the payload, not from wall time).
type Clock interface {
	// Now returns the clock's current time.
	Now() time.Time
	// Sleep blocks the calling goroutine for d (RealClock) or simply advances
	// the virtual clock and returns immediately (FakeClock).
	Sleep(d time.Duration)
}

// RealClock is Clock backed by the wall clock. The zero value is ready to use.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep implements Clock.
func (RealClock) Sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// FakeClock is Clock for tests: Sleep never blocks the caller, it only
// advances the clock's own notion of "now". This lets a test drive a
// multi-second sequence (e.g. the shipped daq001 3 s burn) to completion in
// microseconds while every applied set-point still carries its real relative
// t_ms, because that value comes from the state_update payload, not from how
// long Sleep actually took.
//
// Safe for concurrent use, though in practice the simulator only ever runs one
// entry/exit sequence at a time.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock creates a FakeClock starting at the Unix epoch.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: time.Unix(0, 0)}
}

// Now implements Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep implements Clock: it advances the virtual clock by d and returns
// immediately without blocking.
func (c *FakeClock) Sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
