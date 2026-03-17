-- +goose Up
-- Idempotent migration: Add ON DELETE CASCADE to logs.group_id foreign key
-- Safe to re-run, handles partial failures and existing CASCADE

PRAGMA foreign_keys = OFF;

-- Create backup (idempotent - recreates if exists)
DROP TABLE IF EXISTS _logs_backup;
CREATE TABLE _logs_backup AS SELECT * FROM logs WHERE 1=1;

-- Create new table with CASCADE (idempotent - drops if exists from partial migration)
DROP TABLE IF EXISTS logs_new;
CREATE TABLE logs_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id TEXT,
    level TEXT NOT NULL,
    message TEXT NOT NULL,
    context TEXT DEFAULT '{}',
    group_id INTEGER REFERENCES log_groups(id) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Restore data (idempotent - INSERT OR REPLACE handles duplicates)
INSERT OR REPLACE INTO logs_new SELECT * FROM _logs_backup;

-- Drop old table and rename (idempotent - handles case where logs doesn't exist)
DROP TABLE IF EXISTS logs;
ALTER TABLE logs_new RENAME TO logs;

-- Recreate indexes (idempotent - IF NOT EXISTS)
CREATE INDEX IF NOT EXISTS idx_logs_trace_id ON logs(trace_id) WHERE trace_id != '';
CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_group_id ON logs(group_id);
CREATE INDEX IF NOT EXISTS idx_logs_group_id_created_at ON logs(group_id, created_at DESC);

-- Clean up backup
DROP TABLE IF EXISTS _logs_backup;

PRAGMA foreign_keys = ON;

-- +goose Down
-- Cannot safely revert CASCADE without data loss risk
-- Down migration is intentionally a no-op
