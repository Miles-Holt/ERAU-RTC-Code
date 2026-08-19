// Package broker is the central message hub.  It fans data in from DAQ node
// clients and the health goroutine, broadcasts it to web clients on a ticker,
// and routes commands from web clients to the correct DAQ node.
package broker

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ── Public types ──────────────────────────────────────────────────────────────

// ChannelBounds holds optional engineering-unit min/max for bad-data detection.
// Nil means that side is unchecked.
type ChannelBounds struct {
	Min *float64
	Max *float64
}

// badEntry tracks the last-known bad state for one channel.
type badEntry struct {
	Value  float64
	Min    *float64
	Max    *float64
	Status string  // "high" or "low"
	T      float64 // Unix timestamp (seconds) when it went bad
}

// DataEvent carries a batch of channel values from one source (a DAQ node or
// the health goroutine).
type DataEvent struct {
	Values map[string]float64
}

// ErrEvent carries an error message from a DAQ node.
type ErrEvent struct {
	DaqRefDes string
	T         float64
	Err       string
}

// DaqEventSink receives the daqNode lifecycle and range-check transitions the
// alert engine turns into per-node template alerts.  The broker already sees all
// four events (it tracks connected nodes and does the bound checking), so this
// is the single server-side hook for them — nothing else has to duplicate the
// bookkeeping, and the browser never has to infer an alert from raw data.
//
// Every method is called from a broker or DAQ client goroutine and must not
// block or call back into the broker synchronously.
type DaqEventSink interface {
	// NodeConnected / NodeDisconnected fire when a DAQ client's link comes up
	// (handshake complete) or goes down.
	NodeConnected(node string)
	NodeDisconnected(node string)
	// NodeData fires for every data message received from a node; it is the
	// heartbeat stale detection measures against.
	NodeData(node string)
	// BadData fires only on a range-check TRANSITION.  status is "high", "low"
	// or "ok" (back in range); node is the owning DAQ node, or "" if unknown.
	BadData(refDes, node, status string, value float64)
}

// HistorySink receives every raw data batch as it arrives, for item 04's
// server-side chart aggregation (controlnode/history.Store satisfies this
// structurally — broker does not import that package, same reason
// DaqEventSink exists instead of importing controlnode/alerts directly).
type HistorySink interface {
	Record(t time.Time, values map[string]float64)
}

// CmdMsg is a command received from a web client, already parsed from JSON.
type CmdMsg struct {
	Type   string      `json:"type"`
	RefDes string      `json:"refDes"`
	Value  interface{} `json:"value"`
	User   string      `json:"user"`
}

// ── Internal types ────────────────────────────────────────────────────────────

type subReq struct {
	ch    chan []byte
	unsub bool
}

type daqRegReq struct {
	refDes string
	ch     chan []byte // receives marshalled cmd JSON
}

// ── Broker ────────────────────────────────────────────────────────────────────

// Broker fans data in and commands out.  All fields are only accessed from the
// Run goroutine except the atomic counters and badMu-protected fields, which
// are safe to read from anywhere.
type Broker struct {
	dataIn    chan DataEvent
	errIn     chan ErrEvent
	cmdIn     chan CmdMsg
	rawIn     chan []byte
	subIn     chan subReq
	daqRegIn  chan daqRegReq

	// refDes → DAQ node refDes (immutable after construction)
	refDesMap map[string]string

	// Restart refDes values that cause os.Exit(1) when commanded (immutable)
	restartRefDes map[string]bool

	// channelBounds is the set of channels to range-check (immutable after construction).
	channelBounds map[string]ChannelBounds

	// exit is called when a restart command is received.  Defaults to os.Exit;
	// overridable in tests so the restart path can be exercised without killing
	// the test process.
	exit func(int)

	// badMu protects badSnapshot; written by Run, read by BadDataSnapshot.
	badMu       sync.RWMutex
	badSnapshot []byte // nil when no channels are currently bad

	// sinkMu protects eventSink, which is normally installed once at wiring
	// time but is read from the Run goroutine and every DAQ client goroutine.
	sinkMu    sync.RWMutex
	eventSink DaqEventSink

	// historyMu protects historySink. A second, independent mutex rather than
	// reusing sinkMu: sinkMu already guards a conceptually different
	// single-pointer field, and conflating the two would make a future change
	// to one sink's locking silently change the other's. Like eventSink, this
	// is installed by main.go AFTER go b.Run(...) has already started, so the
	// lock is load-bearing, not defensive boilerplate.
	historyMu   sync.RWMutex
	historySink HistorySink

	// valuesMu protects currentValues; written by Run, read by CurrentValues.
	valuesMu      sync.RWMutex
	currentValues map[string]float64 // last-known values (read-only access from outside Run)

	// Atomic health counters — readable from outside the Run goroutine.
	DaqConnected atomic.Int32
	WcConnected  atomic.Int32
	LoopTimeNs   atomic.Int64 // nanoseconds for last broadcast loop
}

