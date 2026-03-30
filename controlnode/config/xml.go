// Package config parses nodeConfigs XML and builds JSON payloads for
// web clients and DAQ nodes.
package config

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// ── XML structures ────────────────────────────────────────────────────────────

type SystemConfig struct {
	ControlList ControlList `xml:"controlList"`
	Network     Network     `xml:"network"`
	CtrNode     CtrNodeDef  `xml:"ctrNode"`
	DaqNodes    DaqNodes    `xml:"daqNodes"`
}

type Network struct {
	WebSocketPort   int `xml:"webSocketPort"`
	BroadcastRateHz int `xml:"broadcastRateHz"`
}

type CtrNodeDef struct {
	RefDes      string    `xml:"refDes"`
	IP          string    `xml:"ip"`
	Description string    `xml:"description"`
	Enabled     string    `xml:"enabled"`
	WSPort      int       `xml:"wsPort"`
	Health      CtrHealth `xml:"health"`
}

type CtrHealth struct {
	Sensors  []CtrSensor  `xml:"sensors>sensor"`
	Commands []CtrCommand `xml:"commands>command"`
}

type CtrSensor struct {
	RefDes      string `xml:"refDes"`
	Description string `xml:"description"`
	Units       string `xml:"units"`
}

type CtrCommand struct {
	RefDes      string `xml:"refDes"`
	Description string `xml:"description"`
	Role        string `xml:"role"`
}

type ControlList struct {
	Controls []Control `xml:"control"`
}

type Control struct {
	RefDes      string    `xml:"refDes"`
	Description string    `xml:"description"`
	Enabled     string    `xml:"enabled"`
	Type        string    `xml:"type"`
	SubType     string    `xml:"subType"`
	Details     Details   `xml:"details"`
	Channels    []Channel `xml:"channels>channel"`
}

// Details holds all possible type-specific fields; unused fields are empty.
type Details struct {
	// pressure
	Absolute             string `xml:"absolute"`
	AbsoluteSensorRefDes string `xml:"absoluteSensorRefDes"`
	// bangBang
	SenseRefDes string `xml:"senseRefDes"`
}

type Channel struct {
	RefDes            string `xml:"refDes"`
	Role              string `xml:"role"`
	ModelNumber       string `xml:"modelNumber"`
	SerialNumber      string `xml:"serialNumber"`
	RefDesDaq         string `xml:"refDesDaq"`
	ModuleModelNumber string `xml:"moduleModelNumber"`
	ChannelNumber     string `xml:"channelNumber"`
	DaqMx             DaqMx  `xml:"daqMx"`
}

// DaqMx holds all possible daqMx child elements; unused fields stay empty.
type DaqMx struct {
	TaskName                   string `xml:"taskName"              json:"taskName,omitempty"`
	TCType                     string `xml:"type"                  json:"type,omitempty"`
	Units                      string `xml:"units"                 json:"units,omitempty"`
	Sensitivity                string `xml:"sensitivity"           json:"sensitivity,omitempty"`
	Balance                    string `xml:"balance"               json:"balance,omitempty"`
	InputTerminalConfiguration string `xml:"inputTerminalConfiguration" json:"inputTerminalConfiguration,omitempty"`
	BridgeConfiguration        string `xml:"bridgeConfiguration"   json:"bridgeConfiguration,omitempty"`
	VoltageExcitationSource    string `xml:"voltageExcitationSource" json:"voltageExcitationSource,omitempty"`
	ExcitationVoltage          string `xml:"excitationVoltage"     json:"excitationVoltage,omitempty"`
	NominalBridgeResistance    string `xml:"nominalBridgeResistance" json:"nominalBridgeResistance,omitempty"`
	FirstElectricalValue       string `xml:"firstElectricalValue"  json:"firstElectricalValue,omitempty"`
	SecondElectricalValue      string `xml:"secondElectricalValue" json:"secondElectricalValue,omitempty"`
	FirstPhysicalValue         string `xml:"firstPhysicalValue"    json:"firstPhysicalValue,omitempty"`
	SecondPhysicalValue        string `xml:"secondPhysicalValue"   json:"secondPhysicalValue,omitempty"`
	ElectricalUnits            string `xml:"electricalUnits"       json:"electricalUnits,omitempty"`
}

