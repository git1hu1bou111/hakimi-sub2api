-- 渠道缓存率新增「排除冷启动」口径所需的样本计数。
-- 只统计至少命中过一次缓存的成功流式请求，用于展示“可复用场景下的缓存复用率”。
-- 该口径只供渠道状态卡片展示（CacheRateWarm），不影响现有 cache_rate，
-- 因此健康分/健康带、V2 脉冲矩阵、趋势图等仍按原口径计算。
ALTER TABLE channel_monitor_v2_metrics_1m
    ADD COLUMN IF NOT EXISTS cache_sample_read_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_sample_prompt_tokens BIGINT NOT NULL DEFAULT 0;

ALTER TABLE channel_monitor_v2_metrics_rollup
    ADD COLUMN IF NOT EXISTS cache_sample_read_tokens BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_sample_prompt_tokens BIGINT NOT NULL DEFAULT 0;
