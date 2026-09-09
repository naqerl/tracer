if (window.__appFormInstalled) {
	// Already wired (the fragment is re-rendered on htmx swaps).
} else {
	window.__appFormInstalled = true;
	document.addEventListener('htmx:afterSwap', function() {
		scrollToVisibleError();
	});
	document.addEventListener('DOMContentLoaded', function() {
		scrollToVisibleError();
	});
}
function scrollToVisibleError() {
	var el = document.getElementById('app-name-status');
	if (!el || el.classList.contains('hidden')) return;
	var r = el.getBoundingClientRect();
	if (r.top >= 0 && r.bottom <= window.innerHeight) return;
	el.scrollIntoView({ block: 'center', behavior: 'smooth' });
}
