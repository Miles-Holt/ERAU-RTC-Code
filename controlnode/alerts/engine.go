package alerts

import (
	"fmt"
	"log"
	"sync"
	"time"

	"controlnode/dsl"
)

// EngineConfig configures an alert Engine.
type EngineConfig struct {
	// Config is the compiled .alert program.  Required (may be empty).
	Config *Config
	// Registry receives every raise/clear.  Required.
	Registry *Registry
	// Nodes lists the configured daqNode refDes values.  The `every_daqnode`
	// template is instantiated once per entry.
	Nodes []string

	// Values resolves channel names for messages interpolated OUTSIDE a tick
	// (template events arrive from the broker and DAQ client goroutines).  It
	// must be safe for concurrent use.  Optional: without it, channel
	// placeholders in template messages render as "?".
	Values dsl.ChannelSpace

	// KnownChannels is the configured channel space.  It is used only to phrase
	// runtime evaluation failures: a rule referencing a configured channel that
	// has not published a value yet (pre-DAQ-connect) reports "no value yet",
	// not "unknown channel" — which would send an operator hunting for a config
	// typo that does not exist.  Optional.
	KnownChannels []string

	// Now is the wall clock used for stale detection.  Defaults to time.Now.
	Now func() time.Time

	// OnError receives rule evaluation failures (e.g. a condition that is not
	// boolean at runtime).  Defaults to log.Printf.
	OnError func(error)
}

// Engine evaluates alert rules once per engine tick and turns daqNode lifecycle
// events into template alerts.  All exported methods are safe to call from any
// goroutine.
type Engine struct {
	cfg   *Config
	reg   *Registry
	vals  dsl.ChannelSpace
	now   func() time.Time
	onErr func(error)

	staleMs int64
	nodes   []string
	known   map[string]bool

	mu sync.Mutex
	// ruleOn is the last evaluated value of each rule condition; rules are
	// edge-triggered off it.
	ruleOn map[string]bool
	// connected / everConnected drive disconnect/reconnect edges.  A node's
	// FIRST connect is not a "reconnect" — nothing was lost.
	connected     map[string]bool
	everConnected map[string]bool
	// lastSeen is the arrival time of the last data message per node, and
	// staleOn tracks whether a stale alert is currently raised for it.
	lastSeen map[string]int64
	staleOn  map[string]bool
	// errOnce suppresses repeated identical rule errors so a permanently
	// broken condition cannot flood the log at tick rate.
	errSeen map[string]bool
}

// NewEngine creates an alert engine.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.Config == nil {
		return nil, fmt.Errorf("alerts: Config is required")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("alerts: Registry is required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	onErr := cfg.OnError
	if onErr == nil {
		onErr = func(err error) { log.Printf("alerts: %v", err) }
	}

	e := &Engine{
		cfg:           cfg.Config,
		reg:           cfg.Registry,
		vals:          cfg.Values,
		now:           now,
		onErr:         onErr,
		staleMs:       cfg.Config.Template.StaleMs(),
		nodes:         append([]string(nil), cfg.Nodes...),
		ruleOn:        make(map[string]bool, len(cfg.Config.Rules)),
		connected:     make(map[string]bool, len(cfg.Nodes)),
		everConnected: make(map[string]bool, len(cfg.Nodes)),
		lastSeen:      make(map[string]int64, len(cfg.Nodes)),
		staleOn:       make(map[string]bool, len(cfg.Nodes)),
		errSeen:       make(map[string]bool),
	}
	if len(cfg.KnownChannels) > 0 {
		e.known = make(map[string]bool, len(cfg.KnownChannels))
		for _, c := range cfg.KnownChannels {
			e.known[c] = true
		}
	}
	return e, nil
}

// StaleMs returns the effective per-node data-receive timeout.
func (e *Engine) StaleMs() int64 { return e.staleMs }

// Nodes returns the daqNodes the template was instantiated for.
func (e *Engine) Nodes() []string { return append([]string(nil), e.nodes...) }

