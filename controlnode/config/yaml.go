// Package config parses split YAML config files and builds JSON payloads for
// web clients and DAQ nodes.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ── Go struct types (shared by YAML parser and JSON builders) ─────────────────

// SystemConfig is the root configuration object assembled from all YAML files
// in the config directory.  It is consumed by the JSON builder functions and
// main.go to configure the broker, DAQ node clients, and web server.
type SystemConfig struct {
	ControlList ControlList
	Network     Network
	CtrNode     CtrNodeDef
	DaqNodes    DaqNodes
}

// Network holds system-wide timing and port settings from system.yaml.
type Network struct {
	WebSocketPort    int // port the web client WebSocket server listens on
	BroadcastRateHz  int // rate at which the broker ticks data to browsers
	ManagementRateHz int // rate at which DAQ node clients send keepalives
	ChannelStaleMs   int // milliseconds before a channel value is considered stale
}

// CtrNodeDef describes the control node itself, including the virtual health
// channels it exposes to browsers (from controlNode.yaml).
type CtrNodeDef struct {
	RefDes      string
	IP          string
	Description string
	Enabled     bool
	WSPort      int
	Health      CtrHealth
}

// CtrHealth groups the read-only sensor metrics and commandable actions that
// the control node publishes as virtual channels in the data stream.
type CtrHealth struct {
	Sensors  []CtrSensor  // read-only metrics (uptime, loop time, connection counts)
	Commands []CtrCommand // commandable actions (e.g. CTR001-restart)
}

// CtrSensor is a single read-only health metric published by the control node.
type CtrSensor struct {
	RefDes      string
	Description string
	Units       string
}

// CtrCommand is a commandable action on the control node (role: "cmd-bool").
// Any command whose refDes contains "restart" triggers os.Exit(1) when fired.
type CtrCommand struct {
	RefDes      string
	Description string
	Role        string // "cmd-bool"
}

// ControlList holds all controls loaded from controls.yaml.
type ControlList struct {
	Controls []Control
}

// Control represents one physical instrument or actuator group.
//
// Type values: "thrust", "ignition", "temperature", "pressure", "flowMeter",
// "valve", "bangBang", "digitalOut", "VFD".
//
// SubType values (valve): "IO-CMD_IO-FB", "IO-CMD", "IO-CMD_POS-FB", "POS-CMD_POS-FB".
// SubType values (bangBang): "press", "press2" (press + vent valves), "pressVent".
type Control struct {
	RefDes      string
	Description string
	Enabled     bool
	Type        string
	SubType     string
	Details     Details
	Channels    []Channel
}

// Details holds type-specific configuration; only the fields relevant to the
// control's Type are populated — all others are zero values.
type Details struct {
	// pressure: whether the sensor reads absolute pressure (true) or gauge (false).
	// When false and AbsoluteSensorRefDes is empty, a 0 offset is applied on the front panel.
	Absolute             bool
	AbsoluteSensorRefDes string // refDes of the absolute PT used as the reference offset
	// bangBang: refDes of the pressure transducer the controller regulates against
	SenseRefDes string
}

// Channel is one physical I/O line within a Control.
//
// Role values: "cmd-bool" (toggle), "cmd-pct" (0–100% setpoint),
// "cmd-float" (arbitrary float), "" (read-only sensor/feedback).
//
// ValidMin/ValidMax are optional engineering-unit bounds used by the browser for
// bad-data detection (red LED + red value text).  Empty string disables the check.
type Channel struct {
	RefDes            string
	Role              string
	RefDesDaq         string // refDes of the DAQ node that owns this channel
	ModuleModelNumber string // e.g. "Analog-Input", "Digital-IO", "Thermocouple"
	ChannelNumber     string // DAQmx channel string, e.g. "/port3/line0" or "ai05"
	DaqMx             DaqMx
	ValidMin          string // optional; float string or empty
	ValidMax          string // optional; float string or empty
}

