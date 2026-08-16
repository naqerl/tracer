-- +goose Up
-- Store an encrypted copy of the user API key plaintext so it can be shown
-- again on its detail page, mirroring the collector API key reveal feature.
-- Existing rows get an empty key_encrypted, which means their plaintext was
-- never persisted and cannot be revealed.
ALTER TABLE user_api_key ADD COLUMN key_encrypted TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE user_api_key DROP COLUMN key_encrypted;
