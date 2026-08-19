package webclient

import (
	"controlnode/alerts"
	"controlnode/broker"
	"controlnode/config"
	"controlnode/softchan"
	"controlnode/statemachine"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// This file is the single source of truth for the wire contract between the Go
// control node and the browser WebClient.  Each message the browser parses (see
// the switch in WebClient/js/ws.js) is produced here by its real Go emitter and
// checked for the exact top-level fields the JS reads.  If someone renames a Go
// field (e.g. data "d" → "data"), these tests fail instead of the browser going
// silently dark.  This is the closest we can get to a JS contract test without a
// JavaScript runtime installed.
//
// GET /api/history (item 04) is NOT part of this switch — it is a plain HTTP
// JSON endpoint, not a /ws/data or /ws/ctrl message, so it has no place in the
// per-type table below. Its own contract is covered by history_test.go.

const realConfigDir = "../../config"

// requireType decodes raw and asserts it is a JSON object of the given "type"
// containing every listed field.
func requireType(t *testing.T, label string, raw []byte, typ string, fields ...string) map[string]interface{} {
	t.Helper()
	if raw == nil {
		t.Fatalf("%s: emitter produced nil", label)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: not valid JSON: %v", label, err)
	}
	if m["type"] != typ {
		t.Errorf("%s: type = %v, want %q", label, m["type"], typ)
	}
	for _, f := range fields {
		if _, ok := m[f]; !ok {
			t.Errorf("%s: missing field %q that the WebClient reads", label, f)
		}
	}
	return m
}

// captureBroadcast subscribes, runs fn, and returns the first broadcast message
// matching type typ.
func captureBroadcast(t *testing.T, b *broker.Broker, typ string, fn func()) []byte {
	t.Helper()
	ch, unsub := b.Subscribe()
	defer unsub()
	// Barrier: ensure the subscription is live before triggering.
	waitFor(t, ch, "data", time.Second)
	fn()
	deadline := time.After(time.Second)
	for {
		select {
		case raw := <-ch:
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil && m["type"] == typ {
				return raw
			}
		case <-deadline:
			t.Fatalf("no %q broadcast captured", typ)
			return nil
		}
	}
}

func waitFor(t *testing.T, ch <-chan []byte, typ string, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case raw := <-ch:
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil && m["type"] == typ {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", typ)
		}
	}
}

