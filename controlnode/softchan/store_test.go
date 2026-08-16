package softchan

import (
	"controlnode/broker"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestStore creates a .chan file (and no values file) in a temp dir and
// loads it via LoadFromDir. Returns a loaded Store.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	chanDir := filepath.Join(dir, "channels")
	if err := os.MkdirAll(chanDir, 0755); err != nil {
		t.Fatalf("mkdir channels: %v", err)
	}

	chanFile := filepath.Join(chanDir, "test.chan")
	defs := `channel SET-PRESS
    type float
    description "Target pressure"
    units psi
    default 100
    min 0
    max 500

channel SYS-STATE
    description "System state"
    default 0
`
	if err := os.WriteFile(chanFile, []byte(defs), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "softChannelValues.yaml")
	s, err := LoadFromDir(chanDir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	return s
}

// TestStoreLoadAndDefaults verifies definitions load and defaults are applied.
func TestStoreLoadAndDefaults(t *testing.T) {
	s := newTestStore(t)

	if v, ok := s.Get("SET-PRESS"); !ok || v != 100 {
		t.Errorf("SET-PRESS default = %v (ok=%v), want 100", v, ok)
	}
	if _, ok := s.Get("MISSING"); ok {
		t.Error("Get(MISSING) returned ok=true")
	}

	rm := s.RefDesMap()
	if rm["SET-PRESS"] != "_SOFTCHAN" || rm["SYS-STATE"] != "_SOFTCHAN" {
		t.Errorf("RefDesMap = %v, want both channels mapped to _SOFTCHAN", rm)
	}
}

// TestStoreSetValidation covers the Set guard rails: bounds, read-only, unknown.
func TestStoreSetValidation(t *testing.T) {
	s := newTestStore(t)

	if err := s.Set("SET-PRESS", 250); err != nil {
		t.Fatalf("Set valid value: %v", err)
	}
	if v, _ := s.Get("SET-PRESS"); v != 250 {
		t.Errorf("after Set, value = %v, want 250", v)
	}

	if err := s.Set("SET-PRESS", 999); err == nil {
		t.Error("Set above max should error")
	}
	if err := s.Set("SET-PRESS", -1); err == nil {
		t.Error("Set below min should error")
	}
	if err := s.Set("SYS-STATE", 1); err == nil {
		t.Error("Set on read-only channel should error")
	}
	if err := s.Set("MISSING", 1); err == nil {
		t.Error("Set on unknown channel should error")
	}

	// A rejected Set must not change the stored value.
	if v, _ := s.Get("SET-PRESS"); v != 250 {
		t.Errorf("rejected Set changed value to %v, want 250", v)
	}
}

// TestStorePersistence verifies values are written and re-loaded from disk.
func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	chanDir := filepath.Join(dir, "channels")
	if err := os.MkdirAll(chanDir, 0755); err != nil {
		t.Fatalf("mkdir channels: %v", err)
	}

	chanFile := filepath.Join(chanDir, "test.chan")
	defs := `channel SET-PRESS
    type float
    default 10
    min 0
    max 500
`
	if err := os.WriteFile(chanFile, []byte(defs), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "softChannelValues.yaml")
	s1, err := LoadFromDir(chanDir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if err := s1.Set("SET-PRESS", 321); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Persistence is off the hot path now: Set only marks the store dirty, and a
	// background flusher (or shutdown) writes the file.
	s1.Flush()

	// A fresh store must read the persisted value, not the default.
	s2, err := LoadFromDir(chanDir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir reopen: %v", err)
	}
	if v, _ := s2.Get("SET-PRESS"); v != 321 {
		t.Errorf("persisted value = %v, want 321", v)
	}
}

// TestStoreConfigJSON verifies the softchan_config payload is valid and complete.
func TestStoreConfigJSON(t *testing.T) {
	s := newTestStore(t)
	raw := s.ConfigJSON()
	if raw == nil {
		t.Fatal("ConfigJSON returned nil")
	}
	var msg struct {
		Type     string `json:"type"`
		Channels []struct {
			RefDes string `json:"refDes"`
			Role   string `json:"role"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("ConfigJSON not valid JSON: %v", err)
	}
	if msg.Type != "softchan_config" {
		t.Errorf("type = %q, want softchan_config", msg.Type)
	}
	if len(msg.Channels) != 2 {
		t.Fatalf("got %d channels, want 2", len(msg.Channels))
	}
}

// TestSoftchan_StateMachineChannelsAreRealDefs covers F-A3/B8: the auto-created
// SM-<NAME>-* channels must be full definitions, not bare value-map entries —
// SetInternal keys off defIndex, so without a def the engine's state publishes
// were silently dropped.
func TestSoftchan_StateMachineChannelsAreRealDefs(t *testing.T) {
	s := newTestStore(t)
	s.RegisterStateMachineChannels([]string{"fuelSeq"})

	stateCh := "SM-fuelSeq-STATE"
	targetCh := "SM-fuelSeq-TARGET"

	if _, ok := s.Get(stateCh); !ok {
		t.Fatalf("%s not registered", stateCh)
	}
	if m := s.RefDesMap(); m[stateCh] != "_SOFTCHAN" || m[targetCh] != "_SOFTCHAN" {
		t.Errorf("SM channels missing from RefDesMap: %v", m)
	}

	// SetInternal must actually update the value.
	s.SetInternal(stateCh, 3)
	if v, _ := s.Get(stateCh); v != 3 {
		t.Errorf("%s = %v after SetInternal, want 3", stateCh, v)
	}

	// STATE is read-only to operators; TARGET is writable.
	if err := s.Set(stateCh, 1); err == nil {
		t.Errorf("Set on %s should be rejected (read-only)", stateCh)
	}
	if err := s.Set(targetCh, 2); err != nil {
		t.Errorf("Set on %s: %v", targetCh, err)
	}

	// Both must appear in the config the browser receives.
	raw := s.ConfigJSON()
	var msg struct {
		Channels []struct {
			RefDes string `json:"refDes"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("ConfigJSON: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range msg.Channels {
		seen[c.RefDes] = true
	}
	if !seen[stateCh] || !seen[targetCh] {
		t.Errorf("SM channels missing from softchan_config: %v", seen)
	}
}

// TestSoftchan_SetInternalPublishes checks SetInternal reaches the broker so the
// HMI sees state changes without waiting for the keepalive.
func TestSoftchan_SetInternalPublishes(t *testing.T) {
	s := newTestStore(t)
	s.RegisterStateMachineChannels([]string{"fuelSeq"})

	b := broker.New(nil, nil, nil)
	go b.Run(50)
	sub, unsub := b.Subscribe()
	defer unsub()

	s.mu.Lock()
	s.b = b
	s.mu.Unlock()

	s.SetInternal("SM-fuelSeq-STATE", 2)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case raw := <-sub:
			var m struct {
				Type string             `json:"type"`
				D    map[string]float64 `json:"d"`
			}
			if json.Unmarshal(raw, &m) != nil || m.Type != "data" {
				continue
			}
			if v, ok := m.D["SM-fuelSeq-STATE"]; ok {
				if v != 2 {
					t.Fatalf("published SM-fuelSeq-STATE = %v, want 2", v)
				}
				return
			}
		case <-deadline:
			t.Fatal("SetInternal never published to the broker")
		}
	}
}

// TestSoftchan_SetDoesNotBlockReaders covers F-A12: persistence used to run
// inline under the store lock, putting a disk write in front of every reader.
// Set must now be a memory-only operation.
func TestSoftchan_SetDoesNotBlockReaders(t *testing.T) {
	s := newTestStore(t)

	stop := make(chan struct{})
	done := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				done <- n
				return
			default:
				s.Get("SET-PRESS")
				n++
			}
		}
	}()

	start := time.Now()
	const writes = 2000
	for i := 0; i < writes; i++ {
		if err := s.Set("SET-PRESS", float64(i%400)); err != nil {
			t.Fatalf("Set: %v", err)
		}
	}
	elapsed := time.Since(start)
	close(stop)
	reads := <-done

	if elapsed > 2*time.Second {
		t.Errorf("%d Sets took %v — persistence appears to still be on the hot path", writes, elapsed)
	}
	if reads == 0 {
		t.Errorf("readers were starved entirely during %d Sets", writes)
	}

	// The values file must not have been touched yet: only Flush writes it.
	if _, err := os.Stat(s.valuesPath); !os.IsNotExist(err) {
		t.Errorf("values file written on the Set hot path (stat err = %v)", err)
	}
	s.Flush()
	if _, err := os.Stat(s.valuesPath); err != nil {
		t.Errorf("Flush did not write the values file: %v", err)
	}
}

