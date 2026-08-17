// =============================================================================
// Helpers & utilities
// =============================================================================

// isCmd reports whether a channel is a commandable (non-sensor) role.
const isCmd = ch => ch.role === 'cmd-bool' || ch.role === 'cmd-pct' || ch.role === 'cmd-float';

// rebuildConfigIndex refreshes channelIndex / controlIndex from configControls.
// Call after configControls is replaced (on each config message) so lookups stay
// O(1) instead of scanning every control/channel on each call.
function rebuildConfigIndex() {
    for (const k in channelIndex) delete channelIndex[k];
    for (const k in controlIndex) delete controlIndex[k];
    for (const ctrl of configControls) {
        controlIndex[ctrl.refDes] = ctrl;
        for (const ch of (ctrl.channels ?? [])) channelIndex[ch.refDes] = { ctrl, ch };
    }
}

// lookupChannel returns { ctrl, ch } for a channel refDes, or null if unknown.
function lookupChannel(refDes) {
    return channelIndex[refDes] || null;
}

function mkEl(tag, className, text) {
    const e = document.createElement(tag);
    if (className) e.className = className;
    if (text !== undefined) e.textContent = text;
    return e;
}

function debounce(fn, ms) {
    let timer;
    return (...args) => { clearTimeout(timer); timer = setTimeout(() => fn(...args), ms); };
}

// markCmdWidget tags an interactive command control so auth gating can find it.
// Every button/slider/input that sends a cmd must be marked; updateCommandWidgets()
// (auth.js) then enables/disables all `.cmd-widget` elements based on login state.
// Also sets the initial disabled state so widgets built after login start correct.
function markCmdWidget(el) {
    el.classList.add('cmd-widget');
    el.disabled = !operatorName;
    return el;
}

// createChannelSearchDropdown builds the shared regex-search dropdown used to
// find and pick a channel refDes, appended to <body> for unambiguous fixed
// positioning against `.graph-dropdown` (position: fixed). Consolidates the
// dropdown copy-pasted across the graph cell, object sidebar, channel list
// (Data View), and in-panel P&ID graph search bars — those differ only in
// what's already-selected, what happens on pick, and where the dropdown sits
// relative to the input, which is why they're parameters rather than baked in.
//
// input                — the search <input> element to watch/position against
// opts.getExcluded()   — () => Set<refDes> already present; excluded from matches
// opts.onPick(refDes)  — called when the user picks a match (mousedown, so it
//                         fires before the input's blur)
// opts.position        — 'above' (default) or 'below' the input
// opts.styleInputError — if true, toggle .input-error + title on `input` when
//                         the regex is invalid (default false — some call
//                         sites style the error, some just close silently)
// opts.maxResults      — cap on matches shown (default 20)
//
// Returns { dropdownEl, refresh, destroy }. refresh() re-runs the search
// against the input's current value (also called automatically on `input`);
// destroy() removes the dropdown from the DOM.
function createChannelSearchDropdown(input, opts) {
    const { getExcluded, onPick, position = 'above', styleInputError = false, maxResults = 20 } = opts;

    const dropdown = mkEl('div', 'graph-dropdown');
    dropdown.style.display = 'none';
    document.body.appendChild(dropdown);

    const close = () => { dropdown.style.display = 'none'; };

    const clearInputError = () => {
        if (!styleInputError) return;
        input.classList.remove('input-error');
        input.title = '';
    };
    const setInputError = () => {
        if (!styleInputError) return;
        input.classList.add('input-error');
        input.title = 'Invalid regex';
    };

    const position_ = () => {
        const r = input.getBoundingClientRect();
        if (position === 'below') {
            dropdown.style.top    = (r.bottom + window.scrollY + 2) + 'px';
            dropdown.style.left   = (r.left + window.scrollX) + 'px';
            dropdown.style.width  = r.width + 'px';
            dropdown.style.bottom = '';
            dropdown.style.display = '';
        } else {
            dropdown.style.left    = r.left + 'px';
            dropdown.style.width   = r.width + 'px';
            dropdown.style.top     = '-9999px';   // off-screen while measuring
            dropdown.style.bottom  = '';
            dropdown.style.display = '';
            const h = dropdown.offsetHeight;
            dropdown.style.top = Math.max(4, r.top - h) + 'px';
        }
    };

    const refresh = debounce(() => {
        const q = input.value.trim();
        if (!q) { clearInputError(); close(); return; }
        let re;
        try { re = new RegExp(q, 'i'); clearInputError(); }
        catch { setInputError(); close(); return; }

        const excluded = getExcluded ? getExcluded() : new Set();
        const matches = [];
        for (const ctrl of configControls) {
            for (const ch of (ctrl.channels ?? [])) {
                if (!excluded.has(ch.refDes) &&
                    (re.test(ch.refDes) || re.test(ctrl.description || ''))) {
                    matches.push({ refDes: ch.refDes, desc: ctrl.description || '' });
                }
            }
        }
        const trimmed = matches.slice(0, maxResults);
        dropdown.innerHTML = '';
        if (!trimmed.length) { close(); return; }
        for (const { refDes, desc } of trimmed) {
            const item = mkEl('div', 'graph-dropdown-item');
            item.appendChild(mkEl('span', 'graph-dropdown-refdes', refDes));
            if (desc) item.appendChild(mkEl('span', 'graph-dropdown-desc', desc));
            item.addEventListener('mousedown', (e) => {
                e.preventDefault();
                onPick(refDes);
                input.focus();
                refresh();
            });
            dropdown.appendChild(item);
        }
        position_();
    }, 150);

    input.addEventListener('input', refresh);
    input.addEventListener('blur', () => setTimeout(close, 150));

    return {
        dropdownEl: dropdown,
        refresh,
        destroy: () => dropdown.remove(),
    };
}

function setStatus(state, text) {
    document.getElementById('status-indicator').className = `status-indicator status-${state}`;
    document.getElementById('status-text').textContent = text;
}

function updateTimestamp(unixSeconds) {
    const el = document.getElementById('timestamp');
    if (!el) return;
    el.textContent = new Date(unixSeconds * 1000).toLocaleTimeString('en-US', {
        hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3
    });
}
