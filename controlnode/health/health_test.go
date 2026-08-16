package health

import (
	"context"
	"controlnode/broker"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced clock, avoiding any reliance on wall time.
// It is read from the Publisher's Run() goroutine and advanced from the test
// goroutine, so both sides go through a mutex.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// waitForValue polls sub for a data message where refDes has the given value
// (within tolerance), failing the test if none arrives within timeout.
func waitForValue(t *testing.T, sub <-chan []byte, refDes string, want, tol float64, timeout time.Duration) float64 {
	t.Helper()
	deadline := time.After(timeout)
	var last float64
	for {
		select {
		case raw := <-sub:
			var m struct {
				Type string             `json:"type"`
				D    map[string]float64 `json:"d"`
			}
			if json.Unmarshal(raw, &m) != nil || m.Type != "data" {
				continue
			}
			v, ok := m.D[refDes]
			if !ok {
				continue
			}
			last = v
			if diff := v - want; diff < tol && diff > -tol {
				return v
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s ~= %v, last seen %v", refDes, want, last)
		}
	}
}

// waitForAnyValue polls sub until refDes appears at all, returning its value.
func waitForAnyValue(t *testing.T, sub <-chan []byte, refDes string, timeout time.Duration) float64 {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case raw := <-sub:
			var m struct {
				Type string             `json:"type"`
				D    map[string]float64 `json:"d"`
			}
			if json.Unmarshal(raw, &m) != nil || m.Type != "data" {
				continue
			}
			if v, ok := m.D[refDes]; ok {
				return v
			}
		case <-deadline:
			t.Fatalf("timed out waiting for any value of %s", refDes)
		}
	}
}

// TestPublisherPublishesUnderExpectedRefDes verifies each configured metric
// appears on the broker under its configured refDes.
func TestPublisherPublishesUnderExpectedRefDes(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(200)
	sub, unsub := b.Subscribe()
	defer unsub()

	sensorRefDes := map[string]string{
		"uptime":       "CTR-UPTIME",
		"loopTime":     "CTR-LOOPTIME",
		"daqConnected": "CTR-DAQCONN",
		"wcConnected":  "CTR-WCCONN",
	}
	p := New(b, sensorRefDes, nil)
	clk := &fakeClock{t: time.Unix(1000, 0)}
	p.SetClock(clk.now)
	p.SetStartTime(clk.now())

	b.DaqConnected.Store(2)
	// WcConnected is maintained by the broker itself from its actual
	// subscriber set (see broker.go), so it isn't independently settable
	// here — our one Subscribe() call above makes it 1.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, 200)

	if v := waitForAnyValue(t, sub, "CTR-DAQCONN", 2*time.Second); v != 2 {
		t.Errorf("CTR-DAQCONN = %v, want 2 (from broker.DaqConnected)", v)
	}
	if v := waitForValue(t, sub, "CTR-WCCONN", 1, 0.5, 2*time.Second); v != 1 {
		t.Errorf("CTR-WCCONN = %v, want 1 (this test's own subscriber, tracked by the broker)", v)
	}
	// uptime starts at (roughly) zero since startTime == now at t=0 advance.
	if v := waitForAnyValue(t, sub, "CTR-UPTIME", 2*time.Second); v < 0 || v > 1 {
		t.Errorf("CTR-UPTIME = %v, want ~0 immediately after start", v)
	}
	// loopTime just needs to be present (its value comes from the broker's
	// own loop timer, not something this test controls).
	waitForAnyValue(t, sub, "CTR-LOOPTIME", 2*time.Second)
}

// TestUptimeAdvancesWithInjectedClock verifies uptime tracks the injected
// clock rather than wall time — advancing the fake clock changes the
// published value with no sleeping involved.
func TestUptimeAdvancesWithInjectedClock(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(200)
	sub, unsub := b.Subscribe()
	defer unsub()

	start := time.Unix(5000, 0)
	clk := &fakeClock{t: start}
	p := New(b, map[string]string{"uptime": "CTR-UPTIME"}, nil)
	p.SetClock(clk.now)
	p.SetStartTime(start)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, 200)

	waitForValue(t, sub, "CTR-UPTIME", 0, 1, 2*time.Second)

	clk.advance(90 * time.Second)
	waitForValue(t, sub, "CTR-UPTIME", 90, 1, 2*time.Second)

	clk.advance(3600 * time.Second)
	waitForValue(t, sub, "CTR-UPTIME", 3690, 1, 2*time.Second)
}

// TestConnectionCountsReflectBroker verifies daqConnected/wcConnected track
// the broker's live atomic counters as they change, not just a value snapshot
// taken at Publisher construction.
func TestConnectionCountsReflectBroker(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(200)
	sub, unsub := b.Subscribe()
	defer unsub()

	p := New(b, map[string]string{"daqConnected": "CTR-DAQCONN"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, 200)

	waitForValue(t, sub, "CTR-DAQCONN", 0, 0.5, 2*time.Second)

	b.DaqConnected.Store(4)
	waitForValue(t, sub, "CTR-DAQCONN", 4, 0.5, 2*time.Second)

	b.DaqConnected.Store(1)
	waitForValue(t, sub, "CTR-DAQCONN", 1, 0.5, 2*time.Second)
}

// TestCmdRefDesKeepaliveEmitted verifies the command refDes list is published
// as 0 so the browser has a baseline for time-history graphs.
func TestCmdRefDesKeepaliveEmitted(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(200)
	sub, unsub := b.Subscribe()
	defer unsub()

	p := New(b, map[string]string{}, []string{"CTR-RESTART"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx, 200)

	if v := waitForAnyValue(t, sub, "CTR-RESTART", 2*time.Second); v != 0 {
		t.Errorf("CTR-RESTART = %v, want 0", v)
	}
}

// TestPublisherStopsOnCtxCancel verifies the publish loop exits promptly when
// its context is cancelled, without relying on a sleep for synchronisation.
func TestPublisherStopsOnCtxCancel(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(200)

	p := New(b, map[string]string{"uptime": "CTR-UPTIME"}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx, 200); close(done) }()

	// Let it publish at least once so we know the loop is actually running.
	sub, unsub := b.Subscribe()
	waitForAnyValue(t, sub, "CTR-UPTIME", 2*time.Second)
	unsub()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop promptly after ctx cancellation")
	}
}
