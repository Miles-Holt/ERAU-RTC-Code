// Package health publishes CTR node metrics into the broker at the broadcast rate.
package health

import (
	"controlnode/broker"
	"time"
)

// Publisher reads atomic counters from the broker and injects them as data events.
type Publisher struct {
	b            *broker.Broker
	startTime    time.Time
	sensorRefDes map[string]string // metric name → refDes
	cmdRefDes    []string          // command refDes values to publish as 0 each tick
}

// New creates a Publisher.  sensorRefDes maps metric keys to the refDes values
// defined in the XML <ctrNode><health><sensors> section.
//
// Expected keys:
//
//	"uptime"       — seconds since CTR start
//	"loopTime"     — last broker loop time in µs
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
	}
}

// Run publishes health metrics at broadcastRateHz.  Blocks until process exits.
func (p *Publisher) Run(broadcastRateHz int) {
	if broadcastRateHz <= 0 {
		broadcastRateHz = 20
	}
	ticker := time.NewTicker(time.Second / time.Duration(broadcastRateHz))
	defer ticker.Stop()

	for range ticker.C {
		values := make(map[string]float64, 4)

		if rd, ok := p.sensorRefDes["uptime"]; ok {
			values[rd] = time.Since(p.startTime).Seconds()
		}
		if rd, ok := p.sensorRefDes["loopTime"]; ok {
			values[rd] = float64(p.b.LoopTimeNs.Load()) / 1000.0 // ns → µs
		}
		if rd, ok := p.sensorRefDes["daqConnected"]; ok {
			values[rd] = float64(p.b.DaqConnected.Load())
		}
		if rd, ok := p.sensorRefDes["wcConnected"]; ok {
			values[rd] = float64(p.b.WcConnected.Load())
		}

		for _, rd := range p.cmdRefDes {
			values[rd] = 0
		}

		if len(values) > 0 {
			p.b.PublishData(broker.DataEvent{Values: values})
		}
	}
}
