(function() {
    function initChart() {
        if (typeof uPlot === 'undefined') {
            setTimeout(initChart, 50);
            return;
        }
        renderChart();
    }

    function renderChart() {
        // Extract data from table - aggregate by hour
        const rows = document.querySelectorAll('#logs-data tbody tr');
        if (rows.length === 0) return;

        const timeCounts = new Map();
        let minTime = Infinity;
        let maxTime = 0;

        rows.forEach(row => {
            const timestamp = parseInt(row.dataset.timestamp);
            if (!timestamp) return;
            minTime = Math.min(minTime, timestamp);
            maxTime = Math.max(maxTime, timestamp);
            const hour = Math.floor(timestamp / 3600) * 3600; // Round to hour
            timeCounts.set(hour, (timeCounts.get(hour) || 0) + 1);
        });

        // Sort by time
        const sortedEntries = Array.from(timeCounts.entries()).sort((a, b) => a[0] - b[0]);
        const timestamps = sortedEntries.map(e => e[0]);
        const counts = sortedEntries.map(e => e[1]);

        // Common chart options
        const containerWidth = document.getElementById('log-chart-container').offsetWidth;
        const isMobile = window.innerWidth < 768;

        // Determine color based on log level from data attribute
        const levelContainer = document.getElementById('log-chart-container');
        const level = levelContainer ? (levelContainer.dataset.level || '').toLowerCase() : '';
        let levelColor = '#808080';
        let levelFill = 'rgba(128, 128, 128, 0.1)';
        if (level === 'error') {
            levelColor = '#ef4444';
            levelFill = 'rgba(239, 68, 68, 0.1)';
        } else if (level === 'warn' || level === 'warning') {
            levelColor = '#eab308';
            levelFill = 'rgba(234, 179, 8, 0.1)';
        } else if (level === 'info') {
            levelColor = '#3b82f6';
            levelFill = 'rgba(59, 130, 246, 0.1)';
        }

        const chart = new uPlot({
            width: containerWidth,
            height: isMobile ? 200 : 250,
            title: "Log Frequency",
            scales: {
                x: {
                    time: true,
                    range: () => [minTime, maxTime + 3600]
                }
            },
            axes: [
                {
                    stroke: "#808080",
                    grid: { stroke: "#1E1E1E", width: 1 },
                    size: isMobile ? 40 : 50
                },
                {
                    stroke: "#808080",
                    size: isMobile ? 30 : 40,
                    grid: { stroke: "#1E1E1E", width: 1 },
                    values: (u, vals) => vals.map(v => v.toFixed(0))
                }
            ],
            series: [
                { },
                {
                    label: "Count",
                    stroke: levelColor,
                    fill: levelFill,
                    width: 2
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
        }, [timestamps, counts], document.getElementById('log-chart'));

        // Handle window resize
        let resizeTimeout;
        window.addEventListener('resize', () => {
            clearTimeout(resizeTimeout);
            resizeTimeout = setTimeout(() => {
                const newWidth = document.getElementById('log-chart-container').offsetWidth;
                const newIsMobile = window.innerWidth < 768;
                const newHeight = newIsMobile ? 200 : 250;
                chart.setSize({ width: newWidth, height: newHeight });
            }, 150);
        });
    }

    initChart();
})();
