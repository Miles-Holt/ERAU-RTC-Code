// =============================================================================
// App bootstrap — theme, tab init, intervals, WebSocket connect
//
// The WebSocket protocol lives in ws.js; the message schema is documented in
// docs/websocket-protocol.md.  (This file used to carry a protocol comment that
// described a single LabVIEW→Browser socket; that is obsolete — see ws.js for the
// current /ws/data + /ws/ctrl design.)
// =============================================================================

// =============================================================================
// Theme toggle
// =============================================================================

(function () {
    const html    = document.documentElement;
    const btn     = document.getElementById('theme-btn');
    const moon    = document.getElementById('theme-icon-moon');
    const sun     = document.getElementById('theme-icon-sun');
    const PREF_KEY = 'rtc-theme';

    function applyTheme(theme) {
        if (theme === 'light') {
            html.setAttribute('data-theme', 'light');
            moon.style.display = 'none';
            sun.style.display  = 'block';
        } else {
            html.removeAttribute('data-theme');
            moon.style.display = 'block';
            sun.style.display  = 'none';
        }
    }

    applyTheme(localStorage.getItem(PREF_KEY) || 'dark');

    btn.addEventListener('click', () => {
        const next = html.getAttribute('data-theme') === 'light' ? 'dark' : 'light';
        localStorage.setItem(PREF_KEY, next);
        applyTheme(next);
        if (typeof updateAllChartColors === 'function') updateAllChartColors();
    });
})();

document.getElementById('tab-add').addEventListener('click', (e) => {
    e.stopPropagation();
    showAddTabMenu(e.currentTarget);
});

addTab('frontPanel');

setDevMode(devMode);
buildOperatorButton();
updateCommandWidgets();

let _graphInterval = setInterval(updateAllGraphs,    500);
let _dvInterval    = setInterval(updateAllDataViews, 500);
setInterval(refreshDevTabs, 2000);

function setLiveUpdateRate(hz) {
    const ms = Math.round(1000 / Math.max(1, hz));
    clearInterval(_graphInterval);
    clearInterval(_dvInterval);
    // Graph renders are expensive (Chart.js canvas repaints); cap at 10 Hz regardless of data rate.
    _graphInterval = setInterval(updateAllGraphs,    Math.max(ms, 100));
    _dvInterval    = setInterval(updateAllDataViews, ms);
}
connect();
connectCtrl();

// One-time boot hint overlay
(function showBootHint() {
    const overlay = document.createElement('div');
    overlay.className = 'boot-overlay';
    overlay.innerHTML = `
        <div class="boot-hint">
            <div>Click <b>+</b> to add a tab of any type, or right-click a tab to change it — or click a shortcut below</div>
            <div style="margin-top:6px">
                <span class="boot-hint-type-btn" data-type="frontPanel">Front Panel</span>
                &nbsp;·&nbsp;
                <span class="boot-hint-type-btn" data-type="dataView">Channel List</span>
                &nbsp;·&nbsp;
                <span class="boot-hint-type-btn" data-type="graph">Graph</span>
                &nbsp;·&nbsp;
                <span class="boot-hint-type-btn" data-type="console">Console</span>
                &nbsp;·&nbsp;
                <span class="boot-hint-type-btn" data-type="dev">Dev</span>
            </div>
            <div style="margin-top:10px"><span class="boot-hint-dismiss">Click an active tab to rename &nbsp;·&nbsp; Click anywhere to dismiss</span></div>
        </div>`;
    document.body.appendChild(overlay);
    overlay.addEventListener('click', (e) => {
        const btn = e.target.closest('.boot-hint-type-btn');
        if (btn) changeTabType(activeTabId, btn.dataset.type);
        overlay.remove();
    }, { once: true });
    document.addEventListener('keydown', () => overlay.remove(), { once: true });
}());