type DaqNodes struct {
	Nodes []DaqNodeDef `xml:"daqNode"`
}

type DaqNodeDef struct {
	RefDes      string   `xml:"refDes"`
	IP          string   `xml:"ip"`
	Description string   `xml:"description"`
	Enabled     string   `xml:"enabled"`
	WSPort      int      `xml:"wsPort"`
	Modules     []Module `xml:"modules>module"`
}

type Module struct {
	SlotID            int          `xml:"slotId"`
	ModuleModelNumber string       `xml:"moduleModelNumber"`
	Description       string       `xml:"description"`
	IOMode            string       `xml:"ioMode"`
	Enabled           string       `xml:"enabled"`
	SampleRateHz      int          `xml:"sampleRateHz"`
	Channels          []DaqChannel `xml:"channels>channel"`
}

type DaqChannel struct {
	RefDes                  string `xml:"refDes"`
	SerialNumber            string `xml:"serialNumber"`
	ModelNumber             string `xml:"modelNumber"`
	Type                    string `xml:"type"`
	Channel                 string `xml:"channel"`
	Enabled                 string `xml:"enabled"`
	Sensitivity             string `xml:"sensitivity"`
	SensitivityUnits        string `xml:"sensitivityUnits"`
	CalibrationBalance      string `xml:"calibrationBalance"`
	CalibrationBalanceUnits string `xml:"calibrationBalanceUnits"`
	Units                   string `xml:"units"`
}

// ── Parsing ───────────────────────────────────────────────────────────────────

// Parse reads and parses the XML config file at the given path.
func Parse(path string) (*SystemConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg SystemConfig
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config XML: %w", err)
	}
	if cfg.Network.BroadcastRateHz == 0 {
		cfg.Network.BroadcastRateHz = 20
	}
	if cfg.Network.WebSocketPort == 0 {
		cfg.Network.WebSocketPort = 8000
	}
	return &cfg, nil
}

// ── JSON builders ─────────────────────────────────────────────────────────────

// webclientControl is the JSON shape the browser expects in the config message.
type webclientControl struct {
	RefDes      string                   `json:"refDes"`
	Description string                   `json:"description"`
	Type        string                   `json:"type"`
	SubType     string                   `json:"subType"`
	Details     map[string]interface{}   `json:"details"`
	Channels    []webclientChannel       `json:"channels"`
}

type webclientChannel struct {
	RefDes string `json:"refDes"`
	Role   string `json:"role"`
	Units  string `json:"units"`
}

