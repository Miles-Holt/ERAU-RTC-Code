// Package softchan implements software channels: virtual channels that live in
// the control node's memory, appear in the data stream, and are commandable from
// the browser using the same cmd messages as hardware channels.
package softchan

import (
	"context"
	"controlnode/broker"
	"controlnode/dsl"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"
)

// ── YAML file shapes (values only; definitions now come from .chan DSL files) ──

type yamlValuesFile struct {
	Values map[string]float64 `yaml:"values"`
}

// ── Internal types ────────────────────────────────────────────────────────────

// chanDef is the in-memory definition of a software channel.
type chanDef struct {
	RefDes      string
	Description string
	Units       string
	Role        string   // "cmd-float" | "" (read-only)
	Default     float64
	Min         *float64
	Max         *float64
}

// computeMeta carries the documentation-only fields of a computed channel
// (units, description).  They play no part in evaluation, but the /docs pages
// render them, and the compiled expression is the only place the units of a
// computed channel are recorded.
type computeMeta struct {
	Units       string
	Description string
}

// storeChannelSpace implements dsl.ChannelSpace for evaluating computed channels
// against the current values in the store plus hardware channels from the broker.
type storeChannelSpace struct {
	store  *Store
	broker *broker.Broker
}

// Get implements dsl.ChannelSpace
func (cs *storeChannelSpace) Get(name string) (dsl.Value, bool) {
	// Check soft channels first
	cs.store.mu.RLock()
	v, ok := cs.store.values[name]
	cs.store.mu.RUnlock()
	if ok {
		return dsl.NewFloat(v), true
	}

	// Check hardware channels from broker if available.  Single-key lookup:
	// copying the whole broker map per identifier made every compute expression
	// O(channels) and could tear across identifiers within one expression.
	if cs.broker != nil {
		if hval, ok := cs.broker.CurrentValue(name); ok {
			return dsl.NewFloat(hval), true
		}
	}

	return dsl.Value{}, false
}

// staticChannelSpace is a lock-free implementation of dsl.ChannelSpace that uses
// pre-captured values. Used during Recompute to avoid deadlock.
type staticChannelSpace struct {
	values map[string]float64
	broker *broker.Broker
}

// Get implements dsl.ChannelSpace
func (cs *staticChannelSpace) Get(name string) (dsl.Value, bool) {
	// Check soft channels first (no lock needed - values are pre-captured)
	if v, ok := cs.values[name]; ok {
		return dsl.NewFloat(v), true
	}

	// Check hardware channels from broker if available.  Single-key lookup:
	// copying the whole broker map per identifier made every compute expression
	// O(channels) and could tear across identifiers within one expression.
	if cs.broker != nil {
		if hval, ok := cs.broker.CurrentValue(name); ok {
			return dsl.NewFloat(hval), true
		}
	}

	return dsl.Value{}, false
}

// ── Store ─────────────────────────────────────────────────────────────────────

// Store holds all software channel definitions and their current values.
// It publishes values to the broker and handles set commands.
type Store struct {
	mu       sync.RWMutex
	defs     []chanDef          // ordered list of definitions (settable channels only)
	defIndex map[string]int     // refDes → index into defs
	values   map[string]float64 // refDes → current value (both settable and computed)

	defsPath   string // path to channels directory (config/channels/)
	valuesPath string // path to softChannelValues.yaml (for value persistence)

	b *broker.Broker // set by Run; used for immediate publish from SetInternal

	// Computed channel support
	computeExprs map[string]dsl.Expr     // channel name → compute expression
	computeOrder []string                // topological sort order for recomputation
	computeMeta  map[string]computeMeta  // channel name → doc-only metadata

	// dirty is set by Set/SetInternal and cleared by Flush.  Persistence never
	// happens on the caller's goroutine or under the store lock.
	dirty atomic.Bool
}


// RegisterStateMachineChannels adds auto-generated SM-<NAME>-STATE and
// SM-<NAME>-TARGET channels for each compiled machine.  They are created as
// REAL definitions (defs + defIndex entries), not bare value-map entries: the
// index is what SetInternal, Set and ConfigJSON key off, so without it the
// engine's state publishes would be silently dropped.
//
// STATE is read-only (role "", updated by the engine via SetInternal);
// TARGET is operator-writable (the HMI and other machines command it).
func (s *Store) RegisterStateMachineChannels(machineNames []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range machineNames {
		s.addDefLocked(chanDef{
			RefDes:      "SM-" + name + "-STATE",
			Description: "Current state of machine " + name,
			Role:        "", // read-only
		})
		s.addDefLocked(chanDef{
			RefDes:      "SM-" + name + "-TARGET",
			Description: "Requested state for machine " + name,
			Role:        "cmd-float",
		})
	}
}

