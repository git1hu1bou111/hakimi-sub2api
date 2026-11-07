package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"go.uber.org/zap"
)

// minGrokSafetyCacheReadRatio 是合成用量里缓存读取占比的硬下限。
// 需求要求"90%+ 缓存读取"，配置只能在该值之上调整，不能把记录改成非缓存形态。
const minGrokSafetyCacheReadRatio = 0.90

// GrokSafetyBillingParameters 是 gateway.grok.safety_rejection_billing 的运行时口径。
// 它由配置归一化而来，保证：开关、目标额度、缓存读取下限（>=0.90）、输出 token 区间、
// 合成输入上限在任何调用方眼里都是自洽的。
type GrokSafetyBillingParameters struct {
	Enabled           bool
	AmountUSD         float64
	CacheReadRatioMin float64
	CacheReadRatioMax float64
	OutputTokensMin   int
	OutputTokensMax   int
	MaxInputTokens    int
}

// GrokSafetyBillingParametersFromConfig 归一化配置：缓存读取占比夹到 [0.90,1]，
// 区间非法时退回安全默认值，避免配置笔误把扣费记录改成非法形态。
func GrokSafetyBillingParametersFromConfig(cfg config.GatewayGrokSafetyRejectionBillingConfig) GrokSafetyBillingParameters {
	return GrokSafetyBillingParameters{
		Enabled:           cfg.Enabled,
		AmountUSD:         cfg.AmountUSD,
		CacheReadRatioMin: cfg.CacheReadRatioMin,
		CacheReadRatioMax: cfg.CacheReadRatioMax,
		OutputTokensMin:   cfg.OutputTokensMin,
		OutputTokensMax:   cfg.OutputTokensMax,
		MaxInputTokens:    cfg.MaxInputTokens,
	}.normalize()
}

// normalize 把参数夹到自洽区间；幂等，可被配置入口与求解入口重复调用。
func (params GrokSafetyBillingParameters) normalize() GrokSafetyBillingParameters {
	if math.IsNaN(params.AmountUSD) || math.IsInf(params.AmountUSD, 0) || params.AmountUSD < 0 {
		params.AmountUSD = 0
	}
	if params.CacheReadRatioMin < minGrokSafetyCacheReadRatio || params.CacheReadRatioMin > 1 {
		params.CacheReadRatioMin = minGrokSafetyCacheReadRatio
	}
	if params.CacheReadRatioMax < params.CacheReadRatioMin || params.CacheReadRatioMax > 1 {
		params.CacheReadRatioMax = math.Min(1, params.CacheReadRatioMin+0.06)
	}
	if params.OutputTokensMin < 0 {
		params.OutputTokensMin = 0
	}
	if params.OutputTokensMin == 0 && params.OutputTokensMax == 0 {
		// 未配置（含手工构造的零值 Config）时回退到默认随机区间。
		params.OutputTokensMin, params.OutputTokensMax = 32, 256
	}
	if params.OutputTokensMax < params.OutputTokensMin {
		params.OutputTokensMax = params.OutputTokensMin
	}
	if params.MaxInputTokens <= 0 {
		params.MaxInputTokens = 500_000
	}
	return params
}

// GrokSafetyRejectionBillingInput 是"确认的 Grok 安全拒绝"落一条 token 用量行所需的入参。
// 它与 CyberPolicyUsageInput 的区别是：用量不由上游报告，而是按官方价卡反向合成。
type GrokSafetyRejectionBillingInput struct {
	APIKey       *APIKey
	Account      *Account
	Subscription *UserSubscription
	// Model is the requested model; UpstreamModel is captured from the rejection.
	// ChannelUsageFields selects the same billing model as ordinary usage.
	Model         string
	UpstreamModel string
	// Stream 只影响用量行的 transport 语义（sync/stream），不影响计费。
	Stream             bool
	RequestID          string
	InboundEndpoint    string
	UpstreamEndpoint   string
	UserAgent          string
	IPAddress          string
	SessionID          string
	RequestPayloadHash string
	APIKeyService      APIKeyQuotaUpdater
	NativeCompactionV2 bool
	PricingAt          time.Time
	ChannelUsageFields
}

