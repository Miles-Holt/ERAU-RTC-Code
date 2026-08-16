package alerts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodSrc = `template every_daqnode
    on disconnect -> alarm   "{node} disconnected"
    on reconnect  -> info    "{node} reconnected"
    on bad_data   -> warning "{refDes} out of range: {value}"
    on stale 5s   -> warning "{node} data stale"

alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psia"
    latch

alert LOW-FUEL
    if PT-01 < 10
    severity warning
    message "Fuel pressure low"
`

func testOpts() Options {
	return Options{
		KnownChannels: []string{"CPT-01", "LIM-CPT01-HIGH", "PT-01"},
		MachineNames:  []string{"fuelSeq"},
	}
}

func loadSrc(t *testing.T, src string) (*Config, error) {
	t.Helper()
	return Load([]Source{{Name: "test.alert", Text: src}}, testOpts())
}

func TestLoadGood(t *testing.T) {
	cfg, err := loadSrc(t, goodSrc)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Template == nil {
		t.Fatal("template not parsed")
	}
	if got := len(cfg.Template.Events); got != 4 {
		t.Errorf("template events = %d, want 4", got)
	}
	if ev := cfg.Template.Events[EventDisconnect]; ev.Severity != SeverityAlarm {
		t.Errorf("disconnect severity = %q, want alarm", ev.Severity)
	}
	// `on stale 5s` overrides the 2000 ms default.
	if got := cfg.Template.StaleMs(); got != 5000 {
		t.Errorf("StaleMs = %d, want 5000", got)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(cfg.Rules))
	}
	if cfg.Rules[0].Name != "CHAMBER-HIGH" || !cfg.Rules[0].Latch {
		t.Errorf("rule[0] = %+v, want CHAMBER-HIGH with latch", cfg.Rules[0])
	}
	if cfg.Rules[1].Latch {
		t.Error("rule[1] LOW-FUEL should not latch")
	}
	if cfg.Rules[0].ID() != "rule:CHAMBER-HIGH" {
		t.Errorf("rule id = %q, want rule:CHAMBER-HIGH", cfg.Rules[0].ID())
	}
}

func TestStaleDefaultWhenUnqualified(t *testing.T) {
	cfg, err := loadSrc(t, "template every_daqnode\n    on stale -> warning \"{node} stale\"\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Template.StaleMs(); got != DefaultStaleMs {
		t.Errorf("StaleMs = %d, want %d", got, DefaultStaleMs)
	}
}

// A rule referencing a channel the running config does not define must abort
// startup — never evaluate to a silent 0.
func TestUnknownChannelIsFatal(t *testing.T) {
	cases := map[string]string{
		"condition": "alert BAD\n    if NOPE-01 > 5\n    severity alarm\n    message \"nope\"\n",
		"message":   "alert BAD\n    if CPT-01 > 5\n    severity alarm\n    message \"value {NOPE-01}\"\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := loadSrc(t, src)
			if err == nil {
				t.Fatal("expected a fatal error for the unknown channel reference")
			}
			if !strings.Contains(err.Error(), "NOPE-01") {
				t.Errorf("error %q should name the offending channel", err)
			}
			if !strings.Contains(err.Error(), "test.alert:") {
				t.Errorf("error %q should carry file:line", err)
			}
		})
	}
}

func TestLoadRejections(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"bad severity", "alert X\n    if CPT-01 > 1\n    severity urgent\n    message \"m\"\n", "severity"},
		{"missing if", "alert X\n    severity alarm\n    message \"m\"\n", "if"},
		{"missing message", "alert X\n    if CPT-01 > 1\n    severity alarm\n", "message"},
		{"missing severity", "alert X\n    if CPT-01 > 1\n    message \"m\"\n", "severity"},
		{"duplicate rule", goodSrc + "\nalert LOW-FUEL\n    if PT-01 < 1\n    severity info\n    message \"m\"\n", "already defined"},
		{"unknown template", "template every_node\n    on stale -> info \"x\"\n", "every_daqnode"},
		{"unknown event", "template every_daqnode\n    on exploded -> info \"x\"\n", "unknown event"},
		{"duration on wrong event", "template every_daqnode\n    on bad_data 5s -> info \"x\"\n", "duration"},
		{"bad template severity", "template every_daqnode\n    on stale -> urgent \"x\"\n", "severity"},
		{"unknown placeholder field", "template every_daqnode\n    on stale -> info \"{nope} x\"\n", "placeholder"},
		{"duplicate event", "template every_daqnode\n    on stale -> info \"a\"\n    on stale -> info \"b\"\n", "already declared"},
		{"junk at top level", "channel FOO\n    type float\n", "top level"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadSrc(t, tc.src)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// Template messages may use the event fields AND channel names.
func TestTemplateMessageMayUseChannels(t *testing.T) {
	src := "template every_daqnode\n    on bad_data -> warning \"{refDes}={value} (limit {LIM-CPT01-HIGH})\"\n"
	if _, err := loadSrc(t, src); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.alert"), []byte(goodSrc), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadDir(dir, testOpts())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(cfg.Rules) != 2 || cfg.Template == nil {
		t.Errorf("LoadDir gave %d rules, template=%v", len(cfg.Rules), cfg.Template != nil)
	}
	files, err := AlertFiles(dir)
	if err != nil || len(files) != 1 || files[0] != "a.alert" {
		t.Errorf("AlertFiles = %v, %v", files, err)
	}

	// A missing directory is a legitimate configuration, not an error.
	empty, err := LoadDir(filepath.Join(dir, "nope"), testOpts())
	if err != nil {
		t.Fatalf("LoadDir(missing): %v", err)
	}
	if len(empty.Rules) != 0 || empty.Template != nil {
		t.Error("missing dir should yield an empty config")
	}
}

// The shipped config must always load against the shipped channel set.
func TestShippedConfigLoads(t *testing.T) {
	cfg, err := LoadDir("../../config/alerts", Options{
		KnownChannels: []string{
			"CPT-01", "LIM-CPT01-HIGH", "LIM-CPT01-LOW", "SEQ-CUTOFF-T", "SEQ-IGN-LEAD",
		},
	})
	if err != nil {
		t.Fatalf("shipped config/alerts: %v", err)
	}
	if cfg.Template == nil {
		t.Error("shipped config should define the every_daqnode template")
	}
	if len(cfg.Rules) == 0 {
		t.Error("shipped config should define at least the example rule")
	}
}
