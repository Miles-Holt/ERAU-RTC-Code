package webclient

import (
	"controlnode/broker"
	"controlnode/config"
	"controlnode/softchan"
	"encoding/json"
	"testing"
	"time"
)

// This file is the single source of truth for the wire contract between the Go
// control node and the browser WebClient.  Each message the browser parses (see
// the switch in WebClient/js/ws.js) is produced here by its real Go emitter and
// checked for the exact top-level fields the JS reads.  If someone renames a Go
// field (e.g. data "d" → "data"), these tests fail instead of the browser going
// silently dark.  This is the closest we can get to a JS contract test without a
// JavaScript runtime installed.

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

	// state_config — applyStateConfig reads daqNodes[]; each needs states +
	// sysTargetStateRefDes so the browser knows what refDes to command.
	if sc := config.BuildStateConfigJSON(cfg.DaqControls); sc != nil {
		m := requireType(t, "state_config", sc, "state_config", "daqNodes")
		nodes, _ := m["daqNodes"].([]interface{})
		if len(nodes) == 0 {
			t.Error("state_config: daqNodes is empty")
		}
		for i, n := range nodes {
			node, _ := n.(map[string]interface{})
			for _, f := range []string{"daqNode", "sysTargetStateRefDes", "states"} {
				if _, ok := node[f]; !ok {
					t.Errorf("state_config daqNodes[%d]: missing %q", i, f)
				}
			}
		}
	}

	// softchan_config — applySoftchanConfig reads channels[].
	store, err := softchan.New(realConfigDir+"/softChannels.yaml", realConfigDir+"/softChannelValues.yaml")
	if err != nil {
		t.Fatalf("softchan.New: %v", err)
	}
	requireType(t, "softchan_config", store.ConfigJSON(), "softchan_config", "channels")
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
	s := New(0, `{"type":"config"}`, nil, nil, nil, b, "", nil, nil, nil)

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
}
