// Command daqsim runs the non-LabVIEW daqNode simulator (controlnode/daqsim)
// standalone, as a WebSocket server a real control node can dial to rehearse
// a sequence without hardware or LabVIEW. See docs/daqsim.md for exact
// commands to run it against config/daqNodes/daq001.yaml.
package main

import (
	"bufio"
	"controlnode/daqsim"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

func main() {
	port := flag.Int("port", 8001, "TCP port to listen on (must match the wsPort in config/daqNodes/<node>.yaml)")
	host := flag.String("host", "", "interface to listen on (empty = all interfaces)")
	refDes := flag.String("refdes", "DAQSIM", "node refDes sent in config_req (cosmetic; the control node identifies the node by which config it dialled)")
	seed := flag.Int64("seed", 0, "seed for sensor-noise RNG (0 = fixed, reproducible)")
	dataRate := flag.Float64("datarate", 0, "override the data-frame rate in Hz (0 = use the sampleRateHz from the received config)")
	flag.Parse()

	sim := daqsim.New(daqsim.Options{
		RefDes:             *refDes,
		Seed:               *seed,
		DataRateOverrideHz: *dataRate,
	})

	addr := fmt.Sprintf("%s:%d", *host, *port)
	got, err := sim.Start(addr)
	if err != nil {
		log.Fatalf("daqsim: %v", err)
	}
	log.Printf("daqsim: %s listening on %s — dial ws://<this host>:%d/ from the control node", *refDes, got, *port)
	log.Printf("daqsim: stdin commands — \"set <refDes> <value>\" forces a channel (e.g. to trip an abort_rule), \"drop\" simulates a dead link, \"quit\" exits")
	log.Printf("daqsim: running without a terminal (CI, background, piped stdin) is fine too — stdin EOF does not stop the simulator; use Ctrl-C/SIGTERM")

	if quit := runStdinLoop(sim); quit {
		return
	}

	// stdin hit EOF without a "quit" — e.g. launched under a supervisor,
	// backgrounded, or with stdin redirected from /dev/null, which is exactly
	// the non-interactive CI/build use case docs/daqsim.md and the restructure
	// plan ask this binary to cover. Keep serving until a real stop signal
	// arrives instead of exiting just because there is no console attached.
	log.Printf("daqsim: stdin closed — still serving; send SIGINT/SIGTERM to stop")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	log.Printf("daqsim: shutting down")
	sim.Close()
}

// runStdinLoop is the simulator's manual trigger: a line-oriented console so
// an operator can force a channel over an abort_rule threshold, or simulate a
// dropped link, without hardware. Kept deliberately simple (no HTTP server,
// no extra port to coordinate) since this binary's whole point is standing in
// for hardware with the least ceremony possible. Returns true (simulator
// already closed) on an explicit "quit"/"exit" line, or false on stdin EOF —
// the caller keeps serving on false instead of exiting just because there is
// no console attached.
func runStdinLoop(sim *daqsim.Simulator) bool {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch strings.ToLower(fields[0]) {
		case "quit", "exit":
			sim.Close()
			return true

		case "drop":
			sim.DropConnection()
			log.Printf("daqsim: connection dropped (simulated link loss)")

		case "set":
			if len(fields) != 3 {
				log.Printf(`daqsim: usage: set <refDes> <value>  (e.g. "set CPT-01 900" to trip a chamber-pressure abort_rule)`)
				continue
			}
			v, err := strconv.ParseFloat(fields[2], 64)
			if err != nil {
				log.Printf("daqsim: %q is not a number: %v", fields[2], err)
				continue
			}
			if sim.SetSensor(fields[1], daqsim.SensorSpec{Base: v}) {
				log.Printf("daqsim: %s forced to %g", fields[1], v)
			} else {
				log.Printf("daqsim: %s is not a known sensor channel (command channels are driven by the control node's cmd/state_update messages, not this console)", fields[1])
			}

		default:
			log.Printf("daqsim: unrecognised command %q (try \"set\", \"drop\", or \"quit\")", fields[0])
		}
	}
	return false
}
