package main

import "log"

// Commander dispatches cmd messages to the driver.
// It is called from the server read loop and from the state machine sequence executor.
type Commander struct {
	driver Driver
}

func newCommander(d Driver) *Commander {
	return &Commander{driver: d}
}

// Execute writes a value to the named channel. value is treated as a float;
// digital outputs will receive 0 or 1.
func (c *Commander) Execute(refDes string, value float64) {
	if err := c.driver.Write(refDes, value); err != nil {
		log.Printf("commander: write %s=%v: %v", refDes, value, err)
	}
}

// HandleCmd parses and dispatches an inbound CmdMsg.
func (c *Commander) HandleCmd(msg CmdMsg) {
	var v float64
	switch val := msg.Value.(type) {
	case float64:
		v = val
	case bool:
		if val {
			v = 1
		}
	}
	c.Execute(msg.RefDes, v)
}
