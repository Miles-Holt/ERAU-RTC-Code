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
        <div class="op-pop-label">Operator</div>
        <input class="op-pop-name" type="text" placeholder="Enter your name"
               maxlength="32" autocomplete="off" spellcheck="false"
               value="${operatorName}">
    `;

    document.body.appendChild(pop);
    _authPopoverEl = pop;

    const inp = pop.querySelector('.op-pop-name');
    inp.focus();
    inp.setSelectionRange(inp.value.length, inp.value.length);

    inp.addEventListener('input', () => {
        operatorName = inp.value.trim();
        updateOperatorButton();
        updateCommandWidgets();
    });

    inp.addEventListener('keydown', e => {
        if (e.key === 'Enter' || e.key === 'Escape') closeOperatorPopover();
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
