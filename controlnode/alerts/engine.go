package alerts

import (
	"fmt"
	"log"
	"math"
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

	// Sensors are the SENSOR channels whose validMin/validMax bounds become an
	// auto-generated alert, one per channel.  Out of range IS an alarm — there
	// is no separate "bad data" idea in the operator's vocabulary — and these
	// alerts LATCH: a spike that came back down on its own still leaves the
	// object red until a person acks it, because the fact that it happened is
	// the thing worth knowing.  Channels with no usable bound generate nothing.
	Sensors []SensorChannel

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
	// sensors are the compiled auto-generated bounds alerts, in config order.
	sensors []sensorBand

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
	// sensorState is the last out-of-range status per sensor channel: "" when
	// the value is inside its band, otherwise "low" or "high".  Sensor alerts
	// are edge-triggered off it, so a channel that sits out of range raises
	// once, not once per tick.
	sensorState map[string]string
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
		sensorState:   make(map[string]string, len(cfg.Sensors)),
		errSeen:       make(map[string]bool),
	}
	e.sensors = compileSensors(cfg.Sensors, onErr)
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

// SensorAlerts returns the refDes of every channel that got an auto-generated
// bounds alert, in config order.  Used for startup logging: a channel silently
// missing from this list has no usable validMin/validMax.
func (e *Engine) SensorAlerts() []string {
	out := make([]string, len(e.sensors))
	for i := range e.sensors {
		out[i] = e.sensors[i].refDes
	}
	return out
}

// ── Rule evaluation ───────────────────────────────────────────────────────────

// Tick evaluates every rule against the tick's channel snapshot and sweeps the
// per-node stale timers.  It is wired to the state machine engine's PostTick
// hook, so rules see exactly the values this tick's controllers saw, evaluated
// after they ran (spec order: computed channels → controllers → alert rules).
//
// space must not be retained: the engine reuses the same snapshot map every tick.
func (e *Engine) Tick(space dsl.ChannelSpace) {
	e.evalRules(space)
	e.sweepSensors(space)
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
			description := ""
			if rule.Description != "" {
				description = interpolate(rule.Description, nil, space)
			}
			e.reg.RaiseFor(rule.ID(), rule.Severity,
				interpolate(rule.Message, nil, space), rule.channels, "", description,
				rule.PlotChannels, plotLines(rule.Lines))
		case !on && prev && !rule.Latch:
			// A rule with no `latch` asked to clear itself when the condition
			// goes away, so this is the one place the server may ack for the
			// operator.  Latching rules stay raised — Resolve, not ack — until a
			// person acknowledges them.
			e.reg.ResolveAndAck(rule.ID())
		case !on && prev && rule.Latch:
			// The condition recovered but the row stays red: resolved, unacked.
			e.reg.Resolve(rule.ID())
		}
	}
}

