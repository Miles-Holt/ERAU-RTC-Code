package broker

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// recvWithin reads one message from ch or fails the test after d.
func recvWithin(t *testing.T, ch <-chan []byte, d time.Duration) []byte {
	t.Helper()
	select {
	case msg, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for message")
		}
		return msg
	case <-time.After(d):
		t.Fatal("timed out waiting for message")
		return nil
	}
}

// waitForType drains ch until a message with the given "type" arrives, or fails
// after d.  Returns the decoded message.
func waitForType(t *testing.T, ch <-chan []byte, typ string, d time.Duration) map[string]interface{} {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case raw, ok := <-ch:
			if !ok {
				t.Fatalf("channel closed while waiting for %q message", typ)
			}
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil && m["type"] == typ {
				return m
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q message", typ)
			return nil
		}
	}
}

// TestBrokerDataBroadcast is the core smoke test: data published into the broker
// is fanned out to subscribers on the broadcast tick.
func TestBrokerDataBroadcast(t *testing.T) {
	b := New(nil, nil, nil)
	go b.Run(50) // 50 Hz → ~20 ms ticks

	ch, unsub := b.Subscribe()
	defer unsub()

	// Barrier: wait for one broadcast so the subscription is live before we
	// publish the value we want to observe (unsubscribed publishes are dropped).
	waitForType(t, ch, "data", time.Second)

	b.PublishData(DataEvent{Values: map[string]float64{"PT-01": 42.5}})

	deadline := time.After(time.Second)
	for {
		select {
		case raw := <-ch:
			var m struct {
				Type string             `json:"type"`
				D    map[string]float64 `json:"d"`
			}
			if json.Unmarshal(raw, &m) != nil || m.Type != "data" {
				continue
			}
			if v, ok := m.D["PT-01"]; ok {
				if v != 42.5 {
					t.Fatalf("PT-01 = %v, want 42.5", v)
				}
				return // success
			}
		case <-deadline:
			t.Fatal("never received data broadcast containing PT-01")
		}
	}
}

// TestBrokerCommandRouting checks a web-client command is routed to the correct
// DAQ node's registered channel.
func TestBrokerCommandRouting(t *testing.T) {
	b := New(map[string]string{"OV-01": "DAQ-1"}, nil, nil)
	go b.Run(50)

	daqCh := make(chan []byte, 8)
	b.RegisterDaq("DAQ-1", daqCh)
	// RegisterDaq is processed by the Run goroutine; give it a moment.
	time.Sleep(20 * time.Millisecond)

	b.SendCmd(CmdMsg{Type: "cmd", RefDes: "OV-01", Value: true, User: "tester"})

	raw := recvWithin(t, daqCh, time.Second)
	var cmd struct {
		Type   string      `json:"type"`
		RefDes string      `json:"refDes"`
		Value  interface{} `json:"value"`
	}
	if err := json.Unmarshal(raw, &cmd); err != nil {
		t.Fatalf("routed cmd is not valid JSON: %v", err)
	}
	if cmd.Type != "cmd" || cmd.RefDes != "OV-01" {
		t.Fatalf("routed cmd = %+v, want type=cmd refDes=OV-01", cmd)
	}
	if cmd.Value != true {
		t.Fatalf("routed cmd value = %v, want true", cmd.Value)
	}
}

// TestBrokerUnknownCmdDropped ensures a command for an unmapped refDes is dropped
// without blocking or panicking (subscribers keep receiving data).
func TestBrokerUnknownCmdDropped(t *testing.T) {
	b := New(map[string]string{}, nil, nil)
	go b.Run(50)

	ch, unsub := b.Subscribe()
	defer unsub()

	b.SendCmd(CmdMsg{Type: "cmd", RefDes: "GHOST", Value: 1})
	// Broker must still be alive and broadcasting.
	b.PublishData(DataEvent{Values: map[string]float64{"X": 1}})
	waitForType(t, ch, "data", time.Second)
}

