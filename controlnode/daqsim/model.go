package daqsim

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
)

// ── Wire config (CTR -> DAQ), mirrors config.BuildDaqNodeConfigJSON ───────────

// configModule is one entry of the `config` message's "modules" array.
type configModule struct {
	ModuleModelNumber string  `json:"moduleModelNumber"`
	SampleRateHz      float64 `json:"sampleRateHz"`
}

// configChannel is one entry of the `config` message's "channels" array. Only
// the fields the simulator actually uses are named explicitly; everything
// else on the wire (bridge-completion fields, thermocouple type, etc.) is
// accepted but not needed to drive the channel model.
type configChannel struct {
	RefDes            string `json:"refDes"`
	ModuleModelNumber string `json:"moduleModelNumber"`
	ChannelNumber     string `json:"channelNumber"`
	Units             string `json:"units"`
}

// daqConfig is the `config` message the control node sends after config_req.
type daqConfig struct {
	Type             string          `json:"type"`
	SampleRateHz     float64         `json:"sampleRateHz"`
	ManagementRateHz float64         `json:"managementRateHz"`
	Modules          []configModule  `json:"modules"`
	Channels         []configChannel `json:"channels"`
}

// ── Channel classification ─────────────────────────────────────────────────
//
// PROTOCOL GAP (see docs/daqsim.md "Protocol ambiguities"): the DAQ-side
// `config` message (docs/websocket-protocol.md Part 2) carries no `role`
// field the way the browser-side `config` does, so nothing on the wire says
// whether a channel is a command the node must echo or a read-only sensor —
// e.g. NV-03-CMD and NV-03-FB are both moduleModelNumber "Digital-IO",
// distinguishable only by the "-CMD" suffix convention used throughout
// config/controls.yaml. A physical output module can never be a sensor, so
// that direction is unambiguous; Digital-IO is the genuinely ambiguous case,
// resolved here by that naming convention rather than by a wire field.
const cmdSuffix = "-CMD"

func isCommandChannel(ch configChannel) bool {
	if strings.HasSuffix(ch.RefDes, cmdSuffix) {
		return true
	}
	// An output-only module can never be a sensor, regardless of naming.
	return ch.ModuleModelNumber == "Analog-Output"
}

// ── Channel model ───────────────────────────────────────────────────────────

// SensorSpec configures the plausible value a sensor channel reports:
// value(t) = Base + RampPerSec*t + noise, where noise is uniform in
// [-NoiseAmp, +NoiseAmp] drawn from the simulator's seeded RNG (zero when the
// simulator's Seed is 0 and NoiseAmp is left at its default, so a caller who
// wants strict determinism gets it without having to know that).
type SensorSpec struct {
	Base       float64
	NoiseAmp   float64
	RampPerSec float64
}

// chanKind classifies a channel discovered in the received config.
type chanKind int

const (
	kindSensor chanKind = iota
	kindCommand
)

type chanEntry struct {
	kind  chanKind
	value float64
	spec  SensorSpec
	// simT0 is the model's t=0 reference for RampPerSec, in seconds since the
	// model was built (Model.epoch), so ramps are deterministic under FakeClock.
}

// Model is the simulator's in-memory channel space, built entirely from the
// channels named in a received `config` message — no channel names are
// hardcoded. Command channels start at 0 and echo whatever was last written
// by a `cmd` message or a state_update set-point. Sensor channels report
// Base +/- noise (deterministic when a seed is supplied) plus an optional
// linear ramp.
type Model struct {
	mu      sync.RWMutex
	entries map[string]*chanEntry
	order   []string // config order, for deterministic iteration/logging
	rng     *rand.Rand
}

// NewModel creates an empty model. seed drives the sensor-noise RNG; 0 gives a
// fixed, still-deterministic seed (not real entropy) so default runs are
// reproducible unless the caller explicitly wants per-run variation.
func NewModel(seed int64) *Model {
	return &Model{
		entries: make(map[string]*chanEntry),
		rng:     rand.New(rand.NewSource(seed)),
	}
}

