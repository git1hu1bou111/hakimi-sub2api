-- Account-level intelligence now stores one row per (account, model, minute)
-- instead of one row per (account, group, minute): an account that belongs to
-- several groups is probed once per model and must not be duplicated per group.
ALTER TABLE channel_monitor_account_intelligence_history
    ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT 'gpt-6-astra';

-- Collapse legacy per-group duplicates (same account/minute) before the primary
-- key changes shape.
DELETE FROM channel_monitor_account_intelligence_history a
USING channel_monitor_account_intelligence_history b
WHERE a.ctid < b.ctid
  AND a.account_id = b.account_id
  AND a.model = b.model
  AND a.checked_at = b.checked_at;

ALTER TABLE channel_monitor_account_intelligence_history
    DROP CONSTRAINT IF EXISTS channel_monitor_account_intelligence_history_pkey;
ALTER TABLE channel_monitor_account_intelligence_history
    DROP COLUMN IF EXISTS group_id CASCADE;

ALTER TABLE channel_monitor_account_intelligence_history
    ADD PRIMARY KEY (account_id, model, checked_at);

CREATE INDEX IF NOT EXISTS idx_account_intelligence_history_account_model_time
    ON channel_monitor_account_intelligence_history (account_id, model, checked_at DESC);
