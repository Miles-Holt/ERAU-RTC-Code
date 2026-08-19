package alerts

import (
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"controlnode/dsl"
)

// ── Test doubles ─────────────────────────────────────────────────────────────

// space is a fixed channel space for rule evaluation.
type space map[string]float64

func (s space) Get(name string) (dsl.Value, bool) {
	v, ok := s[name]
	if !ok {
		return dsl.Value{}, false
	}
	return dsl.NewFloat(v), true
}

// recorder captures everything the registry publishes, in order.
type recorder struct {
	mu     sync.Mutex
	raised []Record
	acked  []string
}

func (r *recorder) PublishAlert(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raised = append(r.raised, rec)
}

func (r *recorder) PublishAlertAcked(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acked = append(r.acked, id)
}

func (r *recorder) raises() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Record(nil), r.raised...)
}

func (r *recorder) acks() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.acked...)
}

// fakeClock is a manually advanced clock for stale detection.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Unix(1700000000, 0)}
}
func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestEngine(t *testing.T, src string, nodes []string, vals dsl.ChannelSpace) (*Engine, *Registry, *recorder, *fakeClock) {
	t.Helper()
	cfg, err := Load([]Source{{Name: "test.alert", Text: src}}, testOpts())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	clk := newClock()
	reg.SetClock(clk.now)
	eng, err := NewEngine(EngineConfig{
		Config:   cfg,
		Registry: reg,
		Nodes:    nodes,
		Values:   vals,
		Now:      clk.now,
		OnError:  func(error) {},
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng, reg, rec, clk
}

// ── Rule alerts ──────────────────────────────────────────────────────────────

const ruleSrc = `alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psia"
`

const latchSrc = `alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psia"
    latch
`

// describeSrc carries a `describe` with a DIFFERENT placeholder than message,
// so a test can tell the two fields apart rather than merely proving both are
// non-empty.
const describeSrc = `alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psia"
    describe "CPT-01 exceeded the configured limit of {LIM-CPT01-HIGH} psia"
`

// A rule raises on the false→true edge and only then: a condition that stays
// true must not republish on every tick (100 Hz of duplicate alerts).
func TestRuleEdgeTriggered(t *testing.T) {
	eng, reg, rec, _ := newTestEngine(t, ruleSrc, nil, nil)
	s := space{"CPT-01": 100, "LIM-CPT01-HIGH": 450}

	eng.Tick(s)
	if len(rec.raises()) != 0 {
		t.Fatalf("condition false but alert raised: %+v", rec.raises())
	}

	s["CPT-01"] = 500
	eng.Tick(s)
	eng.Tick(s)
	eng.Tick(s)

	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want exactly 1 (edge-triggered)", len(raises))
	}
	if raises[0].ID != "rule:CHAMBER-HIGH" {
		t.Errorf("id = %q, want rule:CHAMBER-HIGH", raises[0].ID)
	}
	if raises[0].Category != SeverityAlarm {
		t.Errorf("category = %q, want alarm", raises[0].Category)
	}
	// Interpolated from the same snapshot the condition was evaluated against.
	if raises[0].Message != "Chamber pressure high: 500 psia" {
		t.Errorf("message = %q", raises[0].Message)
	}
	if !reg.Active("rule:CHAMBER-HIGH") {
		t.Error("alert should be active")
	}
}

// A rule's `describe` is interpolated the same way `message` is, from the
// same tick's snapshot, and lands in Record.Description distinct from
// Record.Message (item 07a).
func TestRuleDescribeInterpolated(t *testing.T) {
	eng, _, rec, _ := newTestEngine(t, describeSrc, nil, nil)
	s := space{"CPT-01": 500, "LIM-CPT01-HIGH": 450}
	eng.Tick(s)

	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	if want := "Chamber pressure high: 500 psia"; raises[0].Message != want {
		t.Errorf("message = %q, want %q", raises[0].Message, want)
	}
	if want := "CPT-01 exceeded the configured limit of 450 psia"; raises[0].Description != want {
		t.Errorf("description = %q, want %q", raises[0].Description, want)
	}
}

// A rule with no `describe` raises with Record.Description == "" — describe
// is optional, and its absence must not surface as a literal "?" or panic.
func TestRuleNoDescribeLeavesDescriptionEmpty(t *testing.T) {
	eng, _, rec, _ := newTestEngine(t, ruleSrc, nil, nil)
	s := space{"CPT-01": 500, "LIM-CPT01-HIGH": 450}
	eng.Tick(s)

	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	if raises[0].Description != "" {
		t.Errorf("description = %q, want empty (no describe in ruleSrc)", raises[0].Description)
	}
}

// channelsSrc carries a `channels` block (item 09) with one `plot` and two
// `line`s — one a literal value, one a channel reference — so a single test
// can pin both PlotLine.Value and PlotLine.Channel reaching the wire-facing
// Record correctly.
const channelsSrc = `alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psia"
    channels
        plot CPT-01
        line 850 "over-pressure ceiling"
        line LIM-CPT01-HIGH "abort limit"
`

// A rule's `channels` block reaches the raised Record's PlotChannels/Lines
// unchanged: the plot list verbatim, and each line resolved to exactly one
// of Value/Channel (item 09).
func TestRuleChannelsReachRecord(t *testing.T) {
	eng, _, rec, _ := newTestEngine(t, channelsSrc, nil, nil)
	s := space{"CPT-01": 500, "LIM-CPT01-HIGH": 450}
	eng.Tick(s)

	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	rec0 := raises[0]

	if len(rec0.PlotChannels) != 1 || rec0.PlotChannels[0] != "CPT-01" {
		t.Errorf("plot channels = %v, want [CPT-01]", rec0.PlotChannels)
	}
	if len(rec0.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(rec0.Lines))
	}

	lit := rec0.Lines[0]
	if lit.Value == nil || *lit.Value != 850 {
		t.Errorf("lines[0].Value = %v, want 850", lit.Value)
	}
	if lit.Channel != "" {
		t.Errorf("lines[0].Channel = %q, want empty (literal line)", lit.Channel)
	}
	if lit.Label != "over-pressure ceiling" {
		t.Errorf("lines[0].Label = %q", lit.Label)
	}

	chanLine := rec0.Lines[1]
	if chanLine.Value != nil {
		t.Errorf("lines[1].Value = %v, want nil (channel-valued line)", chanLine.Value)
	}
	if chanLine.Channel != "LIM-CPT01-HIGH" {
		t.Errorf("lines[1].Channel = %q, want LIM-CPT01-HIGH", chanLine.Channel)
	}
	if chanLine.Label != "abort limit" {
		t.Errorf("lines[1].Label = %q", chanLine.Label)
	}
}

