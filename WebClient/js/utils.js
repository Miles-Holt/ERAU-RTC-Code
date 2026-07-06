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
