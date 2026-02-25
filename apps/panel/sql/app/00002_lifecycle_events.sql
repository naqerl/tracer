-- +goose Up
CREATE TABLE IF NOT EXISTS lifecycle_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    raw_line TEXT NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_lifecycle_events_created_at ON lifecycle_events(created_at);
CREATE INDEX IF NOT EXISTS idx_lifecycle_events_kind ON lifecycle_events(kind);

-- +goose Down
DROP TABLE IF EXISTS lifecycle_events;