// GrokSafetyBillingPlan 是一次安全拒绝合成出来的用量与金额。
type GrokSafetyBillingPlan struct {
	Model string
	// Multiplier 是本次请求最终生效的 token 倍率（含分组/用户倍率与高峰因子）。
	Multiplier float64
	// TotalInputTokens 是上游口径的输入总数（未缓存输入 + 缓存读取）。
	TotalInputTokens int
	// InputTokens 是未缓存输入，落库时写入 usage_logs.input_tokens。
	InputTokens int
	// CacheReadTokens 是缓存读取部分，至少占输入总数的 90%。
	CacheReadTokens int
	OutputTokens    int
	CacheReadRatio  float64
	// TargetUSD 是配置的扣费额度，BilledUSD 是合成用量按官方价卡算出的实际扣费。
	TargetUSD float64
	BilledUSD float64
	Cost      *CostBreakdown
}

// GrokSafetyRejectionBillingParameters 返回当前进程生效的安全拒绝计费参数。
// 后台设置（settings 表）优先；没有设置记录时回退 config.yaml 的默认值。
func (s *OpenAIGatewayService) GrokSafetyRejectionBillingParameters(ctx context.Context) GrokSafetyBillingParameters {
	if s == nil {
		return GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{})
	}
	if s.settingService != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return s.settingService.GetGrokSafetyRejectionBillingSettings(ctx).toGrokSafetyBillingParameters()
	}
	if s.cfg == nil {
		return GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{})
	}
	return GrokSafetyBillingParametersFromConfig(s.cfg.Gateway.Grok.SafetyRejectionBilling)
}

// GrokSafetyRejectionBillingEnabled 供 handler 在无标记时快速短路。
func (s *OpenAIGatewayService) GrokSafetyRejectionBillingEnabled(ctx context.Context, apiKeys ...*APIKey) bool {
	var apiKey *APIKey
	if len(apiKeys) > 0 {
		apiKey = apiKeys[0]
	}
	params, legacyFee, err := s.grokSafetyBillingPolicy(ctx, apiKey)
	if err != nil {
		logger.L().Warn("grok_safety_rejection.policy_failed", zap.Error(err))
	}
	return err == nil && (legacyFee > 0 || params.Enabled && params.AmountUSD > 0)
}

// RecordGrokSafetyRejectionUsage 为一次确认的 Grok 安全拒绝记录一条按官方价卡
// 合成的 token 用量行：输入/输出 token 随机、缓存读取 >=90%，并且总金额命中
// 配置的扣费额度。未配置新版全局规则时兼容旧分组固定费；显式关闭新版
// 设置时不回退旧规则。两条计费路径互斥，不会同时收取两次费用。
//
// 之所以用"普通用量行"而不是分组按次固定费：用量行是 sub2api 的原生计费载体，
// 下游、下游的下游拿到同样的 usage 就能按 token 自然计费，不必二开或配置私有
// 的"安全拒绝固定费"字段。
func (s *OpenAIGatewayService) RecordGrokSafetyRejectionUsage(ctx context.Context, in GrokSafetyRejectionBillingInput) {
	if s == nil || s.billingService == nil {
		return
	}
	if in.APIKey == nil || in.APIKey.User == nil || in.Account == nil {
		return
	}
	params, legacyFee, err := s.grokSafetyBillingPolicy(ctx, in.APIKey)
	if err != nil {
		logger.L().Warn("grok_safety_rejection.policy_failed", zap.Error(err))
		return
	}
	if legacyFee > 0 {
		if in.Account.Platform != PlatformGrok {
			return
		}
		if err := s.recordLegacyGrokSafetyRejectionUsage(ctx, in, legacyFee); err != nil {
			logger.L().Error("grok_safety_rejection.record_usage_failed", zap.Error(err))
		} else {
			logger.L().Info("grok_safety_rejection.legacy_billed",
				zap.String("request_id", in.RequestID), zap.Int64("account_id", in.Account.ID),
				zap.Float64("billed_usd", legacyFee))
		}
		return
	}
	if !params.Enabled || params.AmountUSD <= 0 {
		return
	}
	pricingAt := in.PricingAt
	if pricingAt.IsZero() {
		pricingAt = timezone.Now()
	}
	result := &OpenAIForwardResult{
		RequestID:     in.RequestID,
		Model:         strings.TrimSpace(in.Model),
		UpstreamModel: strings.TrimSpace(in.UpstreamModel),
		BillingModel:  strings.TrimSpace(in.UpstreamModel),
		Stream:        in.Stream,
	}
	if result.Model == "" {
		result.Model = result.UpstreamModel
		if result.Model == "" {
			result.Model = grokDefaultResponsesModel
		}
	}
	billingModels := openAIRecordUsageBillingModels(result, in.ChannelUsageFields)
	rng := rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // 仅用于用量形态随机化，不涉及安全用途
	plan, err := s.buildGrokSafetyBillingPlan(ctx, in.APIKey, in.Account, billingModels, params, pricingAt, rng)
	if err != nil {
		logger.L().With(
			zap.String("component", "service.openai_gateway.grok_safety"),
			zap.String("request_id", in.RequestID),
			zap.String("model", in.Model),
			zap.Int64("api_key_id", in.APIKey.ID),
			zap.Int64("account_id", in.Account.ID),
			zap.Error(err),
		).Warn("grok_safety_rejection.billing_plan_failed")
		return
	}

	result.Usage = OpenAIUsage{
		InputTokens:          plan.TotalInputTokens,
		CacheReadInputTokens: plan.CacheReadTokens,
		OutputTokens:         plan.OutputTokens,
	}
	if err := s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result:             result,
		APIKey:             in.APIKey,
		User:               in.APIKey.User,
		Account:            in.Account,
		Subscription:       in.Subscription,
		InboundEndpoint:    in.InboundEndpoint,
		UpstreamEndpoint:   in.UpstreamEndpoint,
		UserAgent:          in.UserAgent,
		IPAddress:          in.IPAddress,
		SessionID:          in.SessionID,
		RequestPayloadHash: in.RequestPayloadHash,
		APIKeyService:      in.APIKeyService,
		ChannelUsageFields: in.ChannelUsageFields,
		NativeCompactionV2: in.NativeCompactionV2,
		PricingAt:          pricingAt,
	}); err != nil {
		logger.L().With(
			zap.String("component", "service.openai_gateway.grok_safety"),
			zap.String("request_id", in.RequestID),
			zap.String("model", plan.Model),
			zap.Int64("api_key_id", in.APIKey.ID),
			zap.Int64("account_id", in.Account.ID),
			zap.Error(err),
		).Error("grok_safety_rejection.record_usage_failed")
		return
	}
	logger.L().With(
		zap.String("component", "service.openai_gateway.grok_safety"),
		zap.String("request_id", in.RequestID),
		zap.String("model", plan.Model),
		zap.Int64("api_key_id", in.APIKey.ID),
		zap.Int64("user_id", in.APIKey.User.ID),
		zap.Int64("account_id", in.Account.ID),
		zap.Int("input_tokens", plan.InputTokens),
		zap.Int("cache_read_tokens", plan.CacheReadTokens),
		zap.Int("output_tokens", plan.OutputTokens),
		zap.Float64("cache_read_ratio", plan.CacheReadRatio),
		zap.Float64("target_usd", plan.TargetUSD),
		zap.Float64("billed_usd", plan.BilledUSD),
		zap.Float64("multiplier", plan.Multiplier),
	).Info("grok_safety_rejection.billed")
}

