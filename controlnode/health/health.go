// Package health publishes CTR node metrics into the broker at the broadcast rate.
package health

import (
	"context"
	"controlnode/broker"
	"time"
)

// Publisher reads atomic counters from the broker and injects them as data events.
type Publisher struct {
	b            *broker.Broker
	startTime    time.Time
	sensorRefDes map[string]string // metric name → refDes
	cmdRefDes    []string          // command refDes values to publish as 0 each tick

	// now is the clock uptime is measured against.  Defaults to time.Now;
	// overridable via SetClock so tests can advance time deterministically
	// instead of sleeping.
	now func() time.Time
}

// New creates a Publisher.  sensorRefDes maps metric keys to the refDes values
// defined in the XML <ctrNode><health><sensors> section.
//
// Expected keys:
//
//	"uptime"       — seconds since CTR start
//	"loopTime"     — last broker loop time in ms
//	"daqConnected" — number of connected DAQ nodes
//	"wcConnected"  — number of connected web clients
//
// cmdRefDes is a list of CTR command refDes values that should always appear
// in the data stream as 0 (so the web client can show their time history).
func New(b *broker.Broker, sensorRefDes map[string]string, cmdRefDes []string) *Publisher {
	return &Publisher{
		b:            b,
		startTime:    time.Now(),
		sensorRefDes: sensorRefDes,
		cmdRefDes:    cmdRefDes,
		now:          time.Now,
	}
}

// SetClock overrides the clock uptime is measured against.  Must be called
// before Run(); intended for tests that need to advance "time" without
// sleeping.  Passing a clock does not retroactively move startTime, which is
// still whatever it was when New() ran — call SetStartTime too if the test
// needs full control over both ends.
func (p *Publisher) SetClock(now func() time.Time) {
	p.now = now
}

// SetStartTime overrides the reference point uptime is measured from.
// Intended for tests, alongside SetClock.
func (p *Publisher) SetStartTime(t time.Time) {
	p.startTime = t
}

// Run publishes health metrics at broadcastRateHz until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context, broadcastRateHz int) {
	if broadcastRateHz <= 0 {
		broadcastRateHz = 20
	}
	ticker := time.NewTicker(time.Second / time.Duration(broadcastRateHz))
	defer ticker.Stop()

	// The CTR command channels are always 0 here and never change; re-sending them
	// on every frame is pure overhead.  Emit them only once per keepalive interval
	// (~1 s) so new clients still get their baseline for time-history graphs.
	keepaliveEvery := broadcastRateHz
	if keepaliveEvery < 1 {
		keepaliveEvery = 1
	}
	tick := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		values := make(map[string]float64, 4)

		if rd, ok := p.sensorRefDes["uptime"]; ok {
			values[rd] = p.now().Sub(p.startTime).Seconds()
		}
		if rd, ok := p.sensorRefDes["loopTime"]; ok {
			values[rd] = float64(p.b.LoopTimeNs.Load()) / 1000000.0 // ns → ms
		}
		if rd, ok := p.sensorRefDes["daqConnected"]; ok {
			values[rd] = float64(p.b.DaqConnected.Load())
		}
		if rd, ok := p.sensorRefDes["wcConnected"]; ok {
			values[rd] = float64(p.b.WcConnected.Load())
		}

		if tick%keepaliveEvery == 0 {
			for _, rd := range p.cmdRefDes {
				values[rd] = 0
			}
		}
		tick++

		if len(values) > 0 {
			p.b.PublishData(broker.DataEvent{Values: values})
		}
	}
}
