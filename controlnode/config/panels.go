package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

// PanelLayoutsConfig is the top-level structure of panelLayouts.yaml.
type PanelLayoutsConfig struct {
	Panels []PanelLayoutEntry `yaml:"panels"`
}

// PanelLayoutEntry describes one front-panel layout file.
type PanelLayoutEntry struct {
	Name    string `yaml:"name"`
	File    string `yaml:"file"`
	Enabled bool   `yaml:"enabled"`
}

// LoadPanelLayouts reads and parses panelLayouts.yaml at the given path.
// Returns an empty config (no panels) if the file does not exist.
func LoadPanelLayouts(path string) (*PanelLayoutsConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &PanelLayoutsConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg PanelLayoutsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
