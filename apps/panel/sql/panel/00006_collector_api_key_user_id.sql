-- +goose Up
-- Associate each collector API key with the user who created it.
ALTER TABLE collector_api_key ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_collector_api_key_user ON collector_api_key(user_id);

-- +goose Down
DROP INDEX IF EXISTS idx_collector_api_key_user;
ALTER TABLE collector_api_key DROP COLUMN user_id;
