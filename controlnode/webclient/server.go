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
	port          int
	configJSON    []byte
	panelMessages [][]byte // pid_layout messages sent on each new connection
	b             *broker.Broker
	fileServer    http.Handler
	userAuth      *UserAuthConfig
}

// New creates a Server. Pass an empty webRoot to serve from embedded instead.
// Pass embedded=nil to disable static file serving entirely.
// panelMessages is the list of pre-built pid_layout JSON payloads to send on connect.
// Pass userAuth=nil to disable authentication (all connections approved).
func New(port int, configJSON string, panelMessages [][]byte, b *broker.Broker, webRoot string, embedded fs.FS, userAuth *UserAuthConfig) *Server {
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

	// Send front panel layout messages (one per configured panel).
	for _, msg := range s.panelMessages {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("webclient: send panel layout to %s: %v", r.RemoteAddr, err)
			return
		}
	}

	// Subscribe to broker broadcasts.
	broadcastCh, unsub := s.b.Subscribe()
	defer unsub()

	var authorized bool
	authRespCh := make(chan []byte, 4)
	errCh := make(chan error, 2)

	go s.readLoop(conn, r.RemoteAddr, &authorized, authRespCh, errCh)
	go s.writeLoop(conn, r.RemoteAddr, broadcastCh, authRespCh, errCh)

	err = <-errCh
	log.Printf("webclient: disconnected %s: %v", r.RemoteAddr, err)
}

// readLoop reads messages from the browser, handling auth_request and cmd types.
func (s *Server) readLoop(conn *websocket.Conn, addr string,
	authorized *bool, authRespCh chan<- []byte, errCh chan<- error) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read: %w", err)
			return
		}

		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			log.Printf("webclient %s: bad JSON: %v", addr, err)
			continue
		}

		switch peek.Type {
		case "auth_request":
			var req authRequestMsg
			if err := json.Unmarshal(raw, &req); err != nil {
				log.Printf("webclient %s: bad auth_request: %v", addr, err)
				continue
			}
			resp := s.handleAuth(addr, req, authorized)
			b, _ := json.Marshal(resp)
			authRespCh <- b

		case "cmd":
			if !*authorized {
				log.Printf("webclient %s: rejected cmd from unauthorized client", addr)
				continue
			}
			var cmd broker.CmdMsg
			if err := json.Unmarshal(raw, &cmd); err != nil {
				log.Printf("webclient %s: bad cmd JSON: %v", addr, err)
				continue
			}
			s.b.SendCmd(cmd)

		default:
			log.Printf("webclient %s: unexpected message type %q", addr, peek.Type)
		}
	}
}

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

// writeLoop forwards broker broadcasts and auth responses to this browser connection.
func (s *Server) writeLoop(conn *websocket.Conn, addr string,
	broadcastCh <-chan []byte, authRespCh <-chan []byte, errCh chan<- error) {
	for {
		select {
		case msg, ok := <-broadcastCh:
			if !ok {
				errCh <- fmt.Errorf("broadcast channel closed")
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				errCh <- fmt.Errorf("write: %w", err)
				return
			}
		case msg := <-authRespCh:
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				errCh <- fmt.Errorf("write auth: %w", err)
				return
			}
		}
	}
}
