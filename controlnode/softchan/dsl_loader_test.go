package softchan

import (
	"controlnode/broker"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadFromDir loads a basic .chan file and verifies channels are loaded correctly
func TestLoadFromDir_BasicChannels(t *testing.T) {
	dir := t.TempDir()
	chanFile := filepath.Join(dir, "test.chan")

	content := `channel SET-PRESS
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

	if err := os.WriteFile(chanFile, []byte(content), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "values.yaml")
	store, err := LoadFromDir(dir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	// Check basic channels were loaded
	if v, ok := store.Get("SET-PRESS"); !ok || v != 100 {
		t.Errorf("SET-PRESS value = %v (ok=%v), want 100", v, ok)
	}
	if v, ok := store.Get("SYS-STATE"); !ok || v != 0 {
		t.Errorf("SYS-STATE value = %v (ok=%v), want 0", v, ok)
	}

	// Check refDesMap includes both channels
	rm := store.RefDesMap()
	if rm["SET-PRESS"] != "_SOFTCHAN" || rm["SYS-STATE"] != "_SOFTCHAN" {
		t.Errorf("RefDesMap missing channels: %v", rm)
	}
}

// TestLoadFromDir_ComputedChannels verifies computed channels are recognized
func TestLoadFromDir_ComputedChannels(t *testing.T) {
	dir := t.TempDir()
	chanFile := filepath.Join(dir, "test.chan")

	content := `channel PT-01
    type float
    description "Pressure 1"
    units psi
    default 0

channel PT-02
    type float
    description "Pressure 2"
    units psi
    default 0

channel PT-AVG
    description "Average pressure"
    units psi
    compute (PT-01 + PT-02) / 2
`

	if err := os.WriteFile(chanFile, []byte(content), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "values.yaml")
	store, err := LoadFromDir(dir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	// Check that computed channel exists but is read-only
	if _, ok := store.Get("PT-AVG"); !ok {
		t.Error("PT-AVG not found")
	}

	// Try to set computed channel (should fail)
	if err := store.Set("PT-AVG", 50); err == nil {
		t.Error("Set on computed channel should fail")
	}
}

// TestComputeChannelRecompute verifies computed channels are evaluated correctly
func TestComputeChannelRecompute(t *testing.T) {
	dir := t.TempDir()
	chanFile := filepath.Join(dir, "test.chan")

	content := `channel PT-01
    type float
    default 100

channel PT-02
    type float
    default 200

channel PT-AVG
    compute (PT-01 + PT-02) / 2
`

	if err := os.WriteFile(chanFile, []byte(content), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "values.yaml")
	store, err := LoadFromDir(dir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	// Initially PT-AVG should be 0
	if v, _ := store.Get("PT-AVG"); v != 0 {
		t.Errorf("initial PT-AVG = %v, want 0", v)
	}

	// Recompute with broker
	broker := broker.New(nil, nil, nil)
	if err := store.Recompute(broker); err != nil {
		t.Fatalf("Recompute: %v", err)
	}

	// After recompute, PT-AVG should be (100 + 200) / 2 = 150
	if v, _ := store.Get("PT-AVG"); v != 150 {
		t.Errorf("after recompute PT-AVG = %v, want 150", v)
	}

	// Change PT-01 and verify recompute updates PT-AVG
	if err := store.Set("PT-01", 50); err != nil {
		t.Fatalf("Set PT-01: %v", err)
	}

	if err := store.Recompute(broker); err != nil {
		t.Fatalf("Recompute after Set: %v", err)
	}

	// PT-AVG should now be (50 + 200) / 2 = 125
	if v, _ := store.Get("PT-AVG"); v != 125 {
		t.Errorf("after Set and recompute PT-AVG = %v, want 125", v)
	}
}

// TestComputeChannelCyclicDependency verifies cycle detection
func TestComputeChannelCyclicDependency(t *testing.T) {
	dir := t.TempDir()
	chanFile := filepath.Join(dir, "test.chan")

	// Create a cycle: A depends on B, B depends on A
	content := `channel CH-A
    compute CH-B + 1

channel CH-B
    compute CH-A + 2
`

	if err := os.WriteFile(chanFile, []byte(content), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "values.yaml")
	_, err := LoadFromDir(dir, valuesPath)
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("expected cycle detection error, got: %v", err)
	}
}

// TestComputeChannelDependencyOrder verifies topological sort
func TestComputeChannelDependencyOrder(t *testing.T) {
	dir := t.TempDir()
	chanFile := filepath.Join(dir, "test.chan")

	// Create dependencies: A depends on B, B depends on C, C has no deps
	content := `channel CH-C
    type float
    default 10

channel CH-B
    compute CH-C * 2

channel CH-A
    compute CH-B + 5
`

	if err := os.WriteFile(chanFile, []byte(content), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "values.yaml")
	store, err := LoadFromDir(dir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	// Verify computeOrder is correct (CH-C before CH-B before CH-A, or at least CH-B before CH-A)
	orderMap := make(map[string]int)
	for i, name := range store.computeOrder {
		orderMap[name] = i
	}

	if orderMap["CH-C"] > orderMap["CH-B"] {
		t.Errorf("CH-C should come before CH-B in topological order")
	}
	if orderMap["CH-B"] > orderMap["CH-A"] {
		t.Errorf("CH-B should come before CH-A in topological order")
	}

	// Recompute and verify results
	broker := broker.New(nil, nil, nil)
	if err := store.Recompute(broker); err != nil {
		t.Fatalf("Recompute: %v", err)
	}

	// CH-C = 10, CH-B = 10 * 2 = 20, CH-A = 20 + 5 = 25
	if v, _ := store.Get("CH-C"); v != 10 {
		t.Errorf("CH-C = %v, want 10", v)
	}
	if v, _ := store.Get("CH-B"); v != 20 {
		t.Errorf("CH-B = %v, want 20", v)
	}
	if v, _ := store.Get("CH-A"); v != 25 {
		t.Errorf("CH-A = %v, want 25", v)
	}
}

// TestConfigJSONIncludesComputedChannels verifies the JSON config includes computed channels
func TestConfigJSONIncludesComputedChannels(t *testing.T) {
	dir := t.TempDir()
	chanFile := filepath.Join(dir, "test.chan")

	content := `channel PT-01
    type float
    default 100

channel PT-AVG
    compute PT-01 + 0
`

	if err := os.WriteFile(chanFile, []byte(content), 0644); err != nil {
		t.Fatalf("write .chan file: %v", err)
	}

	valuesPath := filepath.Join(dir, "values.yaml")
	store, err := LoadFromDir(dir, valuesPath)
	if err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	jsonBytes := store.ConfigJSON()
	if jsonBytes == nil {
		t.Fatal("ConfigJSON returned nil")
	}

	// The JSON should be parseable and contain both channels
	var msg struct {
		Type     string `json:"type"`
		Channels []struct {
			RefDes   string `json:"refDes"`
			Computed bool   `json:"computed"`
		} `json:"channels"`
	}

	if err := json.Unmarshal(jsonBytes, &msg); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}

	if msg.Type != "softchan_config" {
		t.Errorf("type = %q, want softchan_config", msg.Type)
	}

	if len(msg.Channels) != 2 {
		t.Errorf("got %d channels, want 2", len(msg.Channels))
	}

	// Find the computed channel
	var foundComputed bool
	for _, ch := range msg.Channels {
		if ch.RefDes == "PT-AVG" && ch.Computed {
			foundComputed = true
			break
		}
	}
	if !foundComputed {
		t.Error("computed channel PT-AVG not found in config")
	}
}
