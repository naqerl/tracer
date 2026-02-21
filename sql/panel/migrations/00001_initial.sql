-- +goose Up
CREATE TABLE IF NOT EXISTS user (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    is_admin INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS session (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_session_expires ON session(expires_at);
CREATE INDEX IF NOT EXISTS idx_session_user ON session(user_id);

CREATE TABLE IF NOT EXISTS user_app (
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    app_name TEXT NOT NULL,
    PRIMARY KEY (user_id, app_name)
);

CREATE TABLE IF NOT EXISTS config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS notifier (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    type TEXT NOT NULL,
    config TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS notifier_app (
    notifier_id INTEGER NOT NULL REFERENCES notifier(id) ON DELETE CASCADE,
    app_name TEXT NOT NULL,
    PRIMARY KEY (notifier_id, app_name)
);

CREATE INDEX IF NOT EXISTS idx_notifier_app_app ON notifier_app(app_name);

-- +goose Down
DROP TABLE IF EXISTS notifier_app;
DROP TABLE IF EXISTS notifier;
DROP TABLE IF EXISTS config;
DROP TABLE IF EXISTS user_app;
DROP TABLE IF EXISTS session;
DROP TABLE IF EXISTS user;
