-- Compatibility repair for installations where migration 143 was recorded
-- but the Ent-backed column was not present in the groups table.
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS models_list_config JSONB NOT NULL DEFAULT '{}'::jsonb;