// plotLines converts a rule's compile-time-resolved PlotLine (config.go) into
// the wire-facing Line (registry.go) RaiseFor takes. The two types are
// structurally identical (Value *float64, Channel string, Label string) —
// PlotLine exists separately because it is alerts-internal compile-time
// state, while Line's JSON tags are the actual wire contract read by
// WebClient/js — so this is a plain field copy, not a resolution step
// (resolvePlotLine already did that work at load time). Returns nil for a
// nil/empty input so a rule with no `channels` block raises with a nil
// Lines, not an empty-but-allocated slice, keeping Record.Lines' `omitempty`
// meaningful on the wire.
func plotLines(in []PlotLine) []Line {
	if len(in) == 0 {
		return nil
	}
	out := make([]Line, len(in))
	for i, l := range in {
		out[i] = Line{Value: l.Value, Channel: l.Channel, Label: l.Label}
	}
	return out
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

// BadID is the stable registry id for a channel's template bad_data alert.
func BadID(refDes string) string { return "bad:" + refDes }

// SensorID is the stable registry id for a channel's auto-generated bounds
// alarm.  One id per channel, so a channel that keeps leaving its band updates
// the same row instead of stacking duplicates.
func SensorID(refDes string) string { return "sensor:" + refDes }

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
		e.reg.Resolve(StaleID(node))
	}
	if already {
		return
	}
	if first {
		return // initial connect — nothing was lost, so nothing to report
	}
	if ev, ok := e.templateEvent(EventReconnect); ok {
		e.reg.RaiseFor(ConnID(node), ev.Severity, e.renderNode(ev.Message, node), nil, node, "", nil, nil)
		return
	}
	// No reconnect rule configured — which is the normal case now that the
	// template no longer declares one.  The link being back does not mean the
	// outage has been dealt with, so the disconnect alarm is resolved, not
	// acked: it stays on the board, and the node stays red, until an operator
	// acknowledges it.
	e.reg.Resolve(ConnID(node))
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
		e.reg.Resolve(StaleID(node))
	}
	if !was {
		return // already known to be down
	}
	if ev, ok := e.templateEvent(EventDisconnect); ok {
		e.reg.RaiseFor(ConnID(node), ev.Severity, e.renderNode(ev.Message, node), nil, node, "", nil, nil)
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
		e.reg.Resolve(StaleID(node))
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
		e.reg.RaiseFor(StaleID(r.node), ev.Severity,
			e.renderNode(ev.Message, r.node), nil, r.node, "", nil, nil)
	}
}