// TestSoftchan_ComputedChannelsAreNotPersisted checks computed values stay out
// of softChannelValues.yaml — they are derived every tick.
func TestSoftchan_ComputedChannelsAreNotPersisted(t *testing.T) {
	dir := t.TempDir()
	chanDir := filepath.Join(dir, "channels")
	if err := os.MkdirAll(chanDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	src := "" +
		"channel SET-PRESS\n    type float\n    default 10\n" +
		"channel DOUBLED\n    compute SET-PRESS * 2\n" +
		"channel HIGH\n    type bool\n    compute SET-PRESS > 5\n"
	if err := os.WriteFile(filepath.Join(chanDir, "t.chan"), []byte(src), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	valuesPath := filepath.Join(dir, "values.yaml")
	s, err := LoadFromDir(chanDir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if err := s.Recompute(nil); err != nil {
		t.Fatalf("Recompute: %v", err)
	}
	// A boolean compute channel must store 1/0, not Value.Float()'s 0.
	if v, _ := s.Get("HIGH"); v != 1 {
		t.Errorf("HIGH = %v, want 1 (true coerced to 1)", v)
	}
	if v, _ := s.Get("DOUBLED"); v != 20 {
		t.Errorf("DOUBLED = %v, want 20", v)
	}

	if err := s.Set("SET-PRESS", 3); err != nil {
		t.Fatalf("Set: %v", err)
	}
	s.Flush()
	data, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatalf("read values: %v", err)
	}
	if strings.Contains(string(data), "DOUBLED") || strings.Contains(string(data), "HIGH") {
		t.Errorf("computed channels were persisted:\n%s", data)
	}
	if !strings.Contains(string(data), "SET-PRESS") {
		t.Errorf("settable channel missing from persisted values:\n%s", data)
	}

	// Computed channels are rejected for writes.
	if err := s.Set("DOUBLED", 1); err == nil {
		t.Errorf("Set on a computed channel should be rejected")
	}
}

// TestSoftchan_ChanErrorsHaveFileLine checks .chan loader errors carry the same
// "<file>:<line>:" prefix that .sm compile errors do.
func TestSoftchan_ChanErrorsHaveFileLine(t *testing.T) {
	dir := t.TempDir()
	chanDir := filepath.Join(dir, "channels")
	if err := os.MkdirAll(chanDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(chanDir, "bad.chan"),
		[]byte("channel A\n    type float\n    default 5min\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadFromDir(chanDir, filepath.Join(dir, "v.yaml"))
	if err == nil {
		t.Fatal("expected a load error")
	}
	if !strings.Contains(err.Error(), "bad.chan:") {
		t.Errorf("error %q is missing a file:line prefix", err)
	}
}
