# Grok 安全审查拒绝计费（Usage-Row 模式）

## 背景

上游 xAI/Grok 在命中安全审查时会返回结构化拒绝，例如：

- HTTP 403 + `{"error":{"code":"safety_review_rejected", ...}}`
- HTTP 403 + `{"status_code":403,"error_code":"violation_fee.grok.csam"}`
- 流式 200 + `response.failed` 里的错误体，message 末尾带 `Failed check: SAFETY_CHECK_TYPE`

这些拒绝不会产生任何 token 用量，但上游会实际处理请求。本项目因此支持"确认拒绝也计费"。

**本项目的实现方式**：确认拒绝时落一条**标准 token 用量行**（按模型官方价卡反解 token 数量与金额）。

> 旧实现是"分组按次固定费"（`groups.grok_safety_rejection_fee`，`billing_mode=per_request`，
> token 全为 0）。滚动升级时仍兼容已有分组费用，见文末"迁移"。

## 为什么用"标准用量行"而不是"按次固定费"

`usage_logs` 是 sub2api 的原生计费载体。把拒绝计费表达成一条普通 token 用量行，带来三个直接好处：

1. **下游不需要二开**：下游（以及下游的下游）只要是按 token 计费的 sub2api 实例，拿到这条
   usage 就能自然计费，不需要认识 `grok_safety_rejection_fee` 这种私有字段，也不需要打补丁。
2. **口径统一**：金额、模型价卡、缓存读取占比、分组/用户倍率、高峰因子、长上下文阶梯，全部
   走与真实请求**同一个计价器**（`calculateOpenAIRecordUsageTokenCost`）。因此账面上这条记录
   与一次真实调用没有区别，报表、渠道统计、账号统计都能自然归集。
3. **无需分组级配置**：开关与额度是全局运行时设置（后台可改、数据库可覆盖），不用逐个分组配置，
   也不会因为新建分组忘记配置而漏收费。

## 触发条件（只认结构化拒绝）

`backend/internal/service/grok_safety_review.go` 精确认定：

- `error.code` / `response.error.code` / `error_code` / `error_type` 为
  `safety_review_rejected`，或以 `violation_fee.grok.` 开头；
- 或者响应体（403/200）里含大写的 `SAFETY_CHECK_TYPE`。

**不视为**安全拒绝、因此**不收费**的情况：

- 裸 403（无结构化码、无 `SAFETY_CHECK_TYPE`）；
- `new_sensitive` 等内容策略拒绝；
- 账号封禁 / 资格不足 / 额度上限 / 浏览器校验类的 4xx；
- 普通模型侧的道歉文案（"I'm sorry, I can't help with that request"）且无上述标记。

命中后同一逻辑请求只记一次（`ops_grok_safety_billing_recorded`），SSE 里重复下发的错误帧
不会重复扣费；多轮 WS 每个 turn 收尾会重置该守卫。

## 计费形态（合成用量）

`backend/internal/service/grok_safety_billing.go` 的求解流程：

1. 随机抽取输出 token `[output_tokens_min, output_tokens_max]` 与缓存读取占比
   `[cache_read_ratio_min, cache_read_ratio_max]`，占比硬性夹到 `>= 0.90`；
2. 用真实计费函数对"输入 token 总数"做单调二分，使实际扣费最接近 `amount_usd`；
3. 目标额度小到连最小输出都付不起时，先二分收缩输出 token，再解输入；
4. 目标落在长上下文跳价带时追加边界形态候选（含 100% 缓存），取最接近者；
5. 最后用输出 token 的边际单价微调残差，同时保留至少 1 个输出 token，让记录仍像一次真实调用。

因此落库的行满足：

| 字段 | 形态 |
| --- | --- |
| `input_tokens` | 未缓存输入（总输入 - 缓存读取） |
| `cache_read_tokens` | `>= 90%` 的总输入 |
| `output_tokens` | 配置区间内的随机值 |
| `billing_mode` | `token`（不再是 `per_request`） |
| `request_type` | 普通 `sync` / `stream`（不再是 `grok_safety_rejected`） |
| `actual_cost` | 命中 `amount_usd`（已含分组/用户倍率与高峰因子） |

