-- +goose Up
CREATE TABLE IF NOT EXISTS log_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message TEXT NOT NULL,
    level TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(message, level)
);
CREATE INDEX IF NOT EXISTS idx_log_groups_message_level ON log_groups(message, level);

CREATE TABLE IF NOT EXISTS logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id TEXT NOT NULL DEFAULT '',
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    source TEXT NOT NULL,
    context TEXT DEFAULT '{}',
    raw TEXT NOT NULL,
    group_id INTEGER REFERENCES log_groups(id),
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_logs_trace_id ON logs(trace_id) WHERE trace_id != '';
CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_source ON logs(source);
CREATE INDEX IF NOT EXISTS idx_logs_group_id ON logs(group_id);

-- +goose Down
DROP TABLE IF EXISTS logs;
DROP TABLE IF EXISTS log_groups;
