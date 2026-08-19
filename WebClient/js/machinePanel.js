// =============================================================================
// State machine command panel
// =============================================================================
// Design: docs/design/sensor-object-options.html, section COMMAND (third
// drawn stage — "State machine — same panel"). Replaces the native <select>
// that used to live inside the daqControl widget's foreignObject.
//
// Structurally this mirrors valveDropdown.js — same panel shell (.vp/.vp-head),
// same open/raise/close/pin/Esc rules, its own Map of open panels so the two
// families dismiss independently. What differs is the command control itself:
// instead of a segmented pair it is a vertical list of every operator-
// accessible state, one row each, with the current state marked and any row
// blocked by an `operator from …` gate drawn dimmed with the reason — the
// thing the old <select> could not do (it silently offered every operator
// state and let the server reject the illegal ones).
//
// GATING DATA: `machineConfig.states[].from` comes straight off the wire in
// state_config (controlnode/webclient/server.go stateConfigState.From, built
// from Program.State.OperatorFrom()) — it is not guessed or invented here.
// See _machineGated below.
//
// PENDING TARGET: there is no equivalent wire signal for "a target was just
// requested and hasn't been reached yet" — see the machinePendingTarget
// comment in state.js. The panel reads that client-side record the same way
// the glyph's accent ring does, so the two never disagree.
// =============================================================================

// Open panels, keyed by front-panel object id — same pattern as
// _valvePanels, and deliberately a separate Map so a valve panel and a
// machine panel dismiss independently of one another.
const _machinePanels = new Map();   // daqControlObj.id -> record

let _machineZTop = 700;             // shares no state with _valveZTop; each family raises its own

// ── Dismiss on a click off the panel ─────────────────────────────────────────
document.addEventListener('pointerdown', e => {
    if (_machinePanels.size === 0) return;
    for (const rec of Array.from(_machinePanels.values())) {
        if (rec.el.contains(e.target)) {
            _mpRaise(rec);
            return;             // a click on a panel dismisses nothing, ever
        }
    }
    for (const rec of Array.from(_machinePanels.values())) {
        if (!rec.pinned) _mpDestroy(rec);
    }
});

// ── Esc ──────────────────────────────────────────────────────────────────────
document.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || _machinePanels.size === 0) return;
    for (const rec of Array.from(_machinePanels.values())) {
        if (!rec.pinned) _mpDestroy(rec);
    }
});

// =============================================================================
// Open
// =============================================================================

// openMachineDropdown builds and places one panel for a daqControl object.
// clientX/clientY are the fallback anchor; glyphRect (getBoundingClientRect
// of the glyph) is preferred, same contract as openValveDropdown.
function openMachineDropdown(obj, clientX, clientY, glyphRect) {
    const already = _machinePanels.get(obj.id);
    if (already) {
        // Re-clicking the object must not move or rebuild the panel.
        _mpRaise(already);
        return;
    }

    for (const rec of Array.from(_machinePanels.values())) {
        if (!rec.pinned) _mpDestroy(rec);
    }

    const machineName = obj.daqRefDes;
    const machineConfig = machineName ? machineStateConfig[machineName] : null;

    const el = document.createElement('div');
    el.className = 'vp';

    // ── Header ───────────────────────────────────────────────────────────────
    const head = document.createElement('div');
    head.className = 'vp-head';

    const title = document.createElement('span');
    title.className = 'vp-title';
    title.textContent = machineName || '(no machine)';

    const desc = document.createElement('span');
    desc.className = 'vp-desc';
    desc.textContent = obj.label || '';

    const pinBtn = document.createElement('button');
    pinBtn.className = 'vp-pin';
    pinBtn.type = 'button';
    pinBtn.textContent = 'PIN';
    pinBtn.title = 'Pin: keep this panel when you click off it';

    const closeBtn = document.createElement('button');
    closeBtn.className = 'vp-x';
    closeBtn.type = 'button';
    closeBtn.textContent = '✕';
    closeBtn.title = 'Close (Esc)';

    head.append(title, desc, pinBtn, closeBtn);
    el.appendChild(head);

    // ── Command block — the state list ──────────────────────────────────────
    const cmdWrap = document.createElement('div');
    cmdWrap.className = 'vp-cmd';
    el.appendChild(cmdWrap);

    let anchorEl = null;
    let sync = () => {};

    if (!machineConfig) {
        const hint = document.createElement('div');
        hint.className = 'vp-none';
        hint.textContent = machineName
            ? 'Waiting for state configuration…'
            : 'No state machine configured.';
        cmdWrap.appendChild(hint);
        anchorEl = hint;
    } else {
        const built = _mpBuildStateList(machineName, machineConfig);
        cmdWrap.appendChild(built.el);
        sync = built.sync;
        anchorEl = built.anchor;

        const hint = document.createElement('span');
        hint.className = 'vp-hint';
        hint.textContent = 'gated targets dimmed, not silently refused';
        cmdWrap.appendChild(hint);
    }

    // ── Record ───────────────────────────────────────────────────────────────
    const rec = {
        el,
        objId: obj.id,
        machineName,
        pinned: false,
        sync,
        z: 0,
    };

    pinBtn.addEventListener('click', () => {
        rec.pinned = !rec.pinned;
        pinBtn.classList.toggle('pinned', rec.pinned);
        pinBtn.title = rec.pinned
            ? 'Pinned: a click off the panel leaves it open'
            : 'Pin: keep this panel when you click off it';
    });
    closeBtn.addEventListener('click', () => _mpDestroy(rec));

    // ── Place it — centred under the glyph, never on top of it ──────────────
    el.style.position = 'fixed';
    el.style.visibility = 'hidden';
    el.style.left = '0px';
    el.style.top  = '0px';
    document.body.appendChild(el);

    const pr = el.getBoundingClientRect();
    const gr = glyphRect || (anchorEl || el).getBoundingClientRect();
    const GAP = 8;
    let left = gr.left + gr.width / 2 - pr.width / 2;
    let top  = gr.bottom + GAP;
    const vh0 = window.innerHeight;
    if (top + pr.height + 4 > vh0) {
        top = gr.top - GAP - pr.height;
    }

    const vw = window.innerWidth, vh = vh0;
    left = Math.max(4, Math.min(left, vw - pr.width  - 4));
    top  = Math.max(4, Math.min(top,  vh - pr.height - 4));
    el.style.left = left + 'px';
    el.style.top  = top  + 'px';
    el.style.visibility = '';

    _machinePanels.set(rec.objId, rec);
    _mpRaise(rec);
    rec.sync();
}