## 配置

### config.yaml（默认值来源）

```yaml
gateway:
  grok:
    safety_rejection_billing:
      enabled: false            # 全局 token 计费开关；未保存全局设置时仍兼容旧分组固定费
      amount_usd: 0.05          # 每确认一次拒绝的目标扣费（USD）
      cache_read_ratio_min: 0.90
      cache_read_ratio_max: 0.96
      output_tokens_min: 32
      output_tokens_max: 256
      max_input_tokens: 500000  # 合成输入上限，防配置笔误
```

### 后台运行时设置（数据库覆盖，立即生效，30s 进程内缓存）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/v1/admin/settings/grok-safety-billing` | 读取生效值 + `override` + `defaults` |
| PUT | `/api/v1/admin/settings/grok-safety-billing` | 保存（校验非法值直接 400） |
| DELETE | `/api/v1/admin/settings/grok-safety-billing` | 删除覆盖值，回退 config.yaml |

后台设置优先于 config.yaml；读库失败时 fail-safe 回退默认值，绝不阻塞计费判定。

前端入口：管理端「系统设置 → Grok 安全审查拒绝计费」。

## 关键代码位置

| 文件 | 作用 |
| --- | --- |
| `backend/internal/service/grok_safety_review.go` | 拒绝识别与 gin context 打标 |
| `backend/internal/service/grok_safety_billing.go` | 参数归一化、金额反解、落用量行 |
| `backend/internal/service/setting_grok_safety_billing.go` | 运行时设置读写与缓存 |
| `backend/internal/handler/openai_gateway_handler.go` | `recordGrokSafetyBillingIfMarked` 及调用点 |
| `backend/internal/handler/admin/setting_handler_grok_safety_billing.go` | 后台 API |
| `backend/internal/config/config.go` | 配置结构、默认值、校验 |

## 迁移说明（保留已有分组收费规则）

- 数据库没有保存新版全局设置、且 config.yaml 未开启全局计费时，新网关继续读取 `groups.grok_safety_rejection_fee`：NULL/0 免费，正数精确收取原分组金额，不乘分组、用户、高峰或账号倍率，不扣上游账号配额。仍使用零 token 的 `per_request` 行，不将旧固定费转换成合成 token。
- 显式保存的新版全局设置优先，包括显式 `enabled=false`；启用新版全局规则后走本文的 token 计费路径，不再叠加旧固定费。兼容读取失败时不收费并记录 `policy_failed`。
- 哈基米主网关与独立图片进程共用数据库，因此升级时保留旧列及原有金额。清理 SQL 位于 `deploy/manual-migrations/251_drop_group_grok_safety_rejection_fee.sql`，不参与启动迁移；只有在所有旧读方和兼容计费规则退役、金额已核对备份且确认清理后，才可单独执行。
  历史 `migrations/249` 保留不动，只为让已有库的升级路径可回放。
- 新版 `groups` 创建/更新 API 不接受 `grok_safety_rejection_fee` 字段（传了会被忽略）。需要继续编辑旧分组收费规则时应保留旧管理后台；兼容读取不依赖认证快照，旧后台调整金额后新网关立即读取新值。
- 历史 `usage_logs` 里 `request_type=6`（`grok_safety_rejected`）的行仍然可读；
  `usage_logs_request_type_check` 仍允许 `0..6`，兼容固定费路径继续写 6。
- API Key 认证快照版本号提升到 `v28`，旧快照自动重建。

## 观测

命中并落账后输出结构化日志（`component=service.openai_gateway.grok_safety`）：

- `grok_safety_rejection.billing_queued`：handler 入队
- `grok_safety_rejection.legacy_billed`：旧分组固定费落账，金额为原配置值
- `grok_safety_rejection.billed`：落账成功，含 `input_tokens` / `cache_read_tokens` /
  `output_tokens` / `cache_read_ratio` / `target_usd` / `billed_usd` / `multiplier`
- `grok_safety_rejection.billing_plan_failed` / `record_usage_failed`：失败原因（不重试，避免重复扣费）
