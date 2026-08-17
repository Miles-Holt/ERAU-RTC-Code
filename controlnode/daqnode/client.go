// Package daqnode manages one persistent WebSocket connection to a LabVIEW
// DAQ node.  It reconnects automatically on disconnect.
package daqnode

import (
	"context"
	"controlnode/broker"
	"controlnode/statemachine"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	reconnectDelay = 2 * time.Second
	writeTimeout   = 5 * time.Second
	readTimeout    = 3 * time.Second // used only for config_req handshake
)

// daqDialer is DefaultDialer with permessage-deflate enabled.  If a DAQ node's
// WS server does not advertise compression, negotiation is skipped and the
// connection proceeds uncompressed — safe either way.
var daqDialer = func() *websocket.Dialer {
	d := *websocket.DefaultDialer
	d.EnableCompression = true
	return &d
}()

// EngineController is what the daqnode client needs from the state machine
// engine.  Every method is safe to call from the client's goroutines.
type EngineController interface {
	// MachinesForNode lists machines with at least one daq_local state on node.
	MachinesForNode(node string) []string
	// IsRunningOnNode reports whether the machine is currently in a daq_local
	// state on node.
	IsRunningOnNode(machine, node string) bool
	// CurrentDaqPayload returns a fresh payload when the machine is currently in
	// a daq_local state on node.  ok==false means "send nothing"; a non-nil err
	// means the state IS local here but could not be resolved.
	CurrentDaqPayload(machine, node string) (*statemachine.DaqStateUpdate, bool, error)
	// NotifyAbortTriggered moves the machine to the declared abort destination.
	NotifyAbortTriggered(machine string) error
	// NotifySequenceCompleteRun applies a sequence_complete report; runID is the
	// echoed payload runId, or 0 when the node does not echo it.
	NotifySequenceCompleteRun(machine string, runID int64) error
	// NotifyDaqReconnect handles a reconnect while the machine is mid-flight in
	// a daq_local state on node (state-uncertain → abort destination + alarm).
	NotifyDaqReconnect(machine, node string) error
	// NotifyDaqFirstConnect handles a node's very first-ever handshake
	// completing while the machine is already believed to be running a
	// daq_local state on it. Distinct from NotifyDaqReconnect: there is no
	// prior connection to this node to have gone uncertain, but the control
	// node still cannot know how much of the sequence has elapsed, so it takes
	// the same corrective action (fire the abort destination) under a
	// distinct, clearly-worded error/alarm.
	NotifyDaqFirstConnect(machine, node string) error
}

// Client connects to a single DAQ node and bridges its data/commands to the broker.
type Client struct {
	refDes     string
	addr       string // "ip:port"
	configJSON []byte // pre-marshalled config to send after config_req
	b          *broker.Broker
	cmdCh      chan []byte // broker writes cmd JSON here; we forward to DAQ node
	outCh      chan []byte // state_update payloads bound for the DAQ node
	engine     EngineController

	agg         *ConnectAggregator // optional; see SetAggregator
	loggedFirst bool               // true once the first connect attempt has been logged

	// hasConnected is true once this Client has completed at least one
	// successful handshake with the node. It is only ever read/written from
	// the Run() goroutine (connect() is called sequentially, never
	// concurrently with itself), so it needs no synchronisation. It is what
	// lets connect() tell a genuine reconnect apart from the very first
	// connection this process has ever made to the node — see
	// handleReconnectState vs handleFirstConnectState.
	hasConnected bool

	// connected reports whether the node is live RIGHT NOW (handshake
	// complete, read/write loops running). Unlike hasConnected it goes back
	// to false on every disconnect. SendStateUpdate reads it from whatever
	// goroutine the engine calls it on, so it is an atomic.Bool rather than a
	// plain bool guarded by hasConnected's single-goroutine assumption.
	connected atomic.Bool

	// retryDelay overrides reconnectDelay between reconnect attempts; zero
	// means "use reconnectDelay".  Settable via SetRetryDelay so tests can
	// drive many reconnect cycles quickly instead of sleeping for seconds.
	retryDelay time.Duration
}

