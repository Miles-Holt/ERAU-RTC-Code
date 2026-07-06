package softchan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// newTestStore writes a minimal softChannels.yaml (and no values file) in a temp
// dir and returns a loaded Store.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	defsPath := filepath.Join(dir, "softChannels.yaml")
	valuesPath := filepath.Join(dir, "softChannelValues.yaml")

	defs := `channels:
  - refDes: SET-PRESS
    description: Target pressure
    units: psi
    role: cmd-float
    default: 100
    min: 0
    max: 500
  - refDes: SYS-STATE
    description: System state
    units: ""
    role: ""
    default: 0
`
	if err := os.WriteFile(defsPath, []byte(defs), 0644); err != nil {
		t.Fatalf("write defs: %v", err)
	}

	s, err := New(defsPath, valuesPath)
	if err != nil {
		t.Fatalf("New: %v", err)
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
	defsPath := filepath.Join(dir, "softChannels.yaml")
	valuesPath := filepath.Join(dir, "softChannelValues.yaml")

	defs := `channels:
  - refDes: SET-PRESS
    role: cmd-float
    default: 10
    min: 0
    max: 500
`
	if err := os.WriteFile(defsPath, []byte(defs), 0644); err != nil {
		t.Fatalf("write defs: %v", err)
	}

	s1, err := New(defsPath, valuesPath)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s1.Set("SET-PRESS", 321); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A fresh store must read the persisted value, not the default.
	s2, err := New(defsPath, valuesPath)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
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
