# Writing RTC config: the `.sm` / `.chan` / `.alert` guide

This is the practical guide for the people who write test configurations: how to
add a software channel, write a state machine, arm a DAQ-local abort, and read
the compile errors when it does not work.

For the formal rules see [`restructure/dsl_spec.md`](restructure/dsl_spec.md).
For what the control node actually loaded right now, open
**`http://<controlnode>:8000/docs`** — it renders the compiled configuration,
not the files on disk.

| File | Lives in | Defines | Loaded |
|---|---|---|---|
| `*.chan` | `config/channels/` | software channels | at startup, all files |
| `*.sm` | `config/machines/` | one state machine per file | at startup, all files |
| `*.alert` | `config/alerts/` | alert templates and rules | at startup, all files |

**A config error stops the control node.** That is deliberate: a mis-typed
channel name in an abort rule is an abort that never fires. Every error is
printed as `file:line: message` and the process exits.

---

## The rules that apply to all three file types

**Indentation defines blocks**, Python-style. Four spaces is the house style;
tabs work, but mixing tabs and spaces in one file is an error.

**Comments** start with `#` and run to the end of the line.

**Identifiers may contain hyphens** — `PT-01`, `OV-05-CMD`, `SEQ-CUTOFF-T` are
all single names. The consequence you will hit eventually:

```
sleep 2000 - SEQ-IGN-LEAD     # subtraction: spaces required
sleep 2000-SEQ-IGN-LEAD       # ERROR: that is one weird identifier
```

**Values:** integers, floats, `true`/`false`, `"double quoted strings"`, and
durations `100ms`, `5s`, `2m`.

**Operators:** `+ - * / %`, `== != < <= > >=`, `and or not`, `=` for assignment,
`++` / `--` on numeric channels.

**Time is seconds.** Anywhere a duration appears — `sleep`, `wait_until … timeout`,
`abort_rule … from … to …`, or a soft channel that carries a duration
(`SEQ-IGN-LEAD`, `SEQ-CUTOFF-T`) — a bare number means seconds: `sleep 5` sleeps
5 seconds. A suffixed literal normalises to seconds too: `100ms` -> `0.1`,
`5s` -> `5`, `2m` -> `120`; after that, `sleep 100ms` and `sleep 0.1` are the same
value. **The DAQ wire protocol still speaks milliseconds** (`t_ms`, `t_ms_on`,
`t_ms_off`) — that conversion happens only once, when a `daq_local` payload is sent;
nothing else in the DSL ever sees milliseconds.

**Every channel name you reference must exist**, checked against the whole
configuration at startup. There is no silent zero.

---

## `.chan` — software channels

Software channels live in the control node's memory. They show up in the
browser and in the data stream exactly like a sensor, and they persist across
restarts. Two kinds:

**Settable** — an operator (or a machine) writes them:

```
channel SEQ-CUTOFF-T
    type float
    description "Main-valve cutoff time, absolute from sequence start (burn length = this minus valve-open time)"
    units s                         # optional — seconds, the DSL's base time unit
    default 3.0
    min 0.5                         # writes outside [min,max] are rejected
    max 10.0
```

**Computed** — derived every engine tick, read-only, in dependency order:

```
channel PT-FUEL-AVG
    units psi
    compute (PT-01 + PT-02) / 2

channel IGNITION-OK
    type bool
    compute TC-01 > 400 and PT-FUEL-AVG > 300
```

A `compute` expression may reference hardware channels, other computed channels,
and machine states. A dependency **cycle** is a startup error.

Two channels appear automatically for every state machine, so you never declare
them yourself:

| Channel | Direction | Carries |
|---|---|---|
| `SM-<NAME>-STATE` | read-only | current state **index** (matches `state_config`) |
| `SM-<NAME>-TARGET` | writable | requested state — a name from the HMI, an index from another machine |