// DaqMx holds all possible NI-DAQmx configuration fields.  Only the fields
// relevant to the channel's module type are populated; the rest stay empty
// and are omitted from the JSON sent to LabVIEW.
//
// Fields by module type:
//
//	Thermocouple:     TaskName, TCType (K/E/T), Units
//	Analog-Input:     TaskName, Sensitivity, Balance, InputTerminalConfiguration, Units
//	Analog-Output:    TaskName
//	Digital-IO (out): TaskName
//	Digital-IO (in):  TaskName
//	Bridge-Completion: TaskName, BridgeConfiguration, VoltageExcitationSource,
//	                   ExcitationVoltage, NominalBridgeResistance,
//	                   FirstElectricalValue, SecondElectricalValue,
//	                   FirstPhysicalValue, SecondPhysicalValue, ElectricalUnits, Units
type DaqMx struct {
	TaskName                   string `json:"taskName,omitempty"`
	TCType                     string `json:"type,omitempty"`                       // thermocouple type: K, E, or T
	Units                      string `json:"units,omitempty"`                       // engineering units, e.g. "psi", "Deg F", "Pounds"
	Sensitivity                string `json:"sensitivity,omitempty"`                 // analog input: V/EU scaling factor
	Balance                    string `json:"balance,omitempty"`                     // analog input: zero-offset correction
	InputTerminalConfiguration string `json:"inputTerminalConfiguration,omitempty"` // "Differential", "NRSE", "RSE"
	BridgeConfiguration        string `json:"bridgeConfiguration,omitempty"`         // "Full Bridge", "Half Bridge", "Quarter Bridge"
	VoltageExcitationSource    string `json:"voltageExcitationSource,omitempty"`     // "Internal" or "External"
	ExcitationVoltage          string `json:"excitationVoltage,omitempty"`           // volts
	NominalBridgeResistance    string `json:"nominalBridgeResistance,omitempty"`     // ohms
	FirstElectricalValue       string `json:"firstElectricalValue,omitempty"`        // mV/V at first calibration point
	SecondElectricalValue      string `json:"secondElectricalValue,omitempty"`       // mV/V at second calibration point
	FirstPhysicalValue         string `json:"firstPhysicalValue,omitempty"`          // EU at first calibration point
	SecondPhysicalValue        string `json:"secondPhysicalValue,omitempty"`         // EU at second calibration point
	ElectricalUnits            string `json:"electricalUnits,omitempty"`             // e.g. "mVolts/Volt"
}

// DaqNodes holds the list of DAQ node definitions loaded from daqNodes/*.yaml.
type DaqNodes struct {
	Nodes []DaqNodeDef
}

// DaqNodeDef describes one DAQ node (NI PXIe chassis or test node).
// One YAML file per node lives in config/daqNodes/.
type DaqNodeDef struct {
	RefDes      string
	IP          string // hostname or IP address
	Description string
	Enabled     bool
	WSPort      int     // WebSocket port the DAQ node listens on
	Modules     []Module
}

// Module describes one NI module slot in a DAQ node chassis.
type Module struct {
	SlotID            int    // physical slot number in the chassis (0 if unassigned)
	ModuleModelNumber string // e.g. "Thermocouple", "Analog-Input", "Digital-IO"
	Description       string
	IOMode            string // "input" or "output"
	Enabled           bool
	SampleRateHz      int
}

// ── YAML file shapes ──────────────────────────────────────────────────────────
// These types mirror the YAML files exactly and get mapped to the structs above.

type yamlSystem struct {
	WebSocketPort            int `yaml:"webSocketPort"`
	BroadcastRateHz          int `yaml:"broadcastRateHz"`
	ConnectionManagementRateHz int `yaml:"connectionManagementRateHz"`
	ChannelStaleMs           int `yaml:"channelStaleMs"`
}

type yamlControlNode struct {
	RefDes      string `yaml:"refDes"`
	IP          string `yaml:"ip"`
	Description string `yaml:"description"`
	Enabled     bool   `yaml:"enabled"`
	Health      struct {
		Sensors []struct {
			RefDes      string `yaml:"refDes"`
			Description string `yaml:"description"`
			Units       string `yaml:"units"`
		} `yaml:"sensors"`
		Commands []struct {
			RefDes      string `yaml:"refDes"`
			Description string `yaml:"description"`
			Role        string `yaml:"role"`
		} `yaml:"commands"`
	} `yaml:"health"`
}

type yamlControlsFile struct {
	Controls []yamlControl `yaml:"controls"`
}

