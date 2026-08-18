if (!window.__markdownCheckboxInstalled) {
window.__markdownCheckboxInstalled = true;
document.addEventListener('click', function(e) {
	const btn = e.target.closest('[data-action="toggle-checkbox"]');
	if (!btn) return;
	const id = btn.dataset.id;
	const valueInput = document.getElementById(id + '_value');
	const toggleBtn = document.getElementById(id + '_toggle');
	const checkedValue = valueInput.dataset.checkedValue || 'on';
	const isChecked = valueInput.value === checkedValue;
	const labelText = toggleBtn.dataset.label;
	if (isChecked) {
		valueInput.value = '';
		toggleBtn.textContent = '[ ] ' + labelText;
		toggleBtn.classList.remove('text-fg');
		toggleBtn.classList.add('text-fg-secondary');
	} else {
		valueInput.value = checkedValue;
		toggleBtn.textContent = '[x] ' + labelText;
		toggleBtn.classList.remove('text-fg-secondary');
		toggleBtn.classList.add('text-fg');
	}
});
}
