package webclient

import (
	"controlnode/broker"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const webRootDir = "../../WebClient"

// newTestServer builds a Server backed by the real WebClient directory and a
// running broker, wired into an httptest.Server.  Returns the server and its
// base ws:// URL.
func newTestServer(t *testing.T, auth *UserAuthConfig) (*Server, *broker.Broker, *httptest.Server) {
	t.Helper()

	b := broker.New(map[string]string{"OV-01": "DAQ-1"}, nil, nil)
	go b.Run(50)

	configJSON := `{"type":"config","controls":[]}`
	s := New(0, configJSON, nil, nil, nil, b, webRootDir, nil, auth, nil, nil, nil)

	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, b, ts
}

func wsURL(httpURL, path string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + path
}

// TestServeStaticFiles is the WebClient smoke test: index.html must load and
// every script it references must be served with 200.  This catches a missing
// or misnamed JS file before it reaches a browser.
func TestServeStaticFiles(t *testing.T) {
	if _, err := os.Stat(webRootDir); err != nil {
		t.Skipf("WebClient dir not found (%v)", err)
	}
	_, _, ts := newTestServer(t, nil)

	resp, err := http.Get(ts.URL + "/index.html")
	if err != nil {
		t.Fatalf("GET index.html: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET index.html status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	srcRe := regexp.MustCompile(`src="(js/[^"]+)"`)
	matches := srcRe.FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		t.Fatal("index.html referenced no js/ scripts — parsing likely broke")
	}
	for _, m := range matches {
		path := m[1]
		r, err := http.Get(ts.URL + "/" + path)
		if err != nil {
			t.Errorf("GET %s: %v", path, err)
			continue
		}
		r.Body.Close()
		if r.StatusCode != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200", path, r.StatusCode)
		}
	}
}

// readJSON reads one WebSocket text message and decodes it into a generic map.
func readJSON(t *testing.T, c *websocket.Conn) map[string]interface{} {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, raw, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("ws read: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("ws message not JSON: %v", err)
	}
	return m
}

// TestWsDataSendsConfig verifies a client connecting to /ws/data receives the
// config message first.
func TestWsDataSendsConfig(t *testing.T) {
	_, _, ts := newTestServer(t, nil)

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/data"), nil)
	if err != nil {
		t.Fatalf("dial /ws/data: %v", err)
	}
	defer c.Close()

	m := readJSON(t, c)
	if m["type"] != "config" {
		t.Fatalf("first message type = %v, want config", m["type"])
	}
}

// TestWsCtrlAuthAndCmd walks the authenticated control path end-to-end:
// auth_request → auth_response → cmd → routed to the DAQ node's channel.
func TestWsCtrlAuthAndCmd(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	_, b, ts := newTestServer(t, auth)

	// Register a DAQ cmd channel so the broker can route the command.
	daqCh := make(chan []byte, 4)
	b.RegisterDaq("DAQ-1", daqCh)
	time.Sleep(20 * time.Millisecond)

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	defer c.Close()

	// Bad credentials are rejected.
	mustWrite(t, c, map[string]interface{}{"type": "auth_request", "name": "alice", "pin": "wrong"})
	if resp := readJSON(t, c); resp["approved"] != false {
		t.Fatalf("expected rejection, got %v", resp)
	}

	// Good credentials are approved.
	mustWrite(t, c, map[string]interface{}{"type": "auth_request", "name": "alice", "pin": "1234"})
	if resp := readJSON(t, c); resp["approved"] != true {
		t.Fatalf("expected approval, got %v", resp)
	}

	// A command from the authorized client is routed to the DAQ channel.
	mustWrite(t, c, map[string]interface{}{"type": "cmd", "refDes": "OV-01", "value": true})
	select {
	case raw := <-daqCh:
		var cmd struct {
			RefDes string `json:"refDes"`
		}
		if json.Unmarshal(raw, &cmd) != nil || cmd.RefDes != "OV-01" {
			t.Fatalf("routed cmd = %s, want refDes OV-01", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("command was not routed to DAQ channel")
	}
}

// TestWsCtrlCmdRejectedWhenUnauthorized ensures commands sent before auth are
// dropped (never reach the DAQ channel).
func TestWsCtrlCmdRejectedWhenUnauthorized(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	_, b, ts := newTestServer(t, auth)

	daqCh := make(chan []byte, 4)
	b.RegisterDaq("DAQ-1", daqCh)
	time.Sleep(20 * time.Millisecond)

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	defer c.Close()

	mustWrite(t, c, map[string]interface{}{"type": "cmd", "refDes": "OV-01", "value": true})
	select {
	case raw := <-daqCh:
		t.Fatalf("unauthorized command was routed: %s", raw)
	case <-time.After(250 * time.Millisecond):
		// expected: nothing routed
	}
}

func mustWrite(t *testing.T, c *websocket.Conn, v interface{}) {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatalf("ws write: %v", err)
	}
}
