// Package webclient implements the WebSocket server that browser clients connect to.
package webclient

import (
	"controlnode/alerts"
	"controlnode/broker"
	"controlnode/statemachine"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// StateMachineRequester is the interface the webclient needs from the state machine engine.
type StateMachineRequester interface {
	RequestTarget(machine, state string) error
}

// marshalOrLog JSON-marshals v, logging and returning nil on failure instead
// of propagating the error — every message builder below is best-effort: a
// bad message is dropped rather than taking the connection down. what names
// the message kind in the log line.
func marshalOrLog(v interface{}, what string) []byte {
	payload, err := json.Marshal(v)
	if err != nil {
		log.Printf("webclient: marshal %s: %v", what, err)
		return nil
	}
	return payload
}

// =============================================================================
// Browser protocol builders
// =============================================================================
//
// Every browser-bound message whose shape the WebClient depends on is built by
// one of the functions below.  main.go calls them for the live system and
// protocol_test.go calls the same functions, so a renamed Go field breaks the
// contract test instead of silently blanking a widget in the browser.

// stateConfigState is one entry of machines[].states[] in the state_config
// message.  Read by WebClient/js/ws.js (applyStateConfig) and
// WebClient/js/pid.js (_updateDaqControlState).
type stateConfigState struct {
	Name     string `json:"name"`
	Index    int    `json:"index"`
	Operator bool   `json:"operator"`

	// From is the `operator from a, b` gate list: the states this one may be
	// commanded from.  The key is OMITTED entirely for an ungated state (a
	// state commandable from anywhere), so the browser's test is simply
	// "no from ⇒ always offer it".  The server still re-checks every request —
	// this list only keeps illegal targets out of the operator's dropdown.
	From []string `json:"from,omitempty"`
}

// stateConfigMachine is one entry of machines[] in the state_config message.
type stateConfigMachine struct {
	Name         string             `json:"name"`
	TargetRefDes string             `json:"targetRefDes"`
	States       []stateConfigState `json:"states"`
}

type stateConfigMsg struct {
	Type     string               `json:"type"`
	Machines []stateConfigMachine `json:"machines"`
}

// BuildStateConfigJSON builds the state_config message from compiled machines.
// It lists every machine with its states and its SM-<NAME>-TARGET refDes.
// Returns nil when no machines are loaded (the server then sends nothing).
func BuildStateConfigJSON(prog *statemachine.Program) []byte {
	if prog == nil || len(prog.Machines) == 0 {
		return nil
	}

	msg := stateConfigMsg{Type: "state_config"}
	for _, m := range prog.Machines {
		mc := stateConfigMachine{
			Name:         m.Name,
			TargetRefDes: "SM-" + m.Name + "-TARGET",
			States:       []stateConfigState{},
		}
		for _, st := range m.States {
			mc.States = append(mc.States, stateConfigState{
				Name:     st.Name,
				Index:    st.Index,
				Operator: st.Operator,
				From:     st.OperatorFrom(),
			})
		}
		msg.Machines = append(msg.Machines, mc)
	}

	return marshalOrLog(msg, "state_config")
}

// stateChangeMsg is the authoritative "machine X is now in state Y" broadcast.
// Read by WebClient/js/ws.js (applyStateChange).
type stateChangeMsg struct {
	Type    string `json:"type"`
	Machine string `json:"machine"`
	State   string `json:"state"`
}

// StateChangeJSON builds the state_change broadcast the engine's OnStateChange
// callback publishes.  The state is the state NAME (the numeric
// SM-<NAME>-STATE channel carries the index on the data path).
func StateChangeJSON(machine, state string) []byte {
	return marshalOrLog(stateChangeMsg{Type: "state_change", Machine: machine, State: state}, "state_change")
}

// pidLayoutMsg carries one front-panel layout file to the browser.
// Read by WebClient/js/ws.js (applyPidLayout).
type pidLayoutMsg struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// PidLayoutJSON builds a pid_layout message.  Used both when panels are loaded
// from disk at startup and when an operator saves a layout over /ws/ctrl.
func PidLayoutJSON(name, filename, content string) []byte {
	return marshalOrLog(pidLayoutMsg{
		Type: "pid_layout", Name: name, Filename: filename, Content: content,
	}, fmt.Sprintf("pid_layout %q", filename))
}

// alertAckedMsg tells every browser an alert was acknowledged.
// Read by WebClient/js/ws.js (ackAlertLocally(msg.id)).
type alertAckedMsg struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// AlertAckedJSON builds the alert_acked broadcast.  It means a person has
// acknowledged the alert — or, for a rule whose author left `latch` off, that the
// server acked on their behalf because the config asked the row to clear itself.
//
// A condition merely RECOVERING no longer comes through here.  That is a
// resolved-but-unacked row, published as an `alert` carrying resolved=true, so a
// spike that came back down on its own stays red until somebody looks at it.
func AlertAckedJSON(id string) []byte {
	return marshalOrLog(alertAckedMsg{Type: "alert_acked", ID: id}, "alert_acked")
}

// alertMsg is one alert row pushed to the browser.  The record fields are
// inlined at the top level (id/category/message/timestamp/acked/resolved)
// because WebClient/js/alerts.js ingestAlert reads them straight off the
// message.  `resolved` distinguishes "the condition recovered but nobody has
// acknowledged it" from "a person has seen this", which the panel needs in order
// to keep a latched object red without pretending the value is still bad.
type alertMsg struct {
	Type string `json:"type"`
	alerts.Record
}

// AlertJSON builds the `alert` broadcast for one registry record.
func AlertJSON(rec alerts.Record) []byte {
	return marshalOrLog(alertMsg{Type: "alert", Record: rec}, "alert")
}

// alertSnapshotMsg is the full alert list, sent on connect and once a second.
type alertSnapshotMsg struct {
	Type   string          `json:"type"`
	Alerts []alerts.Record `json:"alerts"`
}

// AlertSnapshotJSON builds the alert_snapshot broadcast, or nil when there is
// nothing to send (the server then skips the send entirely).
func AlertSnapshotJSON(recs []alerts.Record) []byte {
	if len(recs) == 0 {
		return nil
	}
	return marshalOrLog(alertSnapshotMsg{Type: "alert_snapshot", Alerts: recs}, "alert_snapshot")
}

var upgrader = websocket.Upgrader{
	// Allow all origins — this runs on a private LAN with no cross-origin concerns.
	CheckOrigin: func(r *http.Request) bool { return true },
	// permessage-deflate: telemetry frames are highly repetitive JSON (refDes keys
	// repeated every tick), so compression is a large byte-savings on the browser link.
	EnableCompression: true,
}

// Server listens for browser WebSocket connections on the configured port.
// It also serves static files from webRoot for plain HTTP requests.
type Server struct {
	port               int
	configJSON         []byte
	softchanConfigJSON []byte // softchan_config message; nil if no software channels
	stateConfigJSON    []byte // state_config message; nil if no DAQ control configs
	b                  *broker.Broker
	fileServer         http.Handler
	userAuth           *UserAuthConfig
	layoutPaths        map[string]string     // filename → absolute disk path (immutable)
	engine             StateMachineRequester // state machine engine, or nil if not running

	// alerts is THE server-side alert registry.  Rule alerts, per-daqNode
	// template alerts and the server's own notices (layout saved, rejected
	// state-machine target) all live in it, and this server is its publisher.
	alerts *alerts.Registry

	// docs holds the compiled configuration the /docs pages are rendered from.
	// It is set once at startup (SetDocs) and read-only afterwards; nil in tests
	// that do not exercise the documentation route.
	docs *DocsInput

	mu            sync.RWMutex
	panelMessages [][]byte // pid_layout messages; updated when a layout is saved
}

// PublishAlert implements alerts.Sink: broadcast one alert row.
func (s *Server) PublishAlert(rec alerts.Record) {
	if payload := AlertJSON(rec); payload != nil {
		s.b.Publish(payload)
	}
}

// PublishAlertAcked implements alerts.Sink: broadcast an ack/resolve.
func (s *Server) PublishAlertAcked(id string) {
	if payload := AlertAckedJSON(id); payload != nil {
		s.b.Publish(payload)
	}
}

// Alerts returns the registry the server publishes for, so the wiring layer can
// hand it to the alert engine.
func (s *Server) Alerts() *alerts.Registry { return s.alerts }

// New creates a Server.
// layoutPaths maps layout filename (e.g. "test_panel.yaml") → absolute path on disk.
// Pass userAuth=nil to disable authentication (any credentials are accepted).
// Pass softchanConfigJSON=nil if there are no software channels.
// engine may be nil if no state machine engine is running.
// alertRegistry may be nil, in which case the server creates its own (tests, and
// any deployment without .alert config); either way the server is the registry's
// publisher, so every alert reaches the browser through the same path.
func New(port int, configJSON string, softchanConfigJSON []byte, stateConfigJSON []byte,
	panelMessages [][]byte, b *broker.Broker, webRoot string, embedded fs.FS,
	userAuth *UserAuthConfig, layoutPaths map[string]string, engine StateMachineRequester,
	alertRegistry *alerts.Registry) *Server {

	var fsh http.Handler
	if webRoot != "" {
		fsh = http.FileServer(http.Dir(webRoot))
	} else if embedded != nil {
		fsh = http.FileServer(http.FS(embedded))
	}
	if alertRegistry == nil {
		alertRegistry = alerts.NewRegistry()
	}
	s := &Server{
		port:               port,
		configJSON:         []byte(configJSON),
		softchanConfigJSON: softchanConfigJSON,
		stateConfigJSON:    stateConfigJSON,
		panelMessages:      panelMessages,
		b:                  b,
		fileServer:         fsh,
		userAuth:           userAuth,
		layoutPaths:        layoutPaths,
		engine:             engine,
		alerts:             alertRegistry,
	}
	alertRegistry.SetSink(s)
	return s
}

// Handler builds the HTTP mux (WebSocket endpoints + static file serving).
// It is used by ListenAndServe and is exported so tests can drive the exact
// production routing via httptest without binding a port.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/data", s.ServeWsData)
	mux.HandleFunc("/ws/ctrl", s.ServeWsCtrl)
	// /docs is read-only reference material generated from the loaded config;
	// it carries no operational authority, so it needs no auth (same as the
	// WebClient itself on this closed LAN).
	mux.HandleFunc("/docs", s.handleDocs)
	mux.HandleFunc("/docs/", s.handleDocs)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// ListenAndServe starts the HTTP/WebSocket server.  Blocks until the process exits.
func (s *Server) ListenAndServe() error {
	// Broadcast active alert list to all data subscribers at 1 Hz so clients
	// that dismissed an alert locally will see it re-appear if it is still active.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			if snap := s.alertSnapshot(); snap != nil {
				s.b.Publish(snap)
			}
		}
	}()

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("webclient: listening on http://0.0.0.0%s", addr)
	return http.ListenAndServe(addr, s.Handler())
}

