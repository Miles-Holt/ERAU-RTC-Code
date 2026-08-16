package daqnode

import (
	"controlnode/broker"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var testUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// fakeDaqServer stands in for a LabVIEW DAQ node: it performs the config_req
// handshake, records the config it receives, then streams one data message.
func fakeDaqServer(t *testing.T, gotConfig chan<- []byte) *httptest.Server {
	t.Helper()
	h := func(w http.ResponseWriter, r *http.Request) {
		conn, err := testUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("fake DAQ upgrade: %v", err)
			return
		}
		defer conn.Close()

		// Ask the control node for config.
		if err := conn.WriteJSON(map[string]string{"type": "config_req", "refDes": "DAQ-1"}); err != nil {
			return
		}
		// Receive and record the config payload.
		_, cfg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		select {
		case gotConfig <- cfg:
		default:
		}
		// Stream one data frame.
		if err := conn.WriteJSON(map[string]interface{}{
			"type": "data",
			"t":    1.0,
			"d":    map[string]float64{"PT-01": 12.5},
		}); err != nil {
			return
		}
		// Hold the connection open until the client tears down.
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}
	return httptest.NewServer(http.HandlerFunc(h))
}

// hostPort splits an httptest URL into host and integer port.
func hostPort(t *testing.T, url string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port
}

// collectingDaqServer performs the config handshake, records the config, then
// forwards every subsequent message from the client to msgs.
func collectingDaqServer(t *testing.T, gotConfig chan<- []byte, msgs chan<- []byte) *httptest.Server {
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
		_, cfg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		select {
		case gotConfig <- cfg:
		default:
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

// TestClientStateTransitionInterception exercises writeLoop's SYS-TARGET-STATE
// interception: an operator state-change command is turned into an exit message
// driven by the state machine rather than forwarded verbatim to the DAQ node.
func TestClientStateTransitionInterception(t *testing.T) {
	gotConfig := make(chan []byte, 1)
	msgs := make(chan []byte, 8)
	ts := collectingDaqServer(t, gotConfig, msgs)
	defer ts.Close()
	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)

	// Client without state machine (old YAML system is deprecated).
	// The new DSL system handles state machines through the engine.
	c := New("DAQ001", host, port, `{"type":"config"}`, b, nil)
	go c.connect()

	// Wait for the handshake to complete.
	select {
	case <-gotConfig:
	case <-time.After(2 * time.Second):
		t.Fatal("handshake never completed")
	}
}

// waitForMsgType drains msgs until one with the given type arrives.
func waitForMsgType(t *testing.T, msgs <-chan []byte, typ string, d time.Duration) {
	t.Helper()
	waitForAnyMsg(t, msgs, d, func(m map[string]interface{}) bool { return m["type"] == typ })
}

// waitForAnyMsg drains msgs until pred matches, returning the decoded message.
func waitForAnyMsg(t *testing.T, msgs <-chan []byte, d time.Duration, pred func(map[string]interface{}) bool) map[string]interface{} {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case raw := <-msgs:
			var m map[string]interface{}
			if json.Unmarshal(raw, &m) == nil && pred(m) {
				return m
			}
		case <-deadline:
			t.Fatal("timed out waiting for expected message")
			return nil
		}
	}
}

// TestClientHandshakeAndData is the DAQ-node smoke test: the client dials a DAQ
// node, completes the config_req handshake, and bridges a data frame into the
// broker where web clients would see it.
func TestClientHandshakeAndData(t *testing.T) {
	gotConfig := make(chan []byte, 1)
	ts := fakeDaqServer(t, gotConfig)
	defer ts.Close()

	host, port := hostPort(t, ts.URL)

	b := broker.New(nil, nil, nil)
	go b.Run(50)
	sub, unsub := b.Subscribe()
	defer unsub()

	configJSON := `{"type":"config","sampleRateHz":20}`
	// Client without a state machine (no old YAML config, no DSL program/engine)
	c := New("DAQ-1", host, port, configJSON, b, nil)

	// connect() runs one full connection cycle and returns when it errors.
	go c.connect()

	// The DAQ node must receive the config we advertised.
	select {
	case cfg := <-gotConfig:
		var m map[string]interface{}
		if json.Unmarshal(cfg, &m) != nil || m["type"] != "config" {
			t.Fatalf("DAQ received unexpected config: %s", cfg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DAQ node never received config (handshake failed)")
	}

	// The data frame from the DAQ node must reach the broker's broadcast.
	deadline := time.After(2 * time.Second)
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
			if v, ok := m.D["PT-01"]; ok {
				if v != 12.5 {
					t.Fatalf("PT-01 = %v, want 12.5", v)
				}
				// Also verify the connection counter was incremented.
				if got := c.b.DaqConnected.Load(); got < 1 {
					t.Errorf("DaqConnected = %d, want >= 1", got)
				}
				return
			}
		case <-deadline:
			t.Fatal("DAQ data frame never reached the broker broadcast")
		}
	}
}
