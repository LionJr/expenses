-- One family bot dispatches updates and its outbox serially across processes.
-- The lease is written only while processing a request; no idle polling.
CREATE TABLE telegram_worker_lease (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    owner TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
