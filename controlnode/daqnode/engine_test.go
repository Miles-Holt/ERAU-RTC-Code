package daqnode

import (
	"context"
	"controlnode/broker"
	"controlnode/statemachine"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ── Engine double ─────────────────────────────────────────────────────────────

type fakeEngine struct {
	mu sync.Mutex

	machines []string
	running  bool // IsRunningOnNode result
	payload  *statemachine.DaqStateUpdate
	resolve  error // returned by CurrentDaqPayload

	aborts       []string
	completes    []int64
	reconnects   []string
	firstConnect []string
}

func (f *fakeEngine) MachinesForNode(string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.machines...)
}

func (f *fakeEngine) IsRunningOnNode(string, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running
}

func (f *fakeEngine) CurrentDaqPayload(machine, node string) (*statemachine.DaqStateUpdate, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolve != nil {
		return nil, true, f.resolve
	}
	if !f.running {
		return nil, false, nil
	}
	return f.payload, true, nil
}

func (f *fakeEngine) NotifyAbortTriggered(machine string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aborts = append(f.aborts, machine)
	return nil
}

func (f *fakeEngine) NotifySequenceCompleteRun(machine string, runID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completes = append(f.completes, runID)
	return nil
}

func (f *fakeEngine) NotifyDaqReconnect(machine, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reconnects = append(f.reconnects, machine)
	return nil
}

func (f *fakeEngine) NotifyDaqFirstConnect(machine, node string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.firstConnect = append(f.firstConnect, machine)
	return nil
}

func (f *fakeEngine) snapshot() (aborts []string, completes []int64, reconnects []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.aborts...),
		append([]int64(nil), f.completes...),
		append([]string(nil), f.reconnects...)
}

func (f *fakeEngine) firstConnects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.firstConnect...)
}

// ── Fake DAQ servers ──────────────────────────────────────────────────────────

// stateReqDaqServer completes the handshake then sends a state_req.
func stateReqDaqServer(t *testing.T, msgs chan<- []byte) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]string{"type": "config_req", "refDes": "DAQ001"}); err != nil {
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]string{"type": "state_req"}); err != nil {
			return
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			select {
			case msgs <- raw:
			default:
			}
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

// scriptedDaqServer completes the handshake then relays strings from the
// returned channel to the client as DAQ messages.
func scriptedDaqServer(t *testing.T) (*httptest.Server, chan string) {
	t.Helper()
	script := make(chan string, 8)
	h := func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]string{"type": "config_req", "refDes": "DAQ001"}); err != nil {
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		go func() {
			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case msg := <-script:
				if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
					return
				}
			case <-timeout:
				return
			}
		}
	}
	return httptest.NewServer(http.HandlerFunc(h)), script
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestClient_NoStatePushOnConnect covers F-A6/B4: connecting must NOT push a
// cached state payload (that would re-fire a sequence from t=0).  With no
// machine running on the node, nothing at all is sent after the config.
func TestClient_NoStatePushOnConnect(t *testing.T) {
	gotConfig := make(chan []byte, 1)
	msgs := make(chan []byte, 8)
	ts := collectingDaqServer(t, gotConfig, msgs)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	eng := &fakeEngine{machines: []string{"fuelSeq"}, running: false}
	c := New("DAQ001", host, port, `{"type":"config"}`, b, eng)
	go c.connect()

	select {
	case <-gotConfig:
	case <-time.After(2 * time.Second):
		t.Fatal("handshake never completed")
	}

	select {
	case raw := <-msgs:
		t.Fatalf("client sent an unsolicited message after connect: %s", raw)
	case <-time.After(300 * time.Millisecond):
	}

	if _, _, reconnects := eng.snapshot(); len(reconnects) != 0 {
		t.Errorf("reconnect-uncertain path fired for an idle machine: %v", reconnects)
	}
}

