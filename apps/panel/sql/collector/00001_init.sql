-- +goose Up
CREATE TABLE IF NOT EXISTS cpu_period (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    collector_name TEXT NOT NULL,
    usage_percent REAL NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at INTEGER
);

CREATE TABLE IF NOT EXISTS mem_period (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    collector_name TEXT NOT NULL,
    total_mb INTEGER NOT NULL,
    used_mb INTEGER NOT NULL,
    available_mb INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at INTEGER
);

CREATE TABLE IF NOT EXISTS disk_period (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    collector_name TEXT NOT NULL,
    total_gb INTEGER NOT NULL,
    used_gb INTEGER NOT NULL,
    avail_gb INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_cpu_period_collector ON cpu_period(collector_name, started_at);
CREATE INDEX IF NOT EXISTS idx_mem_period_collector ON mem_period(collector_name, started_at);
CREATE INDEX IF NOT EXISTS idx_disk_period_collector ON disk_period(collector_name, started_at);

-- +goose Down
DROP TABLE IF EXISTS disk_period;
DROP TABLE IF EXISTS mem_period;
DROP TABLE IF EXISTS cpu_period;
