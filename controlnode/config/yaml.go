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

type SystemConfig struct {
	ControlList ControlList
	Network     Network
	CtrNode     CtrNodeDef
	DaqNodes    DaqNodes
}

type Network struct {
	WebSocketPort            int
	BroadcastRateHz          int
	ManagementRateHz         int
	ChannelStaleMs           int
}

type CtrNodeDef struct {
	RefDes      string
	IP          string
	Description string
	Enabled     bool
	WSPort      int
	Health      CtrHealth
}

type CtrHealth struct {
	Sensors  []CtrSensor
	Commands []CtrCommand
}

type CtrSensor struct {
	RefDes      string
	Description string
	Units       string
}

type CtrCommand struct {
	RefDes      string
	Description string
	Role        string
}

type ControlList struct {
	Controls []Control
}

type Control struct {
	RefDes      string
	Description string
	Enabled     bool
	Type        string
	SubType     string
	Details     Details
	Channels    []Channel
}

// Details holds type-specific fields; unused fields are zero values.
type Details struct {
	// pressure
	Absolute             bool
	AbsoluteSensorRefDes string
	// bangBang
	SenseRefDes string
}

type Channel struct {
	RefDes            string
	Role              string
	RefDesDaq         string // DAQ node refDes
	ModuleModelNumber string
	ChannelNumber     string
	DaqMx             DaqMx
	ValidMin          string
	ValidMax          string
}

// DaqMx holds all possible DAQmx configuration fields; unused fields stay empty.
type DaqMx struct {
	TaskName                   string `json:"taskName,omitempty"`
	TCType                     string `json:"type,omitempty"`
	Units                      string `json:"units,omitempty"`
	Sensitivity                string `json:"sensitivity,omitempty"`
	Balance                    string `json:"balance,omitempty"`
	InputTerminalConfiguration string `json:"inputTerminalConfiguration,omitempty"`
	BridgeConfiguration        string `json:"bridgeConfiguration,omitempty"`
	VoltageExcitationSource    string `json:"voltageExcitationSource,omitempty"`
	ExcitationVoltage          string `json:"excitationVoltage,omitempty"`
	NominalBridgeResistance    string `json:"nominalBridgeResistance,omitempty"`
	FirstElectricalValue       string `json:"firstElectricalValue,omitempty"`
	SecondElectricalValue      string `json:"secondElectricalValue,omitempty"`
	FirstPhysicalValue         string `json:"firstPhysicalValue,omitempty"`
	SecondPhysicalValue        string `json:"secondPhysicalValue,omitempty"`
	ElectricalUnits            string `json:"electricalUnits,omitempty"`
}

type DaqNodes struct {
	Nodes []DaqNodeDef
}

type DaqNodeDef struct {
	RefDes      string
	IP          string
	Description string
	Enabled     bool
	WSPort      int
	Modules     []Module
}

type Module struct {
	SlotID            int
	ModuleModelNumber string
	Description       string
	IOMode            string
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

// ── Helpers ───────────────────────────────────────────────────────────────────

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