// New creates a Broker.  refDesMap maps channel refDes → DAQ node refDes.
// restartRefDes is the set of refDes values that trigger a CTR restart.
// channelBounds is the set of channels to range-check for bad-data detection;
// pass nil to disable range checking entirely.
func New(refDesMap map[string]string, restartRefDes []string, channelBounds map[string]ChannelBounds) *Broker {
	rr := make(map[string]bool, len(restartRefDes))
	for _, r := range restartRefDes {
		rr[r] = true
	}
	if channelBounds == nil {
		channelBounds = make(map[string]ChannelBounds)
	}
	return &Broker{
		dataIn:        make(chan DataEvent, 256),
		errIn:         make(chan ErrEvent, 64),
		cmdIn:         make(chan CmdMsg, 64),
		rawIn:         make(chan []byte, 64),
		subIn:         make(chan subReq, 64),
		daqRegIn:      make(chan daqRegReq, 32),
		refDesMap:     refDesMap,
		restartRefDes: rr,
		channelBounds: channelBounds,
		exit:          os.Exit,
		currentValues: make(map[string]float64),
	}
}

// Run is the main broker goroutine.  It blocks until the process exits.
func (b *Broker) Run(broadcastRateHz int) {
	if broadcastRateHz <= 0 {
		broadcastRateHz = 20
	}
	ticker := time.NewTicker(time.Second / time.Duration(broadcastRateHz))
	defer ticker.Stop()

	currentValues := make(map[string]float64)
	subscribers := make(map[chan []byte]struct{})
	daqCmds := make(map[string]chan []byte) // DAQ refDes → write channel
	badState := make(map[string]badEntry)   // refDes → current bad state (only bad channels present)

	for {
		select {

		// ── Error messages from DAQ nodes ─────────────────────────────────
		case ev := <-b.errIn:
			payload, err := json.Marshal(map[string]interface{}{
				"type":    "err",
				"t":       ev.T,
				"daqNode": ev.DaqRefDes,
				"err":     ev.Err,
			})
			if err != nil {
				log.Printf("broker: marshal err event: %v", err)
				continue
			}
			for ch := range subscribers {
				select {
				case ch <- payload:
				default:
				}
			}

		// ── Incoming data from DAQ nodes / health ─────────────────────────
		case ev := <-b.dataIn:
			for k, v := range ev.Values {
				currentValues[k] = v
				b.checkBounds(k, v, time.Now(), badState, subscribers)
			}
			// Record the raw batch for item 04's server-side history, at the
			// DAQ's actual sample rate rather than the decimated broadcast
			// tick below — see history.Store.Record's own comment for why.
			if hs := b.historySinkPtr(); hs != nil {
				hs.Record(time.Now(), ev.Values)
			}
			// Sync to the protected copy for external access
			b.valuesMu.Lock()
			for k, v := range ev.Values {
				b.currentValues[k] = v
			}
			b.valuesMu.Unlock()

		// ── Broadcast tick ────────────────────────────────────────────────
		case t := <-ticker.C:
			start := time.Now()
			msg, err := marshalDataMsg(t, currentValues)
			if err != nil {
				log.Printf("broker: marshal error: %v", err)
				continue
			}
			// Flush so next broadcast only contains freshly received values.
			currentValues = make(map[string]float64)
			b.WcConnected.Store(int32(len(subscribers)))
			for ch := range subscribers {
				select {
				case ch <- msg:
				default:
					// slow client — drop frame rather than block
				}
			}
			b.LoopTimeNs.Store(time.Since(start).Nanoseconds())

		// ── Raw broadcast (alerts, layout pushes) ─────────────────────────
		case msg := <-b.rawIn:
			for ch := range subscribers {
				select {
				case ch <- msg:
				default:
				}
			}

		// ── Web client subscribe / unsubscribe ────────────────────────────
		case req := <-b.subIn:
			if req.unsub {
				delete(subscribers, req.ch)
				close(req.ch)
			} else {
				subscribers[req.ch] = struct{}{}
			}
			b.WcConnected.Store(int32(len(subscribers)))

		// ── DAQ node cmd channel registration ─────────────────────────────
		case req := <-b.daqRegIn:
			if req.ch == nil {
				delete(daqCmds, req.refDes)
			} else {
				daqCmds[req.refDes] = req.ch
			}

		// ── Commands from web clients ──────────────────────────────────────
		case cmd := <-b.cmdIn:
			if b.restartRefDes[cmd.RefDes] {
				log.Printf("broker: restart command received from user %q — exiting", cmd.User)
				b.exit(1)
				continue
			}
			daqRefDes, ok := b.refDesMap[cmd.RefDes]
			if !ok {
				log.Printf("broker: unknown refDes in cmd: %q", cmd.RefDes)
				continue
			}
			ch, ok := daqCmds[daqRefDes]
			if !ok {
				log.Printf("broker: DAQ node %q not connected, dropping cmd for %q", daqRefDes, cmd.RefDes)
				continue
			}
			payload, err := json.Marshal(map[string]interface{}{
				"type":   "cmd",
				"refDes": cmd.RefDes,
				"value":  cmd.Value,
			})
			if err != nil {
				log.Printf("broker: marshal cmd: %v", err)
				continue
			}
			select {
			case ch <- payload:
			default:
				log.Printf("broker: cmd channel full for DAQ %q, dropping", daqRefDes)
			}
		}
	}
}

