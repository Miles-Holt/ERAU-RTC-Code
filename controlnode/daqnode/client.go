// Package daqnode manages one persistent WebSocket connection to a LabVIEW
// DAQ node.  It reconnects automatically on disconnect.
package daqnode

import (
	"controlnode/broker"
	"controlnode/config"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 2 * time.Second
	writeTimeout   = 5 * time.Second
	readTimeout    = 3 * time.Second // used only for config_req handshake
)

// Client connects to a single DAQ node and bridges its data/commands to the broker.
type Client struct {
	refDes     string
	addr       string // "ip:port"
	configJSON []byte // pre-marshalled config to send after config_req
	b          *broker.Broker
	cmdCh      chan []byte // broker writes cmd JSON here; we forward to DAQ node
	stateCh    chan []byte // readLoop enqueues state messages; writeLoop sends them to DAQ
	sm         *stateMachine
}

// New creates a Client.  configJSON is the config payload to send after the
// DAQ node requests it.  control may be nil if this DAQ node has no state
// machine config; vars is the soft channel store used for variable resolution
// and may be nil if control is nil.
func New(refDes, ip string, port int, configJSON string, b *broker.Broker,
	control *config.DaqControl, vars varGetter) *Client {

	addr := fmt.Sprintf("%s:%d", ip, port)
	return &Client{
		refDes:     refDes,
		addr:       addr,
		configJSON: []byte(configJSON),
		b:          b,
		cmdCh:      make(chan []byte, 64),
		stateCh:    make(chan []byte, 16),
		sm:         newStateMachine(control, vars),
	}
}

// Run connects to the DAQ node and blocks, reconnecting on any error.
// It also registers/deregisters the cmd channel with the broker.
func (c *Client) Run() {
	for {
		c.b.RegisterDaq(c.refDes, c.cmdCh)
		connected, err := c.connect()
		c.b.RegisterDaq(c.refDes, nil) // deregister while disconnected
		if connected {
			c.b.DaqConnected.Add(-1)
		}
		if err != nil {
			log.Printf("daqnode %s: disconnected: %v — retrying in %s", c.refDes, err, reconnectDelay)
		}
		time.Sleep(reconnectDelay)
	}
}

// connect dials, does the handshake, then runs read/write loops until an error.
// Returns (true, err) if the connection was established (DaqConnected was incremented),
// or (false, err) if it failed before that point.
func (c *Client) connect() (connected bool, err error) {
	u := url.URL{Scheme: "ws", Host: c.addr, Path: "/"}
	log.Printf("daqnode %s: connecting to %s", c.refDes, u.String())

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// ── Handshake: wait for config_req, send config ───────────────────────
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return false, fmt.Errorf("config_req read: %w", err)
	}
	conn.SetReadDeadline(time.Time{})

	var req struct {
		Type   string `json:"type"`
		RefDes string `json:"refDes"`
	}
	if err := json.Unmarshal(msg, &req); err != nil || req.Type != "config_req" {
		return false, fmt.Errorf("expected config_req, got: %s", msg)
	}
	log.Printf("daqnode %s: received config_req, sending config", c.refDes)

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, c.configJSON); err != nil {
		return false, fmt.Errorf("send config: %w", err)
	}
	conn.SetWriteDeadline(time.Time{})

	// ── State machine: push current state to DAQ immediately after config ─
	// The DAQ may also send state_req after this, which is handled in readLoop.
	if c.sm.control != nil {
		payload, err := c.sm.HandleStateReq()
		if err != nil {
			log.Printf("daqnode %s: initial state_update error: %v", c.refDes, err)
		} else {
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return false, fmt.Errorf("send initial state_update: %w", err)
			}
			conn.SetWriteDeadline(time.Time{})
			c.b.Publish(stateChangeBroadcast(c.refDes, c.sm.Current()))
			log.Printf("daqnode %s: sent initial state_update for state %q", c.refDes, c.sm.Current())
		}
	}

	log.Printf("daqnode %s: connected", c.refDes)
	c.b.DaqConnected.Add(1)

	// ── Run read and write concurrently ───────────────────────────────────
	errCh := make(chan error, 2)
	go c.readLoop(conn, errCh)
	go c.writeLoop(conn, errCh)
	return true, <-errCh
}