// TestClient_ReconnectWhileRunningIsStateUncertain covers F-A6: a GENUINE
// reconnect (the node drops and comes back) while a machine is mid-flight in
// a daq_local state on this node must take the state-uncertain path (abort
// destination + alarm), not re-send the payload. It drives two full connect
// cycles through Run() — dropAfterOneServer completes the handshake, streams
// one data frame, then closes — so the first cycle is a genuine first connect
// (must NOT count as a reconnect) and the second is a genuine reconnect (must
// count as one). This also guards against the first-connect/reconnect
// distinction collapsing back into "every handshake is a reconnect".
func TestClient_ReconnectWhileRunningIsStateUncertain(t *testing.T) {
	var connCount atomic.Int32
	ts := dropAfterOneServer(t, "DAQ001", &connCount)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	eng := &fakeEngine{machines: []string{"fuelSeq"}, running: true}
	c := New("DAQ001", host, port, `{"type":"config"}`, b, eng)
	c.SetRetryDelay(5 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	// First handshake: the machine is already "running" here, but this is the
	// node's first-ever connection — must produce the first-connect handling,
	// NOT a reconnect.
	deadline := time.After(2 * time.Second)
	for {
		if fc := eng.firstConnects(); len(fc) == 1 && fc[0] == "fuelSeq" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first-connect handling never fired on the first-ever handshake")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, _, reconnects := eng.snapshot(); len(reconnects) != 0 {
		t.Fatalf("reconnect path fired on the FIRST-EVER connection: %v", reconnects)
	}

	// The server drops the link right after the first handshake; Run() loops
	// back and reconnects — a genuine reconnect this time.
	deadline = time.After(2 * time.Second)
	for {
		if _, _, reconnects := eng.snapshot(); len(reconnects) == 1 && reconnects[0] == "fuelSeq" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("reconnect-uncertain path never fired on the genuine reconnect")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	// The first-connect count must not have grown on the reconnect.
	if fc := eng.firstConnects(); len(fc) != 1 {
		t.Errorf("first-connect handling fired %d times, want exactly 1 (only the first-ever handshake)", len(fc))
	}
}

// TestClient_StateReqRepliesOnlyWhenRunning covers F-A7.
func TestClient_StateReqRepliesOnlyWhenRunning(t *testing.T) {
	payload := &statemachine.DaqStateUpdate{
		Type: "state_update", State: "autoSequence", RunID: 7, Machine: "fuelSeq",
	}

	t.Run("running", func(t *testing.T) {
		msgs := make(chan []byte, 8)
		ts := stateReqDaqServer(t, msgs)
		defer ts.Close()
		host, port := hostPort(t, ts.URL)

		b := broker.New(nil, nil, nil)
		go b.Run(50)
		eng := &fakeEngine{machines: []string{"fuelSeq"}, running: true, payload: payload}
		c := New("DAQ001", host, port, `{"type":"config"}`, b, eng)
		go c.connect()

		m := waitForAnyMsg(t, msgs, 2*time.Second, func(m map[string]interface{}) bool {
			return m["type"] == "state_update"
		})
		if m["state"] != "autoSequence" {
			t.Errorf("state_update state = %v, want autoSequence", m["state"])
		}
		if m["runId"] != float64(7) {
			t.Errorf("state_update runId = %v, want 7", m["runId"])
		}
	})

	t.Run("not running", func(t *testing.T) {
		msgs := make(chan []byte, 8)
		ts := stateReqDaqServer(t, msgs)
		defer ts.Close()
		host, port := hostPort(t, ts.URL)

		b := broker.New(nil, nil, nil)
		go b.Run(50)
		eng := &fakeEngine{machines: []string{"fuelSeq"}, running: false}
		c := New("DAQ001", host, port, `{"type":"config"}`, b, eng)
		go c.connect()

		select {
		case raw := <-msgs:
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil && m["type"] == "state_update" {
				t.Fatalf("state_req answered while no machine was running here: %s", raw)
			}
		case <-time.After(500 * time.Millisecond):
		}
	})
}

// TestClient_StateReqResolutionFailureAlerts covers F-A15: a send-time
// resolution failure must reach the operator through the broker Err path, and
// no payload may be sent.
func TestClient_StateReqResolutionFailureAlerts(t *testing.T) {
	msgs := make(chan []byte, 8)
	ts := stateReqDaqServer(t, msgs)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)
	sub, unsub := b.Subscribe()
	defer unsub()

	eng := &fakeEngine{
		machines: []string{"fuelSeq"},
		running:  true,
		resolve:  errors.New(`unresolvable reference: "SEQ-IGN-LEAD"`),
	}
	c := New("DAQ001", host, port, `{"type":"config"}`, b, eng)
	go c.connect()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-sub:
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) != nil || m["type"] != "err" {
				continue
			}
			if s, _ := m["err"].(string); strings.Contains(s, "SEQ-IGN-LEAD") {
				return
			}
		case <-deadline:
			t.Fatal("resolution failure never reached the broker err path")
		}
	}
}

// TestClient_AbortAndCompleteRouting checks abort_triggered and
// sequence_complete reach the engine, the latter carrying the echoed runId.
func TestClient_AbortAndCompleteRouting(t *testing.T) {
	ts, sendToClient := scriptedDaqServer(t)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)
	eng := &fakeEngine{machines: []string{"fuelSeq"}}
	c := New("DAQ001", host, port, `{"type":"config"}`, b, eng)
	go c.connect()

	sendToClient <- `{"type":"abort_triggered"}`
	sendToClient <- `{"type":"sequence_complete","runId":42}`

	deadline := time.After(2 * time.Second)
	for {
		aborts, completes, _ := eng.snapshot()
		if len(aborts) == 1 && len(completes) == 1 {
			if aborts[0] != "fuelSeq" {
				t.Errorf("abort routed to %q, want fuelSeq", aborts[0])
			}
			if completes[0] != 42 {
				t.Errorf("sequence_complete runId = %d, want 42 (echo not forwarded)", completes[0])
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("engine never saw the messages (aborts=%v completes=%v)", aborts, completes)
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}
