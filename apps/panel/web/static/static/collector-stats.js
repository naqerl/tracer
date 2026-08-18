(function() {
// Chart instances live on window so they survive the script re-execution on
// every htmx swap; destroyCharts can then release the previous run's uPlot
// objects instead of orphaning them.
let instances = window.__collectorStatsInstances = window.__collectorStatsInstances || [];

function destroyCharts() {
	instances.forEach(u => u.destroy());
	instances.length = 0;
}

function initCharts() {
	if (typeof uPlot === 'undefined') {
		setTimeout(initCharts, 50);
		return;
	}
	renderCharts();
}

function renderCharts() {
destroyCharts();
const containerWidth = document.getElementById('charts-container').offsetWidth;
const isMobile = window.innerWidth < 768;
const commonOpts = {
	width: containerWidth,
	height: isMobile ? 200 : 250,
	scales: {
		x: { time: true }
	},
	axes: [
		{ 
			stroke: "#808080",
			grid: { stroke: "#1E1E1E", width: 1 },
			size: isMobile ? 40 : 50
		},
		{ 
			stroke: "#808080",
			size: isMobile ? 50 : 60,
			grid: { stroke: "#1E1E1E", width: 1 }
		}
	],
	legend: { show: false },
	cursor: {
		drag: { x: false, y: false },
		focus: { prox: isMobile ? 50 : 30 },
		points: {
			size: isMobile ? 6 : 8,
			width: 2
		}
	}
};

// Chart instances live in the IIFE scope so destroyCharts can reset them
// before each render.

// Build stepped data from periods: each period becomes two points (start, end)
// For open periods (end=0), emit current time as endpoint so the line extends to "now"
function periodsToSeries(rows, valueKey) {
	const timestamps = [];
	const values = [];
	rows.forEach(row => {
		const start = parseInt(row.dataset.start);
		const end = parseInt(row.dataset.end);
		const val = parseFloat(row.dataset[valueKey]);
		timestamps.push(start);
		values.push(val);
		if (end > 0) {
			timestamps.push(end);
			values.push(val);
		} else {
			// Open period: extend to current time
			timestamps.push(Math.floor(Date.now() / 1000));
			values.push(val);
		}
	});
	// Sort by timestamp
	const indices = timestamps.map((_, i) => i).sort((a, b) => timestamps[a] - timestamps[b]);
	return [
		indices.map(i => timestamps[i]),
		indices.map(i => values[i])
	];
}

const cpuRows = document.querySelectorAll('#cpu-periods tbody tr');
if (cpuRows.length > 0) {
	const [cpuTs, cpuVals] = periodsToSeries(cpuRows, 'value');
	instances.push(new uPlot({
		...commonOpts,
		title: "CPU Usage",
		series: [
			{ },
			{ 
				label: "CPU %", 
				stroke: "#5C9CF5", 
				fill: "rgba(92, 156, 245, 0.1)",
				width: 2
			}
		],
		axes: [
			commonOpts.axes[0],
			{ 
				...commonOpts.axes[1],
				values: (u, vals) => vals.map(v => v.toFixed(1) + '%')
			}
		],
	}, [cpuTs, cpuVals], document.getElementById('cpu-chart')));
}

const memRows = document.querySelectorAll('#mem-periods tbody tr');
if (memRows.length > 0) {
	const timestamps = [];
	const values = [];
	memRows.forEach(row => {
		const start = parseInt(row.dataset.start);
		const end = parseInt(row.dataset.end);
		const total = parseInt(row.dataset.total);
		const used = parseInt(row.dataset.used);
		const pct = total > 0 ? (used / total) * 100 : 0;
		timestamps.push(start);
		values.push(pct);
		if (end > 0) {
			timestamps.push(end);
			values.push(pct);
		} else {
			// Open period: extend to current time
			timestamps.push(Math.floor(Date.now() / 1000));
			values.push(pct);
		}
	});
	const indices = timestamps.map((_, i) => i).sort((a, b) => timestamps[a] - timestamps[b]);
	const sortedTs = indices.map(i => timestamps[i]);
	const sortedVals = indices.map(i => values[i]);

	instances.push(new uPlot({
		...commonOpts,
		title: "RAM Usage",
		series: [
			{ },
			{ 
				label: "RAM %", 
				stroke: "#F5A742", 
				fill: "rgba(245, 167, 66, 0.1)",
				width: 2
			}
		],
		axes: [
			commonOpts.axes[0],
			{ 
				...commonOpts.axes[1],
				values: (u, vals) => vals.map(v => v.toFixed(1) + '%')
			}
		],
	}, [sortedTs, sortedVals], document.getElementById('ram-chart')));
}

const diskRows = document.querySelectorAll('#disk-periods tbody tr');
if (diskRows.length > 0) {
	const [diskTs, diskVals] = periodsToSeries(diskRows, 'value');
	instances.push(new uPlot({
		...commonOpts,
		title: "Disk Available",
		series: [
			{ },
			{ 
				label: "Free GB", 
				stroke: "#22C55E", 
				fill: "rgba(34, 197, 94, 0.1)",
				width: 2
			}
		],
		axes: [
			commonOpts.axes[0],
			{ 
				...commonOpts.axes[1],
				values: (u, vals) => vals.map(v => v.toFixed(0) + ' GB')
			}
		],
	}, [diskTs, diskVals], document.getElementById('disk-chart')));
}

}

if (!window.__collectorStatsInstalled) {
	window.__collectorStatsInstalled = true;

	document.body.addEventListener('htmx:afterSwap', function(evt) {
		if (evt.target && evt.target.id === 'resources-data') {
			renderCharts();
		}
	});

	let resizeTimeout;
	window.addEventListener('resize', () => {
		clearTimeout(resizeTimeout);
		resizeTimeout = setTimeout(() => {
			const newWidth = document.getElementById('charts-container').offsetWidth;
			const newIsMobile = window.innerWidth < 768;
			const newHeight = newIsMobile ? 200 : 250;

			instances.forEach(u => {
				u.setSize({ 
					width: newWidth, 
					height: newHeight 
				});
			});
		}, 150);
	});
}

initCharts();
})();
