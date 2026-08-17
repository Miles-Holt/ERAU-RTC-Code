# RTC DSL Specification (v1)

One language family, three file types, all parsed by a shared compiler front-end in the
controlnode and compiled to Go structures at startup. Parse/compile errors abort startup
with `file:line: message`.

| Extension | Location            | Defines                          |
|-----------|---------------------|----------------------------------|
| `.sm`     | `config/machines/`  | one named state machine per file |
| `.chan`   | `config/channels/`  | software channels                |
| `.alert`  | `config/alerts/`    | alert templates and rules        |

## Lexical rules (shared)

- Python-like: indentation defines blocks (4 spaces preferred; tabs accepted; mixed
  indentation within one file is a compile error). Blank lines ignored. `#` starts a comment.
- **Identifiers** match `[A-Za-z_][A-Za-z0-9_.-]*`. Hyphens are part of the identifier
  (refDes like `PT-01`, `OV-05-CMD`). Consequence: **binary minus requires spaces**
  (`a - b`); `PT-01` is always an identifier. Dots access members (`OV-05.inPosition`,
  `machine.fuelSeq.state`).
- Literals: ints, floats, `true`/`false`, double-quoted strings, durations (`100ms`, `5s`,
  `2m`).
- Operators: `+ - * / %`, comparisons `== != < <= > >=`, boolean `and or not`,
  assignment `=`, increment/decrement `++ --` (numeric channels only).

## Time: the base unit is SECONDS

The DSL's canonical time unit is **seconds**, everywhere a duration appears: `sleep`,
`wait_until … timeout`, `abort_rule … from … to …`, and a soft channel used to carry a
duration (`SEQ-IGN-LEAD`, `SEQ-CUTOFF-T`, …).

- A **bare number** in a time position means seconds: `sleep 5` sleeps 5 seconds,
  `timeout 30` is a 30-second timeout.
- A **suffixed literal** normalises to seconds at lex time: `100ms` -> `0.1`, `5s` -> `5`,
  `2m` -> `120`. After that normalisation a suffixed literal and a bare number are the
  same kind of value — `sleep 100ms` and `sleep 0.1` are identical.
- Durations are `float64` end to end (seconds need fractions: `100ms` is `0.1`, not `0`).
- A mixed comparison stays coherent because both sides are already in seconds: a channel
  holding seconds compared against a suffixed literal (`T-TIME < 20000ms`) compares
  like-for-like (`T-TIME < 20`), never seconds against raw milliseconds.

**The DAQ wire protocol is unaffected and still speaks milliseconds.** `daq_local`
payload fields — `DaqStep.TMs` (`t_ms`), `DaqAbortRule.TMsOn`/`TMsOff`
(`t_ms_on`/`t_ms_off`) — are milliseconds on the wire, exactly as before, because that is
what the DAQ node (LabVIEW or daqsim) expects. The seconds-to-milliseconds conversion
happens at exactly one place: `statemachine.resolveDaqLocalState`/`resolveSteps`, the
moment a payload is serialized for sending. Every other stage of compilation and
evaluation — the lexer, the parser, expression evaluation, the engine's `sleep`/
`wait_until` clock — works in seconds only. No other component converts units.

## Expressions

Evaluated against the live channel space: hardware channels, software channels, and
machine state. `machine.<name>.state` yields the machine's current state name (string,
comparable with `==` to a quoted state name). Referencing an unknown channel is a
**compile error** (checked against the full config at load), not a silent 0.

## `.sm` — state machines

```
machine fuelSequence

state safe                     # first state in file = initial state
    operator                   # flag: operator may command entry to this state
    controller                 # runs every engine tick
        if PT-FUEL-AVG > LIM-CPT01-HIGH
            transition abort
        HEARTBEAT-CTR++
    sequence                   # runs once on entry, in its own goroutine
        OV-05-CMD = 0
        wait_until OV-05.closed timeout 5s -> abort
        sleep 250ms
        transition ready

state abort
    daq_local DAQ001           # this state is also compiled + cached on DAQ001
    sequence
        OV-05-CMD = 0
        sleep 100ms
        FV-02-CMD = 0
```

- A file defines exactly one `machine <name>`. Machines are **global**: they may read and
  write any channel on any daqNode or any software channel. Machines interact by reading
  `machine.<other>.state` or shared software channels.
- Each machine auto-publishes a read-only software channel `SM-<NAME>-STATE` (current
  state name) and accepts operator target requests on `SM-<NAME>-TARGET` (only states
  flagged `operator` are accepted).
- **`operator from a, b`**: an optional gate on the `operator` flag restricting which
  states the machine must currently be in for the operator to command entry into this
  one, e.g.:
  ```
  state abort
      operator from manualControl, autoSequence
  ```
  With no `from` clause (bare `operator`), the state is commandable from any current
  state, same as before. `from` with no preceding `operator` on the same state, an
  empty name list, a trailing comma, an unknown state name, and a self-reference are
  all compile errors. **The gate restricts operator input only.** DAQ-originated
  aborts (`NotifyAbortTriggered`), sequence completions, and any `transition` statement
  inside `.sm` code are never gated — they are the machine's own logic, not an operator
  request, and always take effect regardless of `from`. A rejected operator command
  returns `machine %q: cannot command %q from %q (allowed from: %s)`.
- **controller**: straight-line statements + `if/elif/else`; no loops, no sleeps. Runs
  every engine tick while the state is active. `transition X` ends the tick and switches
  state (kills the running sequence).
- **sequence**: statements run sequentially; supports `sleep <duration|expr>`,
  `wait_until <expr> [timeout <duration> -> <state>]`, assignments, `transition X`.
  A state may have controller, sequence, both, or neither (a bare state — e.g. a
  manual-control mode — rests until an operator or another transition moves it). Sequence completion without a
  `transition` leaves the state active (controller keeps ticking).
