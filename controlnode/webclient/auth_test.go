package webclient

import "testing"

// TestLoadUserAuthRealConfig loads the repository's userAuth.yaml.
func TestLoadUserAuthRealConfig(t *testing.T) {
	cfg, err := LoadUserAuth("../../config/userAuth.yaml")
	if err != nil {
		t.Fatalf("LoadUserAuth: %v", err)
	}
	if cfg.PIN == "" {
		t.Error("loaded auth config has empty PIN")
	}
	if len(cfg.Users) == 0 {
		t.Error("loaded auth config has no users")
	}
}

// TestValidate covers the credential-matching logic.
func TestValidate(t *testing.T) {
	cfg := &UserAuthConfig{PIN: "1234", Users: []string{"alice", "bob"}}

	cases := []struct {
		name, pin string
		want      bool
	}{
		{"alice", "1234", true},
		{"bob", "1234", true},
		{"alice", "0000", false}, // wrong PIN
		{"carol", "1234", false}, // unknown user
		{"", "1234", false},      // empty user
		{"alice", "", false},     // empty PIN
	}
	for _, c := range cases {
		if got := cfg.Validate(c.name, c.pin); got != c.want {
			t.Errorf("Validate(%q, %q) = %v, want %v", c.name, c.pin, got, c.want)
		}
	}
}