// =============================================================================
// The state list
// =============================================================================

// _mpBuildStateList builds one row per operator-accessible state (machineConfig
// carries only states the operator flag exists for — see machineStateConfig
// in state.js / applyStateConfig in ws.js). Rows are built ONCE; a state
// change only ever toggles classes on the already-built rows, same
// no-relayout rule the valve panel follows.
function _mpBuildStateList(machineName, machineConfig) {
    const el = document.createElement('div');
    el.className = 'vp-states';

    const operatorStates = (machineConfig.states || []).filter(s => s.operator);
    const targetRefDes = machineConfig.targetRefDes;
    const rows = [];   // { btn, why, name, from }

    for (const st of operatorStates) {
        const btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'vp-state';

        const mark = svgN('svg', { width: 12, height: 12, viewBox: '0 0 12 12', class: 'vp-state-mark' });
        mark.appendChild(svgN('path', { d: 'M 6 1.2 L 10.8 6 L 6 10.8 L 1.2 6 Z' }));
        btn.appendChild(mark);

        const nameEl = document.createElement('span');
        nameEl.className = 'vp-state-name';
        nameEl.textContent = st.name;
        btn.appendChild(nameEl);

        const why = document.createElement('span');
        why.className = 'why';
        btn.appendChild(why);

        markCmdWidget(btn);   // auth gating — unauthenticated browsers get no live control
        btn.addEventListener('click', () => {
            // Gating and "already there" are enforced here regardless of the
            // auth-only `disabled` attribute markCmdWidget/updateCommandWidgets
            // manage, because that attribute gets reset on every login/logout.
            if (btn.classList.contains('now') || btn.classList.contains('gated')) return;
            pidRequestMachineTarget(machineName, targetRefDes, st.name);
        });

        el.appendChild(btn);
        rows.push({ btn, why, name: st.name, from: st.from });
    }

    const sync = () => {
        const cur = machineCurrentState[machineName];
        const pending = machinePendingTarget[machineName];
        for (const row of rows) {
            const isNow = cur === row.name;
            const gated = _mpGated(row.from, cur);
            row.btn.classList.toggle('now', isNow);
            row.btn.classList.toggle('gated', gated);
            row.why.textContent = isNow ? 'current'
                : gated ? ('from ' + row.from.join(', '))
                : (pending && pending.target === row.name ? 'pending' : '');
        }
    };

    return { el, sync, anchor: rows[0] ? rows[0].btn : el };
}

// _mpGated mirrors the server's own gate check (statemachine engine.go
// operatorCommandableFrom): a state with no `from` list is always offered; a
// state with one is offered only while the machine is currently in one of
// the listed states. If the current state isn't known yet, fail OPEN — same
// choice the old <select> made — because the server is the authority and
// will reject anything actually out of bounds; the browser must never guess
// a *tighter* answer than it can prove.
function _mpGated(from, currentState) {
    if (!from || !from.length) return false;
    if (!currentState) return false;
    return !from.includes(currentState);
}

// =============================================================================
// Close
// =============================================================================

// closeMachineDropdown mirrors closeValveDropdown's contract.
//   force === true  -> every panel closes, pinned included
//   force !== true  -> only unpinned panels close
function closeMachineDropdown(force) {
    for (const rec of Array.from(_machinePanels.values())) {
        if (force || !rec.pinned) _mpDestroy(rec);
    }
}

function _mpDestroy(rec) {
    rec.el.remove();
    _machinePanels.delete(rec.objId);
}

function _mpRaise(rec) {
    _machineZTop += 1;
    rec.z = _machineZTop;
    rec.el.style.zIndex = String(_machineZTop);
}

// =============================================================================
// Live sync (called from pid.js's _repaintMachineWidgets on every state
// update or pending-target change)
// =============================================================================

// updateMachineDropdownState re-syncs every open panel bound to the given
// machine — not just one, the same reasoning updateValveDropdownValue uses.
function updateMachineDropdownState(machineName) {
    for (const rec of _machinePanels.values()) {
        if (rec.machineName === machineName) rec.sync();
    }
}
