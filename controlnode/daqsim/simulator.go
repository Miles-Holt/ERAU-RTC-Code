package daqsim

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ── Options ─────────────────────────────────────────────────────────────────

// Options configures a Simulator.
type Options struct {
	// RefDes is the node's own name, sent in config_req and used in log lines.
	// Purely cosmetic on the wire — the control node identifies the node by
	// which config/daqNodes/*.yaml entry it dialled, not by this field
	// (docs/websocket-protocol.md, "Handshake").
	RefDes string

	// Clock times entry/exit sequence steps and abort_rule scanning. Defaults
	// to RealClock{}. Tests should pass a *FakeClock to run a multi-second
	// burn instantly.
	Clock Clock

	// Seed drives the sensor-noise RNG. 0 is a fixed (not random) seed, so
	// runs are reproducible by default.
	Seed int64

	// Sensors overrides the default SensorSpec (constant 0) for specific
	// sensor-channel refDes values discovered in the received config.
	Sensors map[string]SensorSpec

	// ScanInterval is how often abort_rules are re-evaluated against the live
	// model while an entry_sequence runs, and the granularity at which
	// entry-sequence steps are applied on schedule. Defaults to 20ms. Under a
	// FakeClock this only affects loop iteration count, not wall time.
	ScanInterval time.Duration

	// DataRateOverrideHz, if non-zero, overrides the sampleRateHz the received
	// config advertises for the data-streaming ticker. Standalone-binary use
	// only; tests normally don't care how often data frames arrive.
	DataRateOverrideHz float64

	// Logf receives one line per log-worthy event. Defaults to log.Printf.
	Logf func(format string, args ...interface{})
}

// ── AppliedSetPoint / SeqRecord ────────────────────────────────────────────

// AppliedSetPoint is one timed set-point actually written to the channel
// model, in application order, with enough context to check both ordering and
// relative timing.
type AppliedSetPoint struct {
	RunID  int64
	State  string
	Phase  string // "entry" or "exit"
	RefDes string
	Value  float64
	TMs    float64   // t_ms from the state_update payload
	At     time.Time // Simulator's Clock time when it was applied
}

// SeqRecord is the outcome of one entry_sequence run.
type SeqRecord struct {
	RunID     int64
	State     string
	Outcome   string // "completed" | "aborted"
	TrippedIf string // the abort_rule "if" string, when Outcome == "aborted"
}

// ── Simulator ───────────────────────────────────────────────────────────────

// Simulator is a non-LabVIEW daqNode: a WebSocket SERVER (the control node
// dials it), driving its channel model and sequence execution entirely off
// what it receives on the wire. Safe for concurrent use by its own goroutines
// and by a test/caller reading its exported accessors.
type Simulator struct {
	opts  Options
	clock Clock
	model *Model

	started time.Time // wall-clock reference for sensor ramps

	listener net.Listener
	srv      *http.Server

	connMu  sync.Mutex
	conn    *websocket.Conn
	writeMu sync.Mutex

	cfgMu   sync.RWMutex
	cfg     daqConfig
	haveCfg bool

	activeMu  sync.Mutex
	activeGen int64

	appliedMu sync.Mutex
	applied   []AppliedSetPoint

	runsMu sync.Mutex
	runs   []SeqRecord

	armedMu    sync.Mutex
	armedState string
	armedRunID int64
}

// New creates a Simulator. It does not start listening — call Start.
func New(opts Options) *Simulator {
	if opts.Clock == nil {
		opts.Clock = RealClock{}
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = 20 * time.Millisecond
	}
	if opts.RefDes == "" {
		opts.RefDes = "DAQSIM"
	}
	return &Simulator{
		opts:    opts,
		clock:   opts.Clock,
		model:   NewModel(opts.Seed),
		started: time.Now(),
	}
}

// Start binds addr ("host:port"; empty host or ":0" picks any free port) and
// begins accepting the control node's connection in the background. It
// returns the actual listen address ("host:port") so a test can dial it or a
// binary can log it. The control node is expected to dial ws://<addr>/.
func (s *Simulator) Start(addr string) (string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("daqsim: listen: %w", err)
	}
	s.listener = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveWS)
	srv := &http.Server{Handler: mux}
	s.srv = srv
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			s.logf("serve: %v", err)
		}
	}()
	s.logf("listening on %s", ln.Addr().String())
	return ln.Addr().String(), nil
}

