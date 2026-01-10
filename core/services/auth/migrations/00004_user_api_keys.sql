-- +goose Up
-- Create user_api_key table for per-user API keys (LLM compatibility)

CREATE TABLE IF NOT EXISTS user_api_key (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    api_key TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    UNIQUE(user_id, name)
);

CREATE INDEX IF NOT EXISTS idx_user_api_key_user ON user_api_key(user_id);
CREATE INDEX IF NOT EXISTS idx_user_api_key_key ON user_api_key(api_key);

-- +goose Down
-- Remove user_api_key table

DROP INDEX IF EXISTS idx_user_api_key_key;
DROP INDEX IF EXISTS idx_user_api_key_user;
DROP TABLE IF EXISTS user_api_key;