// TestBrokerRestartCommand verifies that a restart refDes triggers the injected
// exit hook, while a normal command does not.
func TestBrokerRestartCommand(t *testing.T) {
	b := New(map[string]string{"OV-01": "DAQ-1"}, []string{"CTR001-restart"}, nil)

	exitCh := make(chan int, 1)
	b.exit = func(code int) { exitCh <- code }
	go b.Run(50)

	// A normal command must NOT trigger exit.
	b.SendCmd(CmdMsg{Type: "cmd", RefDes: "OV-01", Value: true, User: "op"})
	select {
	case code := <-exitCh:
		t.Fatalf("normal command triggered exit(%d)", code)
	case <-time.After(200 * time.Millisecond):
		// expected: no exit
	}

	// The restart command must trigger exit(1).
	b.SendCmd(CmdMsg{Type: "cmd", RefDes: "CTR001-restart", Value: true, User: "op"})
	select {
	case code := <-exitCh:
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	case <-time.After(time.Second):
		t.Fatal("restart command did not trigger exit")
	}
}

// TestBrokerSlowSubscriberDropsFrames verifies the non-blocking fan-out: a
// subscriber that never drains must not stall delivery to other subscribers or
// deadlock the broker.
func TestBrokerSlowSubscriberDropsFrames(t *testing.T) {
	b := New(nil, nil, nil)
	go b.Run(100) // fast ticks to stress the fan-out

	// Slow subscriber: subscribe but never read from it.
	slow, unsubSlow := b.Subscribe()
	defer unsubSlow()
	_ = slow

	// Fast subscriber: drains normally.
	fast, unsubFast := b.Subscribe()
	defer unsubFast()

	// Barrier so both subscriptions are registered.
	waitForType(t, fast, "data", time.Second)

	// Publish steadily while only the fast subscriber drains.  The fast one must
	// keep receiving despite the slow one's buffer being saturated.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			b.PublishData(DataEvent{Values: map[string]float64{"X": float64(i)}})
			time.Sleep(time.Millisecond)
		}
	}()

	received := 0
	deadline := time.After(3 * time.Second)
	for received < 10 {
		select {
		case _, ok := <-fast:
			if !ok {
				t.Fatal("fast subscriber channel closed unexpectedly")
			}
			received++
		case <-deadline:
			t.Fatalf("fast subscriber only got %d frames — slow subscriber stalled the broker", received)
		}
	}
	<-done // publisher completes without blocking
}

