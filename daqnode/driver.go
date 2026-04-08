package main

// Driver is the hardware abstraction interface for the DAQ node.
// The simulation driver (sim.go) implements this without real hardware.
// A future NI DAQmx driver would implement it via cgo.
type Driver interface {
	// Configure initializes the driver from the config received from the control node.
	// Called once per connection after handshake.
	Configure(cfg *ConfigMsg) error

	// Start begins data acquisition and output tasks.
	Start() error

	// ReadAll reads all configured input channels and returns a refDes→value map.
	// Blocks until a new sample is available. Called at sampleRateHz.
	ReadAll() (map[string]float64, error)

	// Write sets an output channel to a value. 0/1 for digital, float for analog.
	Write(refDes string, value float64) error

	// Stop halts all tasks and releases resources.
	Stop() error
}