The running example is [`config/channels/softchannels.chan`](../config/channels/softchannels.chan):
two sequence timings (`SEQ-CUTOFF-T`, `SEQ-IGN-LEAD`) and two abort limits
(`LIM-CPT01-HIGH`, `LIM-CPT01-LOW`). Putting limits in a channel instead of a
literal is the point — the crew can retune them from the HMI between runs
without a rebuild.

---

## `.sm` — state machines

One file, one machine. The **first state in the file is the initial state.**

```
machine fuelSeq

state safe
    operator
    sequence
        NV-01-POS = 0
        ...
```

### The `operator` flag

`operator` means "the crew may command entry to this state from the HMI". A
state without it is only reachable from another state's logic — which is how
`fire` stays unreachable except through `pressurize`.

Everything else can transition into any state; the flag governs *operator*
requests only.

### `operator from a, b` — restricting where a command may come from

An `operator` state can optionally list the states it must currently be
commanded *from*. [`config/machines/daq001.sm`](../config/machines/daq001.sm)
uses this to restore the intended firingSequence transition graph:

```
state safe
    operator from manualControl, abort, postTest

state manualControl
    operator from safe

state autoSequence
    operator from manualControl

state postTest
    # entered only by autoSequence's completion transition — no operator flag

state abort
    operator from manualControl, autoSequence, postTest
```

Read `operator from manualControl, autoSequence, postTest` on `abort` as: the
operator's abort button only works while the machine is currently in
`manualControl`, `autoSequence` or `postTest` — trying to command `abort` from,
say, `safe` is refused. Bare `operator` (no `from`) stays commandable from
anywhere, as before. `postTest` needs no `operator from` of its own: nothing
commands entry to it from the HMI, only autoSequence's own completion
`transition postTest`.

**This restricts operator input only.** It never blocks:
- DAQ-originated aborts (`NotifyAbortTriggered`, e.g. an `abort_rule` tripping
  and the node reporting `abort_triggered`),
