-- +goose Up
ALTER TABLE log_group ADD COLUMN issue_number INTEGER;
ALTER TABLE log_group ADD COLUMN issue_url TEXT;

-- +goose Down
ALTER TABLE log_group DROP COLUMN issue_number;
ALTER TABLE log_group DROP COLUMN issue_url;
