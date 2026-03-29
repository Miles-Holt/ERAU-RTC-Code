// =============================================================================
// RTC WebSocket Client
// =============================================================================
//
// PROTOCOL (all messages are JSON strings):
//
//   LabVIEW -> Browser:
//     Config  { "type": "config", "controls": [ <control>, ... ] }
//     Data    { "type": "data",   "t": <unix_epoch_s>, "d": { "<refDes>": <value>, ... } }
//
//   Browser -> LabVIEW:
//     Command { "type": "cmd", "refDes": "<refDes>", "value": <number|bool> }
//
//   Config control object:
//     {
//       "refDes":      "NV-03",
//       "description": "LOX Press",
//       "type":        "valve",
//       "subType":     "IO-CMD_IO-FB",
//       "details":     { "senseRefDes": "OPT-02" },
//       "channels": [
//         { "refDes": "NV-03-CMD", "role": "cmd-bool", "units": "" },
//         { "refDes": "NV-03-FB",  "role": "sensor",   "units": "" }
//       ]
//     }
//
//   Channel roles: "sensor" | "cmd-bool" | "cmd-pct" | "cmd-float"
//
//   TIMESTAMP NOTE:
//     LabVIEW epoch starts 1904-01-01. Unix epoch starts 1970-01-01.
//     Convert in LabVIEW before sending: unix_t = lv_t - 2082844800
//
// =============================================================================


// =============================================================================
// Init
// =============================================================================

document.getElementById('tab-add').addEventListener('click', () => addTab('frontPanel'));

(function restoreTabLayout() {
    try {
        const saved = JSON.parse(localStorage.getItem('rtc-tab-layout') || 'null');
        if (Array.isArray(saved) && saved.length) {
            for (const { type, name } of saved) {
                const tab = addTab(type);
                if (name) { tab.name = name; renderTabBar(); }
            }
            return;
        }
    } catch {}
    addTab('frontPanel');
}());

buildOperatorButton();
updateCommandWidgets();

setInterval(updateAllGraphs,  500);                // graph refresh at 2 Hz
setInterval(refreshDevTabs,  2000);                // dev stats refresh every 2s
connect();

// One-time boot hint overlay
(function showBootHint() {
    const overlay = document.createElement('div');
    overlay.className = 'boot-overlay';
    overlay.innerHTML = `
        <div class="boot-hint">
            <div>Right-click any tab to change its type, or click a shortcut below</div>
            <div style="margin-top:6px">
                <span class="boot-hint-type-btn" data-type="frontPanel">Front Panel</span>
                &nbsp;·&nbsp;
                <span class="boot-hint-type-btn" data-type="dataView">Data View</span>
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