// ── Rule evaluation ───────────────────────────────────────────────────────────

// Tick evaluates every rule against the tick's channel snapshot and sweeps the
// per-node stale timers.  It is wired to the state machine engine's PostTick
// hook, so rules see exactly the values this tick's controllers saw, evaluated
// after they ran (spec order: computed channels → controllers → alert rules).
//
// space must not be retained: the engine reuses the same snapshot map every tick.
func (e *Engine) Tick(space dsl.ChannelSpace) {
	e.evalRules(space)
	e.sweepStale()
}

func (e *Engine) evalRules(space dsl.ChannelSpace) {
	for _, rule := range e.cfg.Rules {
		on, err := e.condition(rule, space)
		if err != nil {
			e.reportRuleErr(rule, err)
			continue
		}

		e.mu.Lock()
		prev := e.ruleOn[rule.Name]
		e.ruleOn[rule.Name] = on
		e.mu.Unlock()

		switch {
		case on && !prev:
			// Edge-triggered raise: false → true.
			e.reg.Raise(rule.ID(), rule.Severity, interpolate(rule.Message, nil, space))
		case !on && prev && !rule.Latch:
			// Non-latching rules resolve themselves; latching ones stay raised
			// until an operator acks them.
			e.reg.Clear(rule.ID())
		}
	}
}

func (e *Engine) condition(rule *Rule, space dsl.ChannelSpace) (bool, error) {
	v, err := dsl.Eval(rule.Cond, space)
	if err != nil {
		// A channel that exists in the config but has not published a value yet
		// (nothing connected, or a computed channel that has not run) is a very
		// different report from a name nothing in the config defines.
		if e.known != nil {
			err = dsl.DescribeEvalError(err, func(name string) bool { return e.known[name] })
		}
		return false, err
	}
	if v.Type() != "bool" {
		return false, fmt.Errorf("condition must be boolean, got %s", v.Type())
	}
	return v.Bool(), nil
}

func (e *Engine) reportRuleErr(rule *Rule, err error) {
	key := rule.Name + ":" + err.Error()
	e.mu.Lock()
	seen := e.errSeen[key]
	e.errSeen[key] = true
	e.mu.Unlock()
	if !seen {
		e.onErr(fmt.Errorf("%s:%d: alert %q: %v", rule.File, rule.Line, rule.Name, err))
	}
}

// ── Template events ───────────────────────────────────────────────────────────

// templateEvent returns the compiled event, if the template declares it.
func (e *Engine) templateEvent(name string) (TemplateEvent, bool) {
	if e.cfg.Template == nil {
		return TemplateEvent{}, false
	}
	ev, ok := e.cfg.Template.Events[name]
	return ev, ok
}

// ConnID is the stable registry id for a node's connection state.
func ConnID(node string) string { return "conn:" + node }

// StaleID is the stable registry id for a node's data-staleness alert.
func StaleID(node string) string { return "stale:" + node }

// BadID is the stable registry id for a channel's out-of-range alert.
func BadID(refDes string) string { return "bad:" + refDes }

// NodeConnected records that a daqNode's link came up.  The first connect after
// startup is silent; every later one raises the template's `reconnect` alert and
// resolves the outstanding disconnect.
func (e *Engine) NodeConnected(node string) {
	e.mu.Lock()
	already := e.connected[node]
	first := !e.everConnected[node]
	e.connected[node] = true
	e.everConnected[node] = true
	e.lastSeen[node] = e.nowMs()
	staleWas := e.staleOn[node]
	e.staleOn[node] = false
	e.mu.Unlock()

	if staleWas {
		e.reg.Clear(StaleID(node))
	}
	if already {
		return
	}
	if first {
		return // initial connect — nothing was lost, so nothing to report
	}
	if ev, ok := e.templateEvent(EventReconnect); ok {
		e.reg.Raise(ConnID(node), ev.Severity, e.renderNode(ev.Message, node))
		return
	}
	// No reconnect rule configured: still resolve the disconnect alert.
	e.reg.Clear(ConnID(node))
}