// RegisterCycleTimeChannel adds the engine-provided, read-only CYCLE_TIME
// channel: the tick period in seconds (1 / engineTickRateHz).  It is
// registered the same way as SM-<NAME>-STATE — as a REAL definition, before
// machines compile, so a controller referencing CYCLE_TIME (e.g.
// `T-TIME = T-TIME + CYCLE_TIME`) resolves at compile time.  It is
// deliberately NOT a user-authored .chan entry: an operator write would
// silently corrupt every sequence clock, so the role is left "" (read-only)
// and the value is stamped directly rather than taken from a persisted
// softChannelValues.yaml.
func (s *Store) RegisterCycleTimeChannel(tickHz int) {
	if tickHz <= 0 {
		tickHz = 100
	}
	cycle := 1.0 / float64(tickHz)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addDefLocked(chanDef{
		RefDes:      "CYCLE_TIME",
		Description: "Engine tick period in seconds (1 / engineTickRateHz); read-only",
		Units:       "s",
		Role:        "", // read-only
		Default:     cycle,
	})
	// Always the live tick period, never a stale persisted value (there is
	// none to persist: CYCLE_TIME is a derived constant, not operator data).
	s.values["CYCLE_TIME"] = cycle
}

// addDefLocked registers one definition if it is not already known.
// Caller must hold s.mu.
func (s *Store) addDefLocked(d chanDef) {
	if _, ok := s.defIndex[d.RefDes]; ok {
		return
	}
	s.defs = append(s.defs, d)
	s.defIndex[d.RefDes] = len(s.defs) - 1
	if _, ok := s.values[d.RefDes]; !ok {
		s.values[d.RefDes] = d.Default
	}
}

// RefDesMap returns a map of every software channel refDes → "_SOFTCHAN".
// Add these entries to the broker's refDesMap before starting.
func (s *Store) RefDesMap() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]string, len(s.defs)+len(s.computeExprs))
	for _, d := range s.defs {
		m[d.RefDes] = "_SOFTCHAN"
	}
	for name := range s.computeExprs {
		m[name] = "_SOFTCHAN"
	}
	return m
}

// Get returns the current value of a software channel.
func (s *Store) Get(refDes string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.values[refDes]
	return v, ok
}

// EachValue calls fn for every software channel value under a single read lock.
// fn must not call back into the store.
func (s *Store) EachValue(fn func(refDes string, value float64)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for k, v := range s.values {
		fn(k, v)
	}
}

// Set validates and stores a new value.  Returns an error if the channel is
// unknown, read-only, or out of bounds.
func (s *Store) Set(refDes string, value float64) error {
	s.mu.Lock()

	// Reject computed channels (they are always read-only)
	if _, isComputed := s.computeExprs[refDes]; isComputed {
		s.mu.Unlock()
		return fmt.Errorf("softchan: channel %q is computed (read-only)", refDes)
	}

	idx, ok := s.defIndex[refDes]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("softchan: unknown channel %q", refDes)
	}
	d := s.defs[idx]
	switch {
	case d.Role == "":
		s.mu.Unlock()
		return fmt.Errorf("softchan: channel %q is read-only", refDes)
	case d.Min != nil && value < *d.Min:
		s.mu.Unlock()
		return fmt.Errorf("softchan: %q value %.4g below min %.4g", refDes, value, *d.Min)
	case d.Max != nil && value > *d.Max:
		s.mu.Unlock()
		return fmt.Errorf("softchan: %q value %.4g above max %.4g", refDes, value, *d.Max)
	}
	s.values[refDes] = value
	s.mu.Unlock()

	s.markDirty()
	return nil
}

// SetInternal bypasses role/bounds guards.  Used by the engine to update
// read-only channels such as SM-<NAME>-STATE.
func (s *Store) SetInternal(refDes string, value float64) {
	s.mu.Lock()
	if _, ok := s.defIndex[refDes]; !ok {
		s.mu.Unlock()
		log.Printf("softchan: SetInternal on unknown channel %q — ignored", refDes)
		return
	}
	s.values[refDes] = value
	b := s.b
	s.mu.Unlock()

	s.markDirty()
	if b != nil {
		b.PublishData(broker.DataEvent{Values: map[string]float64{refDes: value}})
	}
}

