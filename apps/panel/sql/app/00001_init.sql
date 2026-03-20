-- +goose Up
CREATE TABLE IF NOT EXISTS log_group (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message TEXT NOT NULL,
    level TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    log_count INTEGER NOT NULL DEFAULT 0,
    last_log_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE(message, level)
);
CREATE INDEX IF NOT EXISTS idx_log_group_message_level ON log_group(message, level);
CREATE INDEX IF NOT EXISTS idx_log_group_count ON log_group(log_count DESC);
CREATE INDEX IF NOT EXISTS idx_log_group_last_log ON log_group(last_log_at DESC);

-- TODO: Remove level and message fields
-- because they are the same for logs with the same group_id
CREATE TABLE IF NOT EXISTS log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id TEXT,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    context TEXT DEFAULT '{}',
    group_id INTEGER REFERENCES log_group(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_log_trace_id ON log(trace_id) WHERE trace_id != '';
CREATE INDEX IF NOT EXISTS idx_log_level ON log(level);
CREATE INDEX IF NOT EXISTS idx_log_created_at ON log(created_at);
CREATE INDEX IF NOT EXISTS idx_log_group_id ON log(group_id);
CREATE INDEX IF NOT EXISTS idx_log_group_id_created_at ON log(group_id, created_at DESC);

CREATE TABLE IF NOT EXISTS lifecycle_event (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    raw_line TEXT NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_lifecycle_event_created_at ON lifecycle_event(created_at);
CREATE INDEX IF NOT EXISTS idx_lifecycle_event_kind ON lifecycle_event(kind);

CREATE TABLE IF NOT EXISTS app_health_period (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    status TEXT NOT NULL,
    started_at INTEGER NOT NULL,
    ended_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_app_health_period_started_at ON app_health_period(started_at);
CREATE INDEX IF NOT EXISTS idx_app_health_period_ended_at ON app_health_period(ended_at);


-- +goose Down
DROP TABLE IF EXISTS app_health_period;
DROP TABLE IF EXISTS log;
DROP TABLE IF EXISTS log_group;
DROP TABLE IF EXISTS lifecycle_event;
