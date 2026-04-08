package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// SequenceStep is one timed action in an entry or exit sequence.
// T_ms may be an integer or a string variable reference like "{{BURN_DUR}}".
// Value may be a bool (0/1) or a float.
type SequenceStep struct {
	T_ms   interface{} `yaml:"t_ms"`
	RefDes string      `yaml:"refDes"`
	Value  interface{} `yaml:"value"`
	Label  string      `yaml:"label,omitempty"`
}

// AbortRule defines a sensor condition that triggers an abort.
// If is a string like "CPT-01 > {{CPT_HIGH}}".
// T_ms_on / T_ms_off bound the window (relative to sequence start) during which
// the rule is active; either may be an int or a "{{VAR}}" reference.
type AbortRule struct {
	If      string      `yaml:"if"`
	T_ms_on  interface{} `yaml:"t_ms_on"`
	T_ms_off interface{} `yaml:"t_ms_off"`
}

// DaqTransition describes one possible transition out of a state.
// ExitType controls whether the control node tells the DAQ to run its cached
// exit sequence ("exit") or skip it ("hard_exit").  Defaults to "hard_exit".
type DaqTransition struct {
	Target   string `yaml:"target"    json:"target"`
	On       string `yaml:"on"        json:"on"`
	ExitType string `yaml:"exit_type" json:"exitType"`
}

// DaqState describes one state in a DAQ node's state machine.
type DaqState struct {
	OperatorControl bool           `yaml:"operator_control" json:"operatorControl"`
	Transitions     []DaqTransition `yaml:"transitions"      json:"transitions"`
	EntrySequence   []SequenceStep  `yaml:"entry_sequence"   json:"-"`
	ExitSequence    []SequenceStep  `yaml:"exit_sequence"    json:"-"`
	AbortRules      []AbortRule     `yaml:"abort_rules"      json:"-"`
}

// DaqControl holds the state machine definition for a single DAQ node,
// parsed from config/control/<daqNode>_control.yaml.
type DaqControl struct {
	DaqNode   string              `yaml:"daqNode"   json:"daqNode"`
	Variables map[string]string   `yaml:"variables" json:"-"` // VAR_NAME → softchan refDes
	States    map[string]DaqState `yaml:"states"    json:"states"`
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