- sequence completions (a `daq_local` state's completion transition),
- or any `transition X` statement inside `.sm` controller/sequence code.

Those are the machine's own logic, not a crew request, so autoSequence's
`transition abort` and `transition postTest` above fire regardless of what
`from` says. A gated command rejected at the engine looks like:

```
machine "firingSequence": cannot command "abort" from "safe" (allowed from: manualControl, autoSequence, postTest)
```

### `controller` — runs every tick

```
state pressurize
    controller
        if CPT-01 > LIM-CPT01-HIGH
            transition abort
        HEARTBEAT-CTR++
```

Straight-line statements plus `if` / `elif` / `else`. **No loops, no sleeps, no
`wait_until`** — a controller must finish inside one tick (10 ms at the default
100 Hz). `transition X` ends the tick and switches state immediately, killing
any running sequence.

Guards belong here. If something must be checked continuously, it is a
controller line.

### `sequence` — runs once on entry

```
    sequence
        VENT-CMD = 0
        OV-05-CMD = 1
        wait_until PT-FUEL-AVG > SEQ-TARGET-PRESS timeout 30s -> safe
        sleep 250ms
        transition fire
```

Runs in its own goroutine, top to bottom. Supports assignments, `sleep`,
`wait_until`, `if`/`elif`/`else`, and `transition`.

- `wait_until <expr>` blocks until the expression is true. **If you give it a
  `timeout` you must also give it `-> <state>`** — timeout behaviour is never
  implicit.
- Reaching the end without a `transition` leaves the state active; the
  controller keeps ticking. That is a valid design (`safe` does exactly this).
- A state may have a controller, a sequence, both, or neither. A bare state is a
  legitimate "manual control" mode: it rests until someone moves it.

Time is **tick-derived**, not wall clock: each tick advances 1000/`engineTickRateHz`
ms, and that is what `sleep` and `timeout` measure against. Timing is therefore
consistent with controller reaction times, and deterministic in tests.

### Cross-machine reads

Machines are global — any machine may read or write any channel on any node.
They coordinate through shared software channels or by reading each other's
state:

```
    controller
        if machine.loxSeq.state == "abort"
            transition abort
```

`machine.<name>.state` yields the state **name** as a string, comparable with
`==` against a quoted name — and that string is **checked at compile time**
against `loxSeq`'s real state names, so `machine.loxSeq.state == "abrot"` is
caught before it ships, not discovered the day it silently never fires.

### `command` — driving another machine directly

To *command* another machine, use `command <machine> -> <state>`, legal in
both `controller` and `sequence` blocks:

```
    sequence
        command pressSeq -> engineRunning
```

This is the owner's real orchestration shape — a master firing sequence
driving a subordinate press system, then waiting on its own computed
"at pressure" channel before continuing the burn:

```
channel AT-PRESSURE
    type bool
    compute PT-LOX-AVG > SEQ-TARGET-PRESS and PT-FUEL-AVG > SEQ-TARGET-PRESS

# pressSeq.sm
machine pressSeq

state idle
    operator

state engineRunning
    operator from armed
    sequence
        OV-LOX-PRESS-CMD = 1
        OV-FUEL-PRESS-CMD = 1

# firingSequence.sm (excerpt)
machine firingSequence

state engineBurn
    sequence
        command pressSeq -> engineRunning
        wait_until AT-PRESSURE timeout 30 -> abort
        # ... continue the burn: ignition, mains open, cutoff
```

`command` is **not** an operator command: it bypasses `pressSeq`'s `operator`
flag and any `operator from` gate on `engineRunning` entirely, because gating
exists to stop a human operator from skipping a step, not to stop
`firingSequence`'s own logic from driving a subordinate machine. Note
`engineRunning` above is gated `operator from armed` — an *operator* could
not command it from `idle` — but `firingSequence`'s `command` can, from
wherever `pressSeq` currently is, because it isn't operator input.

`<machine>` and `<state>` are both resolved and validated **at compile time**:
an unknown machine, an unknown state, or commanding your own machine (use
`transition <state>` for that — it is a compile error pointing you there) are
all caught before startup. `command` is the preferred way to drive another
machine from inside a `.sm` file; the older mechanism — writing a numeric
state index to `SM-<name>-TARGET` — still works (the browser still uses it)
but is fragile for `.sm`-to-`.sm` orchestration, since inserting a state
earlier in the file silently retargets every write that names a later index,
and it is still the `operator`-gated path.

---

## `daq_local` — states that run on the DAQ node

Network latency is far too slow for T-0 aborts. A state flagged
`daq_local <NODE>` is compiled at startup into a timed schedule, sent to the
node when the machine enters the state, and executed there in under a
millisecond.

`config/machines/daq001.sm` no longer uses `daq_local` — its `autoSequence`
runs entirely control-node side, gated by `controller` if-statements instead
(see the `operator from` example above). The running example for `daq_local`
is now [`coldflow.sm`](restructure/demo/coldflow.sm), which exercises every
`daq_local` feature end to end (walkthrough:
[`demo_walkthrough.md`](restructure/demo/demo_walkthrough.md)):

```
state abort
    daq_local DAQ001                # compiled + cached on DAQ001 for local execution
    abort_rule CPT-01 > 850 from 0ms to 20000ms
    sequence                        # restricted subset: literal sets + sleeps only
        FV-02-CMD = 0
        OV-05-CMD = 0
        sleep 100ms
        VENT-CMD = 1
    abort_sequence                  # run locally on DAQ001 when the rule trips
        FV-02-CMD = 0
        OV-05-CMD = 0
        VENT-CMD = 1
        transition safe             # abort destination reported to the engine
```

`sleep 100ms` here is `0.1` seconds — the same seconds-based duration as
everywhere else in the DSL. It is converted to `t_ms: 100` only when this
state's payload is serialized for the wire (see "Literals, channel names, and
constant arithmetic" below).

### What is allowed inside a `daq_local` state

Only what can be reduced to a timed schedule:

| Allowed | Not allowed |
|---|---|
| assignments (`OV-05-CMD = 1`) | `controller` block — **compile error** |
| `sleep <duration>` | `wait_until` |
| a **trailing** `transition` | `transition` anywhere else — compile error |
| `abort_rule` lines | `if` / `elif` / `else` |

The restriction is not arbitrary: the node has no expression evaluator, only a
list of `{t_ms, refDes, value}` steps.

### Literals, channel names, and constant arithmetic

Assignment values, sleep durations and abort-rule thresholds/windows may be:

- literals — `1`, `0.25`, `250ms` (the same value: 0.25 seconds), `true`
- **soft-channel identifiers** — `SEQ-IGN-LEAD`, `LIM-CPT01-HIGH`
- **constant arithmetic over them** — `2 - SEQ-IGN-LEAD`

Identifiers are resolved to numbers **at send time** — when the machine enters
the state, and again on a node `state_req`. That is what keeps
operator-tuned limits live: change `LIM-CPT01-HIGH` on the HMI and the next run
arms the new threshold, with no rebuild and no redeploy.

Constant arithmetic is how you express an *absolute* schedule with sequential
sleeps: an igniter that must fire at `SEQ-IGN-LEAD` with the mains opening at a
fixed t = 2 seconds needs its second sleep written as `2 - SEQ-IGN-LEAD`, not a
literal `2` (which would always wait 2 more seconds AFTER the igniter, however
late `SEQ-IGN-LEAD` was set).

If a value cannot be resolved, or a sleep folds to a negative number (someone
set `SEQ-IGN-LEAD` above 2), the payload is **refused** and an alarm is
raised. It is never sent as a 0.

### The two transitions of a `daq_local` state

Both are declared by trailing `transition` lines, and neither is a timed step:

| Line | Meaning | Fires when |
|---|---|---|
| trailing `transition` in `sequence` | **completion transition** | the node reports `sequence_complete` |
| trailing `transition` in `abort_sequence` | **abort destination** | the node reports `abort_triggered` |

A `daq_local` state with `abort_rule`s **must** declare an `abort_sequence` —
the compiler refuses to arm a rule with no local safing action and nowhere for
the machine to go afterwards.

### Lifecycle you should know about

- `state_update` means **"enter this state now."** It is sent on entry, not at
  connect time.
- On node **reconnect** while a machine is in a `daq_local` state, the control
  node does *not* re-send the state (that would restart the burn from t=0). The
  state is treated as uncertain: the engine fires the **abort destination** and
  raises an alarm.
- Sequences are cancelled by any transition, from anywhere. If the controller
  aborts mid-burn, the pending `sleep` never completes.

---

## `.alert` — server-side alerts

The control node is the single source of alerts. The browser only renders them.
Two kinds live in [`config/alerts/alerts.alert`](../config/alerts/alerts.alert):

**A template**, instantiated once per enabled daqNode:

```
template every_daqnode
    on disconnect -> alarm   "{node} disconnected"
    on reconnect  -> info    "{node} reconnected"
    on bad_data   -> warning "{refDes} out of range: {value}"
    on stale 2s   -> warning "{node} data stale"
```

The four events are fixed: `disconnect`, `reconnect`, `bad_data`, `stale`. Only
`stale` takes a duration (the data-receive timeout; default 2000 ms). Severities
are `info`, `warning`, `alarm`.

**Rules**, evaluated every tick after the controllers:

```
alert CHAMBER-HIGH
    if CPT-01 > LIM-CPT01-HIGH
    severity alarm
    message "Chamber pressure high: {CPT-01} psia (limit {LIM-CPT01-HIGH})"
    describe "CPT-01 exceeded the configured high limit. Check the relief valve and confirm the chamber is venting before re-arming."
    latch
```

- Rules are **edge-triggered**: they raise on false → true.
- Without `latch`, the alert clears itself on true → false.
- With `latch`, it stays raised until an operator acknowledges it — which is
  what you want for a transient nobody was watching.
- `{PLACEHOLDER}` interpolates a channel value at raise time. Template messages
  may also use `{node}`, `{refDes}` and `{value}`. A placeholder that names no
  known channel is a **startup error**, not a literal `?` in an alarm.
- `describe "…"` is an **optional** long form alongside `message`, sized for an
  alarm detail panel rather than a one-line board — the same placeholder rules
  apply, and an unknown placeholder in it is a startup error just like in
  `message`. Only rules can have one; template events (`disconnect`,
  `reconnect`, `bad_data`, `stale`) and auto-generated sensor-bounds alerts do
  not.

---

## Compile errors and what they mean

Everything is reported as `file:line: message`.

| Message | What happened | Fix |
|---|---|---|
| `unknown channel "CPT-1"` | a name that no `controls.yaml` channel, `.chan` channel or generated `SM-*` channel matches | check the spelling against `/docs/channels` |
| `transition to unknown state "abrt" in machine "fuelSeq"` | typo'd target, or the state is in a different machine | states are per-machine; use `command <other> -> <state>` to command another one |
| `command: unknown machine "pressSeq" (available machines: ...)` | typo'd machine name in a `command` statement | check the machine name against the `.sm` files that are actually loaded |
| `command: machine "pressSeq" has no state "enginRunning" (available states: ...)` | typo'd target state | check the spelling against `pressSeq.sm`'s states |
| `machine "fuelSeq": "command" may not target its own machine; use "transition safe" for a self-transition` | `command fuelSeq -> safe` written inside `fuelSeq` itself | use `transition safe` — that is what it is for |
| `machine "fuelSeq" has no state "abrot" (valid states: ...)` | typo'd string literal compared against `machine.fuelSeq.state` | check the spelling; this also fires on `.alert` rule conditions |
| `state "x": abort_rule requires daq_local` | abort rules only run on the node | flag the state `daq_local <NODE>` — or make it a controller guard |
| `state "x": daq_local state with abort_rule(s) must declare an abort_sequence` | you armed a rule with no local safing action | add `abort_sequence`, ending in `transition <abort state>` |
| `state "x": abort_sequence must end with "transition <state>"` | no abort destination declared | the last line of `abort_sequence` names the state the engine enters |
| `state "x": daq_local states cannot have a controller block` | controllers run on the control node; this state runs on the DAQ | move the guard to a control-node state, or into an `abort_rule` |
| `sequence: assignment to "X" must be a literal, soft-channel identifier, or constant arithmetic over them` | a `daq_local` step used a sensor or a comparison | the node cannot evaluate expressions — pre-compute it into a soft channel |
| `daq_local blocks allow only assignments, sleeps, and a trailing transition, got *dsl.IfStmt` | branching inside a DAQ-local sequence | branch on the control node; the node runs a fixed schedule |
| `wait_until timeout requires "-> <state>"` | a timeout with no destination | say where it goes on expiry |
| `state "x" controller: sleep is not allowed` | controllers run every tick | use a sequence |
| `machine "m": no states defined` | empty or badly indented file | check the indentation of the first `state` |
| `cycle detected: "A"` | computed channels that depend on each other | break the loop; computes are evaluated in dependency order |
| `message placeholder {X} is not a known channel` | typo in an alert message | placeholders are channel names (plus `{node}`, `{refDes}`, `{value}` in templates) |

Two **runtime** messages worth recognising (they appear in the log and as `CTR`
error alerts, not at startup):

- `no value yet for channel "CPT-01" (nothing has published it since startup)` —
  the channel is configured correctly but nothing has sent a value yet, usually
  because its DAQ node has not connected. Not a config bug.
- `unresolvable reference: "SEQ-CUTOFF-T" — payload refused` — a `daq_local`
  payload could not be resolved at send time; the node was deliberately sent
  nothing.

---

## Where to look next

- `http://<controlnode>:8000/docs` — the loaded configuration, always current
- [`restructure/dsl_spec.md`](restructure/dsl_spec.md) — formal semantics
- [`restructure/demo/coldflow.sm`](restructure/demo/coldflow.sm) plus its
  [walkthrough](restructure/demo/demo_walkthrough.md) — one machine that
  exercises every feature in the language
- [`websocket-protocol.md`](websocket-protocol.md) — the wire messages a
  `daq_local` state compiles into
