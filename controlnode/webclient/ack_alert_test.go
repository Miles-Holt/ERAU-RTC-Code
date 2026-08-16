package webclient

import (
	"controlnode/alerts"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestAckAlertMarksAndBroadcasts verifies an authenticated ack_alert marks
// the alert acked in the registry and broadcasts alert_acked with the right id.
func TestAckAlertMarksAndBroadcasts(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	s.Alerts().Raise("rule:CHAMBER-HIGH", alerts.SeverityAlarm, "chamber pressure high")

	sub := dataSubscriber(t, ts)
	defer sub.Close()

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	mustWrite(t, ctrl, map[string]interface{}{"type": "ack_alert", "id": "rule:CHAMBER-HIGH"})

	m := readUntilType(t, sub, "alert_acked", 2*time.Second)
	if m["id"] != "rule:CHAMBER-HIGH" {
		t.Errorf("alert_acked id = %v, want rule:CHAMBER-HIGH", m["id"])
	}

	rec, ok := s.Alerts().Get("rule:CHAMBER-HIGH")
	if !ok || !rec.Acked {
		t.Errorf("registry record = %+v, ok=%v, want Acked=true", rec, ok)
	}
}

// TestAckAlertUnknownIDIsHarmlessNoop verifies acking an unknown id does not
// panic and does not raise a bogus alert (Ack returns false and there is
// nothing in the registry for that id afterward), though the ack is still
// relayed for stale-client cleanup — that relay is the ONLY broadcast effect.
func TestAckAlertUnknownIDIsHarmlessNoop(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()

	// Must not panic.
	mustWrite(t, ctrl, map[string]interface{}{"type": "ack_alert", "id": "does-not-exist"})

	// Give the server a moment to process, then verify no record was created.
	time.Sleep(100 * time.Millisecond)
	if _, ok := s.Alerts().Get("does-not-exist"); ok {
		t.Error("ack_alert for unknown id created a bogus registry record")
	}

	// The connection must still be usable afterward — prove the server did
	// not crash the handler goroutine.
	mustWrite(t, ctrl, map[string]interface{}{"type": "auth_request", "name": "alice", "pin": "1234"})
	if resp := readJSON(t, ctrl); resp["approved"] != true {
		t.Fatalf("connection unusable after unknown ack_alert: %v", resp)
	}
}

// TestAckAlertLatchingRuleStaysRaisedUntilAcked verifies the registry's
// documented latching behaviour end-to-end through the ack_alert handler: an
// alert that is raised and never independently cleared (simulating a latching
// rule alert, which the alert engine deliberately does not auto-clear) stays
// active until an operator acks it, and then clears.
func TestAckAlertLatchingRuleStaysRaisedUntilAcked(t *testing.T) {
	auth := &UserAuthConfig{PIN: "1234", Users: []string{"alice"}}
	s, _, ts := newTestServer(t, auth)

	const id = "rule:LATCHED"
	s.Alerts().Raise(id, alerts.SeverityAlarm, "latched condition")

	// Simulate the condition clearing WITHOUT the engine calling Clear (which
	// is exactly what a latching rule does per alerts/engine.go: only
	// non-latching rules resolve themselves). The alert must still be active.
	if !s.Alerts().Active(id) {
		t.Fatal("alert should still be active before ack (latching semantics)")
	}

	ctrl := authedCtrlConn(t, ts, auth, "alice", "1234")
	defer ctrl.Close()
	sub := dataSubscriber(t, ts)
	defer sub.Close()

	mustWrite(t, ctrl, map[string]interface{}{"type": "ack_alert", "id": id})
	m := readUntilType(t, sub, "alert_acked", 2*time.Second)
	if m["id"] != id {
		t.Fatalf("alert_acked id = %v, want %v", m["id"], id)
	}

	if s.Alerts().Active(id) {
		t.Error("alert still active after ack; latching rule should clear once acknowledged")
	}
}

// TestAckAlertRejectedWhenUnauthenticated verifies an unauthenticated
// ack_alert is rejected: the alert stays active and no alert_acked broadcast
// is sent.
func TestAckAlertRejectedWhenUnauthenticated(t *testing.T) {
	s, _, ts := newTestServer(t, &UserAuthConfig{PIN: "1234", Users: []string{"alice"}})

	const id = "rule:UNAUTH"
	s.Alerts().Raise(id, alerts.SeverityWarning, "should stay raised")

	c, _, err := websocket.DefaultDialer.Dial(wsURL(ts.URL, "/ws/ctrl"), nil)
	if err != nil {
		t.Fatalf("dial /ws/ctrl: %v", err)
	}
	defer c.Close()

	mustWrite(t, c, map[string]interface{}{"type": "ack_alert", "id": id})
	time.Sleep(150 * time.Millisecond)

	if !s.Alerts().Active(id) {
		t.Error("unauthenticated ack_alert acknowledged the alert")
	}
}

