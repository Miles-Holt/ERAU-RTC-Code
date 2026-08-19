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

	// Suppressed is an operator's standing decision to silence a known,
	// understood condition — orthogonal to Acked. Acked means "a person has
	// seen this once"; Suppressed means "stop showing this at all, until
	// someone says otherwise". The browser treats Suppressed like Acked for
	// front-panel "alarmed" glow (a client-side decision, not this package's),
	// but the two facts are tracked and can be toggled independently: a
	// suppressed alert can still be unacked underneath, and acking it does not
	// touch Suppressed.
	Suppressed   bool  `json:"suppressed"`
	SuppressedAt int64 `json:"suppressedAt,omitempty"` // Unix ms; 0 when not suppressed

	// What this alert is ABOUT, so a front-panel object can tell whether an
	// alert concerns it. Without this the browser can only guess from the id,
	// which works for the auto-generated ones (`sensor:<refDes>`) and not at
	// all for a rule alert — an object would sit grey while a rule alarm about
	// its own channel was raised on the board.
	Channels []string `json:"channels,omitempty"` // channel refDes this concerns
	Node     string   `json:"node,omitempty"`     // daqNode refDes, for node-level alerts

	// Description is the alert definition's optional long form (`describe
	// "…"` in the `.alert` DSL, item 07a), interpolated the same way Message
	// is. Only rule alerts can carry one — template alerts (node
	// connect/disconnect, stale, bad_data) and auto-generated sensor-bounds
	// alerts have no `describe` in their vocabulary, so this is "" for them.
	Description string `json:"description,omitempty"`

	// PlotChannels overrides which channels the alarm panel charts (item 09) —
	// empty means "no override", and the browser falls back to Channels (see
	// Channels' own doc, and item 07's already-shipped client behavior).
	PlotChannels []string `json:"plotChannels,omitempty"`
	// Lines are optional reference lines the alarm panel draws behind its
	// chart (item 09) — see webclient's Line type for the wire shape (this
	// package doesn't need a JSON-tagged type of its own since Record embeds
	// directly into the browser message the same way Channels/Description do).
	Lines []Line `json:"lines,omitempty"`
}

// Line is one resolved reference line (item 09) — JSON tags define the wire
// shape read by WebClient/js/alarmSidebar.js. Exactly one of Value/Channel
// is non-zero; the browser reads Channel's CURRENT value live off
// channelBuffers on every repaint rather than trusting a value resolved at
// raise time, for the same reason PlotLine (alerts/config.go) documents.
type Line struct {
	Value   *float64 `json:"value,omitempty"`
	Channel string   `json:"channel,omitempty"`
	Label   string   `json:"label"`
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
	r.RaiseFor(id, severity, message, nil, "", "", nil, nil)
}

// RaiseFor records an alert and says what it is about. `channels` are the
// channel refDes it concerns (a rule may reference several); `node` is set
// instead for node-level alerts, where every channel owned by that node is
// affected and listing them would be both long and redundant. `description`
// is the alert definition's optional `describe "…"` long form (item 07a),
// already interpolated by the caller; template/sensor call sites, which have
// no `describe`, pass "". `plotChannels` and `lines` are the alert's
// optional `channels` block (item 09) — nil/nil for every call site except
// the rule one, since only rule alerts can declare one.
//
// This parameter list has grown by simple appending across three additions
// now (channels/node in item 07, description in item 07a, plotChannels/lines
// here in item 09) rather than becoming an options struct, deliberately: an
// options struct would mean touching every existing call site's shape
// again, re-risking calls the last two commits on this branch already
// verified. The cost of that choice is real and worth flagging rather than
// silently accepting: `channels []string` and `plotChannels []string` are
// the same Go type in adjacent-ish positions, so a call that transposed them
// would compile without complaint and fail only by attributing the wrong
// channels to a rule at runtime. Every call site in this codebase is
// covered by a test that checks the actual field values (not just "it
// compiled"), which is the real guard against that mistake — not the
// compiler.
func (r *Registry) RaiseFor(id, severity, message string, channels []string, node string, description string, plotChannels []string, lines []Line) {
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
		ID:           id,
		Category:     severity,
		Message:      message,
		Timestamp:    r.now(),
		Acked:        false,
		Resolved:     false,
		Channels:     channels,
		Node:         node,
		Description:  description,
		PlotChannels: plotChannels,
		Lines:        lines,
	}
	// A rule and its alert are one identity (package doc): RaiseFor is called
	// only on a rule's rising edge (evalRules), and evalRules ALWAYS runs a
	// Resolve or ResolveAndAck on the falling edge before the rule can ever
	// raise again — so the no-op short-circuit above can never catch a
	// genuine re-trigger, and every re-trigger falls through to here and
	// builds a brand new Record. Without this, a suppressed alert would
	// silently un-suppress itself on the very first re-trigger of its rule,
	// which defeats the entire feature (Suppress is supposed to survive any
	// number of re-triggers, not just until the next one). Carry the
	// suppression forward from whatever record this id already had.
	if ok {
		rec.Suppressed = existing.Suppressed
		rec.SuppressedAt = existing.SuppressedAt
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

// Suppress marks an alert suppressed — an operator's standing decision to
// silence a known, understood condition — and republishes it as a normal
// `alert` (not alert_acked; this isn't an acknowledgement, see Record's
// Suppressed doc). Suppressing an unknown id does nothing and returns false.
//
// Suppress targets the ALERT, not the underlying rule, but because RaiseFor
// (above) explicitly carries Suppressed/SuppressedAt forward onto every
// future re-raise of the same id, suppressing survives any number of future
// re-triggers of the same rule with no extra bookkeeping here — that's the
// whole point of a rule and its alert being one identity (see the package
// doc). It does NOT survive a control node restart: the registry is
// memory-only, same as every other alert fact.
func (r *Registry) Suppress(id string) bool {
	r.mu.Lock()
	rec, ok := r.byID[id]
	var out Record
	if ok {
		rec.Suppressed = true
		rec.SuppressedAt = r.now()
		out = *rec
	}
	sink := r.sink
	r.mu.Unlock()

	if ok && sink != nil {
		sink.PublishAlert(out)
	}
	return ok
}

// Unsuppress reverses Suppress. The alert reappears showing its CURRENT
// state, not a stale snapshot from suppression time: RaiseFor has kept
// Message/Category/Resolved current underneath the suppression the whole
// time (see its carry-forward comment) — only Suppressed itself was hiding
// it. Unsuppressing an unknown id does nothing and returns false.
func (r *Registry) Unsuppress(id string) bool {
	r.mu.Lock()
	rec, ok := r.byID[id]
	var out Record
	if ok {
		rec.Suppressed = false
		rec.SuppressedAt = 0
		out = *rec
	}
	sink := r.sink
	r.mu.Unlock()

	if ok && sink != nil {
		sink.PublishAlert(out)
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
