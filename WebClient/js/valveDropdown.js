// =============================================================================
// Valve command panel
// =============================================================================
// Design: docs/design/sensor-object-options.html, section COMMAND.
// Shown when the user left-clicks a valve object on the Front Panel.
//
// Three rules from that page drive everything below, and none of them are
// negotiable:
//
//   COMMANDS COME FIRST.   The command control is the top of the panel, not a
//                          row buried in a channel list. Live channel values
//                          are read-only rows UNDER it.
//   IT OPENS BELOW THE VALVE. The panel is centred under the valve glyph, its
//                          top edge a small gap under the glyph's bottom edge,
//                          so the glyph being commanded is never covered by the
//                          panel that commands it.
//   IT DOES NOT RELAYOUT.  A state change only ever toggles a class. No node is
//                          added, removed, reordered, resized or relabelled, so
//                          open -> closed -> open never chases a moving button.
//
// Closing: the header's X ALWAYS closes its own panel, pinned or not. Esc closes
// every UNPINNED panel at once — the whole unpinned set comes down together, not
// one panel per press — but never touches a pinned one; a pinned panel closes
// only by its own X or by closeValveDropdown(true). Pin otherwise governs one
// thing: whether a click OFF the panel dismisses it. That is what lets an
// operator keep several panels open at once, which in turn is why this module
// tracks a COLLECTION of panels rather than a single one.
//
// Class names follow the DOM mockup on the design page (.vp, .vp-head, .seg,
// .is-now / .is-next, .vp-rows). Every rule in style.css is scoped under .vp.
// =============================================================================

// Open panels, keyed by front-panel object id. The old single `_valvePanel`
// could not express the pinned case at all.
const _valvePanels = new Map();     // valveObj.id -> record

// Last value seen per channel refDes, kept whether or not a panel is open. A
// panel opened between broadcasts then starts on the correct segment instead of
// flickering onto it a frame later.
const _valveLastValue = new Map();  // channel refDes -> value

let _valveZTop = 700;               // z-index counter; raising a panel bumps it

const VALVE_PRESETS = [0, 25, 50, 100];

// ── Dismiss on a click off the panel ─────────────────────────────────────────
// Pin also governs Esc, below.
document.addEventListener('pointerdown', e => {
    if (_valvePanels.size === 0) return;
    for (const rec of Array.from(_valvePanels.values())) {
        if (rec.el.contains(e.target)) {
            _vpRaise(rec);
            return;             // a click on a panel dismisses nothing, ever
        }
    }
    for (const rec of Array.from(_valvePanels.values())) {
        if (!rec.pinned) _vpDestroy(rec);
    }
});

// ── Esc ──────────────────────────────────────────────────────────────────────
// Closes every UNPINNED panel at once — the whole unpinned set comes down on
// one press, not one panel per press. A pinned panel does not respond to Esc
// at all; it closes only by its own ✕ button, or by closeValveDropdown(true).
document.addEventListener('keydown', e => {
    if (e.key !== 'Escape' || _valvePanels.size === 0) return;
    for (const rec of Array.from(_valvePanels.values())) {
        if (!rec.pinned) _vpDestroy(rec);
    }
});

// =============================================================================
// Open
// =============================================================================

