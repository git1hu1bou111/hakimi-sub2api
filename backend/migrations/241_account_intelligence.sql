ALTER TABLE groups ADD COLUMN IF NOT EXISTS account_intelligence_enabled BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS channel_monitor_account_intelligence_history (
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    checked_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('green', 'yellow', 'red')),
    latency_ms INTEGER NOT NULL DEFAULT 0,
    detail TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (account_id, group_id, checked_at)
);
CREATE INDEX IF NOT EXISTS idx_account_intelligence_history_time
    ON channel_monitor_account_intelligence_history (checked_at);
CREATE INDEX IF NOT EXISTS idx_account_intelligence_history_account_group_time
    ON channel_monitor_account_intelligence_history (account_id, group_id, checked_at DESC);
