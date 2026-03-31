package webclient

import (
	"os"

	"gopkg.in/yaml.v3"
)

// UserAuthConfig is the top-level structure of userAuth.yaml.
type UserAuthConfig struct {
	PIN   string   `yaml:"pin"`
	Users []string `yaml:"users"`
}

// authRequestMsg is sent by the browser to request authentication.
type authRequestMsg struct {
	Type string `json:"type"`
	Name string `json:"name"`
	PIN  string `json:"pin"`
}

// authResponseMsg is sent by the server back to the browser.
type authResponseMsg struct {
	Type     string `json:"type"`
	Approved bool   `json:"approved"`
	Name     string `json:"name,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// LoadUserAuth reads and parses the userAuth.yaml file at path.
// Returns nil and an error if the file is missing or malformed.
func LoadUserAuth(path string) (*UserAuthConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg UserAuthConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate returns true if the name is in the approved list and the PIN matches.
func (c *UserAuthConfig) Validate(name, pin string) bool {
	if pin != c.PIN {
		return false
	}
	for _, u := range c.Users {
		if u == name {
			return true
		}
	}
	return false
}