// ── Public API (goroutine-safe) ───────────────────────────────────────────────

// SetEventSink installs the DAQ event sink (the alert engine).  Passing nil
// removes it.  Safe to call at any time.
func (b *Broker) SetEventSink(s DaqEventSink) {
	b.sinkMu.Lock()
	b.eventSink = s
	b.sinkMu.Unlock()
}

func (b *Broker) sink() DaqEventSink {
	b.sinkMu.RLock()
	defer b.sinkMu.RUnlock()
	return b.eventSink
}

// SetHistorySink installs the history store item 04's /api/history endpoint
// reads from.  Passing nil removes it.  Safe to call at any time — main.go
// calls this after go b.Run(...) has already started, exactly like
// SetEventSink, which is why historySinkPtr locks rather than reading a bare
// field.
func (b *Broker) SetHistorySink(s HistorySink) {
	b.historyMu.Lock()
	b.historySink = s
	b.historyMu.Unlock()
}

func (b *Broker) historySinkPtr() HistorySink {
	b.historyMu.RLock()
	defer b.historyMu.RUnlock()
	return b.historySink
}

// NoteDaqConnected reports that a DAQ node's link is up (handshake complete).
// Called by the daqnode client alongside the DaqConnected counter.
func (b *Broker) NoteDaqConnected(node string) {
	if s := b.sink(); s != nil {
		s.NodeConnected(node)
	}
}

// NoteDaqDisconnected reports that a DAQ node's link went down.
func (b *Broker) NoteDaqDisconnected(node string) {
	if s := b.sink(); s != nil {
		s.NodeDisconnected(node)
	}
}