// handleStatic serves embedded/directory static files for non-WS requests.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if s.fileServer != nil {
		s.fileServer.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// =============================================================================
// /ws/data — anonymous, server→client only
// =============================================================================

// ServeWsData upgrades to WebSocket and streams config, layouts, alerts, and
// live data to the client.  The client never sends messages on this connection.
// writeOrClose writes payload to conn as a text message, logging "send
// <what> to <addr>: <err>" and returning false on failure. what names the
// message kind in the log line; callers bail out (closing the connection via
// defer) when it returns false.
func (s *Server) writeOrClose(conn *websocket.Conn, addr, what string, payload []byte) bool {
	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		log.Printf("webclient data: send %s to %s: %v", what, addr, err)
		return false
	}
	return true
}

func (s *Server) ServeWsData(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("webclient data: upgrade error from %s: %v", r.RemoteAddr, err)
		return
	}
	defer conn.Close()
	log.Printf("webclient data: connected %s", r.RemoteAddr)

	// Send config.
	if !s.writeOrClose(conn, r.RemoteAddr, "config", s.configJSON) {
		return
	}

	// Send software channel config (if any).
	if s.softchanConfigJSON != nil {
		if !s.writeOrClose(conn, r.RemoteAddr, "softchan_config", s.softchanConfigJSON) {
			return
		}
	}

	// Send DAQ control state machine config (if any).
	if s.stateConfigJSON != nil {
		if !s.writeOrClose(conn, r.RemoteAddr, "state_config", s.stateConfigJSON) {
			return
		}
	}

	// Send panel layout messages.
	s.mu.RLock()
	panels := make([][]byte, len(s.panelMessages))
	copy(panels, s.panelMessages)
	s.mu.RUnlock()
	for _, msg := range panels {
		if !s.writeOrClose(conn, r.RemoteAddr, "layout", msg) {
			return
		}
	}

	// Subscribe BEFORE sending the snapshots below.
	//
	// This used to sit after them, which opened a window: a broadcast landing
	// between the last snapshot write and Subscribe() was fanned out to a
	// subscriber set that did not yet contain this connection, so the client
	// never received it and had no way to know. The snapshot describes state at
	// the moment it was built, so anything raised microseconds later was simply
	// lost — for an alert on a control panel, silently.
	//
	// Subscribing first cannot lose a message; at worst the client sees a change
	// twice, once in the snapshot and once as a broadcast. Alerts are keyed by
	// id and bad_data is idempotent, so a duplicate is harmless where a drop is
	// not. It also removes a genuine test flake: nothing a client could observe
	// proved registration had happened, so a test that dialled and immediately
	// triggered a broadcast raced the handler goroutine.
	broadcastCh, unsub := s.b.Subscribe()
	defer unsub()

	// Send alert snapshot so the client sees existing alerts immediately.
	if snap := s.alertSnapshot(); snap != nil {
		if !s.writeOrClose(conn, r.RemoteAddr, "alert snapshot", snap) {
			return
		}
	}

	// Send bad-data snapshot so the client sees any currently out-of-range channels.
	if snap := s.b.BadDataSnapshot(); snap != nil {
		if !s.writeOrClose(conn, r.RemoteAddr, "bad_data snapshot", snap) {
			return
		}
	}

	errCh := make(chan error, 1)
	go func() {
		// Drain any stray client messages (should be none on the data WS).
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				errCh <- err
				return
			}
		}
	}()

	for {
		select {
		case msg, ok := <-broadcastCh:
			if !ok {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("webclient data: write to %s: %v", r.RemoteAddr, err)
				return
			}
		case <-errCh:
			log.Printf("webclient data: disconnected %s", r.RemoteAddr)
			return
		}
	}
}

