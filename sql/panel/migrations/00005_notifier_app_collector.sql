-- +goose Up
-- Add collector_name to notifier_app table for collector-specific subscriptions
-- This enables users to subscribe to specific collector-app combinations

-- Create new table with updated schema
CREATE TABLE notifier_app_new (
    notifier_id INTEGER NOT NULL REFERENCES notifier(id) ON DELETE CASCADE,
    app_name TEXT NOT NULL CHECK(app_name != ''),
    collector_name TEXT NOT NULL CHECK(collector_name != ''),
    PRIMARY KEY (notifier_id, app_name, collector_name)
);

-- Copy existing data (set collector_name to '*' for wildcard - any collector)
-- '*' as collector_name means "any collector" for that app
INSERT INTO notifier_app_new (notifier_id, app_name, collector_name)
SELECT notifier_id, app_name, '*'
FROM notifier_app;

-- Drop old table and rename new one
DROP TABLE notifier_app;
ALTER TABLE notifier_app_new RENAME TO notifier_app;

-- +goose Down
-- Revert to old schema without collector_name
CREATE TABLE notifier_app_new (
    notifier_id INTEGER NOT NULL REFERENCES notifier(id) ON DELETE CASCADE,
    app_name TEXT NOT NULL,
    PRIMARY KEY (notifier_id, app_name)
);

-- Copy data back (wildcards become generic subscriptions)
INSERT INTO notifier_app_new (notifier_id, app_name)
SELECT DISTINCT notifier_id, app_name
FROM notifier_app;

DROP TABLE notifier_app;
ALTER TABLE notifier_app_new RENAME TO notifier_app;
