-- +goose Up
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

CREATE TABLE IF NOT EXISTS collector_apps (
    app_name TEXT PRIMARY KEY,
    last_seen_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_collector_apps_last_seen ON collector_apps(last_seen_at);

-- +goose Down
DROP TABLE IF EXISTS collector_apps;
DROP TABLE IF EXISTS system_stats;
