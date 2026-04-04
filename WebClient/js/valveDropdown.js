// =============================================================================
// Valve Dropdown Panel
// =============================================================================
// Shown when the user left-clicks a valve object on the Front Panel.
// Displays all channels of the valve's control with live values, and
// command widgets (OPEN/CLOSE buttons or numeric inputs) for cmd channels.
//
// A "pin" button prevents the panel from closing when the background is
// clicked or another valve is clicked.
// =============================================================================

let _valvePanel = null; // { el, pinned, valveId, valueEls }
// valueEls: Map of refDes → DOM element showing the live value

// ── Background dismiss ────────────────────────────────────────────────────────
document.addEventListener('pointerdown', e => {
    if (_valvePanel && !_valvePanel.pinned && !_valvePanel.el.contains(e.target)) {
        closeValveDropdown(false);
    }
});

// =============================================================================
// Open
// =============================================================================

function openValveDropdown(valveObj, clientX, clientY) {
    if (_valvePanel && _valvePanel.valveId === valveObj.id) return; // already open
    if (_valvePanel && !_valvePanel.pinned) closeValveDropdown(true);
    if (_valvePanel && _valvePanel.pinned)  return; // pinned panel blocks another

    const ctrl = configControls.find(c => c.refDes === valveObj.controlRefDes);

    const el = document.createElement('div');
    el.className = 'valve-panel';

    // ── Header ────────────────────────────────────────────────────────────────
    const header = document.createElement('div');
    header.className = 'valve-panel-header';

    const pinBtn = document.createElement('button');
    pinBtn.className = 'valve-pin-btn';
    pinBtn.title = 'Pin panel open';
    pinBtn.textContent = 'Pin';

    const title = document.createElement('span');
    title.className = 'valve-panel-title';
    if (ctrl) {
        title.textContent = ctrl.refDes + (ctrl.description ? ' \u2014 ' + ctrl.description : '');
    } else {
        title.textContent = valveObj.controlRefDes || '(no control)';
    }

    const closeBtn = document.createElement('button');
    closeBtn.className = 'valve-close-btn';
    closeBtn.title = 'Close';
    closeBtn.textContent = '\u00d7';

    header.append(pinBtn, title, closeBtn);
    el.appendChild(header);

    // ── Channel rows ──────────────────────────────────────────────────────────
    const rows = document.createElement('div');
    rows.className = 'valve-panel-rows';
    el.appendChild(rows);

    const valueEls = new Map(); // refDes → span element

    if (ctrl) {
        for (const ch of (ctrl.channels || [])) {
            const row = document.createElement('div');
            row.className = 'valve-panel-row';

            const label = document.createElement('div');
            label.className = 'valve-row-label';
            const refPart = document.createElement('span');
            refPart.className = 'valve-row-refdes';
            refPart.textContent = ch.refDes;
            label.appendChild(refPart);
            if (ch.units) {
                const unitPart = document.createElement('span');
                unitPart.className = 'valve-row-units';
                unitPart.textContent = ' ' + ch.units;
                label.appendChild(unitPart);
            }

            const valueEl = document.createElement('div');
            valueEl.className = 'valve-row-value stale';
            valueEl.textContent = '--';
            valueEls.set(ch.refDes, valueEl);

            row.appendChild(label);
            row.appendChild(valueEl);

            // Command widgets
            if (ch.role === 'cmd-bool') {
                const cmds = document.createElement('div');
                cmds.className = 'valve-row-cmds';
                const openBtn  = document.createElement('button');
                const closeB   = document.createElement('button');
                openBtn.className  = 'valve-btn-open';
                closeB.className   = 'valve-btn-close';
                openBtn.textContent  = 'Open';
                closeB.textContent   = 'Close';
                openBtn.addEventListener('click', () => sendCommand(ch.refDes, 1));
                closeB.addEventListener('click',  () => sendCommand(ch.refDes, 0));
                cmds.append(openBtn, closeB);
                row.appendChild(cmds);
            } else if (ch.role === 'cmd-pct' || ch.role === 'cmd-float') {
                const cmds = document.createElement('div');
                cmds.className = 'valve-row-cmds';
                const inp = document.createElement('input');
                inp.type = 'number';
                inp.className = 'valve-cmd-input';
                inp.placeholder = ch.role === 'cmd-pct' ? '0\u2013100' : '0';
                if (ch.role === 'cmd-pct') { inp.min = 0; inp.max = 100; }
                const setBtn = document.createElement('button');
                setBtn.className = 'valve-btn-set';
                setBtn.textContent = 'Set';
                setBtn.addEventListener('click', () => {
                    const v = parseFloat(inp.value);
                    if (!isNaN(v)) sendCommand(ch.refDes, v);
                });
                inp.addEventListener('keydown', e => { if (e.key === 'Enter') setBtn.click(); });
                cmds.append(inp, setBtn);
                row.appendChild(cmds);
            }

            rows.appendChild(row);
        }
    } else {
        const hint = document.createElement('div');
        hint.className = 'valve-panel-hint';
        hint.textContent = 'No control configured.';
        rows.appendChild(hint);
    }

    // ── Events ────────────────────────────────────────────────────────────────
    pinBtn.addEventListener('click', () => {
        _valvePanel.pinned = !_valvePanel.pinned;
        pinBtn.classList.toggle('pinned', _valvePanel.pinned);
        pinBtn.title = _valvePanel.pinned ? 'Unpin panel' : 'Pin panel open';
    });

    closeBtn.addEventListener('click', () => closeValveDropdown(true));

    // ── Position ──────────────────────────────────────────────────────────────
    el.style.position = 'fixed';
    el.style.left = '-9999px';
    el.style.top  = '-9999px';
    document.body.appendChild(el);

    // Clamp to viewport after measuring
    const vw = window.innerWidth, vh = window.innerHeight;
    const ew = el.offsetWidth, eh = el.offsetHeight;
    const left = Math.max(4, Math.min(clientX - ew / 2, vw - ew - 4));
    const top  = Math.max(4, Math.min(clientY, vh - eh - 4));
    el.style.left = left + 'px';
    el.style.top  = top  + 'px';

    _valvePanel = { el, pinned: false, valveId: valveObj.id, valueEls };
}

// =============================================================================
// Close
// =============================================================================

function closeValveDropdown(force) {
    if (!_valvePanel) return;
    if (!force && _valvePanel.pinned) return;
    _valvePanel.el.remove();
    _valvePanel = null;
}

// =============================================================================
// Live value updates (called from rebindPidLiveData in pid.js)
// =============================================================================

function updateValveDropdownValue(valveId, refDes, value) {
    if (!_valvePanel || _valvePanel.valveId !== valveId) return;
    const el = _valvePanel.valueEls.get(refDes);
    if (!el) return;
    el.textContent = typeof value === 'number'
        ? (Number.isInteger(value) ? String(value) : value.toFixed(2))
        : String(value);
    el.classList.remove('stale');
}
