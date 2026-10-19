CREATE TABLE IF NOT EXISTS users (
    id BIGINT PRIMARY KEY CHECK (id > 0),
    name TEXT NOT NULL DEFAULT '',
    username TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS expenses (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    amount BIGINT NOT NULL CHECK (amount > 0),
    category TEXT NOT NULL CHECK (
        category IN ('еда', 'транспорт', 'жильё', 'развлечения', 'здоровье', 'покупки', 'другое')
    ),
    spent_at DATE NOT NULL,
    comment TEXT NOT NULL DEFAULT '' CHECK (char_length(comment) <= 1000),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE INDEX IF NOT EXISTS expenses_user_date ON expenses (user_id, spent_at, id);
CREATE INDEX IF NOT EXISTS expenses_date ON expenses (spent_at, id);

CREATE TABLE IF NOT EXISTS bot_sessions (
    user_id BIGINT PRIMARY KEY REFERENCES users(id),
    data JSONB NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

CREATE TYPE telegram_update_state AS ENUM (
    'processing',
    'done',
    'sent',
    'failed',
    'superseded'
);

CREATE TABLE IF NOT EXISTS telegram_updates (
    update_id BIGINT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    state telegram_update_state NOT NULL DEFAULT 'processing',
    reply TEXT NOT NULL DEFAULT '',
    delivery_attempts INTEGER NOT NULL DEFAULT 0 CHECK (delivery_attempts >= 0),
    retry_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code INTEGER,
    is_choice_prompt BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX telegram_updates_pending
    ON telegram_updates (retry_at, update_id)
    WHERE state = 'done';

CREATE TABLE IF NOT EXISTS expense_operations (
    operation_key TEXT PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    result JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