// =============================================================================
// /ws/ctrl — authenticated, bidirectional
// =============================================================================

// ServeWsCtrl upgrades to WebSocket and handles authenticated control messages:
// auth_request, cmd, ack_alert, set_layout.
func (s *Server) ServeWsCtrl(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("webclient ctrl: upgrade error from %s: %v", r.RemoteAddr, err)
		return
	}
	defer conn.Close()
	log.Printf("webclient ctrl: connected %s", r.RemoteAddr)

	var authorized bool
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.Printf("webclient ctrl: disconnected %s: %v", r.RemoteAddr, err)
			return
		}

		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			log.Printf("webclient ctrl %s: bad JSON: %v", r.RemoteAddr, err)
			continue
		}

		switch peek.Type {
		case "auth_request":
			var req authRequestMsg
			if err := json.Unmarshal(raw, &req); err != nil {
				log.Printf("webclient ctrl %s: bad auth_request: %v", r.RemoteAddr, err)
				continue
			}
			resp := s.handleAuth(r.RemoteAddr, req, &authorized)
			b, _ := json.Marshal(resp)
			if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
				log.Printf("webclient ctrl %s: write auth_response: %v", r.RemoteAddr, err)
				return
			}

		case "cmd":
			if !authorized {
				log.Printf("webclient ctrl %s: rejected cmd from unauthorized client", r.RemoteAddr)
				continue
			}
			var cmd broker.CmdMsg
			if err := json.Unmarshal(raw, &cmd); err != nil {
				log.Printf("webclient ctrl %s: bad cmd JSON: %v", r.RemoteAddr, err)
				continue
			}

			// Check if this is a state machine target request (SM-<NAME>-TARGET)
			if strings.HasPrefix(cmd.RefDes, "SM-") && strings.HasSuffix(cmd.RefDes, "-TARGET") && s.engine != nil {
				s.handleStateMachineTarget(cmd)
			} else {
				s.b.SendCmd(cmd)
			}

		case "ack_alert":
			if !authorized {
				log.Printf("webclient ctrl %s: rejected ack_alert from unauthorized client", r.RemoteAddr)
				continue
			}
			var req struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &req); err != nil || req.ID == "" {
				continue
			}
			// The registry broadcasts alert_acked through PublishAlertAcked; an
			// unknown id is still relayed so a client holding a stale row locally
			// can clear it.
			if !s.alerts.Ack(req.ID) {
				log.Printf("webclient ctrl %s: ack for unknown alert %q relayed anyway", r.RemoteAddr, req.ID)
			}

		case "set_layout":
			if !authorized {
				log.Printf("webclient ctrl %s: rejected set_layout from unauthorized client", r.RemoteAddr)
				continue
			}
			var req struct {
				Filename string `json:"filename"`
				Content  string `json:"content"`
				User     string `json:"user"`
			}
			if err := json.Unmarshal(raw, &req); err != nil || req.Filename == "" {
				log.Printf("webclient ctrl %s: bad set_layout: %v", r.RemoteAddr, err)
				continue
			}
			absPath, ok := s.layoutPaths[req.Filename]
			if !ok {
				log.Printf("webclient ctrl %s: set_layout unknown filename %q", r.RemoteAddr, req.Filename)
				continue
			}
			if err := os.WriteFile(absPath, []byte(req.Content), 0644); err != nil {
				log.Printf("webclient ctrl %s: set_layout write %s: %v", r.RemoteAddr, absPath, err)
				continue
			}
			// Re-publish through the same builder used at startup, preserving the
			// panel's display name so the browser's layout picker keeps its label.
			s.mu.Lock()
			name := req.Filename
			idx := -1
			for i, pm := range s.panelMessages {
				var p struct {
					Filename string `json:"filename"`
					Name     string `json:"name"`
				}
				if json.Unmarshal(pm, &p) == nil && p.Filename == req.Filename {
					if p.Name != "" {
						name = p.Name
					}
					idx = i
					break
				}
			}
			payload := PidLayoutJSON(name, req.Filename, req.Content)
			if idx >= 0 {
				s.panelMessages[idx] = payload
			}
			s.mu.Unlock()
			s.b.Publish(payload)
			user := req.User
			if user == "" {
				user = r.RemoteAddr
			}
			s.pushAlert("info", fmt.Sprintf("Layout %q updated by %s", req.Filename, user))
			log.Printf("webclient ctrl %s: saved and broadcast layout %q", r.RemoteAddr, req.Filename)

		default:
			log.Printf("webclient ctrl %s: unexpected message type %q", r.RemoteAddr, peek.Type)
		}
	}
}

