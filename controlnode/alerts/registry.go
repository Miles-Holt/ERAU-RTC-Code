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
type Record struct {
	ID        string `json:"id"`
	Category  string `json:"category"` // "info" | "warning" | "alarm"
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"` // Unix ms
	Acked     bool   `json:"acked"`
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
// it.  A raise always un-acks the row: the condition is (again) true, so the
// operator must acknowledge it again.  Re-raising with an unchanged message and
// an unchanged un-acked state is a no-op, so a condition that stays true does
// not spam the browser.
func (r *Registry) Raise(id, severity, message string) {
	if !ValidSeverity(severity) {
		severity = SeverityWarning
	}
	r.mu.Lock()
	existing, ok := r.byID[id]
	if ok && existing.Message == message && existing.Category == severity && !existing.Acked {
		r.mu.Unlock()
		return // already showing exactly this — nothing changed
	}
	rec := Record{
		ID:        id,
		Category:  severity,
		Message:   message,
		Timestamp: r.now(),
		Acked:     false,
	}
	r.store(id, rec)
	sink := r.sink
	r.mu.Unlock()

	if sink != nil {
		sink.PublishAlert(rec)
	}
}

// Clear resolves an alert that is no longer true (a non-latching rule going
// false, a node reconnecting, data resuming).  The row stays in the list as an
// acknowledged entry so the operator can still see that it happened; the browser
// renders acked rows as resolved.  Clearing an unknown or already-acked id does
// nothing.
func (r *Registry) Clear(id string) {
	r.mu.Lock()
	rec, ok := r.byID[id]
	if !ok || rec.Acked {
		r.mu.Unlock()
		return
	}
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

// Active reports whether an alert with this id is currently raised and un-acked.
func (r *Registry) Active(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	return ok && !rec.Acked
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
// rows are dropped, oldest first: an un-acked alert is never silently removed.
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