// openValveDropdown builds and places one panel. clientX/clientY are the pointer
// position of the click that opened it, kept as a fallback anchor point.
// glyphRect, when given, is the valve glyph's getBoundingClientRect() — the
// panel is centred under it, a small gap below its bottom edge, so the glyph
// stays visible under the panel that commands it.
function openValveDropdown(valveObj, clientX, clientY, glyphRect, tabId) {
    const already = _valvePanels.get(valveObj.id);
    if (already) {
        // Re-clicking the object must not move or rebuild the panel — moving it
        // is exactly the mouse race this redesign removes.
        _vpRaise(already);
        return;
    }

    // Opening a panel clears the unpinned ones. Pinned panels stay: that is the
    // whole point of the pin.
    for (const rec of Array.from(_valvePanels.values())) {
        if (!rec.pinned) _vpDestroy(rec);
    }

    const ctrl = controlIndex[valveObj.controlRefDes] || null;
    const channels = (ctrl && ctrl.channels) ? ctrl.channels : [];
    const cmdChs = channels.filter(c =>
        c.role === 'cmd-bool' || c.role === 'cmd-pct' || c.role === 'cmd-float');
    // The readable channel. _valveSubtypeInfo calls the same thing feedback.
    const fbCh = channels.find(c => c.role === '' || c.role === 'sensor') || null;

    const el = document.createElement('div');
    el.className = 'vp';

    // ── Header ───────────────────────────────────────────────────────────────
    const head = document.createElement('div');
    head.className = 'vp-head';

    const title = document.createElement('span');
    title.className = 'vp-title';
    title.textContent = (ctrl ? ctrl.refDes : valveObj.controlRefDes) || '(no control)';

    const desc = document.createElement('span');
    desc.className = 'vp-desc';
    desc.textContent = (ctrl && ctrl.description) ? ctrl.description : '';

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

    // ── Command block — first, always ────────────────────────────────────────
    const cmdWrap = document.createElement('div');
    cmdWrap.className = 'vp-cmd';
    el.appendChild(cmdWrap);

    const syncs = [];      // () => void, re-read _valveLastValue and set classes
    let anchorEl = null;   // the element laid under the pointer

    if (cmdChs.length === 0) {
        const hint = document.createElement('div');
        hint.className = 'vp-none';
        hint.textContent = ctrl ? 'No command channel.' : 'No control configured.';
        cmdWrap.appendChild(hint);
        anchorEl = hint;
    } else {
        for (const ch of cmdChs) {
            // A control with two command channels (a POS/NEG pair, say) gets one
            // block each, labelled. One channel gets no label — the header
            // already names it.
            if (cmdChs.length > 1) {
                const lbl = document.createElement('div');
                lbl.className = 'vp-cmd-label';
                lbl.textContent = ch.refDes;
                cmdWrap.appendChild(lbl);
            }
            const built = (ch.role === 'cmd-bool')
                ? _vpBuildIoControl(ch, fbCh)
                : _vpBuildPositionControl(ch);
            cmdWrap.appendChild(built.el);
            syncs.push(built.sync);
            if (!anchorEl) anchorEl = built.anchor;
        }
        // The io (cmd-bool) case has no hint here: dark/light is legible from the
        // segment labels alone. The positionable case still gets one below.
        if (!cmdChs.some(c => c.role === 'cmd-bool')) {
            const hint = document.createElement('span');
            hint.className = 'vp-hint';
            hint.textContent = 'presets first · type only when you need a number between';
            cmdWrap.appendChild(hint);
        }
    }

    // ── Live rows — read-only, below the command ─────────────────────────────
    const valueEls = new Map();   // channel refDes -> value element
    if (channels.length) {
        const rows = document.createElement('div');
        rows.className = 'vp-rows';
        for (const ch of channels) {
            const row = document.createElement('div');
            row.className = 'vp-row';

            const r = document.createElement('span');
            r.className = 'r';
            r.textContent = ch.refDes + (ch.units ? ' ' + ch.units : '');

            const v = document.createElement('span');
            v.className = 'v stale';
            v.textContent = _valveLastValue.has(ch.refDes)
                ? _vpFormat(_valveLastValue.get(ch.refDes)) : '--';
            if (_valveLastValue.has(ch.refDes)) v.classList.remove('stale');

            row.append(r, v);
            rows.appendChild(row);
            valueEls.set(ch.refDes, v);
        }
        el.appendChild(rows);
    }

    // ── Record ───────────────────────────────────────────────────────────────
    const rec = {
        el,
        valveId: valveObj.id,
        tabId,
        pinned: false,
        valueEls,
        syncs,
        z: 0,
    };

    pinBtn.addEventListener('click', () => {
        rec.pinned = !rec.pinned;
        pinBtn.classList.toggle('pinned', rec.pinned);
        pinBtn.title = rec.pinned
            ? 'Pinned: a click off the panel leaves it open'
            : 'Pin: keep this panel when you click off it';
    });
    // The X always closes ITS panel regardless of pin — pin only governs
    // click-off and Esc.
    closeBtn.addEventListener('click', () => _vpDestroy(rec));

    // ── Place it ─────────────────────────────────────────────────────────────
    el.style.position = 'fixed';
    el.style.visibility = 'hidden';
    el.style.left = '0px';
    el.style.top  = '0px';
    document.body.appendChild(el);

    // Measure, then place the whole panel centred under the glyph, its top
    // edge VALVE_GAP below the glyph's bottom edge — never on top of the valve
    // it commands. This is computed ONCE, here, at open time: no relayout rule
    // means it is never recomputed while the panel is open, so re-clicking the
    // same valve (handled above, before this point) only raises it.
    const pr = el.getBoundingClientRect();
    const gr = glyphRect || (anchorEl || el).getBoundingClientRect();
    const VALVE_GAP = 8;
    let left = gr.left + gr.width / 2 - pr.width / 2;
    let top  = gr.bottom + VALVE_GAP;
    const vh0 = window.innerHeight;
    if (top + pr.height + 4 > vh0) {
        // Not enough room below: flip above the glyph rather than run off the
        // bottom of the viewport.
        top = gr.top - VALVE_GAP - pr.height;
    }

    const vw = window.innerWidth, vh = vh0;
    left = Math.max(4, Math.min(left, vw - pr.width  - 4));
    top  = Math.max(4, Math.min(top,  vh - pr.height - 4));
    el.style.left = left + 'px';
    el.style.top  = top  + 'px';
    el.style.visibility = '';

    _valvePanels.set(rec.valveId, rec);
    _vpRaise(rec);
    for (const s of rec.syncs) s();
}

