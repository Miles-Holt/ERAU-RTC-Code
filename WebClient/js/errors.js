// =============================================================================
// errors.js — global client-side error safety net.
//
// An uncaught JavaScript error can silently blank part of the UI (e.g. one bad
// P&ID object aborting a whole panel render). In a control room nobody is
// watching the browser console, so these must be made VISIBLE. This module:
//
//   - installs window 'error' + 'unhandledrejection' handlers, and
//   - routes any error through reportClientError(), which logs it and raises a
//     throttled, de-duplicated WARNING in the alert bar (via ingestAlert).
//
// These are BROWSER-LOCAL faults: the control node cannot observe a JavaScript
// exception in one operator's tab, so (like 'cmd-not-sent' in ws.js) this is one
// of the few alerts still constructed client-side. Every alert about the SYSTEM
// comes from the server.
//
// Load this early (before the feature scripts) so it is registered in time to
// catch load-time errors. ingestAlert (alerts.js) may not exist yet at load
// time, but errors are reported at runtime, so the lazy `typeof` guard is enough.
// =============================================================================

const _reportedErrors = new Map();      // dedupe key -> last-reported timestamp
const ERR_THROTTLE_MS = 10000;          // suppress identical errors for 10 s

// reportClientError logs an error and surfaces it to the alert bar (throttled).
// Safe to call from anywhere, including places where the alert bar isn't built.
function reportClientError(err, source, line) {
    const msg = (err && err.message) ? err.message : String(err);
    const key = (source || '') + ':' + msg;

    const now = Date.now();
    const last = _reportedErrors.get(key);
    if (last && now - last < ERR_THROTTLE_MS) return;   // throttle duplicate spam
    _reportedErrors.set(key, now);

    console.error('[client error]', source || '', line != null ? ':' + line : '', err);

    // Never let error reporting itself throw (would re-enter the handler).
    try {
        if (typeof ingestAlert === 'function') {
            const where = source && source !== 'promise'
                ? ' (' + String(source).split('/').pop() + (line != null ? ':' + line : '') + ')'
                : '';
            ingestAlert({
                id:        'clienterr:' + key,   // stable id → replaces, doesn't stack
                category:  'warning',
                message:   'App error: ' + msg + where,
                timestamp: now,
                acked:     false,
            });
        }
    } catch (_) { /* swallow — reporting must not cascade */ }
}

window.addEventListener('error', (e) => {
    reportClientError(e.error || e.message, e.filename, e.lineno);
});

window.addEventListener('unhandledrejection', (e) => {
    reportClientError(e.reason, 'promise');
});
