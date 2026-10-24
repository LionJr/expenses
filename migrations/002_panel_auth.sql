CREATE TABLE panel_accounts (
    user_id BIGINT PRIMARY KEY REFERENCES users(id),
    data JSONB NOT NULL
);

CREATE TABLE panel_sessions (
    token_hash TEXT PRIMARY KEY CHECK (length(token_hash) = 64),
    user_id BIGINT NOT NULL REFERENCES panel_accounts(user_id) ON DELETE CASCADE,
    version TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX panel_sessions_expiry ON panel_sessions(expires_at);
CREATE INDEX panel_sessions_user ON panel_sessions(user_id);
