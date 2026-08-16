-- +goose Up
-- Keys are now revealable, so the masked key_suffix is redundant; drop it from
-- both key tables. collector_api_key also gains an integer id primary key so
-- keys are addressed by id like user_api_key, and the name stays unique.

CREATE TABLE collector_api_key_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    key_hash TEXT NOT NULL UNIQUE,
    key_encrypted TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    last_used_at INTEGER
);

INSERT INTO collector_api_key_new (name, key_hash, key_encrypted, created_at, last_used_at)
SELECT name, key_hash, key_encrypted, created_at, last_used_at FROM collector_api_key;

DROP TABLE collector_api_key;
ALTER TABLE collector_api_key_new RENAME TO collector_api_key;

CREATE INDEX idx_collector_api_key_created_at ON collector_api_key(created_at);
CREATE INDEX idx_collector_api_key_last_used_at ON collector_api_key(last_used_at);

ALTER TABLE user_api_key DROP COLUMN key_suffix;

-- +goose Down
ALTER TABLE user_api_key ADD COLUMN key_suffix TEXT NOT NULL DEFAULT '';

CREATE TABLE collector_api_key_new (
    name TEXT PRIMARY KEY,
    key_hash TEXT NOT NULL UNIQUE,
    key_suffix TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_used_at INTEGER
);

INSERT INTO collector_api_key_new (name, key_hash, key_suffix, created_at, last_used_at)
SELECT name, key_hash, '', created_at, last_used_at FROM collector_api_key;

DROP TABLE collector_api_key;
ALTER TABLE collector_api_key_new RENAME TO collector_api_key;

CREATE INDEX idx_collector_api_key_created_at ON collector_api_key(created_at);
CREATE INDEX idx_collector_api_key_last_used_at ON collector_api_key(last_used_at);
