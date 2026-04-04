// =============================================================================
// Auth / Operator identity
// =============================================================================

let _authPopoverEl   = null;
let _authPopoverOpen = false;

function buildOperatorButton() {
    const btn = document.getElementById('operator-btn');
    btn.addEventListener('click', (e) => {
        e.stopPropagation();
        _authPopoverOpen ? closeOperatorPopover() : openOperatorPopover(btn);
    });
    updateOperatorButton();
}

function openOperatorPopover(anchorEl) {
    closeOperatorPopover();
    _authPopoverOpen = true;

    const pop = document.createElement('div');
    pop.className = 'operator-popover';

    const rect = anchorEl.getBoundingClientRect();
    pop.style.top   = `${rect.bottom + 6}px`;
    pop.style.right = `${window.innerWidth - rect.right}px`;

    pop.innerHTML = `
        <div class="op-pop-label">Operator Login</div>
        <input class="op-pop-name" type="text" placeholder="Name"
               maxlength="32" autocomplete="off" spellcheck="false"
               value="${operatorName}">
        <input class="op-pop-pin" type="password" placeholder="PIN"
               maxlength="16" autocomplete="off">
        <button class="op-pop-submit">Authenticate</button>
        <div class="op-pop-status"></div>
    `;

    document.body.appendChild(pop);
    _authPopoverEl = pop;

    const nameInp   = pop.querySelector('.op-pop-name');
    const pinInp    = pop.querySelector('.op-pop-pin');
    const submitBtn = pop.querySelector('.op-pop-submit');

    // Focus name if empty, otherwise PIN
    if (operatorName) pinInp.focus();
    else nameInp.focus();

    function submitAuth() {
        const name = nameInp.value.trim();
        const pin  = pinInp.value;
        if (!name || !pin) return;
        const status = pop.querySelector('.op-pop-status');
        status.textContent = 'Waiting...';
        status.className = 'op-pop-status pending';
        submitBtn.disabled = true;
        if (wsCtrl && wsCtrl.readyState === WebSocket.OPEN) {
            wsCtrl.send(JSON.stringify({ type: 'auth_request', name, pin }));
        } else {
            status.textContent = 'Not connected';
            status.className = 'op-pop-status error';
            submitBtn.disabled = false;
        }
    }

    submitBtn.addEventListener('click', submitAuth);

    [nameInp, pinInp].forEach(inp => {
        inp.addEventListener('keydown', e => {
            if (e.key === 'Enter') submitAuth();
            if (e.key === 'Escape') closeOperatorPopover();
        });
    });

    const dismiss = (e) => {
        if (!pop.contains(e.target) && e.target !== anchorEl) {
            closeOperatorPopover();
            document.removeEventListener('mousedown', dismiss);
        }
    };
    setTimeout(() => document.addEventListener('mousedown', dismiss), 0);
}

function closeOperatorPopover() {
    _authPopoverEl?.remove();
    _authPopoverEl   = null;
    _authPopoverOpen = false;
}

// Called from ws.js when an auth_response message arrives.
function handleAuthResponse(msg) {
    if (msg.approved) {
        operatorName = msg.name;
        updateOperatorButton();
        updateCommandWidgets();
        closeOperatorPopover();
    } else {
        const status    = _authPopoverEl?.querySelector('.op-pop-status');
        const submitBtn = _authPopoverEl?.querySelector('.op-pop-submit');
        if (status) {
            status.textContent = msg.reason || 'Authentication failed';
            status.className = 'op-pop-status error';
        }
        if (submitBtn) submitBtn.disabled = false;
        _authPopoverEl?.querySelector('.op-pop-pin')?.focus();
    }
}

function updateOperatorButton() {
    const btn = document.getElementById('operator-btn');
    if (!btn) return;
    btn.classList.toggle('operator-named', !!operatorName);
    btn.title = operatorName ? `Operator: ${operatorName}` : 'Set operator name';
}

function updateCommandWidgets() {
    document.querySelectorAll('.cmd-btn, .cmd-slider, .cmd-input')
        .forEach(el => el.disabled = !operatorName);
}
