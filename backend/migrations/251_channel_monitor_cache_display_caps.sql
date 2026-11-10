-- Presentation preferences only; no changes to usage, aggregation or billing.
ALTER TABLE channel_monitor_v2_config
    ADD COLUMN IF NOT EXISTS cache_rate_display_caps JSONB NOT NULL DEFAULT '{}'::jsonb;
