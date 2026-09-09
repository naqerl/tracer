-- +goose Up
CREATE TABLE IF NOT EXISTS app_health_check (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    status TEXT NOT NULL,
    at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_app_health_check_at ON app_health_check(at);

-- +goose Down
DROP TABLE IF EXISTS app_health_check;