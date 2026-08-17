# daqsim — a non-LabVIEW daqNode

`controlnode/daqsim` is a daqNode implementation in Go. It speaks the exact
`controlNode <-> daqNode` protocol documented in `docs/websocket-protocol.md`
Part 2 well enough to rehearse a sequence, develop against, or CI-test the
control node without hardware or the LabVIEW application. It is not a mock:
it is a real WebSocket server the control node dials, drives its channel
model entirely off the `config` it receives (no hardcoded channel names), and
runs `state_update` entry/exit sequences and `abort_rule` monitoring locally,
exactly as the protocol says the node must.

Two things ship:

- **`controlnode/daqsim`** — the library. Usable from Go tests (inject a
  `*daqsim.FakeClock` to run a multi-second burn instantly) or from a binary
  (the default `daqsim.RealClock{}` runs in real time).
- **`controlnode/cmd/daqsim`** — a standalone binary built on top of it.

## Running it against a real control node

```
cd controlnode
go build -o daqsim.exe ./cmd/daqsim
go build -o controlnode.exe .

# Terminal 1 — start the simulator on the port config/daqNodes/daq001.yaml expects
./daqsim.exe -port 8001 -refdes DAQ001

# Terminal 2 — start the control node against the real config
./controlnode.exe -config-dir ../config
```

Watch terminal 2's log for `daqnode DAQ001: connected` (and the absence of
further `waiting for connection` lines) to confirm the link is up; the
control node's `/docs` page and web client will show DAQ001 as connected and
streaming data.

`config/daqNodes/daq001.yaml`'s `ip` is the control node's own machine
hostname in this repo's shipped config (dial-out is always CTR -> DAQ), so
running both processes on the same box works as shown above. Point `-port` at
whatever `wsPort` the config you're running against declares if it differs.

### Forcing an abort without hardware

The binary reads line-oriented commands from stdin:

| Command | Effect |
|---|---|
| `set <refDes> <value>` | Forces a sensor channel's reading, e.g. `set CPT-01 900` drives chamber pressure over `LIM-CPT01-HIGH` (450 psia) to trip daq001's abort_rule live, mid-rehearsal. No-op (logged) on a command channel — those are driven by the control node's `cmd`/`state_update` messages, not this console. |
| `drop` | Closes the active connection, simulating a dead link — pairs with the reconnect-mid-sequence behavior described in `docs/websocket-protocol.md`'s "Reconnect behaviour". |
| `quit` / `exit` | Stops the simulator and exits. |

Running with stdin closed or redirected (a supervisor, CI, `< /dev/null`) is
fine and intentional: the simulator keeps serving on stdin EOF and only stops
on `quit`/`exit` or `SIGINT`/`SIGTERM`. It does **not** exit just because
there is no interactive console attached — the whole point of daqsim is
unattended CI use, so a design that quit on EOF would defeat it. Kill it with
Ctrl-C, or `Stop-Process`/`kill` from another shell.

## Library API sketch

```go
sim := daqsim.New(daqsim.Options{
    RefDes:  "DAQ001",
    Clock:   daqsim.NewFakeClock(), // or daqsim.RealClock{} (default)
    Sensors: map[string]daqsim.SensorSpec{"CPT-01": {Base: 200}},
})
addr, err := sim.Start("127.0.0.1:0") // control node dials ws://addr/
...
sim.DropConnection()                  // simulate a dead link
sim.SetSensor("CPT-01", daqsim.SensorSpec{Base: 900}) // force an abort_rule
sim.AppliedLog()                      // ordered set-points, with t_ms and wall time
sim.Runs()                            // completed/aborted outcome per state_update
sim.SendRaw(v)                        // inject an arbitrary/edge-case wire message (tests)
```

See `controlnode/integration/daqsim_e2e_test.go` for the full end-to-end
wiring (real broker, softchan store, statemachine engine compiled from the
real `config/machines/daq001.sm`, and `daqnode.Client`) against a daqsim over
a real localhost WebSocket.

## Protocol ambiguities found while building this

These are genuine gaps or mismatches noticed while implementing the node
side from `docs/websocket-protocol.md` and `controlnode/daqnode/client.go`
independently, reported per the restructure plan's ask rather than quietly
worked around:

1. **The DAQ-side `config` message has no `role`/direction field.** The
   browser-side `config` (Part 1) tells the browser which channels are
   commandable via `role: "cmd-bool"` etc; the DAQ-side `config` (Part 2,
   built by `config.BuildDaqNodeConfigJSON`) carries no equivalent — a
   command channel and its read-only feedback sibling can be the exact same
   `moduleModelNumber` (`NV-03-CMD` and `NV-03-FB` are both `"Digital-IO"`),
   distinguishable only by the `-CMD` suffix convention used throughout
   `config/controls.yaml`. daqsim resolves this with that naming convention
   (`isCommandChannel` in `controlnode/daqsim/model.go`) plus one
   unambiguous physical fact (an `Analog-Output` module can never be a
   sensor), but a real node — LabVIEW or otherwise — has the identical
   problem unless it also leans on the same convention or an out-of-band
   project file. Recommend adding a `role`/`direction` field to the DAQ
   config's channel entries, mirroring the browser config's `Channel.role`,
   so this stops being convention-dependent.