// BuildWebClientConfigJSON returns the JSON string sent to browsers on connect.
// It includes enabled controls from <controlList> plus CTR health sensors/commands.
func BuildWebClientConfigJSON(cfg *SystemConfig) (string, error) {
	var controls []webclientControl

	// Enabled controls from <controlList>
	for _, ctrl := range cfg.ControlList.Controls {
		if !isEnabled(ctrl.Enabled) {
			continue
		}
		var channels []webclientChannel
		for _, ch := range ctrl.Channels {
			channels = append(channels, webclientChannel{
				RefDes: strings.TrimSpace(ch.RefDes),
				Role:   strings.TrimSpace(ch.Role),
				Units:  strings.TrimSpace(ch.DaqMx.Units),
			})
		}
		controls = append(controls, webclientControl{
			RefDes:      strings.TrimSpace(ctrl.RefDes),
			Description: strings.TrimSpace(ctrl.Description),
			Type:        strings.TrimSpace(ctrl.Type),
			SubType:     strings.TrimSpace(ctrl.SubType),
			Details:     buildDetails(ctrl),
			Channels:    channels,
		})
	}

	// CTR health sensors and commands as a single ctrNode control
	if len(cfg.CtrNode.Health.Sensors) > 0 || len(cfg.CtrNode.Health.Commands) > 0 {
		var channels []webclientChannel
		for _, s := range cfg.CtrNode.Health.Sensors {
			channels = append(channels, webclientChannel{
				RefDes: strings.TrimSpace(s.RefDes),
				Role:   "",
				Units:  strings.TrimSpace(s.Units),
			})
		}
		for _, c := range cfg.CtrNode.Health.Commands {
			channels = append(channels, webclientChannel{
				RefDes: strings.TrimSpace(c.RefDes),
				Role:   strings.TrimSpace(c.Role),
				Units:  "",
			})
		}
		controls = append(controls, webclientControl{
			RefDes:      strings.TrimSpace(cfg.CtrNode.RefDes),
			Description: "CTR Node Health",
			Type:        "ctrNode",
			SubType:     "",
			Details:     map[string]interface{}{},
			Channels:    channels,
		})
	}

	msg := map[string]interface{}{
		"type":     "config",
		"controls": controls,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal web client config: %w", err)
	}
	return string(b), nil
}

// daqNodeConfigMsg is the JSON sent to a DAQ node after it requests config.
type daqNodeConfigMsg struct {
	Type         string           `json:"type"`
	RefDes       string           `json:"refDes"`
	SampleRateHz int              `json:"sampleRateHz"`
	Channels     []daqNodeChannel `json:"channels"`
}

type daqNodeChannel struct {
	RefDes string `json:"refDes"`
	Role   string `json:"role"`
	DaqMx  DaqMx  `json:"daqMx"`
}

// BuildDaqNodeConfigJSON returns the JSON config string for a specific DAQ node,
// containing all channels from <controlList> that reference that DAQ node.
func BuildDaqNodeConfigJSON(cfg *SystemConfig, daqRefDes string, sampleRateHz int) (string, error) {
	var channels []daqNodeChannel
	for _, ctrl := range cfg.ControlList.Controls {
		if !isEnabled(ctrl.Enabled) {
			continue
		}
		for _, ch := range ctrl.Channels {
			if strings.TrimSpace(ch.RefDesDaq) != daqRefDes {
				continue
			}
			channels = append(channels, daqNodeChannel{
				RefDes: strings.TrimSpace(ch.RefDes),
				Role:   strings.TrimSpace(ch.Role),
				DaqMx:  ch.DaqMx,
			})
		}
	}

	msg := daqNodeConfigMsg{
		Type:         "config",
		RefDes:       daqRefDes,
		SampleRateHz: sampleRateHz,
		Channels:     channels,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal DAQ config for %s: %w", daqRefDes, err)
	}
	return string(b), nil
}

// BuildRefDesMap returns a map from every channel refDes to the DAQ node refDes
// that owns it (from <refDesDaq>).  CTR health channel refDes values are excluded
// (they are handled internally).
func BuildRefDesMap(cfg *SystemConfig) map[string]string {
	m := make(map[string]string)
	for _, ctrl := range cfg.ControlList.Controls {
		for _, ch := range ctrl.Channels {
			refDes := strings.TrimSpace(ch.RefDes)
			daqRef := strings.TrimSpace(ch.RefDesDaq)
			if refDes != "" && daqRef != "" {
				m[refDes] = daqRef
			}
		}
	}
	return m
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func isEnabled(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "true")
}

func buildDetails(ctrl Control) map[string]interface{} {
	m := make(map[string]interface{})
	switch strings.TrimSpace(ctrl.Type) {
	case "pressure":
		m["absolute"] = isEnabled(ctrl.Details.Absolute)
		m["absoluteSensorRefDes"] = strings.TrimSpace(ctrl.Details.AbsoluteSensorRefDes)
	case "bangBang":
		m["senseRefDes"] = strings.TrimSpace(ctrl.Details.SenseRefDes)
	}
	return m
}
