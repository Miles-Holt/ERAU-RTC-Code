// Package softchan implements software channels: virtual channels that live in
// the control node's memory, appear in the data stream, and are commandable from
// the browser using the same cmd messages as hardware channels.
package softchan

import (
	"controlnode/broker"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ── YAML file shapes ──────────────────────────────────────────────────────────

type yamlDefsFile struct {
	Channels []yamlDef `yaml:"channels"`
}

type yamlDef struct {
	RefDes      string   `yaml:"refDes"`
	Description string   `yaml:"description"`
	Units       string   `yaml:"units"`
	Role        string   `yaml:"role"`
	Default     float64  `yaml:"default"`
	Min         *float64 `yaml:"min"`
	Max         *float64 `yaml:"max"`
}

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

// ── Store ─────────────────────────────────────────────────────────────────────

// Store holds all software channel definitions and their current values.
// It publishes values to the broker and handles set commands.
type Store struct {
	mu       sync.RWMutex
	defs     []chanDef          // ordered list of definitions
	defIndex map[string]int     // refDes → index into defs
	values   map[string]float64 // refDes → current value

	defsPath   string // path to softChannels.yaml
	valuesPath string // path to softChannelValues.yaml
}

// New creates a Store and loads definitions + persisted values from disk.
// Call Run(b, ...) after the broker is started to begin publishing.
func New(defsPath, valuesPath string) (*Store, error) {
	s := &Store{
		defsPath:   defsPath,
		valuesPath: valuesPath,
		defIndex:   make(map[string]int),
		values:     make(map[string]float64),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads definitions from softChannels.yaml and values from softChannelValues.yaml.
// Values file is optional — missing file is treated as empty.
func (s *Store) load() error {
	// Definitions
	data, err := os.ReadFile(s.defsPath)
	if err != nil {
		return fmt.Errorf("softchan: read %s: %w", s.defsPath, err)
	}
	var defs yamlDefsFile
	if err := yaml.Unmarshal(data, &defs); err != nil {
		return fmt.Errorf("softchan: parse %s: %w", s.defsPath, err)
	}

	// Persisted values (optional)
	persisted := make(map[string]float64)
	valData, err := os.ReadFile(s.valuesPath)
	if err == nil {
		var vf yamlValuesFile
		if err := yaml.Unmarshal(valData, &vf); err == nil && vf.Values != nil {
			persisted = vf.Values
		}
	}

	// Merge: definition order preserved; use persisted value if available, else default.
	s.defs = s.defs[:0]
	s.defIndex = make(map[string]int)
	s.values = make(map[string]float64)
	for i, d := range defs.Channels {
		s.defs = append(s.defs, chanDef{
			RefDes:      d.RefDes,
			Description: d.Description,
			Units:       d.Units,
			Role:        d.Role,
			Default:     d.Default,
			Min:         d.Min,
			Max:         d.Max,
		})
		s.defIndex[d.RefDes] = i
		if v, ok := persisted[d.RefDes]; ok {
			s.values[d.RefDes] = v
		} else {
			s.values[d.RefDes] = d.Default
		}
	}
	return nil
}

// RefDesMap returns a map of every software channel refDes → "_SOFTCHAN".
// Add these entries to the broker's refDesMap before starting.
func (s *Store) RefDesMap() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m := make(map[string]string, len(s.defs))
	for _, d := range s.defs {
		m[d.RefDes] = "_SOFTCHAN"
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

// Set validates and stores a new value.  Returns an error if the channel is
// unknown, read-only, or out of bounds.
func (s *Store) Set(refDes string, value float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.defIndex[refDes]
	if !ok {
		return fmt.Errorf("softchan: unknown channel %q", refDes)
	}
	d := s.defs[idx]
	if d.Role == "" {
		return fmt.Errorf("softchan: channel %q is read-only", refDes)
	}
	if d.Min != nil && value < *d.Min {
		return fmt.Errorf("softchan: %q value %.4g below min %.4g", refDes, value, *d.Min)
	}
	if d.Max != nil && value > *d.Max {
		return fmt.Errorf("softchan: %q value %.4g above max %.4g", refDes, value, *d.Max)
	}
	s.values[refDes] = value
	s.persistLocked()
	return nil
}

// SetInternal bypasses role/bounds guards.  Used by the state machine to update
// read-only channels like SYS-STATE.
func (s *Store) SetInternal(refDes string, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.defIndex[refDes]; !ok {
		return
	}
	s.values[refDes] = value
	s.persistLocked()
}

// persistLocked writes current values to disk.  Caller must hold s.mu.Lock().
func (s *Store) persistLocked() {
	out := map[string]interface{}{
		"values": s.values,
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		log.Printf("softchan: marshal values: %v", err)
		return
	}
	if err := os.WriteFile(s.valuesPath, data, 0644); err != nil {
		log.Printf("softchan: write %s: %v", s.valuesPath, err)
	}
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
	}
	channels := make([]chJSON, 0, len(s.defs))
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
// It publishes all current values to the broker at broadcastRateHz and
// handles set commands routed from the broker. Blocks until the process exits.
func (s *Store) Run(b *broker.Broker, broadcastRateHz int) {
	if broadcastRateHz <= 0 {
		broadcastRateHz = 20
	}

	// Register a cmd channel so the broker can route commands to us.
	cmdCh := make(chan []byte, 64)
	b.RegisterDaq("_SOFTCHAN", cmdCh)
	defer b.RegisterDaq("_SOFTCHAN", nil) // deregister on exit

	ticker := time.NewTicker(time.Second / time.Duration(broadcastRateHz))
	defer ticker.Stop()

	for {
		select {

		// ── Publish current values to the broker ──────────────────────────────
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