// TestBrokerBadData checks that an out-of-range value produces a bad_data
// transition message and populates the snapshot.
func TestBrokerBadData(t *testing.T) {
	max := 100.0
	bounds := map[string]ChannelBounds{"PT-01": {Max: &max}}
	b := New(nil, nil, bounds)
	go b.Run(50)

	ch, unsub := b.Subscribe()
	defer unsub()

	// Barrier: wait for one broadcast so the subscription is registered in the
	// Run goroutine before we publish the value whose transition we want to see.
	waitForType(t, ch, "data", time.Second)

	b.PublishData(DataEvent{Values: map[string]float64{"PT-01": 150}})

	m := waitForType(t, ch, "bad_data", time.Second)
	if m["refDes"] != "PT-01" {
		t.Errorf("bad_data refDes = %v, want PT-01", m["refDes"])
	}
	if m["status"] != "high" {
		t.Errorf("bad_data status = %v, want high", m["status"])
	}

	// Snapshot must now be non-nil and mention the channel.
	if snap := b.BadDataSnapshot(); snap == nil {
		t.Error("BadDataSnapshot is nil after a bad value")
	} else {
		var s struct {
			Type     string `json:"type"`
			Channels []struct {
				RefDes string `json:"refDes"`
			} `json:"channels"`
		}
		if err := json.Unmarshal(snap, &s); err != nil {
			t.Fatalf("snapshot not valid JSON: %v", err)
		}
		if s.Type != "bad_data_snapshot" || len(s.Channels) != 1 || s.Channels[0].RefDes != "PT-01" {
			t.Errorf("unexpected snapshot: %s", snap)
		}
	}

	// Return to range → snapshot clears.
	b.PublishData(DataEvent{Values: map[string]float64{"PT-01": 50}})
	// Snapshot clears asynchronously in the Run goroutine; poll briefly.
	cleared := false
	for i := 0; i < 50; i++ {
		if b.BadDataSnapshot() == nil {
			cleared = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cleared {
		t.Error("BadDataSnapshot did not clear after value returned to range")
	}
}

// ── DAQ event sink ────────────────────────────────────────────────────────────

// sinkSpy records the DaqEventSink callbacks the alert engine relies on.
type sinkSpy struct {
	mu   sync.Mutex
	up   []string
	down []string
	data []string
	bad  []string // "refDes|node|status"
}

func (s *sinkSpy) NodeConnected(node string) {
	s.mu.Lock()
	s.up = append(s.up, node)
	s.mu.Unlock()
}
func (s *sinkSpy) NodeDisconnected(node string) {
	s.mu.Lock()
	s.down = append(s.down, node)
	s.mu.Unlock()
}
func (s *sinkSpy) NodeData(node string) {
	s.mu.Lock()
	s.data = append(s.data, node)
	s.mu.Unlock()
}
func (s *sinkSpy) BadData(refDes, node, status string, value float64) {
	s.mu.Lock()
	s.bad = append(s.bad, refDes+"|"+node+"|"+status)
	s.mu.Unlock()
}
func (s *sinkSpy) badEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bad...)
}

// The broker is the single server-side source of the daqNode lifecycle events
// the alert engine turns into template alerts.  Bad-data events must fire only
// on a TRANSITION (that is what makes the alert edge-triggered) and must carry
// the owning node so "{node}" interpolates.
func TestBrokerEventSink(t *testing.T) {
	max := 100.0
	b := New(map[string]string{"PT-01": "DAQ001"}, nil, map[string]ChannelBounds{"PT-01": {Max: &max}})
	spy := &sinkSpy{}
	b.SetEventSink(spy)
	go b.Run(50)

	b.NoteDaqConnected("DAQ001")
	b.NoteDaqData("DAQ001")
	b.NoteDaqDisconnected("DAQ001")

	spy.mu.Lock()
	up, down, data := len(spy.up), len(spy.down), len(spy.data)
	spy.mu.Unlock()
	if up != 1 || down != 1 || data != 1 {
		t.Errorf("lifecycle events = up:%d down:%d data:%d, want 1 each", up, down, data)
	}

	b.PublishData(DataEvent{Values: map[string]float64{"PT-01": 150}})
	b.PublishData(DataEvent{Values: map[string]float64{"PT-01": 160}}) // still high — no transition
	b.PublishData(DataEvent{Values: map[string]float64{"PT-01": 10}})  // back in range

	var got []string
	for i := 0; i < 100; i++ {
		got = spy.badEvents()
		if len(got) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 2 {
		t.Fatalf("bad-data events = %v, want exactly the two transitions", got)
	}
	if got[0] != "PT-01|DAQ001|high" {
		t.Errorf("first bad-data event = %q, want PT-01|DAQ001|high", got[0])
	}
	if got[1] != "PT-01|DAQ001|ok" {
		t.Errorf("second bad-data event = %q, want PT-01|DAQ001|ok", got[1])
	}
}

// Without a sink the broker behaves exactly as before.
func TestBrokerNoEventSink(t *testing.T) {
	b := New(nil, nil, nil)
	go b.Run(50)
	b.NoteDaqConnected("DAQ001")
	b.NoteDaqData("DAQ001")
	b.NoteDaqDisconnected("DAQ001")
}
