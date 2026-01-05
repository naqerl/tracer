-- +goose Up
CREATE TABLE logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id TEXT NOT NULL DEFAULT '',
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    source TEXT NOT NULL,
    context TEXT DEFAULT '{}',
    raw TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_logs_trace_id ON logs(trace_id) WHERE trace_id != '';
CREATE INDEX idx_logs_level ON logs(level);
CREATE INDEX idx_logs_created_at ON logs(created_at);
CREATE INDEX idx_logs_source ON logs(source);

-- +goose Down
DROP TABLE logs;
