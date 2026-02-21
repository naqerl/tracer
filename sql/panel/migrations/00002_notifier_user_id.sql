-- +goose Up
-- Add user_id column to notifier table
-- SQLite doesn't support ADD COLUMN with NOT NULL without default, so we recreate the table

-- Create new table with user_id
CREATE TABLE notifier_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 1 REFERENCES user(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    config TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);

-- Copy existing data (assign to user_id 1 which should be admin)
INSERT INTO notifier_new (id, user_id, name, type, config, enabled, created_at)
SELECT id, 1, name, type, config, enabled, created_at FROM notifier;

-- Drop old table
DROP TABLE notifier;

-- Rename new table
ALTER TABLE notifier_new RENAME TO notifier;

-- Create index
CREATE INDEX IF NOT EXISTS idx_notifier_user ON notifier(user_id);

-- +goose Down
-- Revert: remove user_id column
CREATE TABLE notifier_old (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    type TEXT NOT NULL,
    config TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO notifier_old (id, name, type, config, enabled, created_at)
SELECT id, name, type, config, enabled, created_at FROM notifier;

DROP TABLE notifier;

ALTER TABLE notifier_old RENAME TO notifier;

DROP INDEX IF EXISTS idx_notifier_user;