// A rule with no `channels` block raises with Record.PlotChannels/Lines both
// nil — unchanged from before item 09, the common case.
func TestRuleNoChannelsLeavesPlotFieldsEmpty(t *testing.T) {
	eng, _, rec, _ := newTestEngine(t, ruleSrc, nil, nil)
	s := space{"CPT-01": 500, "LIM-CPT01-HIGH": 450}
	eng.Tick(s)

	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	if raises[0].PlotChannels != nil {
		t.Errorf("plot channels = %v, want nil (no channels block in ruleSrc)", raises[0].PlotChannels)
	}
	if raises[0].Lines != nil {
		t.Errorf("lines = %v, want nil (no channels block in ruleSrc)", raises[0].Lines)
	}
}

// A non-latching rule resolves itself when the condition clears.
func TestRuleAutoClear(t *testing.T) {
	eng, reg, rec, _ := newTestEngine(t, ruleSrc, nil, nil)
	s := space{"CPT-01": 500, "LIM-CPT01-HIGH": 450}
	eng.Tick(s)

	s["CPT-01"] = 100
	eng.Tick(s)

	if got := rec.acks(); len(got) != 1 || got[0] != "rule:CHAMBER-HIGH" {
		t.Fatalf("acks = %v, want one clear of rule:CHAMBER-HIGH", got)
	}
	if reg.Active("rule:CHAMBER-HIGH") {
		t.Error("cleared alert should no longer be active")
	}

	// And it re-raises on the next false→true edge.
	s["CPT-01"] = 600
	eng.Tick(s)
	if n := len(rec.raises()); n != 2 {
		t.Errorf("raises = %d, want 2 (re-raise after clearing)", n)
	}
}

