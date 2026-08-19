package alerts

import "testing"

// A rule alert must say which channels it is about. Without attribution the
// browser can only map the auto-generated ids (`sensor:<refDes>`), so a front
// panel object would sit grey while a rule alarm about its own channel was
// raised — the panel would be lying by omission.
func TestRuleAlertCarriesItsChannels(t *testing.T) {
	src := `alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "high"
`
	cfg, err := Load([]Source{{Name: "t.alert", Text: src}}, testOpts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(cfg.Rules))
	}
	got := cfg.Rules[0].channels
	want := []string{"CPT-01", "LIM-CPT01-HIGH"}
	if len(got) != len(want) {
		t.Fatalf("channels = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("channels = %v, want %v", got, want)
		}
	}
}

// A machine member access is not a channel and must not be attributed as one.
func TestMachineRefIsNotAChannel(t *testing.T) {
	src := `alert M
    if machine.fuelSeq.state == "abort"
    severity alarm
    message "aborted"
`
	cfg, err := Load([]Source{{Name: "t.alert", Text: src}}, testOpts())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Rules[0].channels; len(got) != 0 {
		t.Errorf("channels = %v, want none", got)
	}
}