// ── Persistence (off the hot path) ────────────────────────────────────────────
//
// Writing softChannelValues.yaml used to happen inline, under the store lock,
// on every single Set — a disk write in front of every reader.  Now a Set only
// raises a dirty flag; a background flusher serialises a snapshot taken under a
// short read lock and writes it outside the lock entirely.

// markDirty records that values changed and need persisting.
func (s *Store) markDirty() { s.dirty.Store(true) }

// snapshotPersistable copies the values that belong in the values file.
// Computed channels are excluded: they are derived every tick, so persisting
// them is both pointless and misleading on restart.
func (s *Store) snapshotPersistable() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]float64, len(s.values))
	for k, v := range s.values {
		if _, computed := s.computeExprs[k]; computed {
			continue
		}
		out[k] = v
	}
	return out
}

// Flush writes pending value changes to disk immediately.  Called by the
// background flusher and once more on shutdown so nothing is lost.
func (s *Store) Flush() {
	if !s.dirty.Swap(false) {
		return
	}
	if s.valuesPath == "" {
		return
	}
	data, err := yaml.Marshal(map[string]interface{}{"values": s.snapshotPersistable()})
	if err != nil {
		log.Printf("softchan: marshal values: %v", err)
		return
	}
	// Write via a temp file so a crash mid-write cannot truncate the real one.
	tmp := s.valuesPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("softchan: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, s.valuesPath); err != nil {
		log.Printf("softchan: rename %s: %v", tmp, err)
	}
}

// RunPersister flushes dirty values at a fixed interval until ctx is done, then
// performs a final flush.  Safe to run in its own goroutine.
func (s *Store) RunPersister(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.Flush()
			return
		case <-t.C:
			s.Flush()
		}
	}
}

// ── Documentation ─────────────────────────────────────────────────────────────

// ChannelDoc describes one software channel for the /docs pages.  It is built
// from the SAME compiled definitions the engine runs against, so the page can
// never drift from the loaded config.
type ChannelDoc struct {
	RefDes      string
	Description string
	Units       string
	Role        string // "cmd-float" for settable, "" for read-only
	Default     float64
	Min         *float64
	Max         *float64
	Computed    bool
	Compute     string // compute expression source text (computed channels only)
}

// Docs returns every software channel: the settable definitions in file order,
// then the computed channels in dependency (recompute) order.
func (s *Store) Docs() []ChannelDoc {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]ChannelDoc, 0, len(s.defs)+len(s.computeExprs))
	for _, d := range s.defs {
		out = append(out, ChannelDoc{
			RefDes:      d.RefDes,
			Description: d.Description,
			Units:       d.Units,
			Role:        d.Role,
			Default:     d.Default,
			Min:         d.Min,
			Max:         d.Max,
		})
	}
	for _, name := range s.computeOrder {
		expr, ok := s.computeExprs[name]
		if !ok {
			continue
		}
		meta := s.computeMeta[name]
		out = append(out, ChannelDoc{
			RefDes:      name,
			Description: meta.Description,
			Units:       meta.Units,
			Computed:    true,
			Compute:     dsl.ExprString(expr),
		})
	}
	return out
}

// ConfigJSON returns the softchan_config JSON bytes to send to browsers on connect.
func (s *Store) ConfigJSON() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()

	type chJSON struct {
		RefDes      string   `json:"refDes"`
		Description string   `json:"description"`
		Units       string   `json:"units"`
		Role        string   `json:"role"`
		Default     float64  `json:"default"`
		Min         *float64 `json:"min"`
		Max         *float64 `json:"max"`
		Computed    bool     `json:"computed,omitempty"`
	}
	channels := make([]chJSON, 0, len(s.defs)+len(s.computeExprs))

	// Add regular channels
	for _, d := range s.defs {
		channels = append(channels, chJSON{
			RefDes:      d.RefDes,
			Description: d.Description,
			Units:       d.Units,
			Role:        d.Role,
			Default:     d.Default,
			Min:         d.Min,
			Max:         d.Max,
		})
	}

	// Add computed channels (read-only, no role/default/min/max)
	for name := range s.computeExprs {
		channels = append(channels, chJSON{
			RefDes:   name,
			Computed: true,
		})
	}

	msg := map[string]interface{}{
		"type":     "softchan_config",
		"channels": channels,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		log.Printf("softchan: marshal config: %v", err)
		return nil
	}
	return b
}

