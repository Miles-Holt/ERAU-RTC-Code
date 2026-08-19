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

// describe is optional (item 07a); when present it compiles onto
// Rule.Description with the same placeholder interpolation as message, and
// when absent Description is simply "".
func TestLoadDescribe(t *testing.T) {
	cfg, err := loadSrc(t, "alert CHAMBER-HIGH\n"+
		"    if CPT-01 > LIM-CPT01-HIGH\n"+
		"    severity alarm\n"+
		"    message \"Chamber pressure high\"\n"+
		"    describe \"CPT-01 read {CPT-01} psia, above the {LIM-CPT01-HIGH} limit\"\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(cfg.Rules))
	}
	want := "CPT-01 read {CPT-01} psia, above the {LIM-CPT01-HIGH} limit"
	if cfg.Rules[0].Description != want {
		t.Errorf("description = %q, want %q", cfg.Rules[0].Description, want)
	}
}

func TestLoadNoDescribe(t *testing.T) {
	cfg, err := loadSrc(t, goodSrc)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Rules[0].Description != "" {
		t.Errorf("description = %q, want empty (no describe in goodSrc)", cfg.Rules[0].Description)
	}
}

// channels (item 09) is optional, same split as describe: present, `plot`
// and `line` compile onto Rule.PlotChannels/Lines; a literal `line` value
// compiles to Lines[0].Value set and Channel empty.
func TestLoadChannels(t *testing.T) {
	cfg, err := loadSrc(t, "alert CHAMBER-HIGH\n"+
		"    if CPT-01 > LIM-CPT01-HIGH\n"+
		"    severity alarm\n"+
		"    message \"Chamber pressure high\"\n"+
		"    channels\n"+
		"        plot CPT-01\n"+
		"        line 850 \"limit\"\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %d, want 1", len(cfg.Rules))
	}
	rule := cfg.Rules[0]
	if len(rule.PlotChannels) != 1 || rule.PlotChannels[0] != "CPT-01" {
		t.Errorf("plot channels = %v, want [CPT-01]", rule.PlotChannels)
	}
	if len(rule.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(rule.Lines))
	}
	if rule.Lines[0].Channel != "" {
		t.Errorf("line channel = %q, want empty (literal value)", rule.Lines[0].Channel)
	}
	if rule.Lines[0].Value == nil || *rule.Lines[0].Value != 850 {
		t.Errorf("line value = %v, want 850", rule.Lines[0].Value)
	}
	if rule.Lines[0].Label != "limit" {
		t.Errorf("line label = %q, want %q", rule.Lines[0].Label, "limit")
	}
}

// A negative literal line value (`line -60.0 "..."`) compiles to a negative
// PlotLine.Value, folded the same way `default -60.0` is elsewhere in this
// codebase (dsl.LiteralOrNegatedLiteral).
func TestLoadChannelsNegativeLineValue(t *testing.T) {
	cfg, err := loadSrc(t, "alert CHAMBER-LOW\n"+
		"    if CPT-01 < -60.0\n"+
		"    severity alarm\n"+
		"    message \"Chamber pressure low\"\n"+
		"    channels\n"+
		"        line -60.0 \"floor\"\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	line := cfg.Rules[0].Lines[0]
	if line.Value == nil || *line.Value != -60.0 {
		t.Errorf("line value = %v, want -60", line.Value)
	}
}

// A `line` naming a channel (no dot, no literal) compiles to Lines[0].Channel
// set and Value nil — the worked example from the design doc.
func TestLoadChannelsLineIsChannelReference(t *testing.T) {
	cfg, err := loadSrc(t, "alert CHAMBER-HIGH\n"+
		"    if CPT-01 > LIM-CPT01-HIGH\n"+
		"    severity alarm\n"+
		"    message \"Chamber pressure high\"\n"+
		"    channels\n"+
		"        plot CPT-01\n"+
		"        line LIM-CPT01-HIGH \"abort limit\"\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	line := cfg.Rules[0].Lines[0]
	if line.Value != nil {
		t.Errorf("line value = %v, want nil (channel reference)", line.Value)
	}
	if line.Channel != "LIM-CPT01-HIGH" {
		t.Errorf("line channel = %q, want LIM-CPT01-HIGH", line.Channel)
	}
	if line.Label != "abort limit" {
		t.Errorf("line label = %q, want %q", line.Label, "abort limit")
	}
}

// plot naming an unknown channel is a startup error, same contract as every
// other channel reference in this file.
func TestChannelsUnknownPlotChannelIsFatal(t *testing.T) {
	_, err := loadSrc(t, "alert BAD\n"+
		"    if CPT-01 > 5\n"+
		"    severity alarm\n"+
		"    message \"m\"\n"+
		"    channels\n"+
		"        plot NOPE-01\n")
	if err == nil {
		t.Fatal("expected a fatal error for the unknown plot channel")
	}
	if !strings.Contains(err.Error(), "NOPE-01") {
		t.Errorf("error %q should name the offending channel", err)
	}
}

// line naming an unknown channel is a startup error too.
func TestChannelsUnknownLineChannelIsFatal(t *testing.T) {
	_, err := loadSrc(t, "alert BAD\n"+
		"    if CPT-01 > 5\n"+
		"    severity alarm\n"+
		"    message \"m\"\n"+
		"    channels\n"+
		"        line NOPE-01 \"limit\"\n")
	if err == nil {
		t.Fatal("expected a fatal error for the unknown line channel")
	}
	if !strings.Contains(err.Error(), "NOPE-01") {
		t.Errorf("error %q should name the offending channel", err)
	}
}

// No `channels` block at all is the common case (most alerts won't have
// one) — Rule.PlotChannels/Lines stay nil, existing alerts keep compiling
// unchanged.
func TestLoadNoChannels(t *testing.T) {
	cfg, err := loadSrc(t, goodSrc)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Rules[0].PlotChannels != nil {
		t.Errorf("plot channels = %v, want nil (no channels block in goodSrc)", cfg.Rules[0].PlotChannels)
	}
	if cfg.Rules[0].Lines != nil {
		t.Errorf("lines = %v, want nil (no channels block in goodSrc)", cfg.Rules[0].Lines)
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
		"condition":   "alert BAD\n    if NOPE-01 > 5\n    severity alarm\n    message \"nope\"\n",
		"message":     "alert BAD\n    if CPT-01 > 5\n    severity alarm\n    message \"value {NOPE-01}\"\n",
		"description": "alert BAD\n    if CPT-01 > 5\n    severity alarm\n    message \"m\"\n    describe \"value {NOPE-01}\"\n",
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