// A latching rule stays raised after its condition clears, until an operator
// acks it.
func TestRuleLatchAndAck(t *testing.T) {
	eng, reg, rec, _ := newTestEngine(t, latchSrc, nil, nil)
	s := space{"CPT-01": 500, "LIM-CPT01-HIGH": 450}
	eng.Tick(s)

	s["CPT-01"] = 10
	eng.Tick(s)
	eng.Tick(s)

	if got := rec.acks(); len(got) != 0 {
		t.Fatalf("latching alert auto-cleared: %v", got)
	}
	if reg.Active("rule:CHAMBER-HIGH") {
		t.Error("the condition cleared, so the alert is no longer active")
	}
	if !reg.Resolved("rule:CHAMBER-HIGH") || !reg.Raised("rule:CHAMBER-HIGH") {
		t.Fatal("a latching alert stays on the board, resolved-but-unacked, after its condition clears")
	}

	if !reg.Ack("rule:CHAMBER-HIGH") {
		t.Fatal("Ack should find the alert")
	}
	if reg.Raised("rule:CHAMBER-HIGH") {
		t.Error("acked alert should not still be raised")
	}
	if got := rec.acks(); len(got) != 1 {
		t.Errorf("acks = %v, want the operator ack broadcast", got)
	}

	// Acking does not permanently silence the rule: a new edge raises it again.
	s["CPT-01"] = 900
	eng.Tick(s)
	if !reg.Active("rule:CHAMBER-HIGH") {
		t.Error("a new false→true edge should re-raise the acked alert")
	}
}