// readLoop reads messages from the DAQ node and handles them.
func (c *Client) readLoop(conn *websocket.Conn, errCh chan<- error) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			errCh <- fmt.Errorf("read: %w", err)
			return
		}
		var msg struct {
			Type string             `json:"type"`
			T    float64            `json:"t"`
			D    map[string]float64 `json:"d"`
			Err  string             `json:"err"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("daqnode %s: bad JSON: %v", c.refDes, err)
			continue
		}
		switch msg.Type {
		case "data":
			c.b.PublishData(broker.DataEvent{Values: msg.D})

		case "err":
			log.Printf("daqnode %s: error: %s", c.refDes, msg.Err)
			c.b.PublishErr(broker.ErrEvent{DaqRefDes: c.refDes, T: msg.T, Err: msg.Err})

		case "state_req":
			// DAQ is ready to receive the current state definition.
			if c.sm.control != nil {
				payload, err := c.sm.HandleStateReq()
				if err != nil {
					log.Printf("daqnode %s: state_req error: %v", c.refDes, err)
				} else {
					c.stateCh <- payload
					c.b.Publish(stateChangeBroadcast(c.refDes, c.sm.Current()))
					log.Printf("daqnode %s: state_req → sending state_update for %q", c.refDes, c.sm.Current())
				}
			}

		case "abort_triggered":
			// DAQ already ran its exit sequence locally; find the abort_triggered
			// transition and send hard_exit so DAQ can request the abort state.
			if c.sm.control != nil {
				exitMsg, err := c.sm.HandleAbortTriggered()
				if err != nil {
					log.Printf("daqnode %s: abort_triggered error: %v", c.refDes, err)
				} else {
					c.stateCh <- exitMsg
					c.b.Publish(stateChangeBroadcast(c.refDes, c.sm.Pending()))
					log.Printf("daqnode %s: abort_triggered → pending state %q", c.refDes, c.sm.Pending())
				}
			}

		case "sequence_complete":
			// Entry sequence finished; check for an auto-transition.
			if c.sm.control != nil {
				exitMsg, err := c.sm.HandleSequenceComplete()
				if err != nil {
					log.Printf("daqnode %s: sequence_complete error: %v", c.refDes, err)
				} else if exitMsg != nil {
					c.stateCh <- exitMsg
					c.b.Publish(stateChangeBroadcast(c.refDes, c.sm.Pending()))
					log.Printf("daqnode %s: sequence_complete → transitioning to %q", c.refDes, c.sm.Pending())
				}
			}

		default:
			log.Printf("daqnode %s: unexpected message type %q", c.refDes, msg.Type)
		}
	}
}

// writeLoop forwards commands from the broker and state messages to the DAQ node.
// It is the only goroutine that writes to conn.
func (c *Client) writeLoop(conn *websocket.Conn, errCh chan<- error) {
	for {
		select {
		case payload, ok := <-c.cmdCh:
			if !ok {
				return
			}

			// Intercept SYS-TARGET-STATE-<daqNode> commands — these drive state
			// transitions rather than being forwarded directly to the DAQ node.
			if c.sm.control != nil {
				var cmd struct {
					RefDes string      `json:"refDes"`
					Value  interface{} `json:"value"`
				}
				if json.Unmarshal(payload, &cmd) == nil && cmd.RefDes == "SYS-TARGET-STATE-"+c.refDes {
					target, _ := cmd.Value.(string)
					if target == "" {
						log.Printf("daqnode %s: SYS-TARGET-STATE-%s with non-string value, ignoring", c.refDes, c.refDes)
						continue
					}
					exitMsg, err := c.sm.RequestTransition("operator_request", target)
					if err != nil {
						// Try operator_abort as the trigger in case no operator_request exists
						exitMsg, err = c.sm.RequestTransition("operator_abort", target)
					}
					if err != nil {
						log.Printf("daqnode %s: invalid transition to %q: %v", c.refDes, target, err)
						continue
					}
					conn.SetWriteDeadline(time.Now().Add(writeTimeout))
					if err := conn.WriteMessage(websocket.TextMessage, exitMsg); err != nil {
						errCh <- fmt.Errorf("write exit msg: %w", err)
						return
					}
					conn.SetWriteDeadline(time.Time{})
					c.b.Publish(stateChangeBroadcast(c.refDes, c.sm.Pending()))
					log.Printf("daqnode %s: operator transition → %q, sent exit msg", c.refDes, c.sm.Pending())
					continue
				}
			}

			// Normal command — forward to DAQ node.
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				errCh <- fmt.Errorf("write cmd: %w", err)
				return
			}
			conn.SetWriteDeadline(time.Time{})

		case payload := <-c.stateCh:
			// State machine response (state_update, hard_exit, etc.) queued by readLoop.
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				errCh <- fmt.Errorf("write state msg: %w", err)
				return
			}
			conn.SetWriteDeadline(time.Time{})
		}
	}
}

// stateChangeBroadcast builds a state_change JSON broadcast for web clients.
func stateChangeBroadcast(daqNode, state string) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"type":    "state_change",
		"daqNode": daqNode,
		"state":   state,
	})
	return b
}
