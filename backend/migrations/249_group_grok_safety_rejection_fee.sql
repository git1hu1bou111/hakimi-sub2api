-- Grok 安全拒绝固定费（USD/次）：仅对配置了该值的分组生效。
-- NULL 或 0 表示不收费；该费用独立于分组文本倍率。
ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS grok_safety_rejection_fee NUMERIC(20,8);

COMMENT ON COLUMN groups.grok_safety_rejection_fee IS 'Grok safety_review_rejected 固定费（USD/次）；NULL 或 0 表示不收费，不参与文本倍率';
