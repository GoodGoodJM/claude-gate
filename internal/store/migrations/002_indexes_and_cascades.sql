CREATE UNIQUE INDEX IF NOT EXISTS idx_real_tokens_access_token ON real_tokens(access_token);
CREATE INDEX IF NOT EXISTS idx_sticky_sessions_expires_at ON sticky_sessions(expires_at);