- **`daq_local <NODE>`**: the state's sequence must be reducible to timed set-steps
  (assignments, `sleep`); the compiler serializes it into the existing DAQ
  `state_update` payload (`entry_sequence` with `t_ms`) and it is cached on the daqNode
  for local (<1 ms) execution. Assignment values, sleep durations, and `abort_rule`
  thresholds/windows may be **literals or soft-channel identifiers**; identifiers are
  resolved to numbers when the payload is sent to the node (connect, reconnect,
  `state_req`) so operator-tuned values (e.g. `SEQ-CUTOFF-T`) stay adjustable, exactly
  like the old `{{VAR}}` behavior. Unresolvable refs at send time are an error, never
  silently 0. Other statements in a `daq_local` state are a compile error.
  Values, durations, and windows may also be **constant arithmetic** over literals and
  soft-channel identifiers (e.g. `sleep 2000 - SEQ-IGN-LEAD`), folded at send time —
  this is how absolute-time schedules are expressed as sequential sleeps. A resolved
  negative sleep is a send-time error (the payload is refused, with an alarm).
  A trailing `transition X` in a `daq_local` sequence is not a timed step: it declares
  the **completion transition** — when the node reports `sequence_complete`, the engine
  transitions the machine to `X`. A `transition` anywhere else in a `daq_local`
  sequence is a compile error.

  A `daq_local` state with `abort_rule`s **must** also declare an `abort_sequence`
  block: timed set-steps (same restrictions as the sequence) serialized into the
  payload's `exit_sequence`, which the DAQ runs locally (<1 ms) when a rule trips.
  Its trailing `transition X` declares the **abort destination**: the state the engine
  enters when the node reports `abort_triggered` (this replaces any hardcoded "abort"
  state name).

  Payload lifecycle: the payload is (re)resolved and sent when the engine **enters**
  the state (`state_update` now means "enter this state now"), and re-sent on DAQ
  `state_req`. On node reconnect the controlnode does **not** re-send a running
  `daq_local` state (re-entry would re-fire the sequence from t=0); instead, a
  reconnect while the machine is in a `daq_local` state is treated as
  state-uncertain and the engine fires the abort destination with an alarm. Abort trigger rules for DAQ-local monitoring are declared:

```
    abort_rule CPT-01 > 850 from 0ms to 20000ms
```

## `.chan` — software channels

```
# operator-settable
channel SEQ-CUTOFF-T
    description "Main-valve cutoff time, absolute from sequence start (burn length = this minus valve-open time)"  # optional, shown in the HMI
    type float
    default 3.0
    min 0.5
    max 10.0
    units s

# computed every tick (read-only; may reference any channel incl. other computed)
channel PT-FUEL-AVG
    units psi
    compute (PT-01 + PT-02) / 2

channel IGNITION-OK
    type bool
    compute TC-01 > 400 and PT-FUEL-AVG > 300
```

- Settable channels keep today's semantics (role derived from type, min/max guards,
  persistence of values across restarts).
- `compute` channels are read-only, recomputed every engine tick, dependency-ordered;
  cycles are a compile error.

## `.alert` — alerts

```
template every_daqnode          # instantiated per configured daqNode automatically
    on disconnect -> alarm "{node} disconnected"
    on reconnect  -> info  "{node} reconnected"
    on bad_data   -> warning "{refDes} out of range: {value}"
    on stale      -> warning "{refDes} stale"

alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psi"
    latch                       # stays raised until operator ack, even if condition clears
```

- Server becomes the single source of alerts (browser only renders). `{name}`
  placeholders interpolate channel values / event fields at raise time.
- Rule alerts are edge-triggered (raise on false→true); non-latching rules auto-clear
  on true→false.

## Engine

- One engine tick loop in the controlnode (rate from `system.yaml`, new key
  `engineTickRateHz`, default 100). Per tick: recompute `.chan` computed channels →
  run every active state's controller → evaluate `.alert` rules.
- Sequences run in per-machine goroutines; `transition` cancels the running sequence
  via context.

## Resolved semantics (locked during implementation)

- `if/elif/else` is allowed in `sequence` blocks too, not just controllers. Loops
  remain absent everywhere.
- `wait_until … timeout <d>` **requires** the `-> <state>` clause (timeout behavior
  must be explicit).
- Engine time is tick-derived, not wall-clock: each tick advances 1/TickHz seconds and
  `sleep`/`timeout` measure against that (deterministic tests, timing consistent with
  controller reaction). The tick period is also published as the read-only,
  engine-provided software channel `CYCLE_TIME` (seconds), auto-registered like the
  `SM-<NAME>-STATE` channels before machines compile — not a `.chan` entry, so an
  operator write is rejected rather than silently corrupting every sequence clock that
  accumulates it (e.g. `T-TIME = T-TIME + CYCLE_TIME`).
- `daq_local` states: the controlnode does **not** re-run the sequence (the DAQ runs
  its cached copy; re-running would double-command valves). Therefore a `daq_local`
  state may not have a `controller` block (compile error) — guards belong on
  controlnode-side states. `abort_rule` lines are only legal inside `daq_local` states.
- Transitioning to the currently-active state re-enters it (restarts its sequence).
- Transition arbitration: per tick, pending requests drain first, then controllers run
  (their `transition` applies that tick), then sequences resume. A sequence transition
  racing a controller abort is discarded (per-state epoch), never resurrecting a dead
  state.
- Writes coerce booleans to 1/0; assigning a string to a channel is a runtime error.