// NodeDisconnected records that a daqNode's link went down and raises the
// template's `disconnect` alert.  Stale detection is suspended while the node is
// down: "no data" is already reported by the disconnect alert.
func (e *Engine) NodeDisconnected(node string) {
	e.mu.Lock()
	was := e.connected[node]
	e.connected[node] = false
	staleWas := e.staleOn[node]
	e.staleOn[node] = false
	e.mu.Unlock()

	if staleWas {
		e.reg.Clear(StaleID(node))
	}
	if !was {
		return // already known to be down
	}
	if ev, ok := e.templateEvent(EventDisconnect); ok {
		e.reg.Raise(ConnID(node), ev.Severity, e.renderNode(ev.Message, node))
	}
}

// NodeData records that a data message arrived from a node.  It re-arms the
// stale timer and resolves an outstanding stale alert.  Called on the DAQ read
// path, so it does no work beyond a map update in the common case.
func (e *Engine) NodeData(node string) {
	e.mu.Lock()
	e.lastSeen[node] = e.nowMs()
	wasStale := e.staleOn[node]
	e.staleOn[node] = false
	e.mu.Unlock()

	if wasStale {
		e.reg.Clear(StaleID(node))
	}
}

// sweepStale raises the template's `stale` alert for every connected node that
// has not delivered data within the timeout.  Called once per tick.
func (e *Engine) sweepStale() {
	ev, ok := e.templateEvent(EventStale)
	if !ok {
		return
	}
	now := e.nowMs()

	type raise struct{ node string }
	var toRaise []raise

	e.mu.Lock()
	for _, node := range e.nodes {
		if !e.connected[node] || e.staleOn[node] {
			continue
		}
		last, seen := e.lastSeen[node]
		if !seen {
			continue
		}
		if now-last >= e.staleMs {
			e.staleOn[node] = true
			toRaise = append(toRaise, raise{node})
		}
	}
	e.mu.Unlock()

	for _, r := range toRaise {
		e.reg.Raise(StaleID(r.node), ev.Severity, e.renderNode(ev.Message, r.node))
	}
}

// BadData applies a broker bad-data transition.  status is "high", "low" or
// "ok"; "ok" resolves the alert.  The broker only calls this on transitions, so
// the alert is naturally edge-triggered.
func (e *Engine) BadData(refDes, node, status string, value float64) {
	if status == "ok" {
		e.reg.Clear(BadID(refDes))
		return
	}
	ev, ok := e.templateEvent(EventBadData)
	if !ok {
		return
	}
	fields := map[string]string{
		FieldRefDes: refDes,
		FieldNode:   node,
		FieldValue:  FormatFloat(value),
	}
	e.reg.Raise(BadID(refDes), ev.Severity, interpolate(ev.Message, fields, e.vals))
}

// renderNode interpolates a node-scoped template message.  {refDes} falls back
// to the node name, so `{refDes} stale` reads sensibly for node-level events.
func (e *Engine) renderNode(msg, node string) string {
	return interpolate(msg, map[string]string{
		FieldNode:   node,
		FieldRefDes: node,
	}, e.vals)
}

func (e *Engine) nowMs() int64 { return e.now().UnixMilli() }

// ── Standalone loop ───────────────────────────────────────────────────────────

// Run ticks the engine on its own timer.  It is only used when no state machine
// engine is running (no .sm files); otherwise the state machine's PostTick hook
// drives Tick, so rules share the machines' per-tick snapshot.
func (e *Engine) Run(stop <-chan struct{}, hz int, space func() dsl.ChannelSpace) {
	if hz <= 0 {
		hz = 100
	}
	t := time.NewTicker(time.Second / time.Duration(hz))
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			e.Tick(space())
		}
	}
}
