if (window.__scopeTreeInstalled) {
	// Already wired (the fragment is re-rendered on htmx swaps).
} else {
	window.__scopeTreeInstalled = true;
	document.addEventListener('click', function(e) {
		const toggle = e.target.closest('[data-action="toggle-scope-collector"]');
		if (!toggle) return;
		const container = toggle.closest('[data-scope-collector]');
		if (!container) return;
		setScopeGroup(container, !allAppsOn(container));
	});
	document.addEventListener('click', function(e) {
		const btn = e.target.closest('[data-action="toggle-checkbox"]');
		if (!btn) return;
		const container = btn.closest('[data-scope-collector]');
		if (!container) return;
		const collectorToggle = container.querySelector('[data-action="toggle-scope-collector"]');
		if (!collectorToggle) return;
		setTimeout(function() {
			renderScopeCollector(container, collectorToggle);
		}, 0);
	});
}
function renderScopeCollector(container, collectorToggle) {
	const label = collectorToggle.dataset.label;
	if (allAppsOn(container)) {
		collectorToggle.textContent = '[x] ' + label;
		collectorToggle.classList.remove('text-fg-secondary');
		collectorToggle.classList.add('text-fg');
	} else {
		collectorToggle.textContent = '[ ] ' + label;
		collectorToggle.classList.remove('text-fg');
		collectorToggle.classList.add('text-fg-secondary');
	}
}
function setScopeGroup(container, on) {
	container.querySelectorAll('[data-action="toggle-checkbox"]').forEach(function(btn) {
		const id = btn.dataset.id;
		const input = document.getElementById(id + '_value');
		const checkedValue = input.dataset.checkedValue || 'on';
		const label = btn.dataset.label;
		if (on) {
			input.value = checkedValue;
			btn.textContent = '[x] ' + label;
			btn.classList.remove('text-fg-secondary');
			btn.classList.add('text-fg');
		} else {
			input.value = '';
			btn.textContent = '[ ] ' + label;
			btn.classList.remove('text-fg');
			btn.classList.add('text-fg-secondary');
		}
	});
	const collectorToggle = container.querySelector('[data-action="toggle-scope-collector"]');
	if (collectorToggle) renderScopeCollector(container, collectorToggle);
}
function allAppsOn(container) {
	const btns = Array.from(container.querySelectorAll('[data-action="toggle-checkbox"]'));
	return btns.length > 0 && btns.every(function(btn) {
		const id = btn.dataset.id;
		const input = document.getElementById(id + '_value');
		const checkedValue = input.dataset.checkedValue || 'on';
		return input.value === checkedValue;
	});
}