// New creates a Client.  configJSON is the config payload to send after the DAQ
// node requests it.  engine may be nil when no state machine engine is running,
// in which case the client is a pure data/command bridge.
func New(refDes, ip string, port int, configJSON string, b *broker.Broker, engine EngineController) *Client {
	addr := fmt.Sprintf("%s:%d", ip, port)
	return &Client{
		refDes:     refDes,
		addr:       addr,
		configJSON: []byte(configJSON),
		b:          b,
		cmdCh:      make(chan []byte, 64),
		outCh:      make(chan []byte, 64),
		engine:     engine,
	}
}

// RefDes returns the node's refDes.
func (c *Client) RefDes() string { return c.refDes }

// SetAggregator wires a shared ConnectAggregator into the client.  When set,
// repeated failed connect retries report their pending state to it instead of
// each logging their own "retrying in Ns" line; connected/disconnected state
// changes are still logged individually by the client.  Must be called before
// Run(); nil is a valid no-op (falls back to the old per-attempt log line).
func (c *Client) SetAggregator(agg *ConnectAggregator) {
	c.agg = agg
}

// SetRetryDelay overrides the delay between reconnect attempts (default
// reconnectDelay, 2s).  Must be called before Run(); intended for tests that
// need many reconnect cycles without sleeping for seconds of wall-clock time.
func (c *Client) SetRetryDelay(d time.Duration) {
	c.retryDelay = d
}

// SendStateUpdate queues a resolved `state_update` payload for the node.  It is
// called from the engine loop (OnDaqStateEnter) and must never block; if the
// queue is somehow full the payload is dropped with a loud error rather than
// stalling the engine.
//
// The node must be connected right now. Before this check existed, a
// daq_local state entered while the node was down would sit in outCh
// (capacity 64) with nothing ever consuming it — no error, no alarm, just a
// "queued" log line — while the engine and the HMI both showed the machine
// believing it was running that sequence on a node it had never reached.
// That is exactly the controlNode/DAQ disagreement the runId/state-uncertain
// design exists to prevent, so it is refused loudly instead.
func (c *Client) SendStateUpdate(payload *statemachine.DaqStateUpdate) {
	if !c.connected.Load() {
		c.reportErr(fmt.Errorf("cannot deliver state_update: node %s is not connected — state %q (machine %q) was entered locally but was NEVER COMMANDED on the node",
			c.refDes, payload.State, payload.Machine))
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		c.reportErr(fmt.Errorf("marshal state_update for %q: %w", payload.State, err))
		return
	}
	select {
	case c.outCh <- raw:
		log.Printf("daqnode %s: queued state_update state=%q machine=%q runId=%d",
			c.refDes, payload.State, payload.Machine, payload.RunID)
	default:
		c.reportErr(fmt.Errorf("outbound queue full, DROPPED state_update for state %q", payload.State))
	}
}

// reportErr surfaces a control-node-side fault to the operator through the
// broker error path (the web client turns these into alarms) as well as the log.
func (c *Client) reportErr(err error) {
	log.Printf("daqnode %s: %v", c.refDes, err)
	if c.b != nil {
		c.b.PublishErr(broker.ErrEvent{
			DaqRefDes: c.refDes,
			T:         float64(time.Now().UnixMilli()) / 1000.0,
			Err:       err.Error(),
		})
	}
}

