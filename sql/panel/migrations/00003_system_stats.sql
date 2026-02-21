-- +goose Up
-- intentionally empty: collector resources moved to per-collector databases
SELECT 1;

-- +goose Down
SELECT 1;
