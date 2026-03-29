// =============================================================================
// Helpers & utilities
// =============================================================================

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
