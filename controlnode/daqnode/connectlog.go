package daqnode

import (
	"log"
	"sort"
	"strings"
	"sync"
	"time"
)

// ConnectAggregator turns per-node "still trying to connect" noise into a
// single periodic summary line.  Every Client reports its pending/connected
// state here instead of logging its own retries; the aggregator logs:
//
//   - immediately, whenever the set of pending (not-yet-connected) nodes
//     CHANGES membership (a node starts waiting, or a different node finishes
//     connecting while others are still down), and
//   - at a slow, fixed cadence (interval) while any node is still pending, so
//     a long outage doesn't go silent but also doesn't flood the log, and
//   - once, when the last pending node connects ("all nodes connected").
//
// The first connect attempt and real state changes (connected/disconnected/
// error) are still logged individually by Client itself — this type owns
// only the "waiting on a retry" volume.
type ConnectAggregator struct {
	mu      sync.Mutex
	total   int
	pending map[string]bool
	lastLog time.Time
	waiting bool // true once we've logged a "waiting for connection" summary
	// that hasn't yet been resolved by an "all connected" line.

	interval time.Duration
	now      func() time.Time
	logf     func(format string, args ...interface{})
}

// NewConnectAggregator creates an aggregator for total nodes.  now and logf
// are injectable so tests can drive the clock and capture log lines without
// sleeping or scraping stdout; pass nil for either to use real time.Now /
// log.Printf.
func NewConnectAggregator(total int, interval time.Duration, now func() time.Time, logf func(string, ...interface{})) *ConnectAggregator {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = log.Printf
	}
	return &ConnectAggregator{
		total:    total,
		pending:  make(map[string]bool),
		interval: interval,
		now:      now,
		logf:     logf,
	}
}

// Pending marks refDes as currently waiting for a connection.  Safe to call
// on every failed dial/retry — it only logs when the pending SET changes.
func (a *ConnectAggregator) Pending(refDes string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending[refDes] {
		return // no membership change; the periodic Tick covers this node
	}
	a.pending[refDes] = true
	a.logSummaryLocked()
}

// Connected marks refDes as connected, removing it from the pending set.  If
// this empties the set, a single "all nodes connected" line replaces the
// pending summary.
func (a *ConnectAggregator) Connected(refDes string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.pending[refDes] {
		return // was never reported pending (e.g. connected on the first try)
	}
	delete(a.pending, refDes)
	if len(a.pending) == 0 {
		if a.waiting {
			a.logf("daqnode: all %d node(s) connected", a.total)
		}
		a.waiting = false
		return
	}
	a.logSummaryLocked()
}

// Tick emits the slow-cadence summary line if any node is still pending and
// at least interval has elapsed since the last log line.  Intended to be
// called from a real-time loop (see Run); calling it more often than
// interval is harmless.
func (a *ConnectAggregator) Tick() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.pending) == 0 {
		return
	}
	if a.now().Sub(a.lastLog) < a.interval {
		return
	}
	a.logSummaryLocked()
}

// Run drives Tick on a real-time ticker until stop is closed/fires.  Intended
// for production use from main(); tests should call Tick directly with an
// injected clock instead.
func (a *ConnectAggregator) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			a.Tick()
		}
	}
}

// logSummaryLocked logs the current pending set.  Caller must hold a.mu.
func (a *ConnectAggregator) logSummaryLocked() {
	names := make([]string, 0, len(a.pending))
	for n := range a.pending {
		names = append(names, n)
	}
	sort.Strings(names)
	a.logf("daqnode: waiting for connection: %s (%d of %d nodes, retrying every %s)",
		strings.Join(names, ", "), len(names), a.total, reconnectDelay)
	a.lastLog = a.now()
	a.waiting = true
}
