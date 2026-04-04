// Package webclient implements the WebSocket server that browser clients connect to.
package webclient

import (
	"controlnode/broker"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Allow all origins — this runs on a private LAN with no cross-origin concerns.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// alertRecord holds one alert entry in the server's in-memory alert list.
type alertRecord struct {
	ID        string `json:"id"`
	Category  string `json:"category"`  // "info" | "warning" | "alarm"
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"` // Unix ms
	Acked     bool   `json:"acked"`
}

// Server listens for browser WebSocket connections on the configured port.
// It also serves static files from webRoot for plain HTTP requests.
type Server struct {
	port         int
	configJSON   []byte
	b            *broker.Broker
	fileServer   http.Handler
	userAuth     *UserAuthConfig
	layoutPaths  map[string]string // filename → absolute disk path (immutable)

	mu            sync.RWMutex
	panelMessages [][]byte      // pid_layout messages; updated when a layout is saved
	alerts        []alertRecord // in-memory alert list
}

// New creates a Server.
// layoutPaths maps layout filename (e.g. "test_panel.yaml") → absolute path on disk.
// Pass userAuth=nil to disable authentication (any credentials are accepted).
func New(port int, configJSON string, panelMessages [][]byte, b *broker.Broker,
	webRoot string, embedded fs.FS, userAuth *UserAuthConfig,
	layoutPaths map[string]string) *Server {

	var fsh http.Handler
	if webRoot != "" {
		fsh = http.FileServer(http.Dir(webRoot))
	} else if embedded != nil {
		fsh = http.FileServer(http.FS(embedded))
	}
	return &Server{
		port:          port,
		configJSON:    []byte(configJSON),
		panelMessages: panelMessages,
		b:             b,
		fileServer:    fsh,
		userAuth:      userAuth,
		layoutPaths:   layoutPaths,
	}
}

// ListenAndServe starts the HTTP/WebSocket server.  Blocks until the process exits.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws/data", s.ServeWsData)
	mux.HandleFunc("/ws/ctrl", s.ServeWsCtrl)
	mux.HandleFunc("/", s.handleStatic)

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
	return http.ListenAndServe(addr, mux)
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
func (s *Server) ServeWsData(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("webclient data: upgrade error from %s: %v", r.RemoteAddr, err)
		return
	}
	defer conn.Close()
	log.Printf("webclient data: connected %s", r.RemoteAddr)

	// Send config.
	if err := conn.WriteMessage(websocket.TextMessage, s.configJSON); err != nil {
		log.Printf("webclient data: send config to %s: %v", r.RemoteAddr, err)
		return
	}

	// Send panel layout messages.
	s.mu.RLock()
	panels := make([][]byte, len(s.panelMessages))
	copy(panels, s.panelMessages)
	s.mu.RUnlock()
	for _, msg := range panels {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("webclient data: send layout to %s: %v", r.RemoteAddr, err)
			return
		}
	}

	// Send alert snapshot so the client sees existing alerts immediately.
	if snap := s.alertSnapshot(); snap != nil {
		if err := conn.WriteMessage(websocket.TextMessage, snap); err != nil {
			log.Printf("webclient data: send alert snapshot to %s: %v", r.RemoteAddr, err)
			return
		}
	}

	// Subscribe to broker and forward all broadcasts.
	broadcastCh, unsub := s.b.Subscribe()
	defer unsub()

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
			s.b.SendCmd(cmd)

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
			s.mu.Lock()
			for i := range s.alerts {
				if s.alerts[i].ID == req.ID {
					s.alerts[i].Acked = true
					break
				}
			}
			s.mu.Unlock()
			payload, _ := json.Marshal(map[string]interface{}{
				"type": "alert_acked",
				"id":   req.ID,
			})
			s.b.Publish(payload)

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
			payload, _ := json.Marshal(map[string]interface{}{
				"type":     "pid_layout",
				"filename": req.Filename,
				"content":  req.Content,
			})
			s.mu.Lock()
			for i, pm := range s.panelMessages {
				var p struct{ Filename string `json:"filename"` }
				if json.Unmarshal(pm, &p) == nil && p.Filename == req.Filename {
					s.panelMessages[i] = payload
					break
				}
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

// pushAlert appends an alert to the in-memory list and broadcasts it to all clients.
func (s *Server) pushAlert(category, message string) {
	rec := alertRecord{
		ID:        fmt.Sprintf("%d", time.Now().UnixNano()),
		Category:  category,
		Message:   message,
		Timestamp: time.Now().UnixMilli(),
	}
	s.mu.Lock()
	s.alerts = append(s.alerts, rec)
	s.mu.Unlock()

	payload, _ := json.Marshal(map[string]interface{}{
		"type":      "alert",
		"id":        rec.ID,
		"category":  rec.Category,
		"message":   rec.Message,
		"timestamp": rec.Timestamp,
		"acked":     false,
	})
	s.b.Publish(payload)
}

// alertSnapshot returns a single alert_snapshot JSON message containing all
// current alerts, or nil if there are none.
func (s *Server) alertSnapshot() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.alerts) == 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"type":   "alert_snapshot",
		"alerts": s.alerts,
	})
	return payload
}
