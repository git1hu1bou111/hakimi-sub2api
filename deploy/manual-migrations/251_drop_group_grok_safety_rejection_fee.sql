-- 取消分组级"Grok 安全拒绝按次固定费"。
-- 安全拒绝改为落一条标准 token 用量行（gateway.grok.safety_rejection_billing），
-- 由官方价卡反解 token 合成金额，下游不需要二开 sub2api 即可自然计费；
-- 分组上的固定费字段因此不再有读写方，直接删除。
ALTER TABLE groups DROP COLUMN IF EXISTS grok_safety_rejection_fee;
