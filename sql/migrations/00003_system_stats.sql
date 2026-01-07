-- +goose Up
-- System statistics from collectors
CREATE TABLE IF NOT EXISTS system_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    collector_name TEXT NOT NULL,
    cpu_usage_percent REAL NOT NULL,
    mem_total_mb INTEGER NOT NULL,
    mem_used_mb INTEGER NOT NULL,
    mem_available_mb INTEGER NOT NULL,
    disk_total_gb INTEGER NOT NULL,
    disk_used_gb INTEGER NOT NULL,
    disk_avail_gb INTEGER NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_system_stats_collector ON system_stats(collector_name);
CREATE INDEX IF NOT EXISTS idx_system_stats_created_at ON system_stats(created_at);

-- Track which apps are served by which collectors
CREATE TABLE IF NOT EXISTS app_collectors (
    app_name TEXT NOT NULL,
    collector_name TEXT NOT NULL,
    last_seen_at DATETIME NOT NULL,
    PRIMARY KEY (app_name, collector_name)
);

CREATE INDEX IF NOT EXISTS idx_app_collectors_app ON app_collectors(app_name);

-- +goose Down
DROP TABLE IF EXISTS system_stats;
DROP TABLE IF EXISTS app_collectors;
