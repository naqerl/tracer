-- +goose Up
CREATE TABLE IF NOT EXISTS collector_config (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    config_json TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS collector_config;