// Close stops accepting connections and closes the active connection, if any.
func (s *Simulator) Close() error {
	s.DropConnection()
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

var upgrader = websocket.Upgrader{
	CheckOrigin:       func(*http.Request) bool { return true },
	EnableCompression: true,
}

func (s *Simulator) serveWS(w http.ResponseWriter, r *http.Request) {
	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logf("upgrade: %v", err)
		return
	}
	s.handleConn(c)
}

// DropConnection closes the active connection, if any, simulating a dead
// link without stopping the listener — the control node will see a read
// error and reconnect. A no-op when nothing is connected.
func (s *Simulator) DropConnection() {
	s.connMu.Lock()
	c := s.conn
	s.conn = nil
	s.connMu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Connected reports whether a control node is currently connected.
func (s *Simulator) Connected() bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	return s.conn != nil
}

// ── Connection handling ─────────────────────────────────────────────────────

// handleConn performs the handshake, builds the channel model from the
// received config, starts streaming data, and dispatches every subsequent
// message until the connection drops.
func (s *Simulator) handleConn(c *websocket.Conn) {
	s.connMu.Lock()
	s.conn = c
	s.connMu.Unlock()
	defer func() {
		s.connMu.Lock()
		if s.conn == c {
			s.conn = nil
		}
		s.connMu.Unlock()
		c.Close()
	}()

	if err := s.send(map[string]string{"type": "config_req", "refDes": s.opts.RefDes}); err != nil {
		s.logf("send config_req: %v", err)
		return
	}
	_, raw, err := c.ReadMessage()
	if err != nil {
		s.logf("config read: %v", err)
		return
	}
	var cfg daqConfig
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.Type != "config" {
		s.logf("expected config, got: %s", raw)
		return
	}
	s.cfgMu.Lock()
	s.cfg = cfg
	s.haveCfg = true
	s.cfgMu.Unlock()
	s.model.BuildFromConfig(cfg, s.opts.Sensors)
	s.logf("connected: %d channel(s), sampleRateHz=%v", len(cfg.Channels), cfg.SampleRateHz)

	stop := make(chan struct{})
	go s.streamData(cfg, stop)
	defer close(stop)

	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			s.logf("disconnected: %v", err)
			return
		}
		s.handleMessage(raw)
	}
}

// send marshals v and writes it to the active connection, if any. Every
// writer (handshake, data streamer, sequence completion/abort reports,
// state_req) goes through this so concurrent writes never race on the
// gorilla/websocket connection, which is not safe for concurrent writers.
func (s *Simulator) send(v interface{}) error {
	s.connMu.Lock()
	c := s.conn
	s.connMu.Unlock()
	if c == nil {
		return fmt.Errorf("daqsim: not connected")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return c.WriteMessage(websocket.TextMessage, b)
}

// SendRaw marshals and sends v verbatim to the active connection. Exported
// for tests that need to inject a message shape a real DAQ node would not
// normally produce on its own — e.g. a `sequence_complete` carrying a
// deliberately stale runId, to prove the control node's correlation logic
// rejects it when talking to a real node rather than a hand-written fake.
func (s *Simulator) SendRaw(v interface{}) error {
	return s.send(v)
}

// LastArmed returns the state name and runId of the most recently received
// state_update, and whether one has been received yet. Exposed so tests can
// construct a guaranteed-stale runId without hardcoding engine internals.
func (s *Simulator) LastArmed() (state string, runID int64, ok bool) {
	s.armedMu.Lock()
	defer s.armedMu.Unlock()
	return s.armedState, s.armedRunID, s.armedState != ""
}

// RequestState sends a `state_req` (DAQ -> CTR): asks the control node to
// re-resolve and re-send the current daq_local state's payload, as a real
// node would after rebooting or losing its cache. No-op-returning-error when
// not connected.
func (s *Simulator) RequestState() error {
	return s.send(map[string]string{"type": "state_req"})
}

// streamData sends one `data` frame at the config's sampleRateHz (or
// DataRateOverrideHz if set) until stop is closed. Always driven by the real
// wall clock — independent of the injected Clock — so a FakeClock-driven test
// doesn't turn this into a busy loop; nothing about data-frame cadence is
// part of what these tests assert.
func (s *Simulator) streamData(cfg daqConfig, stop <-chan struct{}) {
	hz := cfg.SampleRateHz
	if s.opts.DataRateOverrideHz > 0 {
		hz = s.opts.DataRateOverrideHz
	}
	if hz <= 0 {
		hz = 50
	}
	period := time.Duration(float64(time.Second) / hz)
	if period <= 0 {
		period = 20 * time.Millisecond
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-ticker.C:
			snap := s.model.Snapshot(now.Sub(s.started).Seconds())
			_ = s.send(map[string]interface{}{
				"type": "data",
				"t":    float64(now.UnixMilli()) / 1000.0,
				"d":    snap,
			})
		}
	}
}