// BuildGrokSafetyBillingPlan 把"目标扣费额度"反解成一组官方价卡口径的 token 用量。
//
// 求解思路：
//  1. 先随机抽取输出 token 与缓存读取占比（占比被强制 >= 0.90）；
//  2. 用与 RecordUsage 完全相同的计费函数（含分组/渠道定价、长上下文阶梯、倍率）
//     对输入 token 做一次单调二分，找到实际扣费最接近目标额度的 token 组合；
//  3. 若目标额度小到连最小输出都付不起，先二分收缩输出 token，再解输入；
//  4. 目标落在长上下文跳价带时追加边界形态候选，最后用输出 token 的边际单价微调残差。
//
// 因此落库的用量行与真实请求走同一个计价器：金额、缓存读取占比、模型价卡都自洽。
func (s *OpenAIGatewayService) BuildGrokSafetyBillingPlan(
	ctx context.Context,
	apiKey *APIKey,
	account *Account,
	model string,
	params GrokSafetyBillingParameters,
	pricingAt time.Time,
	rng *rand.Rand,
) (*GrokSafetyBillingPlan, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		model = grokDefaultResponsesModel
	}
	return s.buildGrokSafetyBillingPlan(ctx, apiKey, account, usageBillingModelCandidates(model), params, pricingAt, rng)
}

