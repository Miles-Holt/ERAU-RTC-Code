package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// NodeConfig is the minimal local configuration for the DAQ node.
// All channel/hardware config is received from the control node after handshake.
type NodeConfig struct {
	RefDes     string `yaml:"refDes"`
	ListenPort int    `yaml:"listenPort"`
}

func loadConfig(path string) (*NodeConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg NodeConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.RefDes == "" {
		return nil, fmt.Errorf("refDes is required in %s", path)
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 8001
	}
	return &cfg, nil
}