// ── Inbound message dispatch ─────────────────────────────────────────────────

type wireCmd struct {
	Type   string      `json:"type"`
	RefDes string      `json:"refDes"`
	Value  interface{} `json:"value"`
}

func (s *Simulator) handleMessage(raw []byte) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		s.logf("bad JSON from control node: %v", err)
		return
	}
	switch head.Type {
	case "cmd":
		var c wireCmd
		if err := json.Unmarshal(raw, &c); err != nil {
			s.logf("bad cmd: %v", err)
			return
		}
		v, ok := toFloat(c.Value)
		if !ok {
			s.logf("cmd %s: unsupported value %v", c.RefDes, c.Value)
			return
		}
		// Applied immediately — no queuing, no timed step (docs: commands are
		// not sequenced).
		s.model.Set(c.RefDes, v)

	case "state_update":
		s.handleStateUpdate(raw)

	default:
		// config_req/abort_triggered/sequence_complete/state_req are DAQ -> CTR
		// only; the node should never receive them. data/err are also
		// DAQ -> CTR. Anything else arriving here is a protocol violation
		// worth surfacing rather than silently ignoring.
		s.logf("unexpected message type %q from control node", head.Type)
	}
}

func toFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		if t == "1" || t == "true" {
			return 1, true
		}
		if t == "0" || t == "false" {
			return 0, true
		}
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// ── Accessors ────────────────────────────────────────────────────────────────

// ChannelValue reads one channel's current value from the model.
func (s *Simulator) ChannelValue(refDes string) (float64, bool) {
	return s.model.Get(refDes, time.Since(s.started).Seconds())
}

// ChannelSnapshot returns every known channel's current value.
func (s *Simulator) ChannelSnapshot() map[string]float64 {
	return s.model.Snapshot(time.Since(s.started).Seconds())
}

// SetSensor overrides a sensor channel's SensorSpec at runtime — how tests
// drive a value over an abort_rule threshold, and what the standalone
// binary's manual-abort trigger calls.
func (s *Simulator) SetSensor(refDes string, spec SensorSpec) bool {
	return s.model.SetSensor(refDes, spec)
}

// AppliedLog returns a copy of every set-point applied so far, in order.
func (s *Simulator) AppliedLog() []AppliedSetPoint {
	s.appliedMu.Lock()
	defer s.appliedMu.Unlock()
	out := make([]AppliedSetPoint, len(s.applied))
	copy(out, s.applied)
	return out
}

// Runs returns a copy of every entry_sequence run's outcome so far, in order.
func (s *Simulator) Runs() []SeqRecord {
	s.runsMu.Lock()
	defer s.runsMu.Unlock()
	out := make([]SeqRecord, len(s.runs))
	copy(out, s.runs)
	return out
}

// Config returns the most recently received config, if any.
func (s *Simulator) Config() (channels []string, sampleRateHz float64, ok bool) {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if !s.haveCfg {
		return nil, 0, false
	}
	for _, ch := range s.cfg.Channels {
		channels = append(channels, ch.RefDes)
	}
	return channels, s.cfg.SampleRateHz, true
}

func (s *Simulator) logf(format string, args ...interface{}) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
		return
	}
	log.Printf("daqsim %s: "+format, append([]interface{}{s.opts.RefDes}, args...)...)
}
