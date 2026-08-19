package webclient

import (
	"controlnode/alerts"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestSuppressAlertMarksAndBroadcasts verifies an authenticated suppress_alert
// marks the alert suppressed in the registry and broadcasts a normal `alert`
// (not alert_acked — suppressing is not an acknowledgement) carrying
// suppressed: true.
func TestSuppressAlertMarksAndBroadcasts(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	s.Alerts().Raise("rule:CHAMBER-HIGH", alerts.SeverityAlarm, "chamber pressure high")

	sub := dataSubscriber(t, ts)
	defer sub.Close()

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	mustWrite(t, ctrl, map[string]interface{}{"type": "suppress_alert", "id": "rule:CHAMBER-HIGH"})

	m := readUntilType(t, sub, "alert", 10*time.Second)
	if m["id"] != "rule:CHAMBER-HIGH" {
		t.Errorf("alert id = %v, want rule:CHAMBER-HIGH", m["id"])
	}
	if m["suppressed"] != true {
		t.Errorf("alert suppressed = %v, want true", m["suppressed"])
	}

	rec, ok := s.Alerts().Get("rule:CHAMBER-HIGH")
	if !ok || !rec.Suppressed {
		t.Errorf("registry record = %+v, ok=%v, want Suppressed=true", rec, ok)
	}
}

// TestUnsuppressAlertReversesIt verifies unsuppress_alert clears Suppressed
// and broadcasts a normal `alert` with suppressed: false.
func TestUnsuppressAlertReversesIt(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	s.Alerts().Raise("rule:CHAMBER-HIGH", alerts.SeverityAlarm, "chamber pressure high")
	s.Alerts().Suppress("rule:CHAMBER-HIGH")

	sub := dataSubscriber(t, ts)
	defer sub.Close()

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	mustWrite(t, ctrl, map[string]interface{}{"type": "unsuppress_alert", "id": "rule:CHAMBER-HIGH"})

	m := readUntilType(t, sub, "alert", 10*time.Second)
	if m["id"] != "rule:CHAMBER-HIGH" {
		t.Errorf("alert id = %v, want rule:CHAMBER-HIGH", m["id"])
	}
	if m["suppressed"] != false {
		t.Errorf("alert suppressed = %v, want false", m["suppressed"])
	}

	rec, ok := s.Alerts().Get("rule:CHAMBER-HIGH")
	if !ok || rec.Suppressed {
		t.Errorf("registry record = %+v, ok=%v, want Suppressed=false", rec, ok)
	}
}

// TestSuppressAlertRejectedWhenUnauthenticated mirrors
// TestAckAlertRejectedWhenUnauthenticated: an unauthenticated suppress_alert
// must not suppress the alert.
func TestSuppressAlertRejectedWhenUnauthenticated(t *testing.T) {
	s, _, ts := newTestServer(t, &UserAuthConfig{PIN: "1234", Users: []string{"alice"}})

	const id = "rule:UNAUTH"
	s.Alerts().Raise(id, alerts.SeverityWarning, "should stay unsuppressed")

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	defer c.Close()

	mustWrite(t, c, map[string]interface{}{"type": "suppress_alert", "id": id})
	time.Sleep(150 * time.Millisecond)

	rec, ok := s.Alerts().Get(id)
	if !ok || rec.Suppressed {
		t.Errorf("unauthenticated suppress_alert suppressed the alert: %+v, ok=%v", rec, ok)
	}
}

// TestSuppressAlertUnknownIDIsHarmlessNoop mirrors
// TestAckAlertUnknownIDIsHarmlessNoop, but — unlike ack_alert — asserts NO
// alert broadcast happens for the unknown id: there is no stale-client
// relay use case for suppress/unsuppress (see the ServeWsCtrl comment).
func TestSuppressAlertUnknownIDIsHarmlessNoop(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	sub := dataSubscriber(t, ts)
	defer sub.Close()

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	// Must not panic.
	mustWrite(t, ctrl, map[string]interface{}{"type": "suppress_alert", "id": "does-not-exist"})

	assertNoAlertBroadcastForID(t, sub, "does-not-exist", 300*time.Millisecond)

	if _, ok := s.Alerts().Get("does-not-exist"); ok {
		t.Error("suppress_alert for unknown id created a bogus registry record")
	}

	// The connection must still be usable afterward.
	mustWrite(t, ctrl, map[string]interface{}{"type": "auth_request", "name": "alice", "pin": "1234"})
	if resp := readJSON(t, ctrl); resp["approved"] != true {
		t.Fatalf("connection unusable after unknown suppress_alert: %v", resp)
	}
}

// TestSuppressSurvivesEngineStyleRetrigger is the Go-level end-to-end
// counterpart of registry_test.go's TestSuppressSurvivesRuleRetrigger: it
// drives the registry through the ctrl-message path (suppress_alert) and then
// simulates a real rule re-trigger the way the alert engine's evalRules
// actually produces one (RaiseFor -> Resolve -> RaiseFor, called directly on
// s.Alerts() the way TestAckAlertLatchingRuleStaysRaisedUntilAcked calls
// s.Alerts().Raise directly), asserting the record the browser will read is
// STILL suppressed afterward. Worth checking at this layer too, not only
// registry_test.go's unit-level one, since this is the guarantee the browser
// side actually relies on.
func TestSuppressSurvivesEngineStyleRetrigger(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	const id = "rule:X"
	s.Alerts().RaiseFor(id, alerts.SeverityAlarm, "first message", []string{"CPT-01"}, "", "", nil, nil)

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()
	sub := dataSubscriber(t, ts)
	defer sub.Close()

	mustWrite(t, ctrl, map[string]interface{}{"type": "suppress_alert", "id": id})
	readUntilType(t, sub, "alert", 10*time.Second)

	// Condition clears (evalRules' !on && prev branch resolves it) ...
	s.Alerts().Resolve(id)
	// ... and re-triggers with a different, live-value-interpolated message.
	s.Alerts().RaiseFor(id, alerts.SeverityAlarm, "second message, different value", []string{"CPT-01"}, "", "", nil, nil)

	rec, ok := s.Alerts().Get(id)
	if !ok {
		t.Fatal("record vanished across re-trigger")
	}
	if !rec.Suppressed {
		t.Fatal("Suppressed was lost across a rule re-trigger")
	}
	if rec.Message != "second message, different value" {
		t.Errorf("message = %q, want the second raise's message", rec.Message)
	}
}

// assertNoAlertBroadcastForID drains c for the given window and fails if an
// `alert` message for id arrives — used where the point is that nothing was
// broadcast, not that something specific was.
func assertNoAlertBroadcastForID(t *testing.T, c *websocket.Conn, id string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		c.SetReadDeadline(time.Now().Add(remaining))
		_, raw, err := c.ReadMessage()
		if err != nil {
			return // deadline hit (or connection closed) — nothing arrived, as expected
		}
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m["type"] == "alert" && m["id"] == id {
			t.Fatalf("unexpected alert broadcast for unknown id %q: %v", id, m)
		}
	}
}
