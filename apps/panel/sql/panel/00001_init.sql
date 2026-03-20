-- +goose Up
CREATE TABLE IF NOT EXISTS user (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    is_admin INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);

CREATE TABLE IF NOT EXISTS session (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_session_expires ON session(expires_at);
CREATE INDEX IF NOT EXISTS idx_session_user ON session(user_id);

CREATE TABLE IF NOT EXISTS access_policy (
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    collector_name TEXT NOT NULL,
    app_name TEXT NOT NULL,
    PRIMARY KEY (user_id, collector_name, app_name)
);
CREATE INDEX IF NOT EXISTS idx_access_policy_user ON access_policy(user_id);
CREATE INDEX IF NOT EXISTS idx_access_policy_collector ON access_policy(collector_name);
CREATE INDEX IF NOT EXISTS idx_access_policy_app ON access_policy(app_name);

CREATE TABLE IF NOT EXISTS notifier (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    config TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    UNIQUE (user_id, name)
);
CREATE INDEX IF NOT EXISTS idx_notifier_user ON notifier(user_id);

CREATE TABLE IF NOT EXISTS notifier_subscription (
    notifier_id INTEGER NOT NULL REFERENCES notifier(id) ON DELETE CASCADE,
    app_name TEXT NOT NULL,
    collector_name TEXT NOT NULL,
    PRIMARY KEY (notifier_id, app_name, collector_name)
);
CREATE INDEX IF NOT EXISTS idx_notifier_subscription_app ON notifier_subscription(app_name);
CREATE INDEX IF NOT EXISTS idx_notifier_subscription_collector ON notifier_subscription(collector_name);

CREATE TABLE IF NOT EXISTS user_api_key (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    api_key TEXT UNIQUE NOT NULL,
    name TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    last_used_at INTEGER,
    UNIQUE (user_id, name)
);
CREATE INDEX IF NOT EXISTS idx_user_api_key_user ON user_api_key(user_id);
CREATE INDEX IF NOT EXISTS idx_user_api_key_key ON user_api_key(api_key);

CREATE TABLE IF NOT EXISTS collector_api_key (
    name TEXT PRIMARY KEY,
    key_hash TEXT NOT NULL UNIQUE,
    key_suffix TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s', 'now')),
    last_used_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_collector_api_key_created_at ON collector_api_key(created_at);
CREATE INDEX IF NOT EXISTS idx_collector_api_key_last_used_at ON collector_api_key(last_used_at);

-- +goose Down
DROP TABLE IF EXISTS collector_api_key;
DROP TABLE IF EXISTS user_api_key;
DROP TABLE IF EXISTS notifier_subscription;
DROP TABLE IF EXISTS notifier;
DROP TABLE IF EXISTS access_policy;
DROP TABLE IF EXISTS session;
DROP TABLE IF EXISTS user;