// =============================================================================
// Shared helpers
// =============================================================================

// handleAuth validates an auth_request and updates the authorized flag.
func (s *Server) handleAuth(addr string, req authRequestMsg, authorized *bool) authResponseMsg {
	if s.userAuth == nil || s.userAuth.Validate(req.Name, req.PIN) {
		*authorized = true
		log.Printf("webclient %s: authenticated as %q", addr, req.Name)
		return authResponseMsg{Type: "auth_response", Approved: true, Name: req.Name}
	}
	log.Printf("webclient %s: auth failed for %q", addr, req.Name)
	return authResponseMsg{Type: "auth_response", Approved: false, Reason: "Invalid credentials"}
}

// handleStateMachineTarget routes a state machine target request (SM-<NAME>-TARGET)
// to the engine.RequestTarget method.
func (s *Server) handleStateMachineTarget(cmd broker.CmdMsg) {
	if s.engine == nil {
		s.pushAlert("warning", "State machine target rejected: no engine running")
		return
	}

	// Extract machine name from refDes: SM-<NAME>-TARGET → <NAME>
	machineName := cmd.RefDes[3 : len(cmd.RefDes)-7] // remove "SM-" and "-TARGET"

	// Get the target state from the value (must be a string state name, not a number)
	targetState, ok := cmd.Value.(string)
	if !ok {
		s.pushAlert("warning", fmt.Sprintf("SM-%s-TARGET: state name must be a string, not %T", machineName, cmd.Value))
		return
	}
	if targetState == "" {
		s.pushAlert("warning", fmt.Sprintf("SM-%s-TARGET: state name cannot be empty", machineName))
		return
	}

	// Call engine.RequestTarget via the interface
	if err := s.engine.RequestTarget(machineName, targetState); err != nil {
		s.pushAlert("warning", fmt.Sprintf("SM-%s: %v", machineName, err))
		return
	}
	log.Printf("webclient: state machine %q transitioned to %q", machineName, targetState)
}

// pushAlert raises a one-off server notice (layout saved, rejected operator
// request) through the shared registry, which broadcasts it via PublishAlert.
func (s *Server) pushAlert(category, message string) {
	s.alerts.Push(category, message)
}

// alertSnapshot returns a single alert_snapshot JSON message containing all
// current alerts, or nil if there are none.
func (s *Server) alertSnapshot() []byte {
	return AlertSnapshotJSON(s.alerts.Snapshot())
}
