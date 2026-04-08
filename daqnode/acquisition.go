package main

import (
	"log"
	"time"
)

// acqLoop reads from the driver at sampleRateHz, decimates to broadcastRateHz,
// and pushes data messages and raw samples to their respective channels.
type acqLoop struct {
	driver          Driver
	sampleRateHz    int
	broadcastRateHz int
	dataCh          chan<- []byte              // decimated DataMsg JSON for the write loop
	sampleCh        chan<- map[string]float64  // every raw sample for the state machine
	stopCh          <-chan struct{}
}

func newAcqLoop(
	d Driver,
	sampleRateHz, broadcastRateHz int,
	dataCh chan<- []byte,
	sampleCh chan<- map[string]float64,
	stopCh <-chan struct{},
) *acqLoop {
	return &acqLoop{
		driver:          d,
		sampleRateHz:    sampleRateHz,
		broadcastRateHz: broadcastRateHz,
		dataCh:          dataCh,
		sampleCh:        sampleCh,
		stopCh:          stopCh,
	}
}

// Run blocks until stopCh is closed. Call in a goroutine.
func (a *acqLoop) Run() {
	if a.broadcastRateHz <= 0 {
		a.broadcastRateHz = 50
	}
	decimFactor := a.sampleRateHz / a.broadcastRateHz
	if decimFactor <= 0 {
		decimFactor = 1
	}

	// Accumulator for averaging
	acc := make(map[string]float64)
	counts := make(map[string]int)
	sampleCount := 0

	for {
		select {
		case <-a.stopCh:
			return
		default:
		}

		values, err := a.driver.ReadAll()
		if err != nil {
			log.Printf("acq: read error: %v", err)
			continue
		}

		// Push every sample to state machine for abort checking
		select {
		case a.sampleCh <- values:
		default:
			// State machine is busy; drop the sample rather than block acquisition
		}

		// Accumulate for decimation
		for k, v := range values {
			acc[k] += v
			counts[k]++
		}
		sampleCount++

		if sampleCount >= decimFactor {
			// Build averaged data map
			d := make(map[string]float64, len(acc))
			for k, total := range acc {
				if counts[k] > 0 {
					d[k] = total / float64(counts[k])
				}
			}

			t := float64(time.Now().UnixNano()) / 1e9
			payload := msgData(t, d)

			select {
			case a.dataCh <- payload:
			case <-a.stopCh:
				return
			}

			// Reset accumulator
			for k := range acc {
				delete(acc, k)
				delete(counts, k)
			}
			sampleCount = 0
		}
	}
}
