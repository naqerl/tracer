(function() {
			const cookieName = 'tracer_panel_since';
			const defaultSince = '24h';
			const allowedSince = new Set(['1h', '6h', '24h', '7d', '30d']);

			function readCookie(name) {
				const prefix = name + '=';
				const parts = document.cookie.split(';');
				for (let i = 0; i < parts.length; i++) {
					const cookie = parts[i].trim();
					if (cookie.startsWith(prefix)) {
						return decodeURIComponent(cookie.slice(prefix.length));
					}
				}
				return '';
			}

			function writeCookie(name, value) {
				let cookie = name + '=' + encodeURIComponent(value) + '; path=/; max-age=31536000; SameSite=Lax';
				if (window.location.protocol === 'https:') {
					cookie += '; Secure';
				}
				document.cookie = cookie;
			}

			const currentSince = readCookie(cookieName);
			const since = allowedSince.has(currentSince) ? currentSince : defaultSince;
			if (currentSince !== since) {
				writeCookie(cookieName, since);
			}
			const sinceLabel = document.getElementById('since-label');
			sinceLabel.textContent = since;
			document.documentElement.dataset.since = since;

			const path = window.location.pathname;
			document.querySelectorAll('#nav-links a').forEach(function(link) {
				const nav = link.getAttribute('data-nav');
				const isActive =
					(nav === 'panel' && path === '/panel/') ||
					(nav === 'topology' && path === '/topology/') ||
					(nav === 'settings' && path === '/settings/');
				if (isActive) {
					link.classList.remove('text-fg-secondary');
				} else {
					link.classList.add('text-fg-secondary');
				}
			});

			window.navbarSelectSince = function(val) {
				if (!allowedSince.has(val)) {
					return;
				}
				writeCookie(cookieName, val);
				sinceLabel.textContent = val;
				document.documentElement.dataset.since = val;
				window.location.reload();
			};

			function reloadIfSinceChanged() {
				const activeSince = allowedSince.has(readCookie(cookieName)) ? readCookie(cookieName) : defaultSince;
				const pageSince = document.documentElement.dataset.since || sinceLabel.textContent || defaultSince;
				if (activeSince !== pageSince) {
					window.location.reload();
				}
			}

			window.addEventListener('pageshow', function(event) {
				if (event.persisted) {
					reloadIfSinceChanged();
				}
			});

			window.addEventListener('popstate', function() {
				reloadIfSinceChanged();
			});

			document.body.addEventListener('htmx:historyRestore', function() {
				reloadIfSinceChanged();
			});
		})();