type yamlControl struct {
	RefDes      string        `yaml:"refDes"`
	Description string        `yaml:"description"`
	Enabled     bool          `yaml:"enabled"`
	Type        string        `yaml:"type"`
	SubType     string        `yaml:"subType"`
	Details     yamlDetails   `yaml:"details"`
	Channels    []yamlChannel `yaml:"channels"`
}

type yamlDetails struct {
	Absolute             bool   `yaml:"absolute"`
	AbsoluteSensorRefDes string `yaml:"absoluteSensorRefDes"`
	SenseRefDes          string `yaml:"senseRefDes"`
}

type yamlChannel struct {
	RefDes   string   `yaml:"refDes"`
	Role     string   `yaml:"role"`
	DaqNode  string   `yaml:"daqNode"`
	Module   string   `yaml:"module"`
	Channel  string   `yaml:"channel"`
	ValidMin string   `yaml:"validMin"`
	ValidMax string   `yaml:"validMax"`
	DaqMx    yamlDaqMx `yaml:"daqMx"`
}

type yamlDaqMx struct {
	TaskName                   string `yaml:"taskName"`
	Type                       string `yaml:"type"`
	Units                      string `yaml:"units"`
	Sensitivity                string `yaml:"sensitivity"`
	Balance                    string `yaml:"balance"`
	InputTerminalConfiguration string `yaml:"inputTerminalConfiguration"`
	BridgeConfiguration        string `yaml:"bridgeConfiguration"`
	VoltageExcitationSource    string `yaml:"voltageExcitationSource"`
	ExcitationVoltage          string `yaml:"excitationVoltage"`
	NominalBridgeResistance    string `yaml:"nominalBridgeResistance"`
	FirstElectricalValue       string `yaml:"firstElectricalValue"`
	SecondElectricalValue      string `yaml:"secondElectricalValue"`
	FirstPhysicalValue         string `yaml:"firstPhysicalValue"`
	SecondPhysicalValue        string `yaml:"secondPhysicalValue"`
	ElectricalUnits            string `yaml:"electricalUnits"`
}

type yamlDaqNode struct {
	RefDes      string       `yaml:"refDes"`
	IP          string       `yaml:"ip"`
	WSPort      int          `yaml:"wsPort"`
	Description string       `yaml:"description"`
	Enabled     bool         `yaml:"enabled"`
	Modules     []yamlModule `yaml:"modules"`
}

type yamlModule struct {
	SlotID            int    `yaml:"slotId"`
	ModuleModelNumber string `yaml:"moduleModelNumber"`
	Description       string `yaml:"description"`
	IOMode            string `yaml:"ioMode"`
	Enabled           bool   `yaml:"enabled"`
	SampleRateHz      int    `yaml:"sampleRateHz"`
}

// ── ParseDir ──────────────────────────────────────────────────────────────────

