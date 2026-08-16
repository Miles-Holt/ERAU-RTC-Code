package daqnode

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock for ConnectAggregator tests — no
// real sleeping, no stdout scraping.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// logSink captures log lines in order instead of writing to stdout.
type logSink struct{ lines []string }

func (s *logSink) Printf(format string, args ...interface{}) {
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func newTestAggregator(total int, interval time.Duration) (*ConnectAggregator, *fakeClock, *logSink) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	sink := &logSink{}
	agg := NewConnectAggregator(total, interval, clk.now, sink.Printf)
	return agg, clk, sink
}

// TestConnectAggregator_MembershipChangeLogsImmediately verifies a summary is
// logged whenever the pending SET changes, and is a no-op when a node that is
// already pending reports pending again (retry noise suppression).
func TestConnectAggregator_MembershipChangeLogsImmediately(t *testing.T) {
	agg, _, sink := newTestAggregator(4, 30*time.Second)

	agg.Pending("DAQ001")
	if len(sink.lines) != 1 {
		t.Fatalf("after first Pending: got %d log lines, want 1: %v", len(sink.lines), sink.lines)
	}
	if !strings.Contains(sink.lines[0], "DAQ001") || !strings.Contains(sink.lines[0], "1 of 4") {
		t.Errorf("unexpected summary line: %q", sink.lines[0])
	}

	// Repeated retries for the same node before it connects: no new line.
	agg.Pending("DAQ001")
	agg.Pending("DAQ001")
	if len(sink.lines) != 1 {
		t.Fatalf("repeated Pending for same node logged again: %v", sink.lines)
	}

	// A second node joins the pending set: membership changed, logs again.
	agg.Pending("DAQ003")
	if len(sink.lines) != 2 {
		t.Fatalf("after second node pending: got %d log lines, want 2: %v", len(sink.lines), sink.lines)
	}
	if !strings.Contains(sink.lines[1], "DAQ001") || !strings.Contains(sink.lines[1], "DAQ003") ||
		!strings.Contains(sink.lines[1], "2 of 4") {
		t.Errorf("unexpected summary line: %q", sink.lines[1])
	}
}

// TestConnectAggregator_ConnectedTransitions verifies that a node connecting
// removes it from the pending set (logging the shrunk summary), and that the
// LAST node connecting produces a single "all connected" line instead of a
// pending summary.
func TestConnectAggregator_ConnectedTransitions(t *testing.T) {
	agg, _, sink := newTestAggregator(2, 30*time.Second)

	agg.Pending("DAQ001")
	agg.Pending("DAQ003")
	if len(sink.lines) != 2 {
		t.Fatalf("setup: got %d lines, want 2: %v", len(sink.lines), sink.lines)
	}

	// DAQ001 connects; DAQ003 is still pending, so we still get a summary line
	// (just naming the one remaining node), not the "all connected" line.
	agg.Connected("DAQ001")
	if len(sink.lines) != 3 {
		t.Fatalf("after DAQ001 connects: got %d lines, want 3: %v", len(sink.lines), sink.lines)
	}
	if strings.Contains(sink.lines[2], "all") {
		t.Errorf("expected a pending summary, not an all-connected line: %q", sink.lines[2])
	}
	if strings.Contains(sink.lines[2], "DAQ001") || !strings.Contains(sink.lines[2], "DAQ003") {
		t.Errorf("summary should only mention DAQ003 now: %q", sink.lines[2])
	}

	// DAQ003 connects: the pending set is now empty -> exactly one
	// "all connected" line, and connecting an already-connected/unknown node
	// afterward is a no-op.
	agg.Connected("DAQ003")
	if len(sink.lines) != 4 {
		t.Fatalf("after DAQ003 connects: got %d lines, want 4: %v", len(sink.lines), sink.lines)
	}
	if !strings.Contains(sink.lines[3], "all 2 node(s) connected") {
		t.Errorf("expected an all-connected line, got: %q", sink.lines[3])
	}

	agg.Connected("DAQ003") // already connected: must not log again
	if len(sink.lines) != 4 {
		t.Fatalf("Connected on an already-connected node logged again: %v", sink.lines)
	}
}

// TestConnectAggregator_NoAllConnectedWithoutAWait verifies that a node which
// connects on its first try (never reported Pending) produces no spurious
// "all connected" announcement — there was nothing to announce the end of.
func TestConnectAggregator_NoAllConnectedWithoutAWait(t *testing.T) {
	agg, _, sink := newTestAggregator(1, 30*time.Second)
	agg.Connected("DAQ001")
	if len(sink.lines) != 0 {
		t.Fatalf("Connected with no prior Pending logged: %v", sink.lines)
	}
}

// TestConnectAggregator_TickIsSlowCadence verifies Tick only logs once
// interval has elapsed since the last log line, and does nothing while the
// pending set is empty.
func TestConnectAggregator_TickIsSlowCadence(t *testing.T) {
	agg, clk, sink := newTestAggregator(3, 30*time.Second)

	// Nothing pending yet: Tick is a no-op.
	agg.Tick()
	if len(sink.lines) != 0 {
		t.Fatalf("Tick with empty pending set logged: %v", sink.lines)
	}

	agg.Pending("DAQ001") // logs immediately (membership change)
	if len(sink.lines) != 1 {
		t.Fatalf("got %d lines after Pending, want 1", len(sink.lines))
	}

	// Well under the interval: no periodic log yet.
	clk.advance(10 * time.Second)
	agg.Tick()
	if len(sink.lines) != 1 {
		t.Fatalf("Tick before interval elapsed logged: %v", sink.lines)
	}

	// Interval elapsed: exactly one more periodic summary line.
	clk.advance(20 * time.Second)
	agg.Tick()
	if len(sink.lines) != 2 {
		t.Fatalf("got %d lines after interval elapsed, want 2: %v", len(sink.lines), sink.lines)
	}

	// Calling Tick again immediately (no further time passed) must not log.
	agg.Tick()
	if len(sink.lines) != 2 {
		t.Fatalf("extra Tick call logged again: %v", sink.lines)
	}
}
