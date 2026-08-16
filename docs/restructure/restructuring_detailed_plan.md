# controlNode Restructure — Detailed Plan

Derived from `restructuring_highlevel_plan.md` + design Q&A (2026-08-13).
Language spec: `dsl_spec.md`.

## Locked decisions

1. **Custom DSL** parsed in Go at controlnode startup, compiled to in-memory Go structures.
2. **Staged delivery** — each phase leaves `go test ./...` green and the exe buildable.
3. **Clean break** — old YAML SM format (`config/control/*.yaml`, `config/control.go`)
   is removed; `daq001_control.yaml` is hand-ported to the new `.sm` format.
4. **daqNode side: protocol only** — DAQ-local abort/T-0 states compile down to the
   *existing* `state_update` payload (entry/exit sequences + abort_rules); LabVIEW
   execution unchanged. Non-LabVIEW CI daqNode is future work.
5. **Execution moves to the controlnode** — controller blocks tick on the controlnode;
   sequences run in goroutines sending `cmd`s to daqNodes; only `daq_local` states are
   cached on the DAQ.
6. **Machines are global and named**, not per-daqNode; interact via `machine.X.state`
   and shared software channels.
7. **Software channels** move to DSL-style `.chan` files (own file type, own directory).
8. **Alerts** get a dedicated `.alert` config with per-daqNode templates; server becomes
   the single alert source.

## Phases

### Phase 1 — DSL front-end (`controlnode/dsl/`)
Lexer (indentation, hyphenated identifiers, durations), recursive-descent parser → AST,
expression evaluator over a `ChannelSpace` interface, compile-time reference checking.
No integration yet. Heavy unit tests (this package underpins everything).

### Phase 2 — `.chan` software channels
`.chan` parser on top of dsl package; rework `softchan/store.go`: settable channels keep
persistence + broker publish; add computed channels recomputed per tick (dependency
order, cycle detection). Delete `softChannels.yaml` path; port existing channels to
`config/channels/softchannels.chan`. Webclient `softchan_config` message unchanged.

### Phase 3 — `.sm` state machine engine
New `statemachine/` package (engine tick loop, controller execution, sequence
goroutines with sleep/wait_until/cancel-on-transition, operator target handling,
`SM-<NAME>-STATE` publishing, `state_change`/`state_config` messages preserved for the
browser). `daq_local` compiler → existing DAQ `state_update` JSON. Port
`daq001_control.yaml` → `config/machines/fuel.sm` (name TBD by port). Remove
`config/control.go` + `daqnode/statemachine.go` handshake logic (DAQ `state_req` now
answered from cached compiled payloads).

### Phase 4 — `.alert` alerts
`.alert` parser + server-side alert engine (rule evaluation per tick, per-daqNode
templates for disconnect/reconnect/bad_data/stale). Move alert construction out of
browser JS (`alerts.js`, `ws.js` handleDaqError/handleBadData become render-only).
Existing `alert`/`alert_snapshot`/`ack_alert` wire messages reused.

### Phase 5 — cleanup + docs
Fix stale `run.bat` flags, update `docs/websocket-protocol.md`, write
`docs/dsl-guide.md` user guide, document controlNode→daqNode JSON messages
(closes TODO), prune dead config sections.

**Self-documentation route:** add `/docs` to the webclient HTTP server —
HTML pages generated at runtime from the compiled DSL structures (all channels with
units/bounds/compute expressions, all machines with state/transition graphs, all alert
rules), plus embedded markdown guides. Always in sync with the loaded config; no
external toolchain. Dev-side Go API docs stay `go doc` / local `godoc` (no packaging).

## Constraints for all phases

- WebClient wire protocol stays compatible except where noted; `webclient/protocol_test.go`
  must stay green and be extended, not weakened.
- Vendored deps only (`go mod vendor`); no new external deps without approval.
- Build via `controlnode\build.bat`; test via `go test -mod=vendor ./...`.
- JS sources edited in `WebClient/js/`, never `controlnode/static/`.