// ParseDir reads all YAML config files from configDir and returns a unified
// SystemConfig.  Directory layout expected:
//
//	configDir/
//	  system.yaml
//	  controlNode.yaml
//	  controls.yaml
//	  daqNodes/  (one .yaml per DAQ node)
func ParseDir(configDir string) (*SystemConfig, error) {
	cfg := &SystemConfig{}

	// system.yaml
	var sys yamlSystem
	if err := readYAML(filepath.Join(configDir, "system.yaml"), &sys); err != nil {
		return nil, fmt.Errorf("system.yaml: %w", err)
	}
	cfg.Network = Network{
		WebSocketPort:    sys.WebSocketPort,
		BroadcastRateHz:  sys.BroadcastRateHz,
		ManagementRateHz: sys.ConnectionManagementRateHz,
		ChannelStaleMs:   sys.ChannelStaleMs,
	}
	if cfg.Network.BroadcastRateHz == 0 {
		cfg.Network.BroadcastRateHz = 20
	}
	if cfg.Network.WebSocketPort == 0 {
		cfg.Network.WebSocketPort = 8000
	}

	// controlNode.yaml
	var cn yamlControlNode
	if err := readYAML(filepath.Join(configDir, "controlNode.yaml"), &cn); err != nil {
		return nil, fmt.Errorf("controlNode.yaml: %w", err)
	}
	cfg.CtrNode = CtrNodeDef{
		RefDes:      cn.RefDes,
		IP:          cn.IP,
		Description: cn.Description,
		Enabled:     cn.Enabled,
	}
	for _, s := range cn.Health.Sensors {
		cfg.CtrNode.Health.Sensors = append(cfg.CtrNode.Health.Sensors, CtrSensor{
			RefDes:      s.RefDes,
			Description: s.Description,
			Units:       s.Units,
		})
	}
	for _, c := range cn.Health.Commands {
		cfg.CtrNode.Health.Commands = append(cfg.CtrNode.Health.Commands, CtrCommand{
			RefDes:      c.RefDes,
			Description: c.Description,
			Role:        c.Role,
		})
	}

	// controls.yaml
	var ctrlFile yamlControlsFile
	if err := readYAML(filepath.Join(configDir, "controls.yaml"), &ctrlFile); err != nil {
		return nil, fmt.Errorf("controls.yaml: %w", err)
	}
	for _, yc := range ctrlFile.Controls {
		ctrl := Control{
			RefDes:      yc.RefDes,
			Description: yc.Description,
			Enabled:     yc.Enabled,
			Type:        yc.Type,
			SubType:     yc.SubType,
			Details: Details{
				Absolute:             yc.Details.Absolute,
				AbsoluteSensorRefDes: yc.Details.AbsoluteSensorRefDes,
				SenseRefDes:          yc.Details.SenseRefDes,
			},
		}
		for _, ych := range yc.Channels {
			ctrl.Channels = append(ctrl.Channels, Channel{
				RefDes:            ych.RefDes,
				Role:              ych.Role,
				RefDesDaq:         ych.DaqNode,
				ModuleModelNumber: ych.Module,
				ChannelNumber:     ych.Channel,
				ValidMin:          ych.ValidMin,
				ValidMax:          ych.ValidMax,
				DaqMx: DaqMx{
					TaskName:                   ych.DaqMx.TaskName,
					TCType:                     ych.DaqMx.Type,
					Units:                      ych.DaqMx.Units,
					Sensitivity:                ych.DaqMx.Sensitivity,
					Balance:                    ych.DaqMx.Balance,
					InputTerminalConfiguration: ych.DaqMx.InputTerminalConfiguration,
					BridgeConfiguration:        ych.DaqMx.BridgeConfiguration,
					VoltageExcitationSource:    ych.DaqMx.VoltageExcitationSource,
					ExcitationVoltage:          ych.DaqMx.ExcitationVoltage,
					NominalBridgeResistance:    ych.DaqMx.NominalBridgeResistance,
					FirstElectricalValue:       ych.DaqMx.FirstElectricalValue,
					SecondElectricalValue:      ych.DaqMx.SecondElectricalValue,
					FirstPhysicalValue:         ych.DaqMx.FirstPhysicalValue,
					SecondPhysicalValue:        ych.DaqMx.SecondPhysicalValue,
					ElectricalUnits:            ych.DaqMx.ElectricalUnits,
				},
			})
		}
		cfg.ControlList.Controls = append(cfg.ControlList.Controls, ctrl)
	}

	// daqNodes/*.yaml
	daqDir := filepath.Join(configDir, "daqNodes")
	entries, err := os.ReadDir(daqDir)
	if err != nil {
		return nil, fmt.Errorf("daqNodes dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		var yn yamlDaqNode
		if err := readYAML(filepath.Join(daqDir, e.Name()), &yn); err != nil {
			return nil, fmt.Errorf("daqNodes/%s: %w", e.Name(), err)
		}
		node := DaqNodeDef{
			RefDes:      yn.RefDes,
			IP:          yn.IP,
			WSPort:      yn.WSPort,
			Description: yn.Description,
			Enabled:     yn.Enabled,
		}
		for _, ym := range yn.Modules {
			node.Modules = append(node.Modules, Module{
				SlotID:            ym.SlotID,
				ModuleModelNumber: ym.ModuleModelNumber,
				Description:       ym.Description,
				IOMode:            ym.IOMode,
				Enabled:           ym.Enabled,
				SampleRateHz:      ym.SampleRateHz,
			})
		}
		cfg.DaqNodes.Nodes = append(cfg.DaqNodes.Nodes, node)
	}

	return cfg, nil
}

// readYAML reads the file at path and unmarshals it into v.
func readYAML(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

// ── JSON builders ─────────────────────────────────────────────────────────────

// webclientControl is the JSON shape the browser expects in the config message.
type webclientControl struct {
	RefDes      string                 `json:"refDes"`
	Description string                 `json:"description"`
	Type        string                 `json:"type"`
	SubType     string                 `json:"subType"`
	Details     map[string]interface{} `json:"details"`
	Channels    []webclientChannel     `json:"channels"`
}

type webclientChannel struct {
	RefDes   string   `json:"refDes"`
	Role     string   `json:"role"`
	Units    string   `json:"units"`
	ValidMin *float64 `json:"validMin"` // null if not configured
	ValidMax *float64 `json:"validMax"` // null if not configured
}

// parseOptFloat parses an optional float string.
// Returns nil if the string is empty or cannot be parsed.
func parseOptFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &f
}

// BuildWebClientConfigJSON returns the JSON string sent to browsers on connect.
// It includes enabled controls from controls.yaml plus CTR health sensors/commands.
func BuildWebClientConfigJSON(cfg *SystemConfig) (string, error) {
	var controls []webclientControl

	for _, ctrl := range cfg.ControlList.Controls {
		if !ctrl.Enabled {
			continue
		}
		var channels []webclientChannel
		for _, ch := range ctrl.Channels {
			channels = append(channels, webclientChannel{
				RefDes:   ch.RefDes,
				Role:     ch.Role,
				Units:    ch.DaqMx.Units,
				ValidMin: parseOptFloat(ch.ValidMin),
				ValidMax: parseOptFloat(ch.ValidMax),
			})
		}
		controls = append(controls, webclientControl{
			RefDes:      ctrl.RefDes,
			Description: ctrl.Description,
			Type:        ctrl.Type,
			SubType:     ctrl.SubType,
			Details:     buildDetails(ctrl),
			Channels:    channels,
		})
	}

	// CTR health sensors and commands as a single ctrNode control
	if len(cfg.CtrNode.Health.Sensors) > 0 || len(cfg.CtrNode.Health.Commands) > 0 {
		var channels []webclientChannel
		for _, s := range cfg.CtrNode.Health.Sensors {
			channels = append(channels, webclientChannel{
				RefDes: s.RefDes,
				Role:   "",
				Units:  s.Units,
			})
		}
		for _, c := range cfg.CtrNode.Health.Commands {
			channels = append(channels, webclientChannel{
				RefDes: c.RefDes,
				Role:   c.Role,
				Units:  "",
			})
		}
		controls = append(controls, webclientControl{
			RefDes:      cfg.CtrNode.RefDes,
			Description: "CTR Node Health",
			Type:        "ctrNode",
			SubType:     "",
			Details:     map[string]interface{}{},
			Channels:    channels,
		})
	}

	msg := map[string]interface{}{
		"type":            "config",
		"broadcastRateHz": cfg.Network.BroadcastRateHz,
		"channelStaleMs":  cfg.Network.ChannelStaleMs,
		"controls":        controls,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal web client config: %w", err)
	}
	return string(b), nil
}

// daqNodeConfigMsg is the JSON sent to a DAQ node after it requests config.
type daqNodeConfigMsg struct {
	Type             string           `json:"type"`
	SampleRateHz     int              `json:"sampleRateHz"`
	ManagementRateHz int              `json:"managementRateHz"`
	Modules          []daqNodeModule  `json:"modules"`
	Channels         []daqNodeChannel `json:"channels"`
}

type daqNodeModule struct {
	ModuleModelNumber string `json:"moduleModelNumber"`
	SampleRateHz      int    `json:"sampleRateHz"`
}

type daqNodeChannel struct {
	RefDes                     string   `json:"refDes"`
	ModuleModelNumber          string   `json:"moduleModelNumber,omitempty"`
	ChannelNumber              string   `json:"channelNumber,omitempty"`
	TaskName                   string   `json:"taskName,omitempty"`
	Type                       string   `json:"type,omitempty"`
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

// BuildDaqNodeConfigJSON returns the JSON config string for a specific DAQ node,
// containing all channels from controls.yaml that reference that DAQ node.
func BuildDaqNodeConfigJSON(cfg *SystemConfig, daqRefDes string, sampleRateHz int) (string, error) {
	var node *DaqNodeDef
	for i := range cfg.DaqNodes.Nodes {
		if cfg.DaqNodes.Nodes[i].RefDes == daqRefDes {
			node = &cfg.DaqNodes.Nodes[i]
			break
		}
	}
	if node == nil {
		return "", fmt.Errorf("DAQ node %q not found in config", daqRefDes)
	}

	modules := []daqNodeModule{}
	for _, m := range node.Modules {
		if !m.Enabled {
			continue
		}
		modules = append(modules, daqNodeModule{
			ModuleModelNumber: m.ModuleModelNumber,
			SampleRateHz:      m.SampleRateHz,
		})
	}

	channels := []daqNodeChannel{}
	for _, ctrl := range cfg.ControlList.Controls {
		if !ctrl.Enabled {
			continue
		}
		for _, ch := range ctrl.Channels {
			if ch.RefDesDaq != daqRefDes {
				continue
			}
			mx := ch.DaqMx
			channels = append(channels, daqNodeChannel{
				RefDes:                     ch.RefDes,
				ModuleModelNumber:          ch.ModuleModelNumber,
				ChannelNumber:              ch.ChannelNumber,
				TaskName:                   mx.TaskName,
				Type:                       mx.TCType,
				Sensitivity:                parseOptFloat(mx.Sensitivity),
				Balance:                    parseOptFloat(mx.Balance),
				InputTerminalConfiguration: mx.InputTerminalConfiguration,
				BridgeConfiguration:        mx.BridgeConfiguration,
				VoltageExcitationSource:    mx.VoltageExcitationSource,
				ExcitationVoltage:          parseOptFloat(mx.ExcitationVoltage),
				NominalBridgeResistance:    parseOptFloat(mx.NominalBridgeResistance),
				FirstElectricalValue:       parseOptFloat(mx.FirstElectricalValue),
				SecondElectricalValue:      parseOptFloat(mx.SecondElectricalValue),
				FirstPhysicalValue:         parseOptFloat(mx.FirstPhysicalValue),
				SecondPhysicalValue:        parseOptFloat(mx.SecondPhysicalValue),
				ElectricalUnits:            mx.ElectricalUnits,
				Units:                      mx.Units,
			})
		}
	}

	msg := daqNodeConfigMsg{
		Type:             "config",
		SampleRateHz:     sampleRateHz,
		ManagementRateHz: cfg.Network.ManagementRateHz,
		Modules:          modules,
		Channels:         channels,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal DAQ config for %s: %w", daqRefDes, err)
	}
	return string(b), nil
}

// BuildRefDesMap returns a map from every channel refDes to the DAQ node refDes
// that owns it.  CTR health channel refDes values are excluded.
func BuildRefDesMap(cfg *SystemConfig) map[string]string {
	m := make(map[string]string)
	for _, ctrl := range cfg.ControlList.Controls {
		for _, ch := range ctrl.Channels {
			if ch.RefDes != "" && ch.RefDesDaq != "" {
				m[ch.RefDes] = ch.RefDesDaq
			}
		}
	}
	return m
}

// ── Channel bounds ────────────────────────────────────────────────────────────

// ChannelBounds holds optional engineering-unit bounds for bad-data detection.
type ChannelBounds struct {
	Min *float64
	Max *float64
}

// BuildChannelBoundsMap returns a map from channel refDes → ChannelBounds for
// every enabled channel that has at least one of validMin / validMax set.
func BuildChannelBoundsMap(cfg *SystemConfig) map[string]ChannelBounds {
	m := make(map[string]ChannelBounds)
	for _, ctrl := range cfg.ControlList.Controls {
		if !ctrl.Enabled {
			continue
		}
		for _, ch := range ctrl.Channels {
			min := parseOptFloat(ch.ValidMin)
			max := parseOptFloat(ch.ValidMax)
			if min != nil || max != nil {
				m[ch.RefDes] = ChannelBounds{Min: min, Max: max}
			}
		}
	}
	return m
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// buildDetails converts a Control's Details struct into the loosely-typed map
// that the browser expects in the "details" field of the config message.
// Only the fields relevant to the control's Type are populated.
func buildDetails(ctrl Control) map[string]interface{} {
	m := make(map[string]interface{})
	switch ctrl.Type {
	case "pressure":
		m["absolute"] = ctrl.Details.Absolute
		m["absoluteSensorRefDes"] = ctrl.Details.AbsoluteSensorRefDes
	case "bangBang":
		m["senseRefDes"] = ctrl.Details.SenseRefDes
	}
	return m
}
