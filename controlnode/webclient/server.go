// Package webclient implements the WebSocket server that browser clients connect to.
package webclient

import (
	"controlnode/broker"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	// Allow all origins — this runs on a private LAN with no cross-origin concerns.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Server listens for browser WebSocket connections on the configured port.
// It also serves static files from webRoot for plain HTTP requests.
type Server struct {
	port       int
	configJSON []byte
	b          *broker.Broker
	fileServer http.Handler
}

// New creates a Server.  webRoot is the directory containing index.html and
// other static assets; pass an empty string to fall back to embedded.
// embedded is the fallback fs.FS used when webRoot is empty (pass nil to disable static serving).
func New(port int, configJSON string, b *broker.Broker, webRoot string, embedded fs.FS) *Server {
	var fsh http.Handler
	if webRoot != "" {
		fsh = http.FileServer(http.Dir(webRoot))
	} else if embedded != nil {
		fsh = http.FileServer(http.FS(embedded))
	}
	return &Server{
		port:       port,
		configJSON: []byte(configJSON),
		b:          b,
		fileServer: fsh,
	}
}

// ListenAndServe starts the HTTP/WebSocket server.  Blocks until the process exits.
func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("webclient: listening on http://0.0.0.0%s", addr)
	return http.ListenAndServe(addr, mux)
}

// handle routes WebSocket upgrade requests to the WS handler and all other
// requests to the static file server.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		s.handleWS(w, r)
		return
	}
	if s.fileServer != nil {
		s.fileServer.ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

// handleWS upgrades the HTTP connection to WebSocket, then runs the client loop.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("webclient: upgrade error from %s: %v", r.RemoteAddr, err)
		return
	}
	defer conn.Close()
	log.Printf("webclient: connected %s", r.RemoteAddr)

	// Send config immediately on connect.
	if err := conn.WriteMessage(websocket.TextMessage, s.configJSON); err != nil {
		log.Printf("webclient: send config to %s: %v", r.RemoteAddr, err)
		return
	}

	// Subscribe to broker broadcasts.
	broadcastCh, unsub := s.b.Subscribe()
	defer unsub()

	errCh := make(chan error, 2)
	go s.readLoop(conn, r.RemoteAddr, errCh)
	go s.writeLoop(conn, r.RemoteAddr, broadcastCh, errCh)

	err = <-errCh
	log.Printf("webclient: disconnected %s: %v", r.RemoteAddr, err)
}

// readLoop reads cmd messages from the browser and forwards them to the broker.
func (s *Server) readLoop(conn *websocket.Conn, addr string, errCh chan<- error) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read: %w", err)
			return
		}
		var cmd broker.CmdMsg
		if err := json.Unmarshal(raw, &cmd); err != nil {
			log.Printf("webclient %s: bad JSON: %v", addr, err)
			continue
		}
		if cmd.Type != "cmd" {
			log.Printf("webclient %s: unexpected message type %q", addr, cmd.Type)
			continue
		}
		s.b.SendCmd(cmd)
	}
}

// writeLoop forwards broker broadcasts to this browser connection.
func (s *Server) writeLoop(conn *websocket.Conn, addr string, ch <-chan []byte, errCh chan<- error) {
	for msg := range ch {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			errCh <- fmt.Errorf("write: %w", err)
			return
		}
	}
}