## Verification

Per phase: unit tests + `go test ./...`. End-to-end after Phase 3: run controlnode
against a fake DAQ WS server (pattern exists in `daqnode` tests), command a transition
from the browser, watch sequence cmds flow. Final: full build + manual smoke with the
web client.

---

## As-built notes (appended after Phase 5)

The plan above is the historical record and is left as written. Where the
finished implementation differs, it differs like this:

**Naming / layout**

- The ported machine is `config/machines/daq001.sm` declaring `machine fuelSeq`,
  not `fuel.sm` (Phase 3 left the name "TBD by port"). The file is named after
  the node whose config it replaced; the machine is named after what it does.
- Software channels live in `config/channels/softchannels.chan` as planned.
  Alerts live in `config/alerts/alerts.alert`.

**Semantics that were decided during implementation** (all now in
`dsl_spec.md` under "Resolved semantics"): `if`/`elif`/`else` allowed in
sequences; `wait_until … timeout` *requires* `-> <state>`; engine time is
tick-derived rather than wall-clock; a `daq_local` state may not have a
`controller` block; transition arbitration order (pending requests → controllers
→ sequences) with a per-state epoch so a dead state cannot be resurrected.

**`daq_local` grew beyond "existing payload, unchanged"**

- Assignment values, sleep durations and `abort_rule` thresholds/windows accept
  soft-channel identifiers *and constant arithmetic over them*, resolved at send
  time. This replaced the old `{{VAR}}` substitution and is what lets an absolute
  schedule be written as sequential sleeps (`sleep 2000 - SEQ-IGN-LEAD`).
- `abort_sequence` was added as a required companion to `abort_rule`s. It
  compiles into the payload's `exit_sequence` (a field the LabVIEW node already
  understood) and its trailing `transition` declares the abort destination,
  replacing the hardcoded "abort" state name.
- `state_update` was redefined from "cached config" to **"enter this state
  now"**: sent on state entry rather than at connect, and carrying a monotonic
  `runId` for report correlation. A reconnect while a machine is mid-flight in a
  `daq_local` state is treated as state-uncertain — the engine fires the abort
  destination with an alarm rather than re-sending the state.
- Plan item 4 said "daqNode side: protocol only, LabVIEW execution unchanged".
  That held for the message *shapes*, but the lifecycle changed, so the LabVIEW
  side does need review (tracked in `docs/TODO.md` restructure follow-ups),
  including echoing `runId` back on `sequence_complete`.

**Phase 5 scope changes**

- `run.bat` needed no flag fix — it was already correct (`-config-dir ..\config
  -webroot ..\WebClient`).
- `/docs` was implemented in the `webclient` package (`docs.go`) rather than a
  separate package, rendering directly from `*config.SystemConfig`,
  `*statemachine.Program`, `*softchan.Store` and `*alerts.Config` handed over by
  `Server.SetDocs`. Five pages: overview, channels, machines (with an inline SVG
  state diagram derived from the compiled AST), alerts, protocol.
- "plus embedded markdown guides" narrowed to one: `/docs/protocol` renders
  `docs/websocket-protocol.md`, read from disk when present and falling back to a
  copy embedded at build time (`build.bat` refreshes it, mirroring `static/`).
  The DSL guide stayed a plain file (`docs/dsl-guide.md`) — it is written for
  people editing config in an editor, not for someone reading a running system.
- Config pruning found only one genuinely unread key (`loggingRateHz` in
  `system.yaml`); the rest of the "unused sections" TODO was already obsolete.
  Two pieces of dead Go went with it: `config.BuildStateConfigJSON` (a stub that
  always returned nil) and `CtrNodeDef.WSPort`.
- Added along the way: `dsl.ExprString` / `dsl.StmtLines` (render the compiled
  AST back to source, used by `/docs`) and `dsl.UnresolvedError` /
  `DescribeEvalError`, which distinguish "no value yet for channel X" from
  "unknown channel X" in both the alert engine and the state machine engine.
