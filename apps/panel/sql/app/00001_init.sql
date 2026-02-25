-- +goose Up
CREATE TABLE IF NOT EXISTS log_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message TEXT NOT NULL,
    level TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(message, level)
);
CREATE INDEX IF NOT EXISTS idx_log_groups_message_level ON log_groups(message, level);

CREATE TABLE IF NOT EXISTS logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id TEXT NOT NULL DEFAULT '',
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    context TEXT DEFAULT '{}',
    group_id INTEGER REFERENCES log_groups(id),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_logs_trace_id ON logs(trace_id) WHERE trace_id != '';
CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_group_id ON logs(group_id);

CREATE TABLE IF NOT EXISTS lifecycle_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    raw_line TEXT NOT NULL,
    created_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lifecycle_events_created_at ON lifecycle_events(created_at);
CREATE INDEX IF NOT EXISTS idx_lifecycle_events_kind ON lifecycle_events(kind);


-- +goose Down
DROP TABLE IF EXISTS logs;
DROP TABLE IF EXISTS log_groups;
DROP TABLE IF EXISTS lifecycle_events;
