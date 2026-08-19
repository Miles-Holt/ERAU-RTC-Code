// Package alerts is the control node's single source of operator alerts.
//
// Every alert the browser shows — rule alerts from `.alert` files, per-daqNode
// template alerts (disconnect/reconnect/bad_data/stale), and one-off server
// notices such as "layout saved" or a rejected state-machine target — is created
// here, in the Registry.  The browser only renders what the registry publishes;
// it never invents alerts of its own (the one exception is local link state,
// which the server cannot observe).
//
// The registry is keyed by a STABLE id, so a repeated condition updates the
// existing row instead of stacking duplicates:
//
//	rule alerts       "rule:<NAME>"
//	bad data          "bad:<refDes>"
//	sensor bounds     "sensor:<refDes>"
//	connect state     "conn:<node>"
//	stale data        "stale:<node>"
//	one-off notices   "notice:<n>"
package alerts

import (
	"fmt"
	"sync"
	"time"
)

// Severity values accepted in `.alert` files and on the wire.
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityAlarm   = "alarm"
)

// ValidSeverity reports whether s is one of info|warning|alarm.
func ValidSeverity(s string) bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityAlarm:
		return true
	}
	return false
}

// Record is one alert row.  The JSON tags ARE the browser wire contract
// (WebClient/js/alerts.js ingestAlert reads id/category/message/timestamp/acked);
// webclient/protocol_test.go pins them.
//
// Acked and Resolved are two independent facts, and keeping them apart is the
// whole point of the pair:
//
//	Acked=false Resolved=false  ACTIVE — the condition is true right now
//	Acked=false Resolved=true   RESOLVED BUT UNACKED — the condition recovered on
//	                            its own, but nobody has seen it yet, so the row
//	                            (and the object it belongs to) stays red
//	Acked=true                  ACKNOWLEDGED — a person has seen it
//
// Only a person moves a row into the acked column.  The server does not ack on
// the operator's behalf when a condition recovers: a pressure spike that came
// back down still has to be looked at, because the thing worth knowing is that
// it happened.  The one deliberate exception is ResolveAndAck, for a rule whose
// author opted out of latching — see its comment.
type Record struct {
	ID        string `json:"id"`
	Category  string `json:"category"` // "info" | "warning" | "alarm"
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"` // Unix ms
	Acked     bool   `json:"acked"`
	Resolved  bool   `json:"resolved"` // condition no longer true; says nothing about Acked

	// What this alert is ABOUT, so a front-panel object can tell whether an
	// alert concerns it. Without this the browser can only guess from the id,
	// which works for the auto-generated ones (`sensor:<refDes>`) and not at
	// all for a rule alert — an object would sit grey while a rule alarm about
	// its own channel was raised on the board.
	Channels []string `json:"channels,omitempty"` // channel refDes this concerns
	Node     string   `json:"node,omitempty"`     // daqNode refDes, for node-level alerts
}

// Sink publishes registry changes to connected browsers.  The webclient server
// implements it (it owns the JSON builders for the alert / alert_acked
// messages).  Both methods are called with the registry lock released, so a sink
// may call back into the registry without deadlocking, and must not block.
type Sink interface {
	PublishAlert(rec Record)
	PublishAlertAcked(id string)
}

// Registry holds every live alert.  Safe for concurrent use: the engine tick,
// the broker goroutine (bad data), DAQ client goroutines (connect/disconnect)
// and browser control connections (ack) all reach it.
type Registry struct {
	mu     sync.Mutex
	byID   map[string]*Record
	order  []string // insertion order of ids, so the browser list is stable
	seq    int64    // counter for generated one-off ids
	sink   Sink
	nowFn  func() time.Time
	maxLen int
}

// NewRegistry creates an empty registry.  Attach a Sink with SetSink to have
// changes broadcast; without one the registry still tracks state (used in tests).
func NewRegistry() *Registry {
	return &Registry{
		byID:   make(map[string]*Record),
		nowFn:  time.Now,
		maxLen: 500,
	}
}

// SetSink attaches the publisher.  Typically called once at wiring time.
func (r *Registry) SetSink(s Sink) {
	r.mu.Lock()
	r.sink = s
	r.mu.Unlock()
}

// SetClock overrides the timestamp source (tests).
func (r *Registry) SetClock(fn func() time.Time) {
	r.mu.Lock()
	r.nowFn = fn
	r.mu.Unlock()
}

func (r *Registry) now() int64 { return r.nowFn().UnixMilli() }

// Raise creates or refreshes the alert with the given stable id and publishes
// it.  A raise always un-acks AND un-resolves the row: the condition is (again)
// true, so the operator must acknowledge it again.  Re-raising the same message
// onto a row that is already active is a no-op, so a condition that stays true
// does not spam the browser.
// Raise records an alert with no channel or node attribution — for alerts that
// genuinely concern nothing on the panel. Prefer RaiseFor.
func (r *Registry) Raise(id, severity, message string) {
	r.RaiseFor(id, severity, message, nil, "")
}

