package main

import "encoding/json"

// ── Outbound messages (DAQ node → control node) ───────────────────────────────

func msgConfigReq(refDes string) []byte {
	b, _ := json.Marshal(map[string]string{"type": "config_req", "refDes": refDes})
	return b
}

func msgData(t float64, d map[string]float64) []byte {
	b, _ := json.Marshal(map[string]interface{}{"type": "data", "t": t, "d": d})
	return b
}

func msgErr(t float64, err string) []byte {
	b, _ := json.Marshal(map[string]interface{}{"type": "err", "t": t, "err": err})
	return b
}

func msgStateReq() []byte {
	b, _ := json.Marshal(map[string]string{"type": "state_req"})
	return b
}

func msgSequenceComplete() []byte {
	b, _ := json.Marshal(map[string]string{"type": "sequence_complete"})
	return b
}

func msgAbortTriggered() []byte {
	b, _ := json.Marshal(map[string]string{"type": "abort_triggered"})
	return b
}

// ── Inbound config message (control node → DAQ node after config_req) ────────

// ConfigMsg is the config payload sent by the control node after handshake.
// It matches the JSON built by controlnode/config/yaml.go BuildDaqNodeConfigJSON.
type ConfigMsg struct {
	Type             string          `json:"type"`
	SampleRateHz     int             `json:"sampleRateHz"`
	ManagementRateHz int             `json:"managementRateHz"`
	Modules          []ModuleDef     `json:"modules"`
	Channels         []ChannelDef    `json:"channels"`
}

type ModuleDef struct {
	ModuleModelNumber string `json:"moduleModelNumber"`
	SampleRateHz      int    `json:"sampleRateHz"`
}

type ChannelDef struct {
	RefDes                     string   `json:"refDes"`
	ModuleModelNumber          string   `json:"moduleModelNumber"`
	ChannelNumber              string   `json:"channelNumber"`
	TaskName                   string   `json:"taskName"`
	Type                       string   `json:"type,omitempty"`           // thermocouple type: K, E, T
	Sensitivity                *float64 `json:"sensitivity,omitempty"`
	Balance                    *float64 `json:"balance,omitempty"`
	InputTerminalConfiguration string   `json:"inputTerminalConfiguration,omitempty"`
	BridgeConfiguration        string   `json:"bridgeConfiguration,omitempty"`
	VoltageExcitationSource    string   `json:"voltageExcitationSource,omitempty"`
	ExcitationVoltage          *float64 `json:"excitationVoltage,omitempty"`
	NominalBridgeResistance    *float64 `json:"nominalBridgeResistance,omitempty"`
	FirstElectricalValue       *float64 `json:"firstElectricalValue,omitempty"`
	SecondElectricalValue      *float64 `json:"secondElectricalValue,omitempty"`
	FirstPhysicalValue         *float64 `json:"firstPhysicalValue,omitempty"`
	SecondPhysicalValue        *float64 `json:"secondPhysicalValue,omitempty"`
	ElectricalUnits            string   `json:"electricalUnits,omitempty"`
	Units                      string   `json:"units,omitempty"`
}

// channelRole returns the logical role of a channel based on its module type and task name.
// Output channels are "Digital-IO" with "output" in task name, or "Analog-Output".
func (ch *ChannelDef) isOutput() bool {
	switch ch.ModuleModelNumber {
	case "Analog-Output":
		return true
	case "Digital-IO":
		// Convention: task name contains "output" or "DO" for output channels
		return ch.TaskName == "Digital-IO" || ch.ChannelNumber != ""
	}
	return false
}

// ── Inbound runtime messages ──────────────────────────────────────────────────

// CmdMsg is a command from the control node to set a channel value.
type CmdMsg struct {
	RefDes string      `json:"refDes"`
	Value  interface{} `json:"value"` // float64 or bool after JSON decode
}

// StateUpdateMsg is a state definition sent by the control node.
// Variables are already resolved to concrete float64 values.
type StateUpdateMsg struct {
	State         string         `json:"state"`
	EntrySequence []SequenceStep `json:"entry_sequence"`
	ExitSequence  []SequenceStep `json:"exit_sequence"`
	AbortRules    []AbortRule    `json:"abort_rules"`
}

// SequenceStep is one timed action in an entry or exit sequence.
// t_ms is the time offset in milliseconds from sequence start.
type SequenceStep struct {
	T_ms   float64 `json:"t_ms"`
	RefDes string  `json:"refDes"`
	Value  float64 `json:"value"`
	Label  string  `json:"label,omitempty"`
}

// AbortRule is a sensor condition that triggers an abort.
// The comparison (op) is applied against Value using the sensor's current reading.
type AbortRule struct {
	RefDes   string  `json:"refDes"`
	Op       string  `json:"op"`
	Value    float64 `json:"value"`
	T_ms_on  float64 `json:"t_ms_on"`
	T_ms_off float64 `json:"t_ms_off"`
}

// ExitMsg is sent by the control node to trigger a state transition.
// Type is "exit" (run exit sequence first) or "hard_exit" (skip it).
type ExitMsg struct {
	Type   string `json:"type"`
	Target string `json:"target"`
}

// parseInbound parses the type field from a raw JSON message.
func parseType(raw []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	json.Unmarshal(raw, &envelope)
	return envelope.Type
}