2. **The task brief that produced this simulator says `state_req` arrives
   "from the control node"; the protocol doc's own message-inventory table
   says the opposite** (`DAQ -> CTR | state_req | node wants the current
   daq_local state re-sent`), and `controlnode/daqnode/client.go` agrees with
   the table (`handleStateReq` runs in the client's *read* loop, i.e. on a
   message *received from* the DAQ node). Implemented per the doc/code (the
   two agree with each other, so they're the trustworthy pair): daqsim only
   ever **sends** `state_req` (`Simulator.RequestState`), and would log a
   protocol violation if the control node ever sent one to it.

3. **`abort_rules` are documented as monitored "while the entry_sequence
   runs," but the shipped `config/machines/daq001.sm` arms one with a window
   (`from ... to 20000ms`) longer than the entry_sequence itself is ever
   scheduled to take** (a 3000ms burn at defaults). Nothing in the doc says
   whether monitoring is meant to continue past the entry_sequence's own
   completion up to `t_ms_off`, or whether it's implicitly bounded by
   `sequence_complete` ending the run. daqsim monitors only for the
   entry_sequence's own duration (t=0 through its last step) — once
   `sequence_complete` is sent the machine has already left the daq_local
   state on the control node side, so continuing to watch a superseded rule
   read as the less defensible choice, but this is a judgement call in the
   absence of a spec answer, not a wire-format fact.

4. **A first-ever connection could be mistaken for a reconnect. FIXED.**
   `daqnode.Client.connect()` used to call `handleReconnectState()` — the
   state-uncertain/abort-destination path — after *every* successful
   handshake, with no way to distinguish "this daqNode has never connected
   before" from "this connection just replaced one that dropped." If a
   machine entered a `daq_local` state before that node's very first
   connection completed (plausible during a fast/automated startup sequence,
   or exactly the race `controlnode/integration`'s test harness had to work
   around — see `harness.waitConnected`'s doc comment), the first-ever
   `state_update` was silently never delivered (it was queued for a socket
   that didn't exist yet) and the first successful connect immediately fired
   the abort destination for a sequence that was never actually armed on the
   node, reported as a "reconnect" that had never happened.

   Fixed: `Client` now tracks `hasConnected` and only takes the reconnect
   path (`handleReconnectState` / `EngineController.NotifyDaqReconnect`) once
   a handshake has genuinely succeeded before. The very first handshake takes
   a distinct `handleFirstConnectState` / `NotifyDaqFirstConnect` path — same
   corrective action (fire the state's declared abort destination), worded
   differently so operators and logs never conflate "this node just dropped
   and came back" with "this node has never been talked to before and a
   sequence was already believed to be running on it." The sibling bug this
   surfaced — `SendStateUpdate` queueing a payload with no visible error when
   the node isn't connected at all — is fixed alongside it: see
   `docs/websocket-protocol.md`'s "Reconnect behaviour" section. Covered by
   `controlnode/daqnode/engine_test.go`'s
   `TestClient_ReconnectWhileRunningIsStateUncertain` (drives a genuine first
   connect then a genuine reconnect through `Client.Run` against
   `dropAfterOneServer` and asserts each takes its own path) and
   `controlnode/integration/daqsim_e2e_test.go`'s
   `TestFirstConnectWhileAlreadyRunningIsDistinctFromReconnect` /
   `TestStateUpdateUndeliverableWhileDisconnected` (both against a real
   daqsim connection, using `newHarnessDeferredConnect` to hold the node's
   first connection back until after the machine is already in a `daq_local`
   state).

## What the end-to-end tests prove (`controlnode/integration`)

| Test | Proves | Bug class it would catch |
|---|---|---|
| `TestNominalBurn` | `entry_sequence` set-points from the real, compiled `daq001.sm` arrive at daqsim in the documented order and at the documented relative `t_ms`; `sequence_complete` echoes the `runId`; the machine completes back to `safe`. | A send-time resolution bug, a DSL-to-`t_ms` compilation bug, or a `runId` plumbing bug that a hand-written fake (which only asserts what the control node is *believed* to send) could never catch, because the fake and the code under test would share the same wrong assumption. |
| `TestAbort` | A live abort_rule trip on a real node connection runs `exit_sequence` locally and reports `abort_triggered`; the engine lands in the declared abort destination; the igniter step scheduled after the trip never fires. | A race or ordering bug between abort detection and the entry_sequence's own timed steps — exactly the kind of bug that only shows up when both are actually running concurrently against real timing, not when a fake just asserts "abort_triggered was sent." |
| `TestStaleSequenceCompleteIgnored` | A `sequence_complete` echoing a runId that doesn't match the currently-armed run is rejected — proven against a real node connection, with the rejection's log line captured (`sequence_complete for runId 1000 ignored (current run 1)`). | The exact correlation gap `runId` was added to close (`docs/TODO.md`'s "LabVIEW: echo runId" item) — a regression here means a duplicate/delayed report from a real node could silently re-apply a stale completion transition. |
| `TestReconnectMidSequence` | Dropping the connection mid-`autoSequence` makes the engine treat the state as uncertain, fire the abort destination, and — critically — never re-send `state_update` (confirmed by daqsim recording exactly one `autoSequence` run, with the original `runId`, and the igniter firing at most once). | The single most dangerous regression this protocol area could have: a reconnect that re-arms and re-fires a live igniter sequence. |

Together these are the first tests in this repo that run the post-restructure
`controlNode <-> daqNode` protocol against *something that actually executes
it*, rather than a fake that could share the same wrong assumption as the
code under test.