// BadData applies a broker bad-data transition.  status is "high", "low" or
// "ok"; "ok" resolves the alert.  The broker only calls this on transitions, so
// the alert is naturally edge-triggered.
func (e *Engine) BadData(refDes, node, status string, value float64) {
	if status == "ok" {
		// Back in range resolves the row but does not ack it: only a person
		// takes the red away.
		e.reg.Resolve(BadID(refDes))
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
	e.reg.RaiseFor(BadID(refDes), ev.Severity,
		interpolate(ev.Message, fields, e.vals), []string{refDes}, "", "", nil, nil)
}

// ── Auto-generated sensor bounds alerts ───────────────────────────────────────
//
// Every SENSOR channel with a validMin/validMax band gets an alert generated
// from it — no .alert file involved, because "this reading is outside the band
// the config says is valid" is not a matter of taste.  Out of range is an ALARM,
// and the alert LATCHES: the row goes resolved-but-unacked when the value comes
// back, and only a person clears the red.
//
// These are evaluated here, off the engine's own per-tick channel snapshot,
// rather than off the broker's bad_data transitions, so they do not depend on
// bad_data continuing to exist.

// sensorBand is one compiled channel band.  Bounds are stored as plain floats
// plus a "have it" flag: one-sided bands are normal (a pressure with a ceiling
// and no floor), and the rendered "(valid …)" suffix is built once at startup
// rather than on every raise.
type sensorBand struct {
	refDes string
	min    float64
	max    float64
	hasMin bool
	hasMax bool
	suffix string // e.g. " (valid 0 to 800)"
}

// SensorChannel is one channel's configured validity band, as it comes out of
// controls.yaml (validMin / validMax, either of which may be absent).
type SensorChannel struct {
	RefDes string
	Min    *float64
	Max    *float64
}

// compileSensors turns the configured bands into evaluable ones.  A channel with
// nothing usable to compare against generates NO alert at all — silence is
// correct there, an alert that can never fire is just a row that lies.  A band
// that cannot be satisfied (min above max, so every possible reading is out of
// range) is a config mistake rather than an alarm, so it is reported once at
// startup and skipped.
func compileSensors(in []SensorChannel, onErr func(error)) []sensorBand {
	if len(in) == 0 {
		return nil
	}
	out := make([]sensorBand, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, sc := range in {
		if sc.RefDes == "" || seen[sc.RefDes] {
			continue
		}
		b := sensorBand{refDes: sc.RefDes}
		if sc.Min != nil && !math.IsNaN(*sc.Min) && !math.IsInf(*sc.Min, 0) {
			b.min, b.hasMin = *sc.Min, true
		}
		if sc.Max != nil && !math.IsNaN(*sc.Max) && !math.IsInf(*sc.Max, 0) {
			b.max, b.hasMax = *sc.Max, true
		}
		if !b.hasMin && !b.hasMax {
			continue // no bounds worth checking
		}
		if b.hasMin && b.hasMax && b.min > b.max {
			onErr(fmt.Errorf("channel %s: validMin %s is above validMax %s, so no reading could ever be valid: no out-of-range alert generated",
				sc.RefDes, FormatFloat(b.min), FormatFloat(b.max)))
			continue
		}
		switch {
		case b.hasMin && b.hasMax:
			b.suffix = " (valid " + FormatFloat(b.min) + " to " + FormatFloat(b.max) + ")"
		case b.hasMin:
			b.suffix = " (valid min " + FormatFloat(b.min) + ")"
		default:
			b.suffix = " (valid max " + FormatFloat(b.max) + ")"
		}
		seen[sc.RefDes] = true
		out = append(out, b)
	}
	return out
}

// sweepSensors checks every generated band against this tick's snapshot and
// raises on the EDGE into an out-of-range status.  A channel that stays out of
// range does not re-raise, so the value quoted in the message is the value that
// tripped it — the one worth reporting — and the browser is not written to at
// tick rate.  A swing straight from below the floor to above the ceiling is a
// new fact, so a change of direction does re-raise.
func (e *Engine) sweepSensors(space dsl.ChannelSpace) {
	if len(e.sensors) == 0 || space == nil {
		return
	}

	type reading struct {
		band   *sensorBand
		status string
		value  float64
	}
	// Read every band first, with no engine lock held: the channel space may be
	// a live reader whose own lock the broker takes while it calls back into this
	// engine, and holding e.mu across it would invert that order.
	readings := make([]reading, 0, len(e.sensors))
	for i := range e.sensors {
		b := &e.sensors[i]
		status, value, ok := b.evaluate(space)
		if !ok {
			continue // no value yet, or not a number: nothing to judge
		}
		readings = append(readings, reading{band: b, status: status, value: value})
	}

	type edge struct {
		id      string
		refDes  string
		message string
		resolve bool
	}
	var edges []edge

	e.mu.Lock()
	for _, r := range readings {
		if r.status == e.sensorState[r.band.refDes] {
			continue
		}
		e.sensorState[r.band.refDes] = r.status
		if r.status == "" {
			edges = append(edges, edge{
				id: SensorID(r.band.refDes), refDes: r.band.refDes, resolve: true})
			continue
		}
		edges = append(edges, edge{
			id:      SensorID(r.band.refDes),
			refDes:  r.band.refDes,
			message: r.band.refDes + " out of range: " + FormatFloat(r.value) + r.band.suffix,
		})
	}
	e.mu.Unlock()

	for _, ed := range edges {
		if ed.resolve {
			// Latching: the row goes resolved-but-unacked, NOT acked.  The
			// object stays red until an operator acknowledges it.
			e.reg.Resolve(ed.id)
			continue
		}
		e.reg.RaiseFor(ed.id, SeverityAlarm, ed.message, []string{ed.refDes}, "", "", nil, nil)
	}
}

// evaluate reports the channel's out-of-range status ("", "low" or "high") and
// the value it read.  ok is false when the channel has no value yet (nothing has
// published it — typically a DAQ node that has not connected), when the value is
// not a number, or when it is NaN: none of those are "out of range", and the
// broker's bounds check treats them the same way.
func (b *sensorBand) evaluate(space dsl.ChannelSpace) (status string, value float64, ok bool) {
	v, found := space.Get(b.refDes)
	if !found {
		return "", 0, false
	}
	switch v.Type() {
	case "bool", "string":
		return "", 0, false
	}
	f := v.Float()
	if math.IsNaN(f) {
		return "", 0, false
	}
	switch {
	case b.hasMin && f < b.min:
		return "low", f, true
	case b.hasMax && f > b.max:
		return "high", f, true
	}
	return "", f, true
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