// NoteDaqData reports a data message from a node (the stale-detection heartbeat).
func (b *Broker) NoteDaqData(node string) {
	if s := b.sink(); s != nil {
		s.NodeData(node)
	}
}

// PublishErr enqueues an error event from a DAQ node.  Non-blocking; drops if buffer is full.
func (b *Broker) PublishErr(ev ErrEvent) {
	select {
	case b.errIn <- ev:
	default:
		log.Printf("broker: err buffer full, dropping error from DAQ %q", ev.DaqRefDes)
	}
}

// PublishData enqueues a data event.  Non-blocking; drops if buffer is full.
func (b *Broker) PublishData(ev DataEvent) {
	select {
	case b.dataIn <- ev:
	default:
		// buffer full — health/DAQ sent faster than broker can drain
	}
}

// SendCmd enqueues a command from a web client.
func (b *Broker) SendCmd(cmd CmdMsg) {
	select {
	case b.cmdIn <- cmd:
	default:
		log.Printf("broker: cmd buffer full, dropping cmd for %q", cmd.RefDes)
	}
}

// Subscribe registers a new web client.  Returns a channel that receives
// marshalled broadcast JSON, and an unsubscribe function.
func (b *Broker) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	b.subIn <- subReq{ch: ch}
	unsub := func() {
		b.subIn <- subReq{ch: ch, unsub: true}
	}
	return ch, unsub
}

// Publish broadcasts a raw JSON message to all subscribed web clients immediately.
// Non-blocking: drops the message if the internal buffer is full.
func (b *Broker) Publish(msg []byte) {
	select {
	case b.rawIn <- msg:
	default:
		log.Printf("broker: raw publish buffer full, dropping message")
	}
}

// RegisterDaq registers (or deregisters when ch==nil) a DAQ node's cmd channel.
func (b *Broker) RegisterDaq(daqRefDes string, ch chan []byte) {
	b.daqRegIn <- daqRegReq{refDes: daqRefDes, ch: ch}
}

// BadDataSnapshot returns a bad_data_snapshot JSON message containing all
// channels currently outside their configured bounds, or nil if there are none.
// Safe to call from any goroutine.
func (b *Broker) BadDataSnapshot() []byte {
	b.badMu.RLock()
	defer b.badMu.RUnlock()
	return b.badSnapshot
}

// CurrentValues returns a copy of all known channel values.
// Safe to call from any goroutine, but values may be slightly stale.
//
// Prefer CurrentValue (single lookup) or EachValue / FillValues (bulk) on hot
// paths: this allocates and copies the whole map on every call.
func (b *Broker) CurrentValues() map[string]float64 {
	b.valuesMu.RLock()
	defer b.valuesMu.RUnlock()
	result := make(map[string]float64, len(b.currentValues))
	for k, v := range b.currentValues {
		result[k] = v
	}
	return result
}

// CurrentValue looks up one channel without copying the whole map.
func (b *Broker) CurrentValue(refDes string) (float64, bool) {
	b.valuesMu.RLock()
	defer b.valuesMu.RUnlock()
	v, ok := b.currentValues[refDes]
	return v, ok
}

// EachValue calls fn for every known channel value under a single read lock.
// fn must not call back into the broker.  This is the allocation-free way for
// the engine to take one snapshot per tick.
func (b *Broker) EachValue(fn func(refDes string, value float64)) {
	b.valuesMu.RLock()
	defer b.valuesMu.RUnlock()
	for k, v := range b.currentValues {
		fn(k, v)
	}
}

// FillValues copies every known channel value into dst (which is NOT cleared).
func (b *Broker) FillValues(dst map[string]float64) {
	b.valuesMu.RLock()
	defer b.valuesMu.RUnlock()
	for k, v := range b.currentValues {
		dst[k] = v
	}
}

