package alerts

import (
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
	if !reg.Active("rule:CHAMBER-HIGH") {
		t.Fatal("latching alert should still be active after the condition cleared")
	}

	if !reg.Ack("rule:CHAMBER-HIGH") {
		t.Fatal("Ack should find the alert")
	}
	if reg.Active("rule:CHAMBER-HIGH") {
		t.Error("acked alert should not be active")
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

	// Back in range resolves it.
	eng.BadData("CPT-01", "DAQ001", "ok", 100)
	if reg.Active("bad:CPT-01") {
		t.Error("bad_data alert should clear when the channel is back in range")
	}
	if got := rec.acks(); len(got) != 1 || got[0] != "bad:CPT-01" {
		t.Errorf("acks = %v", got)
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

func TestRegistryClearIsIdempotent(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	reg.Raise("x", SeverityAlarm, "boom")
	reg.Clear("x")
	reg.Clear("x")
	reg.Clear("unknown")
	if got := rec.acks(); len(got) != 1 {
		t.Errorf("acks = %v, want exactly one clear", got)
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