// A condition that is not boolean is reported, not silently treated as false,
// and does not raise an alert.
func TestRuleRuntimeErrorReported(t *testing.T) {
	cfg, err := Load([]Source{{Name: "t.alert", Text: "alert X\n    if CPT-01\n    severity info\n    message \"m\"\n"}}, testOpts())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	var errs int
	eng, err := NewEngine(EngineConfig{
		Config: cfg, Registry: reg,
		OnError: func(error) { errs++ },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	eng.Tick(space{"CPT-01": 5})
	eng.Tick(space{"CPT-01": 5})
	if errs != 1 {
		t.Errorf("errors reported = %d, want 1 (repeats suppressed)", errs)
	}
	if len(rec.raises()) != 0 {
		t.Errorf("a broken condition must not raise an alert: %+v", rec.raises())
	}
}

// TestRuleUnresolvedChannelPhrasing covers the operator-facing distinction: a
// rule referencing a CONFIGURED channel that has not published a value yet
// (nothing connected) must not be reported as an unknown channel — that sent
// people hunting for a config typo that was not there.
func TestRuleUnresolvedChannelPhrasing(t *testing.T) {
	cfg, err := Load([]Source{{Name: "t.alert",
		Text: "alert X\n    if CPT-01 > 10\n    severity info\n    message \"m\"\n"}}, testOpts())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	run := func(known []string) string {
		reg := NewRegistry()
		reg.SetSink(&recorder{})
		var got string
		eng, err := NewEngine(EngineConfig{
			Config: cfg, Registry: reg, KnownChannels: known,
			OnError: func(e error) {
				if got == "" {
					got = e.Error()
				}
			},
		})
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		eng.Tick(space{}) // no value for CPT-01
		return got
	}

	if msg := run([]string{"CPT-01"}); !strings.Contains(msg, "no value yet") ||
		strings.Contains(msg, "unknown channel") {
		t.Errorf("configured-but-unpublished channel reported as %q", msg)
	}
	if msg := run([]string{"SOMETHING-ELSE"}); !strings.Contains(msg, "unknown channel") {
		t.Errorf("unknown channel reported as %q", msg)
	}
}

// ── Template alerts ──────────────────────────────────────────────────────────

const tmplSrc = `template every_daqnode
    on disconnect -> alarm   "{node} disconnected"
    on reconnect  -> info    "{node} reconnected"
    on bad_data   -> warning "{refDes} out of range: {value} (limit {LIM-CPT01-HIGH})"
    on stale 2s   -> warning "{node} data stale"
`

// The template is instantiated per node: two nodes produce two independent
// alerts with distinct stable ids.
func TestTemplatePerNode(t *testing.T) {
	eng, reg, rec, _ := newTestEngine(t, tmplSrc, []string{"DAQ001", "DAQ002"}, nil)

	eng.NodeConnected("DAQ001")
	eng.NodeConnected("DAQ002")
	if n := len(rec.raises()); n != 0 {
		t.Fatalf("the FIRST connect must not raise a reconnect alert, got %d", n)
	}

	eng.NodeDisconnected("DAQ001")
	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	if raises[0].ID != "conn:DAQ001" || raises[0].Category != SeverityAlarm {
		t.Errorf("raise = %+v, want conn:DAQ001 alarm", raises[0])
	}
	if raises[0].Message != "DAQ001 disconnected" {
		t.Errorf("message = %q, want %q", raises[0].Message, "DAQ001 disconnected")
	}
	if reg.Active("conn:DAQ002") {
		t.Error("DAQ002 must be unaffected by DAQ001 disconnecting")
	}

	// A repeated disconnect is not a new event.
	eng.NodeDisconnected("DAQ001")
	if n := len(rec.raises()); n != 1 {
		t.Errorf("repeated disconnect raised again (%d raises)", n)
	}

	// Reconnect flips the same row to the info message.
	eng.NodeConnected("DAQ001")
	raises = rec.raises()
	last := raises[len(raises)-1]
	if last.ID != "conn:DAQ001" || last.Category != SeverityInfo || last.Message != "DAQ001 reconnected" {
		t.Errorf("reconnect raise = %+v", last)
	}
}

// bad_data alerts are keyed per channel and interpolate {refDes}, {value} and
// any channel placeholder from the live channel space.
func TestTemplateBadDataInterpolation(t *testing.T) {
	vals := space{"LIM-CPT01-HIGH": 450}
	eng, reg, rec, _ := newTestEngine(t, tmplSrc, []string{"DAQ001"}, vals)

	eng.BadData("CPT-01", "DAQ001", "high", 512.5)
	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	want := "CPT-01 out of range: 512.50 (limit 450)"
	if raises[0].Message != want {
		t.Errorf("message = %q, want %q", raises[0].Message, want)
	}
	if raises[0].ID != "bad:CPT-01" || raises[0].Category != SeverityWarning {
		t.Errorf("raise = %+v, want bad:CPT-01 warning", raises[0])
	}

	// Back in range resolves it — without acking it for the operator.
	eng.BadData("CPT-01", "DAQ001", "ok", 100)
	if reg.Active("bad:CPT-01") {
		t.Error("bad_data alert should go inactive when the channel is back in range")
	}
	if !reg.Resolved("bad:CPT-01") || !reg.Raised("bad:CPT-01") {
		t.Error("recovery must leave the row resolved-but-unacked, not acked")
	}
	if got := rec.acks(); len(got) != 0 {
		t.Errorf("acks = %v, want none: only a person acks", got)
	}
}

// Stale detection: a connected node that stops sending data for longer than the
// template's timeout raises, and resolves as soon as data resumes.
func TestTemplateStale(t *testing.T) {
	eng, reg, rec, clk := newTestEngine(t, tmplSrc, []string{"DAQ001"}, nil)
	if got := eng.StaleMs(); got != 2000 {
		t.Fatalf("StaleMs = %d, want 2000", got)
	}

	eng.NodeConnected("DAQ001")
	eng.NodeData("DAQ001")

	clk.advance(1900 * time.Millisecond)
	eng.Tick(space{})
	if reg.Active("stale:DAQ001") {
		t.Fatal("raised before the timeout elapsed")
	}

	clk.advance(200 * time.Millisecond)
	eng.Tick(space{})
	eng.Tick(space{}) // a second sweep must not raise a duplicate
	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want 1", len(raises))
	}
	if raises[0].ID != "stale:DAQ001" || raises[0].Message != "DAQ001 data stale" {
		t.Errorf("raise = %+v", raises[0])
	}

	eng.NodeData("DAQ001")
	if reg.Active("stale:DAQ001") {
		t.Error("stale alert should clear when data resumes")
	}

	// A disconnected node is not also reported as stale — the disconnect alert
	// already says there is no data.
	eng.NodeDisconnected("DAQ001")
	clk.advance(10 * time.Second)
	eng.Tick(space{})
	if reg.Active("stale:DAQ001") {
		t.Error("a disconnected node must not raise a stale alert on top of the disconnect")
	}
}

// A template that omits an event simply never raises it.
func TestTemplateMissingEventIsSilent(t *testing.T) {
	eng, reg, _, _ := newTestEngine(t, "template every_daqnode\n    on stale -> info \"{node} stale\"\n",
		[]string{"DAQ001"}, nil)
	eng.NodeConnected("DAQ001")
	eng.NodeDisconnected("DAQ001")
	if reg.Active("conn:DAQ001") {
		t.Error("no disconnect rule configured, so nothing should be raised")
	}
}

// With no template at all, node events are inert and rules still work.
func TestNoTemplate(t *testing.T) {
	eng, reg, _, _ := newTestEngine(t, ruleSrc, []string{"DAQ001"}, nil)
	eng.NodeConnected("DAQ001")
	eng.NodeDisconnected("DAQ001")
	eng.BadData("CPT-01", "DAQ001", "high", 5)
	if len(reg.Snapshot()) != 0 {
		t.Errorf("no template configured, but alerts were raised: %+v", reg.Snapshot())
	}
	if eng.StaleMs() != DefaultStaleMs {
		t.Errorf("StaleMs = %d, want the default %d", eng.StaleMs(), DefaultStaleMs)
	}
}

// ── Registry ─────────────────────────────────────────────────────────────────

func TestRegistrySnapshotAndPush(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)

	id := reg.Push(SeverityInfo, "Layout updated")
	reg.Raise("bad:PT-01", SeverityWarning, "PT-01 out of range")

	snap := reg.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %d entries, want 2", len(snap))
	}
	if snap[0].ID != id || snap[0].Message != "Layout updated" {
		t.Errorf("snapshot[0] = %+v", snap[0])
	}
	if snap[0].Timestamp == 0 {
		t.Error("records must carry a timestamp — the browser renders it")
	}

	// A repeated identical raise is not republished.
	reg.Raise("bad:PT-01", SeverityWarning, "PT-01 out of range")
	if n := len(rec.raises()); n != 2 {
		t.Errorf("publishes = %d, want 2 (the duplicate raise is suppressed)", n)
	}

	// Acking an unknown id is still relayed so stale browser rows can clear.
	if reg.Ack("nope") {
		t.Error("Ack of an unknown id should report false")
	}
	if got := rec.acks(); len(got) != 1 || got[0] != "nope" {
		t.Errorf("acks = %v, want the relayed ack", got)
	}
}

