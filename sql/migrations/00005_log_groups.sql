-- +goose Up
-- Create log_groups table
CREATE TABLE log_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message TEXT NOT NULL,
    level TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(message, level)
);

-- Add group_id to logs table
ALTER TABLE logs ADD COLUMN group_id INTEGER REFERENCES log_groups(id);

-- Create indexes for efficient group lookups
CREATE INDEX idx_log_groups_message_level ON log_groups(message, level);
CREATE INDEX idx_logs_group_id ON logs(group_id);

-- Migrate existing data: create groups by message AND level, then update logs
INSERT INTO log_groups (message, level)
SELECT DISTINCT message, level FROM logs ORDER BY message, level;

UPDATE logs
SET group_id = (SELECT id FROM log_groups WHERE log_groups.message = logs.message AND log_groups.level = logs.level);

-- +goose Down
-- Remove group_id column
ALTER TABLE logs DROP COLUMN group_id;

-- Drop log_groups table
DROP TABLE log_groups;
