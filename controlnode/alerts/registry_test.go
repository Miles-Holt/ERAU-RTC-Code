package alerts

import "testing"

// ── Suppress / Unsuppress ────────────────────────────────────────────────────

// Suppress on a known id sets Suppressed+SuppressedAt, publishes the updated
// record as a normal `alert` (via PublishAlert, not PublishAlertAcked — see
// Suppress's doc: suppressing is not an acknowledgement), and returns true.
func TestSuppressKnownID(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	clk := newClock()
	reg.SetClock(clk.now)

	reg.Raise("rule:X", SeverityAlarm, "boom")
	clk.advance(5000)

	if !reg.Suppress("rule:X") {
		t.Fatal("Suppress on a known id should return true")
	}

	got, ok := reg.Get("rule:X")
	if !ok {
		t.Fatal("record vanished")
	}
	if !got.Suppressed {
		t.Error("Suppressed should be true")
	}
	if got.SuppressedAt == 0 {
		t.Error("SuppressedAt should be set")
	}

	raises := rec.raises()
	if len(raises) == 0 {
		t.Fatal("Suppress should publish via PublishAlert")
	}
	last := raises[len(raises)-1]
	if !last.Suppressed {
		t.Errorf("published record Suppressed = false, want true: %+v", last)
	}
}

// Unsuppress reverses Suppress: clears Suppressed+SuppressedAt and publishes.
func TestUnsuppressKnownID(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)

	reg.Raise("rule:X", SeverityAlarm, "boom")
	reg.Suppress("rule:X")

	if !reg.Unsuppress("rule:X") {
		t.Fatal("Unsuppress on a known id should return true")
	}

	got, ok := reg.Get("rule:X")
	if !ok {
		t.Fatal("record vanished")
	}
	if got.Suppressed {
		t.Error("Suppressed should be false after Unsuppress")
	}
	if got.SuppressedAt != 0 {
		t.Errorf("SuppressedAt = %d, want 0 after Unsuppress", got.SuppressedAt)
	}

	last := rec.raises()[len(rec.raises())-1]
	if last.Suppressed {
		t.Errorf("published record Suppressed = true, want false: %+v", last)
	}
}

// Suppress/Unsuppress on an unknown id must not panic, must publish nothing,
// and must return false — there is no locally-held-row use case for these two
// (unlike ack_alert's deliberate relay of unknown ids), so unlike Ack there is
// nothing worth relaying.
func TestSuppressUnsuppressUnknownID(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)

	if reg.Suppress("nope") {
		t.Error("Suppress on unknown id should return false")
	}
	if reg.Unsuppress("nope") {
		t.Error("Unsuppress on unknown id should return false")
	}
	if len(rec.raises()) != 0 {
		t.Errorf("unknown-id Suppress/Unsuppress published %d records, want 0", len(rec.raises()))
	}
}

// ── The critical one: suppression must survive a rule re-trigger ────────────

// This exercises exactly the sequence evalRules produces for a rule that
// fires, clears, and fires again (engine.go's RaiseFor → Resolve/
// ResolveAndAck → RaiseFor). RaiseFor's no-op short-circuit requires
// !Resolved, and Resolve always sets Resolved=true first, so the second
// RaiseFor is guaranteed to fall through to the "build a fresh Record" path
// — the exact path whose Suppressed carry-forward this test is pinning.
func TestSuppressSurvivesRuleRetrigger(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)
	clk := newClock()
	reg.SetClock(clk.now)

	// First raise, then suppress it.
	reg.RaiseFor("rule:X", SeverityAlarm, "first message", []string{"CPT-01"}, "", "", nil, nil)
	if !reg.Suppress("rule:X") {
		t.Fatal("Suppress should have found the alert")
	}

	// Simulate the condition clearing (evalRules' !on && prev branch) — a
	// latching rule calls Resolve, a non-latching one calls ResolveAndAck.
	// Either way Resolved ends up true before the rule can raise again.
	reg.Resolve("rule:X")

	clk.advance(1000)

	// Re-trigger: same id, DIFFERENT message (a live-value-interpolated
	// message that changed on this raise, as a real re-trigger would have).
	reg.RaiseFor("rule:X", SeverityAlarm, "second message, different value", []string{"CPT-01"}, "", "", nil, nil)

	got, ok := reg.Get("rule:X")
	if !ok {
		t.Fatal("record vanished across re-trigger")
	}
	if !got.Suppressed {
		t.Fatal("Suppressed was lost across a rule re-trigger — the whole feature is broken")
	}
	if got.SuppressedAt == 0 {
		t.Error("SuppressedAt was lost across a rule re-trigger")
	}

	// Suppression hides the row; it does not freeze its content. The record
	// must reflect the SECOND raise's facts.
	if got.Message != "second message, different value" {
		t.Errorf("message = %q, want the second raise's message", got.Message)
	}
	if got.Resolved {
		t.Error("Resolved should have been reset to false by the second RaiseFor")
	}
	if got.Acked {
		t.Error("Acked should be false after a fresh raise")
	}
}

// Unsuppress after a survived re-trigger shows the CURRENT (post-re-raise)
// state, not a stale snapshot pinned at suppression time.
func TestUnsuppressAfterRetriggerShowsCurrentState(t *testing.T) {
	rec := &recorder{}
	reg := NewRegistry()
	reg.SetSink(rec)

	reg.RaiseFor("rule:X", SeverityAlarm, "first message", nil, "", "", nil, nil)
	reg.Suppress("rule:X")
	reg.Resolve("rule:X")
	reg.RaiseFor("rule:X", SeverityWarning, "second message", nil, "", "", nil, nil)

	if !reg.Unsuppress("rule:X") {
		t.Fatal("Unsuppress should have found the alert")
	}

	got, ok := reg.Get("rule:X")
	if !ok {
		t.Fatal("record vanished")
	}
	if got.Suppressed {
		t.Error("Suppressed should be false after Unsuppress")
	}
	if got.Message != "second message" {
		t.Errorf("message = %q, want the post-re-raise message (current state, not stale)", got.Message)
	}
	if got.Category != SeverityWarning {
		t.Errorf("category = %q, want the post-re-raise severity", got.Category)
	}
}

// A record's Description round-trips through RaiseFor's description
// parameter unchanged (item 07a's wire path, at the registry layer).
func TestRaiseForDescriptionRoundTrips(t *testing.T) {
	reg := NewRegistry()
	reg.RaiseFor("rule:X", SeverityAlarm, "short message", nil, "", "the long form, unabridged", nil, nil)

	got, ok := reg.Get("rule:X")
	if !ok {
		t.Fatal("record vanished")
	}
	if got.Description != "the long form, unabridged" {
		t.Errorf("description = %q, want %q", got.Description, "the long form, unabridged")
	}

	// And a subsequent raise with a different description overwrites it (no
	// stale carry-forward the way Suppressed deliberately does).
	reg.RaiseFor("rule:X", SeverityAlarm, "short message v2", nil, "", "a different long form", nil, nil)
	got, ok = reg.Get("rule:X")
	if !ok {
		t.Fatal("record vanished")
	}
	if got.Description != "a different long form" {
		t.Errorf("description = %q, want the latest raise's description", got.Description)
	}
}
