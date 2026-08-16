package daqnode

import (
	"context"
	"controlnode/broker"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// dropAfterOneServer stands in for a DAQ node that completes the config_req
// handshake, streams one data frame, then closes the connection — simulating
// a dropped link.  Every new incoming connection is handled the same way, so
// a single test server supports many reconnect cycles.
func dropAfterOneServer(t *testing.T, refDes string, connCount *atomic.Int32) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connCount.Add(1)
		if err := conn.WriteJSON(map[string]string{"type": "config_req", "refDes": refDes}); err != nil {
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil { // config
			return
		}
		_ = conn.WriteJSON(map[string]interface{}{
			"type": "data", "t": 1.0, "d": map[string]float64{"PT-01": 1.0},
		})
		// Return immediately: closes the connection (defer), the client sees
		// a read error and Run() loops back to reconnect.
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

// waitFor polls cond until it is true or timeout elapses, failing the test
// otherwise.  Used instead of a fixed sleep so tests don't rely on timing
// luck: the bound is a generous ceiling, not a synchronisation mechanism.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition not met within %s", timeout)
	}
}

// TestClientReconnectsAfterDrop exercises the reconnect loop against a server
// that drops the connection right after each handshake: the client must keep
// reconnecting, and DaqConnected must never exceed 1 (no double-increment)
// and must settle back to 0 once the loop stops (no leaked decrement).
func TestClientReconnectsAfterDrop(t *testing.T) {
	var connCount atomic.Int32
	ts := dropAfterOneServer(t, "DAQ001", &connCount)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	c := New("DAQ001", host, port, `{"type":"config"}`, b, nil)
	c.SetRetryDelay(5 * time.Millisecond)

	// Track the high-water mark of DaqConnected while the client cycles
	// through several connect/drop cycles, to catch a double-increment bug.
	stopMonitor := make(chan struct{})
	var maxSeen atomic.Int32
	go func() {
		for {
			select {
			case <-stopMonitor:
				return
			default:
			}
			if v := b.DaqConnected.Load(); v > maxSeen.Load() {
				maxSeen.Store(v)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// Let it cycle through at least 3 full connect/drop rounds.
	waitFor(t, 3*time.Second, func() bool { return connCount.Load() >= 3 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop promptly after ctx cancellation")
	}
	close(stopMonitor)

	if got := maxSeen.Load(); got > 1 {
		t.Errorf("DaqConnected reached %d, want never more than 1 (double-increment?)", got)
	}
	// After the loop has fully stopped, the last connection's drop must have
	// been accounted for — no leaked decrement, no stuck increment.
	if got := b.DaqConnected.Load(); got != 0 {
		t.Errorf("DaqConnected settled at %d after shutdown, want 0", got)
	}
}

// TestClientReconnectAggregatorTransitions verifies the ConnectAggregator
// sees a pending→connected→pending→connected... sequence as the client
// cycles through reconnects, driven through the real Run() loop rather than
// calling the aggregator directly.
func TestClientReconnectAggregatorTransitions(t *testing.T) {
	var connCount atomic.Int32
	ts := dropAfterOneServer(t, "DAQ001", &connCount)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	c := New("DAQ001", host, port, `{"type":"config"}`, b, nil)
	c.SetRetryDelay(5 * time.Millisecond)

	agg, _, sink := newTestAggregator(1, time.Hour) // interval irrelevant; only membership-change logs matter
	c.SetAggregator(agg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// After a few drop/reconnect cycles we expect to have seen at least one
	// "waiting for connection" line (logged when the drop makes the node
	// pending) and at least one "all N node(s) connected" line (logged when
	// the client reconnects while the aggregator was waiting on it).
	waitFor(t, 3*time.Second, func() bool {
		sawWaiting, sawAllConnected := false, false
		for _, l := range sink.Snapshot() {
			if strings.Contains(l, "waiting for connection") {
				sawWaiting = true
			}
			if strings.Contains(l, "all 1 node(s) connected") {
				sawAllConnected = true
			}
		}
		return sawWaiting && sawAllConnected && connCount.Load() >= 3
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop promptly after ctx cancellation")
	}
}

// TestClientNeverConnectsRetriesWithoutLeaking exercises a node that never
// comes up: the client must keep retrying (never give up) and must not leak
// goroutines across many retry cycles, and ctx cancellation must stop the
// loop promptly.
func TestClientNeverConnectsRetriesWithoutLeaking(t *testing.T) {
	// Grab a port, then close the listener immediately so dials to it are
	// refused quickly and deterministically (no real DAQ node ever answers).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, portStr, _ := net.SplitHostPort(l.Addr().String())
	l.Close()
	port, err2 := strconv.Atoi(portStr)
	if err2 != nil {
		t.Fatalf("parse port: %v", err2)
	}

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	c := New("DAQ-NEVER", host, port, `{"type":"config"}`, b, nil)
	c.SetRetryDelay(2 * time.Millisecond)

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()

	// Let it fail-and-retry many times.
	time.Sleep(200 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop promptly after ctx cancellation")
	}

	// Give any straggling goroutines a moment to unwind, then check we're
	// back near the starting goroutine count (not accumulating one set of
	// leaked goroutines per retry).
	waitFor(t, time.Second, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= before+2
	})
}
