package daqsim

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeControlNode is a minimal stand-in for the real daqnode.Client, used to
// drive the simulator's server side directly with the exact wire messages,
// without pulling in the statemachine engine. The end-to-end tests (see
// controlnode/integration) exercise the real client instead.
type fakeControlNode struct {
	t    *testing.T
	conn *websocket.Conn
}

func dialSim(t *testing.T, addr string) *fakeControlNode {
	t.Helper()
	u := url.URL{Scheme: "ws", Host: addr, Path: "/"}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	return &fakeControlNode{t: t, conn: c}
}

// handshake reads config_req and answers with cfg (a JSON string).
func (f *fakeControlNode) handshake(cfg string) {
	f.t.Helper()
	_, raw, err := f.conn.ReadMessage()
	if err != nil {
		f.t.Fatalf("read config_req: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil || m["type"] != "config_req" {
		f.t.Fatalf("expected config_req, got: %s", raw)
	}
	if err := f.conn.WriteMessage(websocket.TextMessage, []byte(cfg)); err != nil {
		f.t.Fatalf("write config: %v", err)
	}
}

func (f *fakeControlNode) send(v interface{}) {
	f.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		f.t.Fatalf("marshal: %v", err)
	}
	if err := f.conn.WriteMessage(websocket.TextMessage, b); err != nil {
		f.t.Fatalf("write: %v", err)
	}
}

// next drains messages until pred matches, with a generous timeout.
func (f *fakeControlNode) next(pred func(m map[string]interface{}) bool, d time.Duration) map[string]interface{} {
	f.t.Helper()
	f.conn.SetReadDeadline(time.Now().Add(d))
	for {
		_, raw, err := f.conn.ReadMessage()
		if err != nil {
			f.t.Fatalf("read: %v", err)
		}
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) == nil && pred(m) {
			f.conn.SetReadDeadline(time.Time{})
			return m
		}
	}
}

const testConfig = `{
  "type": "config",
  "sampleRateHz": 200,
  "managementRateHz": 1,
  "channels": [
    {"refDes": "OV-05-CMD", "moduleModelNumber": "Digital-IO", "channelNumber": "/port3/line0"},
    {"refDes": "OV-05-FB",  "moduleModelNumber": "Digital-IO", "channelNumber": "/port0/line0"},
    {"refDes": "IG-01-CMD", "moduleModelNumber": "Digital-IO", "channelNumber": "/port3/line1"},
    {"refDes": "TRIGGER-01","moduleModelNumber": "Analog-Output", "channelNumber": "/port0/line0"},
    {"refDes": "CPT-01",    "moduleModelNumber": "Analog-Input", "channelNumber": "ai31", "units": "psia"}
  ]
}`

