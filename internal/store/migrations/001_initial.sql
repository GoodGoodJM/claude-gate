-- Real OAuth tokens from Claude subscription
CREATE TABLE IF NOT EXISTS real_tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    access_token TEXT NOT NULL,
    refresh_token TEXT NOT NULL DEFAULT '',
    is_active INTEGER NOT NULL DEFAULT 1,
    failure_count INTEGER NOT NULL DEFAULT 0,
    last_failure_at DATETIME,
    last_used_at DATETIME,
    total_input_tokens INTEGER NOT NULL DEFAULT 0,
    total_output_tokens INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Gate-issued tokens for clients
CREATE TABLE IF NOT EXISTS gate_tokens (
    id TEXT PRIMARY KEY,
    token TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL DEFAULT '',
    is_active INTEGER NOT NULL DEFAULT 1,
    total_input_tokens INTEGER NOT NULL DEFAULT 0,
    total_output_tokens INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_gate_tokens_token ON gate_tokens(token);

-- Per-request usage logs
CREATE TABLE IF NOT EXISTS usage_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    gate_token_id TEXT NOT NULL REFERENCES gate_tokens(id),
    real_token_id TEXT NOT NULL REFERENCES real_tokens(id),
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
    request_path TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_usage_logs_gate_token_id ON usage_logs(gate_token_id);
CREATE INDEX IF NOT EXISTS idx_usage_logs_real_token_id ON usage_logs(real_token_id);
CREATE INDEX IF NOT EXISTS idx_usage_logs_created_at ON usage_logs(created_at);

-- Sticky sessions: gate-token <-> real-token mapping with TTL
CREATE TABLE IF NOT EXISTS sticky_sessions (
    gate_token_id TEXT PRIMARY KEY REFERENCES gate_tokens(id),
    real_token_id TEXT NOT NULL REFERENCES real_tokens(id),
    expires_at DATETIME NOT NULL
);

-- Migration tracking
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