func TestRegistryResolveAndAckIsIdempotent(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	reg.Raise("x", SeverityAlarm, "boom")
	reg.ResolveAndAck("x")
	reg.ResolveAndAck("x")
	reg.ResolveAndAck("unknown")
	if got := rec.acks(); len(got) != 1 {
		t.Errorf("acks = %v, want exactly one clear", got)
	}
	if got, _ := reg.Get("x"); !got.Resolved || !got.Acked {
		t.Errorf("record = %+v, want both resolved and acked", got)
	}
}

func TestFormatFloat(t *testing.T) {
	cases := map[float64]string{
		450:    "450",
		512.5:  "512.50",
		-3.128: "-3.13",
		0:      "0",
	}
	for in, want := range cases {
		if got := FormatFloat(in); got != want {
			t.Errorf("FormatFloat(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestInterpolateUnknownPlaceholder(t *testing.T) {
	got := interpolate("{refDes}={value} {MISSING}", map[string]string{
		FieldRefDes: "PT-01", FieldValue: "5",
	}, space{})
	if got != "PT-01=5 ?" {
		t.Errorf("interpolate = %q", got)
	}
}

// ── Auto-generated sensor bounds alerts ───────────────────────────────────────

// f returns a pointer to a bound, the shape SensorChannel takes (an absent bound
// is nil, not zero — 0 is a perfectly good limit).
func f(v float64) *float64 { return &v }

// sensorEngine builds an engine with no .alert config at all, so nothing but the
// auto-generated sensor alerts can raise.
func sensorEngine(t *testing.T, sensors []SensorChannel) (*Engine, *Registry, *recorder, []error) {
	t.Helper()
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	var errs []error
	eng, err := NewEngine(EngineConfig{
		Config:   &Config{},
		Registry: reg,
		Sensors:  sensors,
		OnError:  func(e error) { errs = append(errs, e) },
	})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return eng, reg, rec, errs
}

// A sensor leaving its band raises an ALARM once, on the edge, quoting the value
// that tripped it and the band it left.
func TestSensorAlertEdgeTriggered(t *testing.T) {
	eng, reg, rec, _ := sensorEngine(t, []SensorChannel{
		{RefDes: "CPT-01", Min: f(0), Max: f(800)},
	})
	if got := eng.SensorAlerts(); len(got) != 1 || got[0] != "CPT-01" {
		t.Fatalf("SensorAlerts = %v, want [CPT-01]", got)
	}

	s := space{"CPT-01": 400}
	eng.Tick(s)
	if n := len(rec.raises()); n != 0 {
		t.Fatalf("in-band value raised %d alert(s)", n)
	}

	s["CPT-01"] = 812.5
	eng.Tick(s)
	s["CPT-01"] = 900 // still out of range: a moving value must not re-raise
	eng.Tick(s)
	eng.Tick(s)

	raises := rec.raises()
	if len(raises) != 1 {
		t.Fatalf("raises = %d, want exactly 1 (edge-triggered)", len(raises))
	}
	if raises[0].ID != "sensor:CPT-01" {
		t.Errorf("id = %q, want sensor:CPT-01", raises[0].ID)
	}
	if raises[0].Category != SeverityAlarm {
		t.Errorf("category = %q — out of range is an alarm", raises[0].Category)
	}
	if want := "CPT-01 out of range: 812.50 (valid 0 to 800)"; raises[0].Message != want {
		t.Errorf("message = %q, want %q", raises[0].Message, want)
	}
	if !reg.Active("sensor:CPT-01") {
		t.Error("the channel is out of range right now, so the alert is active")
	}
}

// The whole point of item 2: recovery does NOT take the red away.  A spike that
// came back down leaves the alert resolved-but-unacked, and only a person moves
// it to acked.
func TestSensorAlertLatchesUntilAcked(t *testing.T) {
	eng, reg, rec, _ := sensorEngine(t, []SensorChannel{
		{RefDes: "CPT-01", Min: f(0), Max: f(800)},
	})

	eng.Tick(space{"CPT-01": 900})
	eng.Tick(space{"CPT-01": 400}) // back in band, on its own

	if got := rec.acks(); len(got) != 0 {
		t.Fatalf("recovery acked the alert on the operator's behalf: %v", got)
	}
	if reg.Active("sensor:CPT-01") {
		t.Error("the condition recovered, so the alert is no longer active")
	}
	if !reg.Resolved("sensor:CPT-01") {
		t.Error("recovery must leave the alert resolved-but-unacked")
	}
	if !reg.Raised("sensor:CPT-01") {
		t.Error("a latched alert stays on the board (red) until a person acks it")
	}
	// The browser learns the new state as a normal `alert`, carrying resolved.
	raises := rec.raises()
	last := raises[len(raises)-1]
	if last.ID != "sensor:CPT-01" || !last.Resolved || last.Acked {
		t.Errorf("republished record = %+v, want resolved and un-acked", last)
	}

	if !reg.Ack("sensor:CPT-01") {
		t.Fatal("Ack should find the alert")
	}
	if reg.Raised("sensor:CPT-01") || reg.Resolved("sensor:CPT-01") {
		t.Error("an acked alert is neither raised nor resolved-but-unacked")
	}

	// And the channel leaving its band again raises the same row afresh.
	eng.Tick(space{"CPT-01": 950})
	if !reg.Active("sensor:CPT-01") {
		t.Error("a new out-of-range edge must re-raise the acked alert")
	}
}

// A swing from below the floor to above the ceiling between two ticks is a new
// fact, not a continuation, so it re-raises with the new value.
func TestSensorAlertDirectionChangeReRaises(t *testing.T) {
	eng, _, rec, _ := sensorEngine(t, []SensorChannel{
		{RefDes: "CPT-01", Min: f(10), Max: f(100)},
	})
	eng.Tick(space{"CPT-01": 5})
	eng.Tick(space{"CPT-01": 250})

	raises := rec.raises()
	if len(raises) != 2 {
		t.Fatalf("raises = %d, want 2 (low then high)", len(raises))
	}
	if want := "CPT-01 out of range: 5 (valid 10 to 100)"; raises[0].Message != want {
		t.Errorf("first message = %q, want %q", raises[0].Message, want)
	}
	if want := "CPT-01 out of range: 250 (valid 10 to 100)"; raises[1].Message != want {
		t.Errorf("second message = %q, want %q", raises[1].Message, want)
	}
}

// One-sided bands are normal, and the message says which side it is.
func TestSensorAlertOneSidedBands(t *testing.T) {
	eng, _, rec, _ := sensorEngine(t, []SensorChannel{
		{RefDes: "HI-ONLY", Max: f(100)},
		{RefDes: "LO-ONLY", Min: f(10)},
	})
	// Values far outside the bound that ISN'T configured must not trip.
	eng.Tick(space{"HI-ONLY": -5000, "LO-ONLY": 5000})
	if n := len(rec.raises()); n != 0 {
		t.Fatalf("an unconfigured side tripped: %+v", rec.raises())
	}

	eng.Tick(space{"HI-ONLY": 101, "LO-ONLY": 9})
	raises := rec.raises()
	if len(raises) != 2 {
		t.Fatalf("raises = %d, want 2", len(raises))
	}
	if want := "HI-ONLY out of range: 101 (valid max 100)"; raises[0].Message != want {
		t.Errorf("message = %q, want %q", raises[0].Message, want)
	}
	if want := "LO-ONLY out of range: 9 (valid min 10)"; raises[1].Message != want {
		t.Errorf("message = %q, want %q", raises[1].Message, want)
	}
}

// A channel with nothing usable to compare against generates no alert at all —
// an alert that can never fire is a row that lies.  An impossible band (min above
// max) is a config mistake, reported once and skipped.
func TestSensorAlertUnusableBounds(t *testing.T) {
	nan := math.NaN()
	inf := math.Inf(1)
	eng, _, rec, errs := sensorEngine(t, []SensorChannel{
		{RefDes: "NO-BOUNDS"},
		{RefDes: "NAN-BOUND", Min: &nan},
		{RefDes: "INF-BOUND", Max: &inf},
		{RefDes: "IMPOSSIBLE", Min: f(100), Max: f(10)},
		{RefDes: ""}, // no refDes at all
		{RefDes: "CPT-01", Max: f(800)},
		{RefDes: "CPT-01", Max: f(1)}, // duplicate: first one wins
	})
	if got := eng.SensorAlerts(); len(got) != 1 || got[0] != "CPT-01" {
		t.Fatalf("SensorAlerts = %v, want only [CPT-01]", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "IMPOSSIBLE") {
		t.Errorf("errors = %v, want one report about IMPOSSIBLE", errs)
	}

	eng.Tick(space{"NO-BOUNDS": 1e9, "NAN-BOUND": -1e9, "INF-BOUND": 1e9, "IMPOSSIBLE": 50, "CPT-01": 5})
	if n := len(rec.raises()); n != 0 {
		t.Fatalf("a channel without usable bounds raised: %+v", rec.raises())
	}
}

// A channel nobody has published yet (DAQ node not connected) is not out of
// range — and neither is a NaN reading, which is how the broker's bounds check
// treats it too.
func TestSensorAlertUnknownAndNaNValues(t *testing.T) {
	eng, _, rec, _ := sensorEngine(t, []SensorChannel{
		{RefDes: "CPT-01", Min: f(0), Max: f(800)},
	})
	eng.Tick(space{})                     // no value at all
	eng.Tick(space{"CPT-01": math.NaN()}) // unreadable
	eng.Tick(space{"OTHER": 1e9})         // a different channel
	if n := len(rec.raises()); n != 0 {
		t.Fatalf("raises = %d, want 0: %+v", n, rec.raises())
	}
	if got := rec.acks(); len(got) != 0 {
		t.Fatalf("acks = %v, want none", got)
	}
}

// With no sensors configured the sweep is inert, and a nil channel space (the
// standalone loop before anything publishes) must not panic.
func TestSensorAlertNoneConfigured(t *testing.T) {
	eng, _, rec, _ := sensorEngine(t, nil)
	if got := eng.SensorAlerts(); len(got) != 0 {
		t.Fatalf("SensorAlerts = %v, want none", got)
	}
	eng.Tick(nil)
	eng.Tick(space{"CPT-01": 1e9})
	if n := len(rec.raises()); n != 0 {
		t.Fatalf("raises = %d, want 0", n)
	}
}

// ── Recovery is not an ack ────────────────────────────────────────────────────

// Item 3 as-configured: with no `reconnect` line in the template, a link coming
// back resolves the disconnect alarm but leaves it on the board.  This is the
// shape config/alerts/alerts.alert now has.
const noReconnectSrc = `template every_daqnode
    on disconnect -> alarm   "{node} disconnected"
    on bad_data   -> warning "{refDes} out of range: {value}"
    on stale 2s   -> warning "{node} data stale"
`

func TestReconnectResolvesWithoutAcking(t *testing.T) {
	eng, reg, rec, _ := newTestEngine(t, noReconnectSrc, []string{"DAQ001"}, nil)

	eng.NodeConnected("DAQ001")
	eng.NodeDisconnected("DAQ001")
	if !reg.Active("conn:DAQ001") {
		t.Fatal("disconnect should raise the alarm")
	}

	eng.NodeConnected("DAQ001")
	if got := rec.acks(); len(got) != 0 {
		t.Errorf("reconnect acked the disconnect alarm for the operator: %v", got)
	}
	if reg.Active("conn:DAQ001") {
		t.Error("the link is back, so the alarm is no longer active")
	}
	if !reg.Resolved("conn:DAQ001") || !reg.Raised("conn:DAQ001") {
		t.Error("the disconnect alarm must stay on the board until an operator acks it")
	}
	// No reconnect event is declared, so no new row appears either.
	if n := len(reg.Snapshot()); n != 1 {
		t.Errorf("snapshot = %d rows, want the one disconnect row", n)
	}
}

// Stale recovery is the same story: data resuming does not acknowledge the gap.
func TestStaleRecoveryResolvesWithoutAcking(t *testing.T) {
	eng, reg, rec, clk := newTestEngine(t, noReconnectSrc, []string{"DAQ001"}, nil)
	eng.NodeConnected("DAQ001")
	eng.NodeData("DAQ001")
	clk.advance(3 * time.Second)
	eng.Tick(space{})
	if !reg.Active("stale:DAQ001") {
		t.Fatal("stale alert should be active")
	}

	eng.NodeData("DAQ001")
	if got := rec.acks(); len(got) != 0 {
		t.Errorf("data resuming acked the stale alert: %v", got)
	}
	if !reg.Resolved("stale:DAQ001") || !reg.Raised("stale:DAQ001") {
		t.Error("stale recovery must leave the alert resolved-but-unacked")
	}
}

// ── Registry states ───────────────────────────────────────────────────────────

// The three states are distinguishable, in that order, and a re-raise pulls a
// resolved row all the way back to active.
func TestRegistryThreeStates(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)

	reg.Raise("sensor:CPT-01", SeverityAlarm, "CPT-01 out of range: 900")
	if !reg.Active("sensor:CPT-01") || !reg.Raised("sensor:CPT-01") || reg.Resolved("sensor:CPT-01") {
		t.Fatal("a fresh raise is active and raised, not resolved")
	}

	reg.Resolve("sensor:CPT-01")
	if reg.Active("sensor:CPT-01") || !reg.Raised("sensor:CPT-01") || !reg.Resolved("sensor:CPT-01") {
		t.Fatal("after Resolve: resolved and still raised, no longer active")
	}
	got, ok := reg.Get("sensor:CPT-01")
	if !ok || !got.Resolved || got.Acked {
		t.Fatalf("record = %+v, want resolved and un-acked", got)
	}

	// Resolve is idempotent and never publishes an ack.
	reg.Resolve("sensor:CPT-01")
	reg.Resolve("unknown")
	if n := len(rec.raises()); n != 2 {
		t.Errorf("publishes = %d, want 2 (raise + one resolve)", n)
	}
	if got := rec.acks(); len(got) != 0 {
		t.Errorf("Resolve broadcast an ack: %v", got)
	}

	// A resolved row is re-published on a re-raise even with the same message:
	// the dedup must not swallow the return of the condition.
	reg.Raise("sensor:CPT-01", SeverityAlarm, "CPT-01 out of range: 900")
	if !reg.Active("sensor:CPT-01") {
		t.Error("re-raising a resolved alert must make it active again")
	}
	if n := len(rec.raises()); n != 3 {
		t.Errorf("publishes = %d, want 3", n)
	}

	reg.Ack("sensor:CPT-01")
	if reg.Active("sensor:CPT-01") || reg.Raised("sensor:CPT-01") || reg.Resolved("sensor:CPT-01") {
		t.Error("an acked alert is in none of the un-acked states")
	}
}

// A resolved-but-unacked row is never trimmed away: nobody has seen it yet.
func TestRegistryTrimKeepsResolvedUnacked(t *testing.T) {
	reg := NewRegistry()
	reg.maxLen = 2
	reg.Raise("a", SeverityAlarm, "a")
	reg.Resolve("a")
	reg.Raise("b", SeverityAlarm, "b")
	reg.ResolveAndAck("b") // resolved AND acked — droppable
	reg.Raise("c", SeverityAlarm, "c")
	reg.Raise("d", SeverityAlarm, "d")

	var ids []string
	for _, r := range reg.Snapshot() {
		ids = append(ids, r.ID)
	}
	if len(ids) != 3 || ids[0] != "a" {
		t.Errorf("snapshot ids = %v, want the acked row dropped and a kept", ids)
	}
}