// BuildFromConfig (re)builds the channel set from a received `config`
// message. sensors overrides the default SensorSpec (Base 0, no noise, no
// ramp) for specific refDes values; anything not listed keeps sensible
// defaults. Command-channel values are preserved across a rebuild (e.g. a
// reconnect that re-sends the same config) so a valve's commanded state
// survives a dropped link, matching real hardware.
func (m *Model) BuildFromConfig(cfg daqConfig, sensors map[string]SensorSpec) {
	m.mu.Lock()
	defer m.mu.Unlock()

	prev := m.entries
	m.entries = make(map[string]*chanEntry, len(cfg.Channels))
	m.order = m.order[:0]

	for _, ch := range cfg.Channels {
		e := &chanEntry{}
		if isCommandChannel(ch) {
			e.kind = kindCommand
			if old, ok := prev[ch.RefDes]; ok && old.kind == kindCommand {
				e.value = old.value
			}
		} else {
			e.kind = kindSensor
			e.spec = sensors[ch.RefDes]
		}
		m.entries[ch.RefDes] = e
		m.order = append(m.order, ch.RefDes)
	}
	sort.Strings(m.order)
}

// Get returns a channel's current value: the last commanded value for a
// command channel, or a freshly computed reading for a sensor channel.
// tSec is seconds since the simulator started, used for RampPerSec.
func (m *Model) Get(refDes string, tSec float64) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[refDes]
	if !ok {
		return 0, false
	}
	if e.kind == kindCommand {
		return e.value, true
	}
	v := e.spec.Base + e.spec.RampPerSec*tSec
	if e.spec.NoiseAmp > 0 {
		v += (m.rng.Float64()*2 - 1) * e.spec.NoiseAmp
	}
	return v, true
}

// Snapshot returns every channel's current value (sensors evaluated at tSec).
func (m *Model) Snapshot(tSec float64) map[string]float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]float64, len(m.entries))
	for refDes, e := range m.entries {
		if e.kind == kindCommand {
			out[refDes] = e.value
			continue
		}
		v := e.spec.Base + e.spec.RampPerSec*tSec
		if e.spec.NoiseAmp > 0 {
			v += (m.rng.Float64()*2 - 1) * e.spec.NoiseAmp
		}
		out[refDes] = v
	}
	return out
}

// Set forces a channel's value, regardless of its classification. Applying a
// state_update set-point and forcing a sensor over an abort threshold (as the
// standalone binary's manual-abort trigger does) both go through this.
func (m *Model) Set(refDes string, value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[refDes]
	if !ok {
		// Unknown to the current config: still record it (defensively — a
		// command referencing a channel the config never listed is a bug the
		// simulator should make visible in Snapshot, not swallow), classified
		// as a command channel since that is what the write path implies.
		e = &chanEntry{kind: kindCommand}
		m.entries[refDes] = e
		m.order = append(m.order, refDes)
		sort.Strings(m.order)
	}
	if e.kind == kindCommand {
		e.value = value
	} else {
		// SetSensor is the intended entry point for sensor overrides, but a
		// direct Set on a sensor (e.g. from an abort-sequence step accidentally
		// naming a sensor refDes) still has to do something sane: pin the base
		// so subsequent reads reflect it.
		e.spec.Base = value
		e.spec.RampPerSec = 0
	}
}

// SetSensor overrides a sensor channel's SensorSpec at runtime (used by tests
// and the standalone binary's manual-abort trigger to drive a value over an
// abort_rule threshold). No-op if refDes is a command channel or unknown.
func (m *Model) SetSensor(refDes string, spec SensorSpec) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[refDes]
	if !ok || e.kind != kindSensor {
		return false
	}
	e.spec = spec
	return true
}

// evalCompare applies a comparison operator, matching the operators
// documented for abort_rule "if" strings (docs/websocket-protocol.md).
func evalCompare(op string, lhs, rhs float64) bool {
	switch op {
	case ">":
		return lhs > rhs
	case "<":
		return lhs < rhs
	case ">=":
		return lhs >= rhs
	case "<=":
		return lhs <= rhs
	case "==":
		return approxEqual(lhs, rhs)
	case "!=":
		return !approxEqual(lhs, rhs)
	default:
		return false
	}
}

// approxEqual guards the "==" / "!=" comparisons above against float noise
// from sensor jitter.
func approxEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