// =============================================================================
// Command controls
// =============================================================================

// _vpBuildIoControl is the segmented pair. Both halves exist from the start, in
// a FIXED order (CLOSED then OPEN) with FIXED labels and equal widths, so a
// state change is a class swap and nothing on screen moves.
//
// Dark (.is-now) is where the valve is, light (.is-next) is what you can do.
// Not green: green reads as "good", when all it means is "current".
function _vpBuildIoControl(ch, fbCh) {
    const el = document.createElement('div');
    el.className = 'seg';

    const closeB = document.createElement('button');
    closeB.type = 'button';
    closeB.textContent = 'CLOSED';
    closeB.addEventListener('click', () => sendCommand(ch.refDes, 0));

    const openB = document.createElement('button');
    openB.type = 'button';
    openB.textContent = 'OPEN';
    openB.addEventListener('click', () => sendCommand(ch.refDes, 1));

    markCmdWidget(closeB);
    markCmdWidget(openB);
    el.append(closeB, openB);

    // Where the valve IS: the readable channel when there is one and it has
    // reported, otherwise the command, which is then the only fact there is.
    const current = () => {
        if (fbCh && _valveLastValue.has(fbCh.refDes)) {
            const v = _valveLastValue.get(fbCh.refDes);
            if (v !== null && v !== undefined) return !!v;
        }
        if (_valveLastValue.has(ch.refDes)) {
            const v = _valveLastValue.get(ch.refDes);
            if (v !== null && v !== undefined) return !!v;
        }
        return null;   // nothing known yet: neither half claims to be current
    };

    const sync = () => {
        const isOpen = current();
        _vpSegState(openB,  isOpen === true);
        _vpSegState(closeB, isOpen === false);
    };

    // The half that is actionable RIGHT NOW is what goes under the pointer.
    const isOpen = current();
    return { el, sync, anchor: (isOpen === true) ? closeB : openB };
}