// Run starts the publish/command loop for the software channel store.
// It publishes the full current value set to the broker at a low keepalive rate
// (so new clients aren't stale) and publishes each set immediately as it happens.
// broadcastRateHz is accepted for signature stability but no longer paces the loop.
// Blocks until the process exits.
func (s *Store) Run(b *broker.Broker, broadcastRateHz int) {
	// Store broker reference so SetInternal can publish immediately.
	s.mu.Lock()
	s.b = b
	s.mu.Unlock()

	// Register a cmd channel so the broker can route commands to us.
	cmdCh := make(chan []byte, 64)
	b.RegisterDaq("_SOFTCHAN", cmdCh)
	defer b.RegisterDaq("_SOFTCHAN", nil) // deregister on exit

	// Keepalive: re-publish the full soft-channel state at a low rate so newly
	// connected clients receive current values (the broker broadcasts deltas and
	// keeps no per-client snapshot).  Live changes are published immediately in
	// the Set path below, so this heartbeat only needs to be slow — no reason to
	// re-send unchanged setpoints at the full broadcast rate.
	const keepalive = time.Second
	ticker := time.NewTicker(keepalive)
	defer ticker.Stop()

	for {
		select {

		// ── Keepalive: publish all current values so new clients aren't stale ──
		case <-ticker.C:
			s.mu.RLock()
			vals := make(map[string]float64, len(s.values))
			for k, v := range s.values {
				vals[k] = v
			}
			s.mu.RUnlock()
			b.PublishData(broker.DataEvent{Values: vals})

		// ── Handle set commands from the broker ───────────────────────────────
		case raw, ok := <-cmdCh:
			if !ok {
				return
			}
			var msg struct {
				RefDes string      `json:"refDes"`
				Value  interface{} `json:"value"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("softchan: bad cmd JSON: %v", err)
				continue
			}
			// Value arrives as float64 from JSON.
			val, ok := toFloat64(msg.Value)
			if !ok {
				log.Printf("softchan: cmd for %q has non-numeric value %v", msg.RefDes, msg.Value)
				continue
			}
			if err := s.Set(msg.RefDes, val); err != nil {
				log.Printf("softchan: set %q = %.4g: %v", msg.RefDes, val, err)
			} else {
				log.Printf("softchan: set %q = %.4g", msg.RefDes, val)
				// Publish immediately so the web client sees the update in the
				// next broadcast, not after a full ticker cycle.
				b.PublishData(broker.DataEvent{Values: map[string]float64{msg.RefDes: val}})
			}
		}
	}
}

// toFloat64 coerces a JSON-decoded value (float64 or bool) to float64.
func toFloat64(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// Recompute evaluates all computed channels in dependency order against a ChannelSpace.
// It updates the store's values and publishes changes to the broker if provided.
// Returns an error if any computation fails.
func (s *Store) Recompute(b *broker.Broker) error {
	s.mu.Lock()

	// Make a copy of current values to avoid holding lock during eval
	valuesCopy := make(map[string]float64)
	for k, v := range s.values {
		valuesCopy[k] = v
	}

	computeOrder := make([]string, len(s.computeOrder))
	copy(computeOrder, s.computeOrder)

	computeExprs := make(map[string]dsl.Expr)
	for k, v := range s.computeExprs {
		computeExprs[k] = v
	}

	s.mu.Unlock()

	// Create a channel space that uses our value copies
	cs := &staticChannelSpace{
		values: valuesCopy,
		broker: b,
	}

	published := make(map[string]float64)

	// Evaluate expressions without holding the lock
	updatedValues := make(map[string]float64)
	for _, name := range computeOrder {
		expr, ok := computeExprs[name]
		if !ok {
			continue
		}

		val, err := dsl.Eval(expr, cs)
		if err != nil {
			return fmt.Errorf("softchan: compute %q: %w", name, err)
		}

		// Value.Float() only carries the numeric payload: on a bool-typed value
		// it returns 0 regardless of true/false, which silently pinned every
		// boolean compute channel to 0.  Coerce explicitly by type instead.
		var newVal float64
		switch val.Type() {
		case "float":
			newVal = val.Float()
		case "bool":
			if val.Bool() {
				newVal = 1
			}
		default:
			return fmt.Errorf("softchan: compute %q: expression yielded %s, want float or bool", name, val.Type())
		}
		updatedValues[name] = newVal
		cs.values[name] = newVal // Update for next iterations

		if oldVal, ok := valuesCopy[name]; !ok || oldVal != newVal {
			published[name] = newVal
		}
	}

	// Update store with new values
	s.mu.Lock()
	for name, val := range updatedValues {
		s.values[name] = val
	}
	s.mu.Unlock()

	// Publish any changes outside the lock
	if len(published) > 0 && b != nil {
		b.PublishData(broker.DataEvent{Values: published})
	}

	return nil
}