func startTestSim(t *testing.T, opts Options) (*Simulator, string) {
	t.Helper()
	sim := New(opts)
	addr, err := sim.Start("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { sim.Close() })
	return sim, addr
}

// ── Handshake + classification ──────────────────────────────────────────────

func TestHandshakeAndClassification(t *testing.T) {
	sim, addr := startTestSim(t, Options{RefDes: "DAQ001"})
	cn := dialSim(t, addr)
	defer cn.conn.Close()
	cn.handshake(testConfig)

	waitForConfig(t, sim)

	channels, rate, ok := sim.Config()
	if !ok {
		t.Fatal("Config() not ready after handshake")
	}
	if rate != 200 {
		t.Errorf("sampleRateHz = %v, want 200", rate)
	}
	if len(channels) != 5 {
		t.Errorf("channels = %d, want 5", len(channels))
	}

	// -CMD suffix and Analog-Output are commands (default 0, echo cmd writes).
	// Everything else is a sensor. See model.go's isCommandChannel doc comment
	// for the protocol gap this heuristic works around.
	for _, refDes := range []string{"OV-05-CMD", "IG-01-CMD", "TRIGGER-01"} {
		v, ok := sim.ChannelValue(refDes)
		if !ok || v != 0 {
			t.Errorf("%s initial value = %v, ok=%v; want 0, true", refDes, v, ok)
		}
	}

	cn.send(map[string]interface{}{"type": "cmd", "refDes": "OV-05-CMD", "value": 1})
	waitFor(t, 3*time.Second, func() bool {
		v, _ := sim.ChannelValue("OV-05-CMD")
		return v == 1
	})

	// OV-05-FB is Digital-IO without a -CMD suffix: classified as a sensor, so
	// a cmd targeting it (which the control node would never legitimately
	// send) still lands as a Set rather than panicking or being dropped.
	if v, ok := sim.ChannelValue("OV-05-FB"); !ok || v != 0 {
		t.Errorf("OV-05-FB = %v, ok=%v; want untouched sensor default 0, true", v, ok)
	}
}

// ── Data streaming ───────────────────────────────────────────────────────────

func TestDataStreaming(t *testing.T) {
	_, addr := startTestSim(t, Options{RefDes: "DAQ001", DataRateOverrideHz: 500})
	cn := dialSim(t, addr)
	defer cn.conn.Close()
	cn.handshake(testConfig)

	m := cn.next(func(m map[string]interface{}) bool { return m["type"] == "data" }, 2*time.Second)
	d, ok := m["d"].(map[string]interface{})
	if !ok {
		t.Fatalf("data frame has no d map: %v", m)
	}
	if _, ok := d["CPT-01"]; !ok {
		t.Errorf("data frame missing CPT-01: %v", d)
	}
}

// ── Entry sequence: order, relative timing, completion ─────────────────────

func TestEntrySequenceOrderAndTiming(t *testing.T) {
	clock := NewFakeClock()
	sim, addr := startTestSim(t, Options{RefDes: "DAQ001", Clock: clock})
	cn := dialSim(t, addr)
	defer cn.conn.Close()
	cn.handshake(testConfig)
	waitForConfig(t, sim)

	su := wireStateUpdate{
		Type:  "state_update",
		State: "autoSequence",
		RunID: 7,
		EntrySequence: []wireStep{
			{TMs: 0, RefDes: "IG-01-CMD", Value: 0},
			{TMs: 500, RefDes: "IG-01-CMD", Value: 1},
			{TMs: 2000, RefDes: "OV-05-CMD", Value: 1},
			{TMs: 3000, RefDes: "OV-05-CMD", Value: 0},
			{TMs: 3000, RefDes: "IG-01-CMD", Value: 0},
		},
		ExitSequence: []wireStep{},
		AbortRules:   []wireAbortRule{},
	}
	cn.send(su)

	m := cn.next(func(m map[string]interface{}) bool { return m["type"] == "sequence_complete" }, 2*time.Second)
	if m["runId"] != float64(7) {
		t.Errorf("sequence_complete runId = %v, want 7", m["runId"])
	}

	log := sim.AppliedLog()
	if len(log) != len(su.EntrySequence) {
		t.Fatalf("applied %d step(s), want %d", len(log), len(su.EntrySequence))
	}
	for i, want := range su.EntrySequence {
		got := log[i]
		if got.RefDes != want.RefDes || got.Value != want.Value || got.TMs != want.TMs {
			t.Errorf("step %d = %+v, want refDes=%s value=%g t_ms=%g", i, got, want.RefDes, want.Value, want.TMs)
		}
		if got.RunID != 7 || got.Phase != "entry" {
			t.Errorf("step %d: runId=%d phase=%s, want 7/entry", i, got.RunID, got.Phase)
		}
	}

	runs := sim.Runs()
	if len(runs) != 1 || runs[0].Outcome != "completed" || runs[0].RunID != 7 {
		t.Errorf("Runs() = %+v, want one completed run with runId 7", runs)
	}
}

// ── Abort: rule trips, exit_sequence runs, abort_triggered sent ────────────

func TestAbortRuleTripsExitSequence(t *testing.T) {
	clock := NewFakeClock()
	sim, addr := startTestSim(t, Options{RefDes: "DAQ001", Clock: clock})
	cn := dialSim(t, addr)
	defer cn.conn.Close()
	cn.handshake(testConfig)
	waitForConfig(t, sim)

	// Pressure already over threshold before the state is entered — the very
	// first scan (t=0, within the rule's window) trips it.
	if !sim.SetSensor("CPT-01", SensorSpec{Base: 900}) {
		t.Fatal("SetSensor(CPT-01) failed — not classified as a sensor?")
	}

	su := wireStateUpdate{
		Type:  "state_update",
		State: "autoSequence",
		RunID: 3,
		EntrySequence: []wireStep{
			{TMs: 0, RefDes: "OV-05-CMD", Value: 1},
			{TMs: 500, RefDes: "IG-01-CMD", Value: 1},
		},
		ExitSequence: []wireStep{
			{TMs: 0, RefDes: "OV-05-CMD", Value: 0},
			{TMs: 0, RefDes: "IG-01-CMD", Value: 0},
		},
		AbortRules: []wireAbortRule{
			{If: "CPT-01 > 850", TMsOn: 0, TMsOff: 20000},
		},
	}
	cn.send(su)

	cn.next(func(m map[string]interface{}) bool { return m["type"] == "abort_triggered" }, 2*time.Second)

	if v, _ := sim.ChannelValue("OV-05-CMD"); v != 0 {
		t.Errorf("OV-05-CMD = %v after abort, want 0 (exit_sequence)", v)
	}
	if v, _ := sim.ChannelValue("IG-01-CMD"); v != 0 {
		t.Errorf("IG-01-CMD = %v after abort, want 0 (exit_sequence)", v)
	}

	runs := sim.Runs()
	if len(runs) != 1 || runs[0].Outcome != "aborted" || runs[0].TrippedIf != "CPT-01 > 850" {
		t.Errorf("Runs() = %+v, want one aborted run tripped by \"CPT-01 > 850\"", runs)
	}

	// The t=0 entry step (mains open) runs before the trip is even possible to
	// observe, but the t=500 igniter step — scheduled after the t=0 abort —
	// must never have run.
	for _, sp := range sim.AppliedLog() {
		if sp.Phase == "entry" && sp.RefDes == "IG-01-CMD" {
			t.Errorf("igniter step %+v applied after an immediate abort", sp)
		}
	}
}

// ── Reconnect: command state and applied log survive a dropped link ────────

func TestReconnectPreservesCommandState(t *testing.T) {
	sim, addr := startTestSim(t, Options{RefDes: "DAQ001"})

	cn1 := dialSim(t, addr)
	cn1.handshake(testConfig)
	waitForConfig(t, sim)
	cn1.send(map[string]interface{}{"type": "cmd", "refDes": "OV-05-CMD", "value": 1})
	waitFor(t, 3*time.Second, func() bool {
		v, _ := sim.ChannelValue("OV-05-CMD")
		return v == 1
	})

	sim.DropConnection()
	cn1.conn.Close()
	waitFor(t, 3*time.Second, func() bool { return !sim.Connected() })

	cn2 := dialSim(t, addr)
	defer cn2.conn.Close()
	cn2.handshake(testConfig)
	waitForConfig(t, sim)

	if v, ok := sim.ChannelValue("OV-05-CMD"); !ok || v != 1 {
		t.Errorf("OV-05-CMD after reconnect = %v, ok=%v; want 1, true (command state should persist, like real hardware)", v, ok)
	}
}

// ── A superseding state_update cancels the previous run ─────────────────────

func TestReEntrySupersedesPreviousRun(t *testing.T) {
	clock := NewFakeClock()
	sim, addr := startTestSim(t, Options{RefDes: "DAQ001", Clock: clock, ScanInterval: time.Millisecond})
	cn := dialSim(t, addr)
	defer cn.conn.Close()
	cn.handshake(testConfig)
	waitForConfig(t, sim)

	first := wireStateUpdate{
		Type: "state_update", State: "autoSequence", RunID: 1,
		EntrySequence: []wireStep{{TMs: 5000, RefDes: "IG-01-CMD", Value: 1}},
	}
	cn.send(first)

	// armRun (called synchronously from the read loop before the run
	// goroutine is spawned) bumps the generation on every state_update, so
	// gen ordering is correct regardless of how the two run goroutines get
	// scheduled — no synchronisation needed between these two sends.
	second := wireStateUpdate{
		Type: "state_update", State: "autoSequence", RunID: 2,
		EntrySequence: []wireStep{{TMs: 0, RefDes: "IG-01-CMD", Value: 0}},
	}
	cn.send(second)

	m := cn.next(func(m map[string]interface{}) bool { return m["type"] == "sequence_complete" }, 2*time.Second)
	if m["runId"] != float64(2) {
		t.Fatalf("sequence_complete runId = %v, want 2 (the superseding run)", m["runId"])
	}

	// The superseded run (runId 1) must never report completion.
	for _, r := range sim.Runs() {
		if r.RunID == 1 {
			t.Errorf("superseded run 1 still recorded an outcome: %+v", r)
		}
	}
}

// ── waitFor: poll with a generous bound, not a synchronisation sleep ───────
// Mirrors controlnode/daqnode's waitFor helper.

// waitForConfig polls until the simulator has processed the handshake config,
// not merely accepted the TCP/WS connection (Connected() goes true before the
// config is read and the model is built).
func waitForConfig(t *testing.T, sim *Simulator) {
	t.Helper()
	waitFor(t, 3*time.Second, func() bool {
		_, _, ok := sim.Config()
		return ok
	})
}

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