// _vpBuildPositionControl is presets plus a typed entry. There is deliberately
// NO SLIDER: a drag on a live stand passes through every value between where it
// started and where it stopped. Presets and a typed entry only ever command the
// value that was meant.
function _vpBuildPositionControl(ch) {
    const el = document.createElement('div');
    el.className = 'vp-pos';

    const isPct = ch.role === 'cmd-pct';

    // Presets exist only for a percentage valve; a bare cmd-float has no
    // meaningful 0/25/50/100, so it gets the typed entry alone.
    let seg = null;
    const presetBtns = [];
    if (isPct) {
        seg = document.createElement('div');
        seg.className = 'seg';
        for (const p of VALVE_PRESETS) {
            const b = document.createElement('button');
            b.type = 'button';
            b.textContent = String(p);
            b.addEventListener('click', () => sendCommand(ch.refDes, p));
            markCmdWidget(b);
            seg.appendChild(b);
            presetBtns.push({ btn: b, value: p });
        }
        el.appendChild(seg);
    }

    const entry = document.createElement('div');
    entry.className = 'vp-entry';

    const inp = document.createElement('input');
    inp.type = 'number';
    inp.className = 'vp-entry-input';
    if (isPct) { inp.min = 0; inp.max = 100; }
    inp.placeholder = isPct ? '0–100' : '0';

    const setBtn = document.createElement('button');
    setBtn.type = 'button';
    setBtn.className = 'vp-set';
    setBtn.textContent = 'SET';
    setBtn.addEventListener('click', () => {
        const v = parseFloat(inp.value);
        if (!isNaN(v)) sendCommand(ch.refDes, v);
    });
    inp.addEventListener('keydown', e => {
        if (e.key === 'Enter') setBtn.click();
    });

    markCmdWidget(inp);
    markCmdWidget(setBtn);
    entry.append(inp, setBtn);
    el.appendChild(entry);

    const sync = () => {
        const raw = _valveLastValue.get(ch.refDes);
        const v = (typeof raw === 'number') ? raw : null;
        for (const p of presetBtns) {
            // A preset is a command target, so it matches the COMMANDED value.
            _vpSegState(p.btn, v !== null && Math.round(v) === p.value);
        }
        // The live value is shown as the placeholder, never written into the
        // field: overwriting what the operator is typing would be its own
        // mouse race. Fixed-width field, so this cannot relayout anything.
        if (document.activeElement !== inp && inp.value === '') {
            inp.placeholder = (v === null)
                ? (isPct ? '0–100' : '0') : _vpFormat(v);
        }
    };

    return { el, sync, anchor: isPct ? seg : entry };
}

// _vpSegState toggles the two segment classes. It NEVER touches text, size,
// order or padding — that is the no-relayout guarantee, in one function.
function _vpSegState(btn, isNow) {
    btn.classList.toggle('is-now', isNow);
    btn.classList.toggle('is-next', !isNow);
}

// =============================================================================
// Close
// =============================================================================

// closeValveDropdown keeps its old call signature: pid.js calls it with false
// from the canvas pan handler.
//   force === true  -> every panel closes, pinned included
//   force !== true  -> only unpinned panels close
function closeValveDropdown(force) {
    for (const rec of Array.from(_valvePanels.values())) {
        if (force || !rec.pinned) _vpDestroy(rec);
    }
}

function _vpDestroy(rec) {
    rec.el.remove();
    _valvePanels.delete(rec.valveId);
}

function _vpRaise(rec) {
    _valveZTop += 1;
    rec.z = _valveZTop;
    rec.el.style.zIndex = String(_valveZTop);
}

// setValvePanelsTabVisibility runs on every tab switch (tabs.js activateTab).
// The panels are position:fixed and appended to document.body — not inside
// any tab-content element — so hiding a tab-content pane never touches them.
// Without this they float on top of whatever tab is now active. Pin only
// ever governed click-off/Esc dismissal, never this: a pinned panel must
// still vanish while its P&ID tab is not on screen, then reappear untouched
// (same binding, same position) when that tab comes back. This is visibility
// only — the record, and the DOM node, are never destroyed by a tab switch.
function setValvePanelsTabVisibility(activeTabId) {
    for (const rec of _valvePanels.values()) {
        rec.el.style.display = (rec.tabId === activeTabId) ? '' : 'none';
    }
}

// =============================================================================
// Live values (called from rebindPidLiveData in pid.js on every data update)
// =============================================================================

// updateValveDropdownValue records the value for every channel it is told about
// — open panel or not — and refreshes EVERY open panel, not just one.
function updateValveDropdownValue(valveId, refDes, value) {
    _valveLastValue.set(refDes, value);

    for (const rec of _valvePanels.values()) {
        const el = rec.valueEls.get(refDes);
        if (el) {
            el.textContent = _vpFormat(value);
            el.classList.remove('stale');
        }
        // The segment state can depend on a channel this panel does not list
        // (a command echoed on another control's channel never matches, and
        // syncing is cheap), so every panel re-reads on every update.
        for (const s of rec.syncs) s();
    }
}

function _vpFormat(value) {
    if (typeof value === 'number') {
        return Number.isInteger(value) ? String(value) : value.toFixed(2);
    }
    if (value === null || value === undefined) return '--';
    return String(value);
}
