-- +goose Up
CREATE TABLE budget_snapshots (
    access_key_id TEXT PRIMARY KEY NOT NULL CHECK (length(access_key_id) > 0),
    five_hours_opened_at TEXT,
    five_hours_used BIGINT NOT NULL CHECK (five_hours_used >= 0),
    seven_days_opened_at TEXT,
    seven_days_used BIGINT NOT NULL CHECK (seven_days_used >= 0),
    CHECK (five_hours_opened_at IS NOT NULL OR five_hours_used = 0),
    CHECK (seven_days_opened_at IS NOT NULL OR seven_days_used = 0)
);

-- +goose Down
DROP TABLE budget_snapshots;
