package config

import (
	"encoding/json"
	"testing"
)

// realConfigDir is the repository's config/ directory, relative to this package.
const realConfigDir = "../../config"

// TestParseDirRealConfig is the top-level smoke test: the real config directory
// must parse without error and produce a usable SystemConfig.  If this fails the
// control node cannot start.
func TestParseDirRealConfig(t *testing.T) {
	cfg, err := ParseDir(realConfigDir)
	if err != nil {
		t.Fatalf("ParseDir(%q): %v", realConfigDir, err)
	}

	if cfg.Network.WebSocketPort == 0 {
		t.Error("Network.WebSocketPort is 0 (default should have been applied)")
	}
	if cfg.Network.BroadcastRateHz == 0 {
		t.Error("Network.BroadcastRateHz is 0 (default should have been applied)")
	}
	if len(cfg.ControlList.Controls) == 0 {
		t.Error("no controls loaded from controls.yaml")
	}
	if len(cfg.DaqNodes.Nodes) == 0 {
		t.Error("no DAQ nodes loaded from daqNodes/")
	}
}

// TestParseDirMissing ensures ParseDir returns an error (not a panic) for a
// directory with no config files.
func TestParseDirMissing(t *testing.T) {
	if _, err := ParseDir(t.TempDir()); err == nil {
		t.Fatal("expected error for empty config dir, got nil")
	}
}

// TestBuildWebClientConfigJSON verifies the browser config payload is valid JSON
// with the expected top-level shape.
func TestBuildWebClientConfigJSON(t *testing.T) {
	cfg, err := ParseDir(realConfigDir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	s, err := BuildWebClientConfigJSON(cfg)
	if err != nil {
		t.Fatalf("BuildWebClientConfigJSON: %v", err)
	}

	var msg struct {
		Type            string            `json:"type"`
		BroadcastRateHz int               `json:"broadcastRateHz"`
		Controls        []json.RawMessage `json:"controls"`
	}
	if err := json.Unmarshal([]byte(s), &msg); err != nil {
		t.Fatalf("config JSON is not valid: %v", err)
	}
	if msg.Type != "config" {
		t.Errorf("type = %q, want %q", msg.Type, "config")
	}
	if len(msg.Controls) == 0 {
		t.Error("config JSON contains no controls")
	}
}

// TestBuildDaqNodeConfigJSON verifies each enabled DAQ node gets a valid config
// payload, and that an unknown node is a clean error.
func TestBuildDaqNodeConfigJSON(t *testing.T) {
	cfg, err := ParseDir(realConfigDir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	for _, node := range cfg.DaqNodes.Nodes {
		s, err := BuildDaqNodeConfigJSON(cfg, node.RefDes, cfg.Network.BroadcastRateHz)
		if err != nil {
			t.Errorf("BuildDaqNodeConfigJSON(%q): %v", node.RefDes, err)
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Errorf("DAQ node %q config is not valid JSON: %v", node.RefDes, err)
		}
		if m["type"] != "config" {
			t.Errorf("DAQ node %q config type = %v, want %q", node.RefDes, m["type"], "config")
		}
	}

	if _, err := BuildDaqNodeConfigJSON(cfg, "NO-SUCH-NODE", 20); err == nil {
		t.Error("expected error for unknown DAQ node, got nil")
	}
}

// TestBuildRefDesMap checks every mapped channel points at a non-empty DAQ node.
func TestBuildRefDesMap(t *testing.T) {
	cfg, err := ParseDir(realConfigDir)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	m := BuildRefDesMap(cfg)
	if len(m) == 0 {
		t.Fatal("BuildRefDesMap returned empty map")
	}
	for refDes, daq := range m {
		if refDes == "" || daq == "" {
			t.Errorf("refDesMap has empty entry: %q -> %q", refDes, daq)
		}
	}
}

// TestParseOptFloat covers the optional-float parsing used throughout the config
// builders.
func TestParseOptFloat(t *testing.T) {
	cases := []struct {
		in   string
		want *float64
	}{
		{"", nil},
		{"   ", nil},
		{"not-a-number", nil},
		{"3.5", ptr(3.5)},
		{" -2 ", ptr(-2)},
	}
	for _, c := range cases {
		got := parseOptFloat(c.in)
		switch {
		case c.want == nil && got != nil:
			t.Errorf("parseOptFloat(%q) = %v, want nil", c.in, *got)
		case c.want != nil && got == nil:
			t.Errorf("parseOptFloat(%q) = nil, want %v", c.in, *c.want)
		case c.want != nil && got != nil && *got != *c.want:
			t.Errorf("parseOptFloat(%q) = %v, want %v", c.in, *got, *c.want)
		}
	}
}

func ptr(f float64) *float64 { return &f }
