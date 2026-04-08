package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const writeTimeout = 5 * time.Second

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server is the WebSocket server that accepts connections from the control node.
type Server struct {
	refDes   string
	port     int
	driver   Driver
	commander *Commander

	// channels shared with subsystems
	dataCh   chan []byte
	sampleCh chan map[string]float64
	smOutCh  chan []byte
	smStopCh chan struct{}
	acqStopCh chan struct{}

	sm *stateMachine
}

func newServer(refDes string, port int, driver Driver) *Server {
	return &Server{
		refDes:  refDes,
		port:    port,
		driver:  driver,
	}
}

// ListenAndServe starts the HTTP server and blocks forever.
func (s *Server) ListenAndServe() error {
	s.commander = newCommander(s.driver)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleWS)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("server: listening on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// handleWS upgrades the HTTP connection to WebSocket and runs the session.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("server: upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	log.Printf("server: control node connected from %s", r.RemoteAddr)

	// Stop any existing acquisition/state machine from a previous connection
	s.stopSubsystems()

	if err := s.runSession(conn); err != nil {
		log.Printf("server: session ended: %v", err)
	}

	s.stopSubsystems()
	log.Printf("server: control node disconnected")
}

// runSession runs the full handshake and then the read/write loops for one connection.
func (s *Server) runSession(conn *websocket.Conn) error {
	// ── Handshake: send config_req, receive config ───────────────────────────
	if err := s.writeMsg(conn, msgConfigReq(s.refDes)); err != nil {
		return fmt.Errorf("send config_req: %w", err)
	}
	log.Printf("server: sent config_req (refDes=%s)", s.refDes)

	_, raw, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	var cfg ConfigMsg
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if cfg.Type != "config" {
		return fmt.Errorf("expected config message, got type=%q", cfg.Type)
	}
	log.Printf("server: received config: %d channels, %d Hz sample rate",
		len(cfg.Channels), cfg.SampleRateHz)

	// Configure and start the driver
	if err := s.driver.Configure(&cfg); err != nil {
		return fmt.Errorf("driver configure: %w", err)
	}
	if err := s.driver.Start(); err != nil {
		return fmt.Errorf("driver start: %w", err)
	}

	// Start subsystems
	s.dataCh = make(chan []byte, 16)
	s.sampleCh = make(chan map[string]float64, 4)
	s.smOutCh = make(chan []byte, 16)
	s.smStopCh = make(chan struct{})
	s.acqStopCh = make(chan struct{})

	broadcastHz := cfg.SampleRateHz / 20 // default ~50 Hz
	if broadcastHz <= 0 {
		broadcastHz = 50
	}

	acq := newAcqLoop(s.driver, cfg.SampleRateHz, broadcastHz, s.dataCh, s.sampleCh, s.acqStopCh)
	go acq.Run()

	s.sm = newStateMachine(s.commander, s.smOutCh, s.sampleCh, s.smStopCh)
	go s.sm.Run()

	// The control node may immediately send a state_update after config.
	// Signal that we're ready to receive it by reading in the loops below.
	// Per protocol: control node sends initial state_update right after config
	// (see controlnode/daqnode/client.go:108-121). We don't need to send state_req first.

	log.Printf("server: session active")

	// ── Concurrent read and write loops ──────────────────────────────────────
	errCh := make(chan error, 2)
	go s.readLoop(conn, errCh)
	go s.writeLoop(conn, errCh)
	return <-errCh
}

// readLoop reads messages from the control node and dispatches them.
func (s *Server) readLoop(conn *websocket.Conn, errCh chan<- error) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read: %w", err)
			return
		}

		msgType := parseType(raw)
		switch msgType {
		case "config":
			// Re-configuration: not supported mid-session; log and ignore.
			log.Printf("server: received config mid-session (ignored)")

		case "cmd":
			var msg CmdMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("server: bad cmd JSON: %v", err)
				continue
			}
			s.commander.HandleCmd(msg)

		case "state_update":
			var msg StateUpdateMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("server: bad state_update JSON: %v", err)
				continue
			}
			if s.sm != nil {
				s.sm.HandleStateUpdate(msg)
			}

		case "exit", "hard_exit":
			var msg ExitMsg
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("server: bad exit JSON: %v", err)
				continue
			}
			if s.sm != nil {
				s.sm.HandleExit(msg)
			}

		default:
			log.Printf("server: unknown message type %q", msgType)
		}
	}
}

// writeLoop forwards data and state machine messages to the control node.
// It is the only goroutine that writes to the WebSocket connection.
func (s *Server) writeLoop(conn *websocket.Conn, errCh chan<- error) {
	for {
		var payload []byte
		select {
		case p := <-s.dataCh:
			payload = p
		case p := <-s.smOutCh:
			payload = p
		}
		if err := s.writeMsg(conn, payload); err != nil {
			errCh <- fmt.Errorf("write: %w", err)
			return
		}
	}
}

func (s *Server) writeMsg(conn *websocket.Conn, payload []byte) error {
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := conn.WriteMessage(websocket.TextMessage, payload)
	conn.SetWriteDeadline(time.Time{})
	return err
}

// stopSubsystems shuts down the acquisition loop and state machine goroutines.
func (s *Server) stopSubsystems() {
	if s.acqStopCh != nil {
		select {
		case <-s.acqStopCh: // already closed
		default:
			close(s.acqStopCh)
		}
		s.acqStopCh = nil
	}
	if s.smStopCh != nil {
		select {
		case <-s.smStopCh:
		default:
			close(s.smStopCh)
		}
		s.smStopCh = nil
	}
	s.driver.Stop()
}
