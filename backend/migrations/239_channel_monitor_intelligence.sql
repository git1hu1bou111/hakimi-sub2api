-- Independent, group-bound active intelligence probes for V2. No credentials
-- are copied into this table; API keys are resolved against the group each run.
CREATE TABLE IF NOT EXISTS channel_monitor_intelligence_history (
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    checked_at TIMESTAMPTZ NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('green', 'yellow', 'red')),
    latency_ms INTEGER NOT NULL DEFAULT 0,
    detail TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (group_id, checked_at)
);
CREATE INDEX IF NOT EXISTS idx_channel_monitor_intelligence_time
    ON channel_monitor_intelligence_history (checked_at);
