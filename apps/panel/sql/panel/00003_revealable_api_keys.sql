-- +goose Up
-- Store an encrypted copy of the collector key plaintext so it can be shown
-- again on its detail page. Existing rows get an empty key_encrypted, which
-- means their plaintext was never persisted and cannot be revealed.
-- User API keys are deliberately NOT included: no reveal feature exists for
-- them, and storing their plaintext would widen at-rest exposure for no benefit.
ALTER TABLE collector_api_key ADD COLUMN key_encrypted TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE collector_api_key DROP COLUMN key_encrypted;
