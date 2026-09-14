-- +goose Up
CREATE INDEX IF NOT EXISTS idx_log_level_created_at ON log(level, created_at);

-- +goose Down
DROP INDEX IF EXISTS idx_log_level_created_at;