func (s *OpenAIGatewayService) buildGrokSafetyBillingPlan(
	ctx context.Context,
	apiKey *APIKey,
	account *Account,
	billingModels []string,
	params GrokSafetyBillingParameters,
	pricingAt time.Time,
	rng *rand.Rand,
) (*GrokSafetyBillingPlan, error) {
	if s == nil || s.billingService == nil {
		return nil, errors.New("grok safety billing: billing service is not configured")
	}
	if apiKey == nil || apiKey.User == nil {
		return nil, errors.New("grok safety billing: api key user is required")
	}
	if account == nil {
		return nil, errors.New("grok safety billing: account is required")
	}
	params = params.normalize()
	target := QuantizeUsageBillingAmount(params.AmountUSD)
	if target <= 0 {
		return nil, errors.New("grok safety billing: amount_usd must be positive")
	}
	model := firstUsageBillingModel(billingModels)
	if pricingAt.IsZero() {
		pricingAt = timezone.Now()
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano())) //nolint:gosec // 形态随机化
	}

	// 与 RecordUsage 完全同源的倍率：系统默认 -> 分组/用户倍率 -> 高峰因子。
	multiplier := 1.0
	if s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if apiKey.GroupID != nil && apiKey.Group != nil {
		multiplier = s.ResolveUserGroupRateMultiplier(ctx, apiKey.User.ID, *apiKey.GroupID, apiKey.Group.RateMultiplier)
	}
	multiplier, _ = computePeakAwareMultipliers(apiKey, multiplier, pricingAt)

	longContextGate := openAILongContextBillingGate(account)

	ratio := params.CacheReadRatioMin
	if params.CacheReadRatioMax > params.CacheReadRatioMin {
		ratio += rng.Float64() * (params.CacheReadRatioMax - params.CacheReadRatioMin)
	}
	ratio = math.Min(math.Max(ratio, minGrokSafetyCacheReadRatio), 1)

	outputTokens := params.OutputTokensMin
	if params.OutputTokensMax > params.OutputTokensMin {
		outputTokens += rng.Intn(params.OutputTokensMax - params.OutputTokensMin + 1)
	}

	// evaluateAt 用真实计费函数（含分组/渠道定价、长上下文阶梯、倍率）评估一组
	// 合成 token 的实际扣费；求解与最终微调共用它，保证落库金额可复现。
	evaluateAt := func(cacheRatio float64, totalInput, outputTokens int) (float64, *CostBreakdown, error) {
		cacheRead := grokSafetyCacheReadCount(totalInput, cacheRatio)
		tokens := UsageTokens{
			InputTokens:     totalInput - cacheRead,
			CacheReadTokens: cacheRead,
			OutputTokens:    outputTokens,
		}
		bd, evalErr := s.calculateOpenAIRecordUsageCost(
			ctx, &OpenAIForwardResult{}, apiKey, billingModels,
			multiplier, multiplier, multiplier, multiplier, tokens, "", longContextGate, false, pricingAt,
		)
		if evalErr != nil {
			return 0, nil, evalErr
		}
		if bd == nil {
			return 0, nil, fmt.Errorf("grok safety billing: empty cost breakdown for model %s", model)
		}
		if bd.BillingMode != "" && bd.BillingMode != string(BillingModeToken) {
			return 0, nil, fmt.Errorf("grok safety billing: model %s uses %s pricing, not token pricing", model, bd.BillingMode)
		}
		return bd.ActualCost, bd, nil
	}

	// solveFor 用给定（缓存读取占比, 输出 token）求解最接近目标额度的输入总数。
	solveFor := func(ratio float64, output int) (input, billed int, cost float64, breakdown *CostBreakdown, err error) {
		evaluate := func(totalInput, outputTokens int) (float64, *CostBreakdown, error) {
			return evaluateAt(ratio, totalInput, outputTokens)
		}

		// 第一步：目标额度小于"最小输出"成本时，二分收缩输出 token。
		if minCost, _, evalErr := evaluate(1, output); evalErr == nil && minCost > target && output > 0 {
			loOut, hiOut := 0, output-1
			bestOutput := 0
			for loOut <= hiOut {
				mid := loOut + (hiOut-loOut)/2
				c, _, err := evaluate(1, mid)
				if err != nil {
					return 0, 0, 0, nil, err
				}
				if c <= target {
					bestOutput = mid
					loOut = mid + 1
				} else {
					hiOut = mid - 1
				}
			}
			output = bestOutput
		}

		// 第二步：对输入总数做单调二分（长上下文阶梯会带来跳变，二分收敛到 <= 目标的最大值，
		// 再与相邻一档比较取更接近者）。
		lo, hi := 1, params.MaxInputTokens
		bestInput := 0
		var bestCost float64
		var bestBreakdown *CostBreakdown
		for lo <= hi {
			mid := lo + (hi-lo)/2
			c, bd, err := evaluate(mid, output)
			if err != nil {
				return 0, 0, 0, nil, err
			}
			if c <= target {
				bestInput, bestCost, bestBreakdown = mid, c, bd
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		if bestInput == 0 {
			c, bd, err := evaluate(1, output)
			if err != nil {
				return 0, 0, 0, nil, err
			}
			bestInput, bestCost, bestBreakdown = 1, c, bd
		} else if bestInput < params.MaxInputTokens {
			if c, bd, err := evaluate(bestInput+1, output); err == nil &&
				math.Abs(c-target) < math.Abs(bestCost-target) {
				bestInput, bestCost, bestBreakdown = bestInput+1, c, bd
			}
		}
		return bestInput, output, bestCost, bestBreakdown, nil
	}

	// 首选按配置区间随机抽到的形态；长上下文阶梯会在阈值处跳价，
	// 目标额度落进跳变区间时再尝试边界形态（含 100% 缓存）取最接近者。
	bestInput, bestOutput, bestCost, bestBreakdown, err := solveFor(ratio, outputTokens)
	if err != nil {
		return nil, err
	}
	bestRatio := ratio
	tolerance := math.Max(1e-6, target*0.005)
	if math.Abs(bestCost-target) > tolerance {
		type shape struct {
			ratio  float64
			output int
		}
		candidates := []shape{
			{params.CacheReadRatioMin, outputTokens},
			{params.CacheReadRatioMax, outputTokens},
			{ratio, params.OutputTokensMin},
			{ratio, params.OutputTokensMax},
			{1, outputTokens},
			{1, params.OutputTokensMin},
		}
		seen := map[shape]bool{{ratio, outputTokens}: true}
		for _, candidate := range candidates {
			if seen[candidate] {
				continue
			}
			seen[candidate] = true
			input, output, cost, breakdown, evalErr := solveFor(candidate.ratio, candidate.output)
			if evalErr != nil {
				continue
			}
			if math.Abs(cost-target) < math.Abs(bestCost-target) {
				bestInput, bestOutput, bestCost, bestBreakdown = input, output, cost, breakdown
				bestRatio = candidate.ratio
			}
		}
	}

	cacheRead := grokSafetyCacheReadCount(bestInput, bestRatio)

	// 末段微调：输出 token 的边际单价是最细的粒度，用它把残差压到最低，
	// 让落库金额尽量等于配置额度（而不是被整数输入 token 步进卡住）。
	if math.Abs(bestCost-target) > 1e-9 && bestOutput > 0 {
		if oneLess, _, err := evaluateAt(bestRatio, bestInput, bestOutput-1); err == nil && oneLess < bestCost {
			step := bestCost - oneLess
			// 保留至少 1 个输出 token：拒绝记录仍应像一次真实调用，不把 completion 清零。
			if k := int(math.Round((bestCost - target) / step)); k > 0 && k < bestOutput {
				if cost, breakdown, err := evaluateAt(bestRatio, bestInput, bestOutput-k); err == nil &&
					math.Abs(cost-target) < math.Abs(bestCost-target) {
					bestOutput, bestCost, bestBreakdown = bestOutput-k, cost, breakdown
				}
			}
		}
	}

	billedUSD := QuantizeUsageBillingAmount(bestCost)
	if math.IsNaN(billedUSD) || math.IsInf(billedUSD, 0) || math.Abs(billedUSD-target) > tolerance {
		return nil, fmt.Errorf("grok safety billing: target %.8f USD cannot be reached within tolerance %.8f (closest %.8f)", target, tolerance, billedUSD)
	}

	plan := &GrokSafetyBillingPlan{
		Model:            model,
		Multiplier:       multiplier,
		TotalInputTokens: bestInput,
		InputTokens:      bestInput - cacheRead,
		CacheReadTokens:  cacheRead,
		OutputTokens:     bestOutput,
		CacheReadRatio:   bestRatio,
		TargetUSD:        target,
		BilledUSD:        billedUSD,
		Cost:             bestBreakdown,
	}
	if plan.TotalInputTokens > 0 {
		plan.CacheReadRatio = float64(plan.CacheReadTokens) / float64(plan.TotalInputTokens)
	}
	return plan, nil
}

// grokSafetyCacheReadCount 把输入总数按给定占比换算成缓存读取 token，
// 并强制不低于 90%：四舍五入后不足时用 ceil(0.9*N) 兜底。
func grokSafetyCacheReadCount(totalInput int, ratio float64) int {
	if totalInput <= 0 {
		return 0
	}
	cacheRead := int(math.Round(float64(totalInput) * ratio))
	if minCache := int(math.Ceil(float64(totalInput) * minGrokSafetyCacheReadRatio)); cacheRead < minCache {
		cacheRead = minCache
	}
	if cacheRead > totalInput {
		cacheRead = totalInput
	}
	if cacheRead < 0 {
		cacheRead = 0
	}
	return cacheRead
}
