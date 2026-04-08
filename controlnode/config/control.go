package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DaqControl holds the state machine definition for a single DAQ node,
// parsed from config/control/<daqNode>_control.yaml.
type DaqControl struct {
	DaqNode string              `yaml:"daqNode" json:"daqNode"`
	States  map[string]DaqState `yaml:"states"  json:"states"`
}

// DaqState describes one state in a DAQ node's state machine.
type DaqState struct {
	OperatorControl bool              `yaml:"operator_control" json:"operatorControl"`
	Transitions     []DaqTransition   `yaml:"transitions"      json:"transitions"`
	EntrySequence   []json.RawMessage `yaml:"entry_sequence"   json:"-"` // not sent to browser
	ExitSequence    []json.RawMessage `yaml:"exit_sequence"    json:"-"`
}

// DaqTransition describes one possible transition from a state.
type DaqTransition struct {
	Target string `yaml:"target" json:"target"`
	On     string `yaml:"on"     json:"on"`
}

// parseDaqControls loads all *_control.yaml files from configDir/control/.
func parseDaqControls(configDir string) ([]DaqControl, error) {
	controlDir := filepath.Join(configDir, "control")
	entries, err := os.ReadDir(controlDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no control directory is fine
		}
		return nil, fmt.Errorf("read control dir: %w", err)
	}

	var controls []DaqControl
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(controlDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		var dc DaqControl
		if err := yaml.Unmarshal(data, &dc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if dc.DaqNode == "" {
			continue
		}
		controls = append(controls, dc)
	}
	return controls, nil
}

// BuildStateConfigJSON returns the JSON payload for the "state_config" message
// sent to browsers on connect.  Returns nil if there are no DAQ controls.
func BuildStateConfigJSON(controls []DaqControl) []byte {
	if len(controls) == 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"type":     "state_config",
		"daqNodes": controls,
	})
	return payload
}
