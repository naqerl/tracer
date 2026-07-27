-- +goose Up
CREATE TABLE IF NOT EXISTS update_plans (
    id            TEXT PRIMARY KEY,
    version       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    INTEGER NOT NULL,
    completed_at  INTEGER
);

CREATE TABLE IF NOT EXISTS update_targets (
    id            TEXT PRIMARY KEY,
    plan_id       TEXT NOT NULL REFERENCES update_plans(id),
    collector_id  TEXT,
    target_name   TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    step          TEXT NOT NULL DEFAULT '',
    error         TEXT NOT NULL DEFAULT '',
    old_version   TEXT NOT NULL DEFAULT '',
    new_version   TEXT NOT NULL DEFAULT '',
    binary_digest TEXT NOT NULL DEFAULT '',
    started_at    INTEGER,
    completed_at  INTEGER
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_single_active_plan 
    ON update_plans(status) 
    WHERE status IN ('pending', 'preparing', 'activating');

-- +goose Down
DROP TABLE IF EXISTS update_targets;
DROP TABLE IF EXISTS update_plans;
