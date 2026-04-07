// Package broker is the central message hub.  It fans data in from DAQ node
// clients and the health goroutine, broadcasts it to web clients on a ticker,
// and routes commands from web clients to the correct DAQ node.
package broker

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// ── Public types ──────────────────────────────────────────────────────────────

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
// Run goroutine except the atomic counters, which are safe to read from anywhere.
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

	// Atomic health counters — readable from outside the Run goroutine.
	DaqConnected atomic.Int32
	WcConnected  atomic.Int32
	LoopTimeNs   atomic.Int64 // nanoseconds for last broadcast loop
}

// New creates a Broker.  refDesMap maps channel refDes → DAQ node refDes.
// restartRefDes is the set of refDes values that trigger a CTR restart.
func New(refDesMap map[string]string, restartRefDes []string) *Broker {
	rr := make(map[string]bool, len(restartRefDes))
	for _, r := range restartRefDes {
		rr[r] = true
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
