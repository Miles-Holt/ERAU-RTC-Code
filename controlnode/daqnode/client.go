// Package daqnode manages one persistent WebSocket connection to a LabVIEW
// DAQ node.  It reconnects automatically on disconnect.
package daqnode

import (
	"controlnode/broker"
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
}

// New creates a Client.  configJSON is the config payload to send after the
// DAQ node requests it.
func New(refDes, ip string, port int, configJSON string, b *broker.Broker) *Client {
	addr := fmt.Sprintf("%s:%d", ip, port)
	return &Client{
		refDes:     refDes,
		addr:       addr,
		configJSON: []byte(configJSON),
		b:          b,
		cmdCh:      make(chan []byte, 64),
	}
}

// Run connects to the DAQ node and blocks, reconnecting on any error.
// It also registers/deregisters the cmd channel with the broker.
func (c *Client) Run() {
	for {
		c.b.RegisterDaq(c.refDes, c.cmdCh)
		err := c.connect()
		c.b.RegisterDaq(c.refDes, nil) // deregister while disconnected
		c.b.DaqConnected.Add(-1)
		if err != nil {
			log.Printf("daqnode %s: disconnected: %v — retrying in %s", c.refDes, err, reconnectDelay)
		}
		time.Sleep(reconnectDelay)
	}
}

// connect dials, does the handshake, then runs read/write loops until an error.
func (c *Client) connect() error {
	u := url.URL{Scheme: "ws", Host: c.addr, Path: "/"}
	log.Printf("daqnode %s: connecting to %s", c.refDes, u.String())

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	// ── Handshake: wait for config_req, send config ───────────────────────
	conn.SetReadDeadline(time.Now().Add(readTimeout))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("config_req read: %w", err)
	}
	conn.SetReadDeadline(time.Time{}) // clear deadline for normal operation

	var req struct {
		Type   string `json:"type"`
		RefDes string `json:"refDes"`
	}
	if err := json.Unmarshal(msg, &req); err != nil || req.Type != "config_req" {
		return fmt.Errorf("expected config_req, got: %s", msg)
	}
	log.Printf("daqnode %s: received config_req, sending config", c.refDes)

	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, c.configJSON); err != nil {
		return fmt.Errorf("send config: %w", err)
	}
	conn.SetWriteDeadline(time.Time{})

	log.Printf("daqnode %s: connected", c.refDes)
	c.b.DaqConnected.Add(1)

	// ── Run read and write concurrently ───────────────────────────────────
	errCh := make(chan error, 2)
	go c.readLoop(conn, errCh)
	go c.writeLoop(conn, errCh)
	return <-errCh // first error wins; defer closes conn
}

// readLoop reads data JSON from the DAQ node and publishes to the broker.
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
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("daqnode %s: bad JSON: %v", c.refDes, err)
			continue
		}
		if msg.Type != "data" {
			log.Printf("daqnode %s: unexpected message type %q", c.refDes, msg.Type)
			continue
		}
		c.b.PublishData(broker.DataEvent{Values: msg.D})
	}
}

// writeLoop forwards commands from the broker to the DAQ node.
func (c *Client) writeLoop(conn *websocket.Conn, errCh chan<- error) {
	for payload := range c.cmdCh {
		conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
			errCh <- fmt.Errorf("write cmd: %w", err)
			return
		}
		conn.SetWriteDeadline(time.Time{})
	}
}