// checkBounds evaluates one channel value against its configured bounds.
// Must only be called from the Run goroutine (badState is not mutex-protected).
// On a bad↔ok state transition it fans a bad_data message out to all subscribers
// and updates the mutex-protected snapshot used by BadDataSnapshot.
func (b *Broker) checkBounds(refDes string, value float64, t time.Time,
	badState map[string]badEntry, subscribers map[chan []byte]struct{}) {

	bounds, ok := b.channelBounds[refDes]
	if !ok {
		return
	}

	// Determine new status.
	var newStatus string
	switch {
	case bounds.Min != nil && value < *bounds.Min:
		newStatus = "low"
	case bounds.Max != nil && value > *bounds.Max:
		newStatus = "high"
	default:
		newStatus = "ok"
	}

	_, wasBad := badState[refDes]
	isBad := newStatus != "ok"

	if !wasBad && !isBad {
		return // was fine, still fine — nothing to do
	}
	if wasBad && isBad && badState[refDes].Status == newStatus {
		return // still bad in the same direction — no transition
	}

	// State changed — update badState map.
	if isBad {
		badState[refDes] = badEntry{
			Value:  value,
			Min:    bounds.Min,
			Max:    bounds.Max,
			Status: newStatus,
			T:      float64(t.UnixMilli()) / 1000.0,
		}
	} else {
		delete(badState, refDes)
	}

	// Rebuild the shared snapshot from the updated badState.
	b.updateBadSnapshot(badState)

	// Tell the alert engine about the transition.  The server owns bad-data
	// alerts now: the browser only renders what comes back through the alert
	// messages (it still gets bad_data for the value display itself).
	if s := b.sink(); s != nil {
		s.BadData(refDes, b.refDesMap[refDes], newStatus, value)
	}

	// Build and fan-out the transition message immediately.
	ts := float64(t.UnixMilli()) / 1000.0
	msg := map[string]interface{}{
		"type":   "bad_data",
		"refDes": refDes,
		"value":  value,
		"status": newStatus,
		"t":      ts,
	}
	if bounds.Min != nil {
		msg["validMin"] = *bounds.Min
	}
	if bounds.Max != nil {
		msg["validMax"] = *bounds.Max
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		log.Printf("broker: marshal bad_data: %v", err)
		return
	}
	for ch := range subscribers {
		select {
		case ch <- payload:
		default:
		}
	}
}

// updateBadSnapshot rebuilds the mutex-protected bad_data_snapshot from the
// current badState map.  Must only be called from the Run goroutine.
func (b *Broker) updateBadSnapshot(badState map[string]badEntry) {
	b.badMu.Lock()
	defer b.badMu.Unlock()

	if len(badState) == 0 {
		b.badSnapshot = nil
		return
	}

	type snapshotEntry struct {
		RefDes   string   `json:"refDes"`
		Value    float64  `json:"value"`
		ValidMin *float64 `json:"validMin,omitempty"`
		ValidMax *float64 `json:"validMax,omitempty"`
		Status   string   `json:"status"`
		T        float64  `json:"t"`
	}
	entries := make([]snapshotEntry, 0, len(badState))
	for refDes, e := range badState {
		entries = append(entries, snapshotEntry{
			RefDes:   refDes,
			Value:    e.Value,
			ValidMin: e.Min,
			ValidMax: e.Max,
			Status:   e.Status,
			T:        e.T,
		})
	}
	snap, err := json.Marshal(map[string]interface{}{
		"type":     "bad_data_snapshot",
		"channels": entries,
	})
	if err != nil {
		log.Printf("broker: marshal bad_data_snapshot: %v", err)
		b.badSnapshot = nil
		return
	}
	b.badSnapshot = snap
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func marshalDataMsg(t time.Time, values map[string]float64) ([]byte, error) {
	// Copy the values map so we don't hold a reference that gets mutated.
	d := make(map[string]float64, len(values))
	for k, v := range values {
		d[k] = v
	}
	msg := map[string]interface{}{
		"type": "data",
		"t":    float64(t.UnixMilli()) / 1000.0,
		"d":    d,
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshalDataMsg: %w", err)
	}
	return b, nil
}
