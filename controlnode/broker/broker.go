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

	// badMu protects badSnapshot; written by Run, read by BadDataSnapshot.
	badMu       sync.RWMutex
	badSnapshot []byte // nil when no channels are currently bad

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
				os.Exit(1)
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
