-- +goose Up
CREATE TABLE requests (
    id TEXT PRIMARY KEY NOT NULL,
    finished_at INTEGER NOT NULL,
    key_id TEXT NOT NULL,
    account_id TEXT,
    model TEXT,
    result TEXT NOT NULL,
    duration_ms INTEGER NOT NULL CHECK (duration_ms >= 0),
    response_started INTEGER NOT NULL CHECK (response_started IN (0, 1)),
    input_tokens INTEGER CHECK (input_tokens >= 0),
    output_tokens INTEGER CHECK (output_tokens >= 0),
    total_tokens INTEGER CHECK (total_tokens >= 0)
);
CREATE INDEX requests_finished_at ON requests(finished_at);
CREATE INDEX requests_key_finished_at ON requests(key_id, finished_at);
CREATE INDEX requests_account_finished_at ON requests(account_id, finished_at);
PRAGMA user_version = 2;