// Run connects to the DAQ node and blocks, reconnecting on any error, until
// ctx is cancelled.  It also registers/deregisters the cmd channel with the
// broker.
func (c *Client) Run(ctx context.Context) {
	delay := c.retryDelay
	if delay <= 0 {
		delay = reconnectDelay
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.b.RegisterDaq(c.refDes, c.cmdCh)
		connected, err := c.connect()
		c.b.RegisterDaq(c.refDes, nil) // deregister while disconnected
		if connected {
			c.b.DaqConnected.Add(-1)
			// Server-side alert source: the alert engine raises the template's
			// `disconnect` alert from here, so no browser has to infer it.
			c.b.NoteDaqDisconnected(c.refDes)
			// A drop after a successful connection is a real state change —
			// always worth its own line, not folded into the retry summary.
			log.Printf("daqnode %s: disconnected: %v", c.refDes, err)
		}
		if c.agg != nil {
			// Whether this was the very first attempt or a connection that just
			// dropped, we are now waiting again — let the aggregator own the
			// "still retrying" noise instead of logging every 2s ourselves.
			c.agg.Pending(c.refDes)
		} else if err != nil && !connected {
			log.Printf("daqnode %s: disconnected: %v — retrying in %s", c.refDes, err, delay)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// connect dials, does the handshake, then runs read/write loops until an error.
// Returns (true, err) if the connection was established (DaqConnected was incremented),
// or (false, err) if it failed before that point.
func (c *Client) connect() (connected bool, err error) {
	u := url.URL{Scheme: "ws", Host: c.addr, Path: "/"}
	if !c.loggedFirst {
		// Only the first-ever attempt gets its own line; every retry after
		// this reports through the aggregator (or the fallback line below)
		// instead of repeating "connecting to..." every reconnectDelay.
		log.Printf("daqnode %s: connecting to %s", c.refDes, u.String())
		c.loggedFirst = true
	}

	conn, _, err := daqDialer.Dial(u.String(), nil)
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

	log.Printf("daqnode %s: connected", c.refDes)
	c.b.DaqConnected.Add(1)
	// The link is up (handshake complete): the alert engine resolves the
	// disconnect alert and raises `reconnect` for every connect after the first.
	c.b.NoteDaqConnected(c.refDes)
	if c.agg != nil {
		c.agg.Connected(c.refDes)
	}
	c.connected.Store(true)
	defer c.connected.Store(false)

	// ── Post-connect state handling ───────────────────────────────────────
	// We deliberately do NOT push a state_update here.  `state_update` means
	// "enter this state now", so re-sending a running daq_local state would
	// re-fire its sequence from t=0 (re-commanding valves and igniters).  If a
	// machine is mid-flight on this node the node's timeline is unknowable
	// after a reconnect, so that is treated as state-uncertain: the engine
	// fires the state's declared abort destination and raises an alarm.  If no
	// machine is in a daq_local state here, nothing is sent at all.
	//
	// A first-ever connection to this node gets a DIFFERENT rule, not the
	// reconnect one: there is no prior connection whose timeline could have
	// gone uncertain, so reporting it as a "reconnect" would tell the operator
	// a link dropped that never existed. But it is not safe to just enter the
	// state either — the control node still has no idea how much of the
	// sequence, if any, has already elapsed on a node it has never exchanged a
	// message with, so silently sending state_update now could fire a
	// sequence partway through a timeline the engine already believes is
	// under way. Both cases refuse to continue and fire the abort
	// destination; only the reported message differs, so logs and alarms
	// never conflate the two failure modes.
	if !c.hasConnected {
		c.handleFirstConnectState()
		c.hasConnected = true
	} else {
		c.handleReconnectState()
	}

	// Drain any state_update left over from a previous connection: it was
	// queued (outCh, capacity 64) but never delivered before the socket that
	// would have carried it went away. Forwarding it now, on a fresh
	// connection, would be exactly the silent-re-fire hazard the reconnect/
	// first-connect handling above just finished refusing.
	for drained := false; !drained; {
		select {
		case <-c.outCh:
		default:
			drained = true
		}
	}

	// ── Run read and write concurrently ───────────────────────────────────
	errCh := make(chan error, 2)
	go c.readLoop(conn, errCh)
	go c.writeLoop(conn, errCh)
	return true, <-errCh
}

// handleReconnectState applies the state-uncertain rule to every machine that
// has daq_local states on this node.
func (c *Client) handleReconnectState() {
	if c.engine == nil {
		return
	}
	for _, machine := range c.engine.MachinesForNode(c.refDes) {
		if !c.engine.IsRunningOnNode(machine, c.refDes) {
			continue // not in a daq_local state here — send nothing
		}
		if err := c.engine.NotifyDaqReconnect(machine, c.refDes); err != nil {
			c.reportErr(fmt.Errorf("machine %s: reconnected with uncertain state: %v", machine, err))
		}
	}
}

// handleFirstConnectState applies the first-connect rule (distinct from
// handleReconnectState — see the comment in connect()) to every machine that
// has daq_local states on this node.
func (c *Client) handleFirstConnectState() {
	if c.engine == nil {
		return
	}
	for _, machine := range c.engine.MachinesForNode(c.refDes) {
		if !c.engine.IsRunningOnNode(machine, c.refDes) {
			continue // not in a daq_local state here — send nothing
		}
		if err := c.engine.NotifyDaqFirstConnect(machine, c.refDes); err != nil {
			c.reportErr(fmt.Errorf("machine %s: node connected for the first time while a sequence was already believed to be running: %v", machine, err))
		}
	}
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
			Type  string             `json:"type"`
			T     float64            `json:"t"`
			D     map[string]float64 `json:"d"`
			Err   string             `json:"err"`
			RunID int64              `json:"runId"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("daqnode %s: bad JSON: %v", c.refDes, err)
			continue
		}
		switch msg.Type {
		case "data":
			c.b.PublishData(broker.DataEvent{Values: msg.D})
			// Heartbeat for stale detection: the alert engine measures the gap
			// between these against the template's `stale` timeout.
			c.b.NoteDaqData(c.refDes)

		case "err":
			log.Printf("daqnode %s: error: %s", c.refDes, msg.Err)
			c.b.PublishErr(broker.ErrEvent{DaqRefDes: c.refDes, T: msg.T, Err: msg.Err})

		case "state_req":
			c.handleStateReq()

		case "abort_triggered":
			// The node already ran its cached exit_sequence locally.  The engine
			// moves the machine to the destination that sequence declared.
			c.handleAbortTriggered()

		case "sequence_complete":
			c.handleSequenceComplete(msg.RunID)

		default:
			log.Printf("daqnode %s: unexpected message type %q", c.refDes, msg.Type)
		}
	}
}

// handleStateReq answers a node's state_req with a freshly-resolved payload,
// but only when a machine is currently in a daq_local state on this node.
// Otherwise the request is logged and ignored — there is no state to give.
func (c *Client) handleStateReq() {
	if c.engine == nil {
		log.Printf("daqnode %s: state_req ignored (no engine)", c.refDes)
		return
	}
	sent := 0
	for _, machine := range c.engine.MachinesForNode(c.refDes) {
		payload, running, err := c.engine.CurrentDaqPayload(machine, c.refDes)
		if err != nil {
			// F-A15: resolution failed at send time.  Refuse to send a payload
			// and raise it as an operator-visible fault; never silently log.
			c.reportErr(fmt.Errorf("state_req: machine %s: %v — payload refused", machine, err))
			continue
		}
		if !running {
			continue
		}
		c.SendStateUpdate(payload)
		sent++
	}
	if sent == 0 {
		log.Printf("daqnode %s: state_req ignored (no machine is in a daq_local state on this node)", c.refDes)
	}
}

func (c *Client) handleAbortTriggered() {
	if c.engine == nil {
		c.reportErr(fmt.Errorf("abort_triggered received but no engine is running"))
		return
	}
	machines := c.engine.MachinesForNode(c.refDes)
	if len(machines) == 0 {
		c.reportErr(fmt.Errorf("abort_triggered received but no machine has daq_local states on this node"))
		return
	}
	for _, machine := range machines {
		if err := c.engine.NotifyAbortTriggered(machine); err != nil {
			c.reportErr(fmt.Errorf("abort_triggered for %s: %v", machine, err))
			continue
		}
		log.Printf("daqnode %s: abort_triggered → %s moving to its declared abort destination", c.refDes, machine)
	}
}

func (c *Client) handleSequenceComplete(runID int64) {
	if c.engine == nil {
		log.Printf("daqnode %s: received sequence_complete but no engine available", c.refDes)
		return
	}
	for _, machine := range c.engine.MachinesForNode(c.refDes) {
		if err := c.engine.NotifySequenceCompleteRun(machine, runID); err != nil {
			// Stale/uncorrelated completions are expected and are not faults.
			log.Printf("daqnode %s: sequence_complete for %s: %v", c.refDes, machine, err)
			continue
		}
		log.Printf("daqnode %s: sequence_complete (runId %d) applied to %s", c.refDes, runID, machine)
	}
}

// writeLoop forwards commands from the broker and state payloads to the DAQ node.
// It is the only goroutine that writes to conn.
func (c *Client) writeLoop(conn *websocket.Conn, errCh chan<- error) {
	for {
		select {
		case payload, ok := <-c.cmdCh:
			if !ok {
				return
			}
			if err := c.write(conn, payload); err != nil {
				errCh <- fmt.Errorf("write cmd: %w", err)
				return
			}

		case payload := <-c.outCh:
			// state_update queued by the engine (state entry) or by state_req.
			if err := c.write(conn, payload); err != nil {
				errCh <- fmt.Errorf("write state_update: %w", err)
				return
			}
		}
	}
}

func (c *Client) write(conn *websocket.Conn, payload []byte) error {
	conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	err := conn.WriteMessage(websocket.TextMessage, payload)
	conn.SetWriteDeadline(time.Time{})
	return err
}