// TestContractConfigMessages checks the config/state_config/softchan_config
// payloads built from the real config directory.
func TestContractConfigMessages(t *testing.T) {
	cfg, err := config.ParseDir(realConfigDir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	// config — applyConfig reads controls, broadcastRateHz, channelStaleMs.
	cj, err := config.BuildWebClientConfigJSON(cfg)
	if err != nil {
		t.Fatalf("BuildWebClientConfigJSON: %v", err)
	}
	requireType(t, "config", []byte(cj), "config", "controls", "broadcastRateHz", "channelStaleMs")

	// state_config is built from compiled .sm machines, not YAML — see
	// TestContractStateConfig, which drives the real builder.

	// softchan_config — applySoftchanConfig reads channels[] and indexes each
	// entry by refDes (ws.js applySoftchanConfig → softchanConfigMap).
	store, err := softchan.LoadFromDir(realConfigDir+"/channels", realConfigDir+"/softChannelValues.yaml")
	if err != nil {
		t.Fatalf("softchan.LoadFromDir: %v", err)
	}
	scRaw := store.ConfigJSON()
	requireType(t, "softchan_config", scRaw, "softchan_config", "channels")

	// Pin the per-channel field names.  state.js documents softchanConfigMap as
	// { refDes, description, units, role, default, min, max }; computed channels
	// additionally carry computed:true (it is omitempty, so only they have it).
	var scMsg struct {
		Channels []map[string]interface{} `json:"channels"`
	}
	if err := json.Unmarshal(scRaw, &scMsg); err != nil {
		t.Fatalf("softchan_config: %v", err)
	}
	if len(scMsg.Channels) == 0 {
		t.Fatal("softchan_config: no channels — the real config should define some")
	}
	var sawSettable bool
	for _, ch := range scMsg.Channels {
		if computed, _ := ch["computed"].(bool); computed {
			continue
		}
		sawSettable = true
		for _, f := range []string{"refDes", "description", "units", "role", "default", "min", "max"} {
			if _, ok := ch[f]; !ok {
				t.Errorf("softchan_config channel %v: missing field %q that the WebClient reads",
					ch["refDes"], f)
			}
		}
	}
	if !sawSettable {
		t.Error("softchan_config: no settable channel found to pin fields against")
	}

	// The computed flag marks read-only channels the HMI must not offer as
	// settable.  The shipped config has none, so build a store that does.
	dir := t.TempDir()
	src := "channel PT-01\n    type float\n    default 100\n\nchannel PT-AVG\n    units psi\n    compute PT-01 + 0\n"
	if err := os.WriteFile(filepath.Join(dir, "test.chan"), []byte(src), 0644); err != nil {
		t.Fatalf("write .chan: %v", err)
	}
	cstore, err := softchan.LoadFromDir(dir, filepath.Join(dir, "values.yaml"))
	if err != nil {
		t.Fatalf("softchan.LoadFromDir(temp): %v", err)
	}
	var cMsg struct {
		Channels []map[string]interface{} `json:"channels"`
	}
	if err := json.Unmarshal(cstore.ConfigJSON(), &cMsg); err != nil {
		t.Fatalf("softchan_config(temp): %v", err)
	}
	var sawComputed bool
	for _, ch := range cMsg.Channels {
		if ch["refDes"] == "PT-AVG" {
			if computed, _ := ch["computed"].(bool); computed {
				sawComputed = true
			}
		}
	}
	if !sawComputed {
		t.Error("softchan_config: computed channel PT-AVG did not carry computed:true")
	}
}

// smSource is a minimal but complete machine used to drive the real state_config
// builder.  Deliberately mixes operator and non-operator states so the test
// fails if the operator flag is ever hardcoded or inverted.
const smSource = `machine fuelSeq

state safe
    operator
    sequence
        OV-01-CMD = 0

state manualControl
    operator

state autoSequence
    sequence
        OV-01-CMD = 1
        transition abort

state abort
    operator
    sequence
        OV-01-CMD = 0
`

// TestContractStateConfig drives the REAL builder (webclient.BuildStateConfigJSON,
// which main.go calls) with a compiled program and pins every field the browser
// reads: WebClient/js/ws.js applyStateConfig stores machines[] keyed by .name and
// pid.js _updateDaqControlState reads .targetRefDes and .states[].{name,index,operator}.
func TestContractStateConfig(t *testing.T) {
	prog, err := statemachine.Compile(
		[]statemachine.Source{{Name: "fuelseq.sm", Text: smSource}},
		statemachine.Options{KnownChannels: []string{
			"OV-01-CMD", "SM-fuelSeq-STATE", "SM-fuelSeq-TARGET",
		}},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	raw := BuildStateConfigJSON(prog)
	requireType(t, "state_config", raw, "state_config", "machines")

	// Decode with the exact field names the JS reads — a renamed Go tag leaves
	// these zero and the assertions below fail.
	var msg struct {
		Machines []struct {
			Name         string `json:"name"`
			TargetRefDes string `json:"targetRefDes"`
			States       []struct {
				Name     string `json:"name"`
				Index    int    `json:"index"`
				Operator bool   `json:"operator"`
			} `json:"states"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("state_config: %v", err)
	}
	if len(msg.Machines) != 1 {
		t.Fatalf("machines = %d, want 1", len(msg.Machines))
	}
	m := msg.Machines[0]
	if m.Name != "fuelSeq" {
		t.Errorf("machines[0].name = %q, want %q", m.Name, "fuelSeq")
	}
	// pid.js sends the operator's choice to this refDes (sendCommand(targetRefDes, name)).
	if m.TargetRefDes != "SM-fuelSeq-TARGET" {
		t.Errorf("machines[0].targetRefDes = %q, want %q", m.TargetRefDes, "SM-fuelSeq-TARGET")
	}

	want := []struct {
		name     string
		index    int
		operator bool
	}{
		{"safe", 0, true},
		{"manualControl", 1, true},
		{"autoSequence", 2, false},
		{"abort", 3, true},
	}
	if len(m.States) != len(want) {
		t.Fatalf("states = %d, want %d", len(m.States), len(want))
	}
	for i, w := range want {
		got := m.States[i]
		if got.Name != w.name || got.Index != w.index || got.Operator != w.operator {
			t.Errorf("states[%d] = {name:%q index:%d operator:%v}, want {name:%q index:%d operator:%v}",
				i, got.Name, got.Index, got.Operator, w.name, w.index, w.operator)
		}
	}

	// The index is the contract with the SM-<NAME>-STATE data channel: the
	// engine publishes the state's position and pid.js resolves it back to a
	// name through states[].index.  Indexes must be dense and ordered.
	for i, st := range m.States {
		if st.Index != i {
			t.Errorf("states[%d].index = %d — SM-fuelSeq-STATE index lookup would resolve the wrong name", i, st.Index)
		}
	}

	// No machines at all → no message (server.go skips the send).
	if BuildStateConfigJSON(nil) != nil {
		t.Error("BuildStateConfigJSON(nil) should return nil")
	}
}

// smSourceGated is modeled on config/machines/daq001.sm's gating, but
// deliberately leaves manualControl as a bare `operator` (no `from`) so the
// contract test covers both a gated state and an ungated one — it cannot
// pass by hardcoding "from" onto every state.
const smSourceGated = `machine fuelSeq

state safe
    operator from manualControl, abort
    sequence
        OV-01-CMD = 0

state manualControl
    operator

state autoSequence
    operator from manualControl
    sequence
        OV-01-CMD = 1
        transition abort

state abort
    operator from manualControl, autoSequence
    sequence
        OV-01-CMD = 0
`

// TestContractStateConfig_OperatorFrom pins the `from` field state_config
// gained for the `operator from a, b` gate — WebClient/js/pid.js's
// _updateDaqControlState filters the operator dropdown against it. Covers
// both a gated state (safe/autoSequence/abort) and an ungated one
// (manualControl, which must serialize `from` as absent, not `[]`).
func TestContractStateConfig_OperatorFrom(t *testing.T) {
	prog, err := statemachine.Compile(
		[]statemachine.Source{{Name: "fuelseq.sm", Text: smSourceGated}},
		statemachine.Options{KnownChannels: []string{
			"OV-01-CMD", "SM-fuelSeq-STATE", "SM-fuelSeq-TARGET",
		}},
	)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	raw := BuildStateConfigJSON(prog)

	var msg struct {
		Machines []struct {
			Name   string `json:"name"`
			States []struct {
				Name string   `json:"name"`
				From []string `json:"from"`
			} `json:"states"`
		} `json:"machines"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("state_config: %v", err)
	}
	if len(msg.Machines) != 1 {
		t.Fatalf("machines = %d, want 1", len(msg.Machines))
	}

	want := map[string][]string{
		"safe":          {"manualControl", "abort"},
		"manualControl": nil,
		"autoSequence":  {"manualControl"},
		"abort":         {"manualControl", "autoSequence"},
	}
	got := map[string][]string{}
	for _, st := range msg.Machines[0].States {
		got[st.Name] = st.From
	}
	for name, wantFrom := range want {
		gotFrom, ok := got[name]
		if !ok {
			t.Fatalf("state %q missing from state_config", name)
		}
		if len(gotFrom) != len(wantFrom) {
			t.Fatalf("state %q from = %#v, want %#v", name, gotFrom, wantFrom)
		}
		for i := range wantFrom {
			if gotFrom[i] != wantFrom[i] {
				t.Errorf("state %q from[%d] = %q, want %q", name, i, gotFrom[i], wantFrom[i])
			}
		}
	}

	// The ungated state must omit "from" entirely (omitempty), not emit "from":[].
	if !strings.Contains(string(raw), `"name":"manualControl"`) {
		t.Fatalf("state_config missing manualControl: %s", raw)
	}
	idx := strings.Index(string(raw), `"name":"manualControl"`)
	nextComma := strings.IndexAny(string(raw)[idx:], "}")
	segment := string(raw)[idx : idx+nextComma]
	if strings.Contains(segment, `"from"`) {
		t.Errorf("ungated state manualControl serialized a from field: %s", segment)
	}
}

// TestContractStateChange pins the state_change message.  main.go's
// OnStateChange callback publishes exactly these bytes (it calls
// StateChangeJSON), so this asserts the real emission path's payload rather
// than a hand-written literal.
func TestContractStateChange(t *testing.T) {
	raw := StateChangeJSON("fuelSeq", "autoSequence")
	requireType(t, "state_change", raw, "state_change", "machine", "state")

	var msg struct {
		Machine string `json:"machine"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("state_change: %v", err)
	}
	if msg.Machine != "fuelSeq" {
		t.Errorf("machine = %q, want %q", msg.Machine, "fuelSeq")
	}
	// ws.js applyStateChange requires a state NAME string, not an index.
	if msg.State != "autoSequence" {
		t.Errorf("state = %q, want %q (the state NAME, not an index)", msg.State, "autoSequence")
	}

	// And it survives the broker unchanged — the path main.go publishes on.
	b := broker.New(nil, nil, nil)
	go b.Run(50)
	got := captureBroadcast(t, b, "state_change", func() {
		b.Publish(StateChangeJSON("fuelSeq", "abort"))
	})
	var relayed struct {
		Machine string `json:"machine"`
		State   string `json:"state"`
	}
	if err := json.Unmarshal(got, &relayed); err != nil {
		t.Fatalf("relayed state_change: %v", err)
	}
	if relayed.Machine != "fuelSeq" || relayed.State != "abort" {
		t.Errorf("relayed state_change = %+v, want {fuelSeq abort}", relayed)
	}
}

// TestContractPidLayout pins the pid_layout message.  ws.js applyPidLayout
// requires filename and content (it bails without them) and uses name for the
// layout picker label.
func TestContractPidLayout(t *testing.T) {
	raw := PidLayoutJSON("Test Panel", "test_panel.yaml", "name: Test Panel\nobjects: []\n")
	requireType(t, "pid_layout", raw, "pid_layout", "name", "filename", "content")

	var msg struct {
		Name     string `json:"name"`
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("pid_layout: %v", err)
	}
	if msg.Name != "Test Panel" || msg.Filename != "test_panel.yaml" {
		t.Errorf("pid_layout = {name:%q filename:%q}, want {Test Panel test_panel.yaml}", msg.Name, msg.Filename)
	}
	if msg.Content == "" {
		t.Error("pid_layout: content is empty — applyPidLayout would drop the message")
	}
}

// TestContractAlertAcked pins alert_acked, which ws.js dispatches straight to
// ackAlertLocally(msg.id).
func TestContractAlertAcked(t *testing.T) {
	raw := AlertAckedJSON("alert-42")
	requireType(t, "alert_acked", raw, "alert_acked", "id")

	var msg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("alert_acked: %v", err)
	}
	if msg.ID != "alert-42" {
		t.Errorf("id = %q, want %q", msg.ID, "alert-42")
	}
}

// TestContractAlertRegistry pins the server-side alert path introduced with the
// .alert engine: every alert (rule, per-daqNode template, and the server's own
// notices) is raised in ONE registry and reaches the browser as the same `alert`
// / `alert_snapshot` / `alert_acked` messages the WebClient already parses.
// alerts.js ingestAlert reads id/category/message/timestamp/acked off the top
// level of each entry — a renamed Go field would blank the alert bar.
func TestContractAlertRegistry(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(50)

	reg := alerts.NewRegistry()
	// The server is the registry's publisher: constructing it wires the sink.
	s := New(0, `{"type":"config"}`, nil, nil, nil, b, "", nil, nil, nil, nil, reg, nil, nil)
	if s.Alerts() != reg {
		t.Fatal("server should expose the registry it publishes for")
	}

	// A raise from the alert engine (stable id) reaches the browser as `alert`,
	// carrying the channels it concerns. The panel colours objects from that
	// attribution: without it a rule alarm could not be matched to the object it
	// is about, and the object would sit grey while the board showed red.
	raw := captureBroadcast(t, b, "alert", func() {
		reg.RaiseFor("rule:CHAMBER-HIGH", alerts.SeverityAlarm,
			"Chamber pressure high: 500 psia", []string{"CPT-01"}, "", "", nil, nil)
	})
	m := requireType(t, "alert", raw, "alert", "id", "category", "message", "timestamp", "acked", "resolved", "channels")
	// isChannelAlarmed() in alerts.js reads this array by name.
	chans, ok := m["channels"].([]interface{})
	if !ok || len(chans) != 1 || chans[0] != "CPT-01" {
		t.Errorf("alert channels = %v, want [CPT-01]", m["channels"])
	}
	if m["id"] != "rule:CHAMBER-HIGH" {
		t.Errorf("alert id = %v, want the engine's stable id", m["id"])
	}
	if m["category"] != "alarm" {
		t.Errorf("alert category = %v, want alarm", m["category"])
	}
	if m["acked"] != false {
		t.Errorf("a freshly raised alert must arrive un-acked, got %v", m["acked"])
	}
	if m["resolved"] != false {
		t.Errorf("a freshly raised alert must arrive un-resolved, got %v", m["resolved"])
	}

	// A condition RECOVERING is not an ack.  It republishes the row as an
	// `alert` with resolved=true and acked still false, which is how the browser
	// tells "recovered, nobody has looked yet" from "a person has seen this" —
	// and why a latched object stays red after the value comes back.
	resolvedRaw := captureBroadcast(t, b, "alert", func() {
		reg.Resolve("rule:CHAMBER-HIGH")
	})
	resolvedMsg := requireType(t, "alert", resolvedRaw, "alert",
		"id", "category", "message", "timestamp", "acked", "resolved")
	if resolvedMsg["id"] != "rule:CHAMBER-HIGH" {
		t.Errorf("resolved alert id = %v", resolvedMsg["id"])
	}
	if resolvedMsg["resolved"] != true || resolvedMsg["acked"] != false {
		t.Errorf("resolved alert = resolved:%v acked:%v, want true/false",
			resolvedMsg["resolved"], resolvedMsg["acked"])
	}

	// A rule whose author left `latch` off asked for the row to clear itself, so
	// that one path does still ack, over the existing alert_acked message.
	ackRaw := captureBroadcast(t, b, "alert_acked", func() {
		reg.ResolveAndAck("rule:CHAMBER-HIGH")
	})
	ackMsg := requireType(t, "alert_acked", ackRaw, "alert_acked", "id")
	if ackMsg["id"] != "rule:CHAMBER-HIGH" {
		t.Errorf("alert_acked id = %v", ackMsg["id"])
	}

	// alert_snapshot carries the same per-entry fields (ws.js runs every entry
	// through ingestAlert).
	reg.Raise("bad:CPT-01", alerts.SeverityWarning, "CPT-01 out of range: 512.50")
	snap := s.alertSnapshot()
	requireType(t, "alert_snapshot", snap, "alert_snapshot", "alerts")
	var snapMsg struct {
		Alerts []map[string]interface{} `json:"alerts"`
	}
	if err := json.Unmarshal(snap, &snapMsg); err != nil {
		t.Fatalf("alert_snapshot: %v", err)
	}
	if len(snapMsg.Alerts) != 2 {
		t.Fatalf("alert_snapshot entries = %d, want 2", len(snapMsg.Alerts))
	}
	for _, entry := range snapMsg.Alerts {
		for _, f := range []string{"id", "category", "message", "timestamp", "acked", "resolved"} {
			if _, ok := entry[f]; !ok {
				t.Errorf("alert_snapshot entry %v: missing field %q that ingestAlert reads", entry["id"], f)
			}
		}
	}

	// An empty registry sends nothing at all (the 1 Hz broadcaster skips it).
	if AlertSnapshotJSON(nil) != nil {
		t.Error("AlertSnapshotJSON(nil) should return nil, not an empty message")
	}
}

// recordingEngine captures RequestTarget calls made through the
// StateMachineRequester interface the webclient server holds.
type recordingEngine struct {
	calls chan [2]string
	err   error
}

func (e *recordingEngine) RequestTarget(machine, state string) error {
	e.calls <- [2]string{machine, state}
	return e.err
}

// TestContractStateTargetCmd walks the browser→server half of the SM-TARGET
// contract over a real WebSocket: pid.js's dropdown handler calls
// sendCommand(targetRefDes, stateName) with the state NAME as a STRING, and
// that must reach engine.RequestTarget with machine and state split correctly.
func TestContractStateTargetCmd(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(50)
	eng := &recordingEngine{calls: make(chan [2]string, 4)}
	s := New(0, `{"type":"config"}`, nil, nil, nil, b, "", nil, nil, nil, eng, nil, nil, nil)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	defer c.Close()

	// userAuth is nil → any credentials authorize, but the handshake is required.
	mustWrite(t, c, map[string]interface{}{"type": "auth_request", "name": "alice", "pin": "x"})
	if resp := readJSON(t, c); resp["approved"] != true {
		t.Fatalf("auth not approved: %v", resp)
	}

	// Exactly the message sendCommand builds in WebClient/js/ws.js.
	mustWrite(t, c, map[string]interface{}{
		"type":   "cmd",
		"refDes": "SM-fuelSeq-TARGET",
		"value":  "autoSequence",
		"user":   "alice",
	})
	select {
	case got := <-eng.calls:
		if got[0] != "fuelSeq" {
			t.Errorf("RequestTarget machine = %q, want %q", got[0], "fuelSeq")
		}
		if got[1] != "autoSequence" {
			t.Errorf("RequestTarget state = %q, want %q", got[1], "autoSequence")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SM-fuelSeq-TARGET cmd never reached engine.RequestTarget")
	}

	// A numeric value is NOT the contract: it must be rejected, not coerced.
	mustWrite(t, c, map[string]interface{}{
		"type": "cmd", "refDes": "SM-fuelSeq-TARGET", "value": 2, "user": "alice",
	})
	select {
	case got := <-eng.calls:
		t.Fatalf("numeric SM-TARGET value reached the engine as %v — the contract is a state NAME string", got)
	case <-time.After(300 * time.Millisecond):
		// expected: rejected with an alert
	}
}

// TestContractBrokerMessages checks the live-stream messages the broker emits.
func TestContractBrokerMessages(t *testing.T) {
	max := 100.0
	bounds := map[string]broker.ChannelBounds{"PT-01": {Max: &max}}
	b := broker.New(nil, nil, bounds)
	go b.Run(50)

	// data — applyData reads t and d.
	dataMsg := captureBroadcast(t, b, "data", func() {
		b.PublishData(broker.DataEvent{Values: map[string]float64{"PT-02": 1}})
	})
	requireType(t, "data", dataMsg, "data", "t", "d")

	// err — handleDaqError reads t, daqNode, err.
	errMsg := captureBroadcast(t, b, "err", func() {
		b.PublishErr(broker.ErrEvent{DaqRefDes: "DAQ-1", T: 123.0, Err: "boom"})
	})
	requireType(t, "err", errMsg, "err", "t", "daqNode", "err")

	// bad_data — handleBadData reads refDes, value, status, t.
	badMsg := captureBroadcast(t, b, "bad_data", func() {
		b.PublishData(broker.DataEvent{Values: map[string]float64{"PT-01": 150}})
	})
	requireType(t, "bad_data", badMsg, "bad_data", "refDes", "value", "status", "t")

	// bad_data_snapshot — forEach(handleBadData) over channels[].
	// The snapshot is populated by the bad value above; poll briefly.
	var snap []byte
	for i := 0; i < 50; i++ {
		if snap = b.BadDataSnapshot(); snap != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	requireType(t, "bad_data_snapshot", snap, "bad_data_snapshot", "channels")
}

// TestContractServerMessages checks the messages emitted by the webclient server
// itself: alerts and the auth response.
func TestContractServerMessages(t *testing.T) {
	b := broker.New(nil, nil, nil)
	go b.Run(50)
	s := New(0, `{"type":"config"}`, nil, nil, nil, b, "", nil, nil, nil, nil, nil, nil, nil)

	// alert — ingestAlert reads id, category, message, timestamp.
	alertMsg := captureBroadcast(t, b, "alert", func() {
		s.pushAlert("info", "hello")
	})
	requireType(t, "alert", alertMsg, "alert", "id", "category", "message", "timestamp")

	// alert_snapshot — forEach(ingestAlert) over alerts[].
	requireType(t, "alert_snapshot", s.alertSnapshot(), "alert_snapshot", "alerts")

	// auth_response — handleAuthResponse reads approved (and name).
	var authorized bool
	resp := s.handleAuth("test", authRequestMsg{Type: "auth_request", Name: "x", PIN: "y"}, &authorized)
	raw, _ := json.Marshal(resp)
	requireType(t, "auth_response", raw, "auth_response", "approved")

	// state_change — broadcast when a machine enters a new state.  Built by the
	// same function main.go's OnStateChange callback uses, never a literal.
	stateChangeMsg := captureBroadcast(t, b, "state_change", func() {
		b.Publish(StateChangeJSON("fuelSeq", "safe"))
	})
	requireType(t, "state_change", stateChangeMsg, "state_change", "machine", "state")

	// alert_acked — ws.js dispatches this straight to ackAlertLocally(msg.id).
	ackMsg := captureBroadcast(t, b, "alert_acked", func() {
		b.Publish(AlertAckedJSON("ack-1"))
	})
	requireType(t, "alert_acked", ackMsg, "alert_acked", "id")
}