// RaiseFor records an alert and says what it is about. `channels` are the
// channel refDes it concerns (a rule may reference several); `node` is set
// instead for node-level alerts, where every channel owned by that node is
// affected and listing them would be both long and redundant.
func (r *Registry) RaiseFor(id, severity, message string, channels []string, node string) {
	if !ValidSeverity(severity) {
		severity = SeverityWarning
	}
	r.mu.Lock()
	existing, ok := r.byID[id]
	if ok && existing.Message == message && existing.Category == severity &&
		!existing.Acked && !existing.Resolved {
		r.mu.Unlock()
		return // already showing exactly this — nothing changed
	}
	rec := Record{
		ID:        id,
		Category:  severity,
		Message:   message,
		Timestamp: r.now(),
		Acked:     false,
		Resolved:  false,
		Channels:  channels,
		Node:      node,
	}
	r.store(id, rec)
	sink := r.sink
	r.mu.Unlock()

	if sink != nil {
		sink.PublishAlert(rec)
	}
}

// Resolve records that an alert's condition is no longer true — a channel back
// inside its bounds, data resuming, a node reconnecting.  It marks the row
// RESOLVED and leaves Acked alone, which is the difference that makes latching
// work: the row stays on the board, and the object stays red, until a person
// acknowledges it.  This is deliberately not an ack.  Its predecessor (Clear)
// set Acked itself, which meant the server quietly acknowledged on the
// operator's behalf the instant a value came back into band.
//
// The updated record is republished as a normal `alert`, so the browser learns
// the new resolved state — not through alert_acked, which would read as though a
// person had acted.  Resolving an unknown or already-resolved id does nothing.
func (r *Registry) Resolve(id string) {
	r.mu.Lock()
	rec, ok := r.byID[id]
	if !ok || rec.Resolved {
		r.mu.Unlock()
		return
	}
	rec.Resolved = true
	out := *rec
	sink := r.sink
	r.mu.Unlock()

	if sink != nil {
		sink.PublishAlert(out)
	}
}

// ResolveAndAck resolves an alert AND acks it on the operator's behalf.  It
// exists for exactly one case: a rule whose author left `latch` off, which the
// DSL documents as "the alert clears itself when the value comes back".  That is
// the author asking for the row to go away by itself, so the server may act for
// them.  Nothing else may use it — every other recovery path calls Resolve and
// waits for a person.
//
// It broadcasts alert_acked, so the row stops demanding attention.  Acking an
// unknown or already-acked id does nothing.
func (r *Registry) ResolveAndAck(id string) {
	r.mu.Lock()
	rec, ok := r.byID[id]
	if !ok || rec.Acked {
		r.mu.Unlock()
		return
	}
	rec.Resolved = true
	rec.Acked = true
	sink := r.sink
	r.mu.Unlock()

	if sink != nil {
		sink.PublishAlertAcked(id)
	}
}

// Ack marks an alert acknowledged by an operator and broadcasts alert_acked.
// It returns false when the id is unknown (the caller may still want to relay
// the ack so a client that has a stale row locally can clear it).
func (r *Registry) Ack(id string) bool {
	r.mu.Lock()
	rec, ok := r.byID[id]
	if ok {
		rec.Acked = true
	}
	sink := r.sink
	r.mu.Unlock()

	if sink != nil {
		sink.PublishAlertAcked(id)
	}
	return ok
}

// Push raises a one-off notice with a generated id — used for events that have
// no natural key (a saved layout, a rejected operator request).  Returns the id.
func (r *Registry) Push(severity, message string) string {
	r.mu.Lock()
	r.seq++
	id := fmt.Sprintf("notice:%d", r.seq)
	r.mu.Unlock()
	r.Raise(id, severity, message)
	return id
}

// Active reports whether this alert's condition is true RIGHT NOW: raised,
// neither resolved nor acked.  It is not "is there still something red on the
// operator's screen" — that is Raised.
func (r *Registry) Active(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	return ok && !rec.Acked && !rec.Resolved
}

// Raised reports whether this alert is still on the board un-acknowledged,
// whether or not its condition has since recovered.  This is the alarm axis the
// front panel colours from: a latched alert whose spike came back down on its
// own is still Raised, and still red, until a person acks it.
func (r *Registry) Raised(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	return ok && !rec.Acked
}

// Resolved reports whether this alert recovered but is still waiting for a
// person: the middle of the three states.
func (r *Registry) Resolved(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	return ok && rec.Resolved && !rec.Acked
}

// Get returns a copy of one record.
func (r *Registry) Get(id string) (Record, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	if !ok {
		return Record{}, false
	}
	return *rec, true
}

// Snapshot returns every known alert in insertion order.
func (r *Registry) Snapshot() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Record, 0, len(r.order))
	for _, id := range r.order {
		if rec, ok := r.byID[id]; ok {
			out = append(out, *rec)
		}
	}
	return out
}

// store inserts or replaces a record.  Caller holds the lock.
func (r *Registry) store(id string, rec Record) {
	if _, ok := r.byID[id]; !ok {
		r.order = append(r.order, id)
	}
	cp := rec
	r.byID[id] = &cp
	r.trim()
}

// trim bounds the list so a long run cannot grow it without limit.  Only acked
// rows are dropped, oldest first: an un-acked alert is never silently removed —
// a resolved-but-unacked one included, since no person has seen it yet.
func (r *Registry) trim() {
	if len(r.order) <= r.maxLen {
		return
	}
	kept := r.order[:0]
	drop := len(r.order) - r.maxLen
	for _, id := range r.order {
		rec, ok := r.byID[id]
		if !ok {
			continue
		}
		if drop > 0 && rec.Acked {
			delete(r.byID, id)
			drop--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}
