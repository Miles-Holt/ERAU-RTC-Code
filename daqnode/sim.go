package main

import (
	"log"
	"math"
	"math/rand"
	"sync"
	"time"
)

// simDriver implements Driver with synthetic sensor data and no-op hardware writes.
// Input channels generate plausible values; digital output feedback mirrors commands.
type simDriver struct {
	mu           sync.Mutex
	cfg          *ConfigMsg
	outputs      map[string]float64 // last commanded value per output refDes
	inputChannels []simChannel
	ticker       *time.Ticker
	sampleRateHz int

	// per-channel random walk state
	walks map[string]*randomWalk
}

type simChannel struct {
	refDes    string
	module    string
	isOutput  bool
	feedbackFor string // for digital-IO input: mirrors this output refDes
}

type randomWalk struct {
	value float64
	base  float64
	noise float64
	drift float64
}

func (w *randomWalk) next() float64 {
	w.value += (rand.Float64()-0.5)*w.noise + (w.base-w.value)*w.drift
	return w.value
}

func newSimDriver() *simDriver {
	return &simDriver{
		outputs: make(map[string]float64),
		walks:   make(map[string]*randomWalk),
	}
}

func (d *simDriver) Configure(cfg *ConfigMsg) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.cfg = cfg
	d.sampleRateHz = cfg.SampleRateHz
	if d.sampleRateHz <= 0 {
		d.sampleRateHz = 1000
	}

	d.inputChannels = nil
	d.outputs = make(map[string]float64)
	d.walks = make(map[string]*randomWalk)

	// Build output refDes set first so feedback channels can reference them
	outputSet := make(map[string]bool)
	for _, ch := range cfg.Channels {
		if ch.ModuleModelNumber == "Analog-Output" || ch.ModuleModelNumber == "Digital-IO" {
			if isOutputChannel(ch) {
				outputSet[ch.RefDes] = true
				d.outputs[ch.RefDes] = 0
			}
		}
	}

	for _, ch := range cfg.Channels {
		sc := simChannel{refDes: ch.RefDes, module: ch.ModuleModelNumber}
		switch ch.ModuleModelNumber {
		case "Analog-Output":
			sc.isOutput = true
		case "Digital-IO":
			if isOutputChannel(ch) {
				sc.isOutput = true
			} else {
				// Input: try to find a matching output to mirror (e.g. "NV-03-FB" ← "NV-03-CMD")
				sc.feedbackFor = guessOutputRefDes(ch.RefDes, outputSet)
			}
		default:
			// Input sensor: set up random walk based on module type
			d.walks[ch.RefDes] = simWalkForModule(ch)
		}
		d.inputChannels = append(d.inputChannels, sc)
	}

	log.Printf("sim: configured %d channels (%d outputs)", len(cfg.Channels), len(d.outputs))
	return nil
}

func (d *simDriver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ticker = time.NewTicker(time.Duration(float64(time.Second) / float64(d.sampleRateHz)))
	log.Printf("sim: started at %d Hz", d.sampleRateHz)
	return nil
}

func (d *simDriver) ReadAll() (map[string]float64, error) {
	<-d.ticker.C

	d.mu.Lock()
	defer d.mu.Unlock()

	result := make(map[string]float64, len(d.inputChannels))
	for _, ch := range d.inputChannels {
		if ch.isOutput {
			continue // output-only channels don't appear in read results
		}
		if ch.feedbackFor != "" {
			// Mirror the last commanded value for this output
			result[ch.refDes] = d.outputs[ch.feedbackFor]
			continue
		}
		if w, ok := d.walks[ch.refDes]; ok {
			result[ch.refDes] = w.next()
		}
	}
	return result, nil
}

func (d *simDriver) Write(refDes string, value float64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.outputs[refDes] = value
	log.Printf("sim: write %s = %v", refDes, value)
	return nil
}

func (d *simDriver) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ticker != nil {
		d.ticker.Stop()
		d.ticker = nil
	}
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// isOutputChannel returns true if a Digital-IO channel is an output.
// Convention: output channels have a task name starting with "Digital-IO"
// and their channel number refers to a port/line (e.g. "/port0/line0").
// Feedback inputs have no cmd-style task name.
func isOutputChannel(ch ChannelDef) bool {
	// If role is cmd-bool or the refDes ends with -CMD, it's an output
	if len(ch.RefDes) >= 4 && ch.RefDes[len(ch.RefDes)-4:] == "-CMD" {
		return true
	}
	// Analog output is always output
	return ch.ModuleModelNumber == "Analog-Output"
}

// guessOutputRefDes tries to find the output refDes that a feedback channel mirrors.
// e.g. "NV-03-FB" → looks for "NV-03-CMD" in the output set.
func guessOutputRefDes(fbRefDes string, outputs map[string]bool) string {
	if len(fbRefDes) >= 3 && fbRefDes[len(fbRefDes)-3:] == "-FB" {
		candidate := fbRefDes[:len(fbRefDes)-3] + "-CMD"
		if outputs[candidate] {
			return candidate
		}
	}
	return ""
}

// simWalkForModule returns a random walk tuned to the sensor type.
func simWalkForModule(ch ChannelDef) *randomWalk {
	switch ch.ModuleModelNumber {
	case "Thermocouple":
		return &randomWalk{value: 70, base: 70, noise: 0.5, drift: 0.05}
	case "Bridge-Completion":
		return &randomWalk{value: 0, base: 0, noise: 0.2, drift: 0.1}
	case "Analog-Input":
		// Pressure sensors: base around 15 psia (atmospheric)
		base := 15.0
		// Use sensitivity as a hint for scale if available
		if ch.Sensitivity != nil && math.Abs(*ch.Sensitivity) > 0 {
			base = 1.0 / math.Abs(*ch.Sensitivity) * 0.1
		}
		return &randomWalk{value: base, base: base, noise: 0.3, drift: 0.05}
	default:
		return &randomWalk{value: 0, base: 0, noise: 0.1, drift: 0.1}
	}
}
