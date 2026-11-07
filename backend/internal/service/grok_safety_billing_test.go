package service

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newGrokSafetyBillingTestService() (*OpenAIGatewayService, *openAIRecordUsageLogRepoStub, *openAIRecordUsageUserRepoStub) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo, nil)
	svc.cfg.Default.RateMultiplier = 1
	return svc, usageRepo, userRepo
}

func grokSafetyTestAPIKey() *APIKey {
	return &APIKey{
		ID:    91,
		User:  &User{ID: 51},
		Group: &Group{ID: 71, Platform: PlatformGrok, RateMultiplier: 1},
	}
}

func grokSafetyTestParams(amount float64) GrokSafetyBillingParameters {
	return GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:           true,
		AmountUSD:         amount,
		CacheReadRatioMin: 0.90,
		CacheReadRatioMax: 0.96,
		OutputTokensMin:   32,
		OutputTokensMax:   256,
		MaxInputTokens:    500_000,
	})
}

// 目标额度必须被命中：合成用量按官方价卡计算的实际扣费与配置额度一致。
// 求解器可能在长上下文跳价带里退到 100% 缓存形态逼近，因此按额度给相对容差。
func TestBuildGrokSafetyBillingPlan_HitsConfiguredAmount(t *testing.T) {
	svc, _, _ := newGrokSafetyBillingTestService()
	apiKey := grokSafetyTestAPIKey()
	account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}
	pricingAt := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		amount    float64
		tolerance float64
	}{
		{amount: 0.01, tolerance: 2e-5},
		{amount: 0.05, tolerance: 2e-5},
		{amount: 0.2, tolerance: 0.005},
	}
	for _, tt := range tests {
		plan, err := svc.BuildGrokSafetyBillingPlan(
			context.Background(), apiKey, account, "grok-4.6", grokSafetyTestParams(tt.amount),
			pricingAt, rand.New(rand.NewSource(int64(tt.amount*1000))),
		)
		require.NoError(t, err)
		require.NotNil(t, plan)
		require.InDelta(t, tt.amount, plan.BilledUSD, math.Max(tt.tolerance, tt.amount*tt.tolerance),
			"billed amount must match configured amount")
		require.GreaterOrEqual(t, plan.OutputTokens, 0)
		require.Greater(t, plan.TotalInputTokens, 0)
		require.Equal(t, plan.TotalInputTokens, plan.InputTokens+plan.CacheReadTokens)
	}
}

// 记录形态：缓存读取恒 >= 90%，输入/输出随机，但金额仍命中额度。
func TestBuildGrokSafetyBillingPlan_CacheHeavyRandomUsage(t *testing.T) {
	svc, _, _ := newGrokSafetyBillingTestService()
	apiKey := grokSafetyTestAPIKey()
	account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}
	pricingAt := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	seen := map[string]bool{}
	for seed := int64(1); seed <= 8; seed++ {
		plan, err := svc.BuildGrokSafetyBillingPlan(
			context.Background(), apiKey, account, "grok-4.6", grokSafetyTestParams(0.05),
			pricingAt, rand.New(rand.NewSource(seed)),
		)
		require.NoError(t, err)
		ratio := float64(plan.CacheReadTokens) / float64(plan.TotalInputTokens)
		require.GreaterOrEqual(t, ratio, 0.90, "cache read share must stay >= 90%")
		require.InDelta(t, 0.05, plan.BilledUSD, math.Max(2e-5, 0.05*2e-4))
		require.Greater(t, plan.OutputTokens, 0)
		seen[planShapeKey(plan)] = true
	}
	require.Greater(t, len(seen), 1, "usage shape must vary across draws")
}

func planShapeKey(plan *GrokSafetyBillingPlan) string {
	return time.Duration(plan.TotalInputTokens).String() + "|" + time.Duration(plan.OutputTokens).String()
}

// 配置归一化：缓存占比下限被夹到 0.90，零值配置回退到默认随机区间。
func TestGrokSafetyBillingParametersFromConfig_Normalizes(t *testing.T) {
	zero := GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{})
	require.Equal(t, minGrokSafetyCacheReadRatio, zero.CacheReadRatioMin)
	require.GreaterOrEqual(t, zero.CacheReadRatioMax, zero.CacheReadRatioMin)
	require.Equal(t, 32, zero.OutputTokensMin)
	require.Equal(t, 256, zero.OutputTokensMax)
	require.Equal(t, 500_000, zero.MaxInputTokens)
	require.False(t, zero.Enabled)

	lowered := GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:           true,
		AmountUSD:         0.05,
		CacheReadRatioMin: 0.10,
		CacheReadRatioMax: 0.20,
	})
	require.Equal(t, minGrokSafetyCacheReadRatio, lowered.CacheReadRatioMin, "cache read floor must stay at 90%")
	require.GreaterOrEqual(t, lowered.CacheReadRatioMax, lowered.CacheReadRatioMin)
}

// 关闭开关或额度为 0 时保持历史行为：不写用量、不扣费。
func TestRecordGrokSafetyRejectionUsage_DisabledWritesNothing(t *testing.T) {
	svc, usageRepo, userRepo := newGrokSafetyBillingTestService()
	svc.cfg.Gateway.Grok.SafetyRejectionBilling = config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:   false,
		AmountUSD: 0.05,
	}
	svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{
		APIKey:    grokSafetyTestAPIKey(),
		Account:   &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth},
		Model:     "grok-4.6",
		RequestID: "rid-disabled",
	})
	require.Zero(t, usageRepo.calls)
	require.Zero(t, userRepo.deductCalls)

	svc.cfg.Gateway.Grok.SafetyRejectionBilling = config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:   true,
		AmountUSD: 0,
	}
	svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{
		APIKey:    grokSafetyTestAPIKey(),
		Account:   &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth},
		Model:     "grok-4.6",
		RequestID: "rid-zero",
	})
	require.Zero(t, usageRepo.calls)
}

// 打开开关后：写一条普通形态的 token 用量行（缓存读取占绝大多数）并按额度扣费。
// 该行与真实请求同源计价，因此下游按 token 计费的实例可以直接复用。
func TestRecordGrokSafetyRejectionUsage_BillsCacheHeavyUsageRow(t *testing.T) {
	svc, usageRepo, _ := newGrokSafetyBillingTestService()
	svc.cfg.Gateway.Grok.SafetyRejectionBilling = config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:           true,
		AmountUSD:         0.05,
		CacheReadRatioMin: 0.90,
		CacheReadRatioMax: 0.96,
		OutputTokensMin:   32,
		OutputTokensMax:   256,
		MaxInputTokens:    500_000,
	}

	svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{
		APIKey:    grokSafetyTestAPIKey(),
		Account:   &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth},
		Model:     "grok-4.6",
		Stream:    true,
		RequestID: "rid-billed",
	})

	require.Equal(t, 1, usageRepo.calls)
	log := usageRepo.lastLog
	require.NotNil(t, log)
	require.Equal(t, "grok-4.6", log.Model)
	require.Equal(t, RequestTypeStream, RequestTypeFromLegacy(log.Stream, log.OpenAIWSMode), "记录保持普通流式用量形态")
	require.Greater(t, log.CacheReadTokens, 0)
	require.Greater(t, log.OutputTokens, 0)
	total := log.InputTokens + log.CacheReadTokens
	require.Greater(t, total, 0)
	require.GreaterOrEqual(t, float64(log.CacheReadTokens)/float64(total), 0.90)
	require.InDelta(t, 0.05, log.ActualCost, math.Max(2e-5, 0.05*2e-4), "row must bill the configured amount")
	require.NotNil(t, log.BillingMode)
	require.Equal(t, string(BillingModeToken), *log.BillingMode, "拒绝扣费走 token 计费口径，不再按次")
}

// Different requested, channel and upstream price cards must still produce the
// configured charge. An unpriced public alias must use the mapped card too.
func TestRecordGrokSafetyRejectionUsage_UsesFinalBillingModel(t *testing.T) {
	const target = 0.05
	tests := []struct {
		name      string
		requested string
		source    string
		price     float64
	}{
		{"requested", "grok-review-requested", BillingModelSourceRequested, 1e-6},
		{"channel_mapped", "grok-review-requested", BillingModelSourceChannelMapped, 5e-6},
		{"upstream", "grok-review-requested", BillingModelSourceUpstream, 1e-5},
		{"unpriced_alias", "public-review-alias", BillingModelSourceChannelMapped, 5e-6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, usageRepo, _ := newGrokSafetyBillingTestService()
			apiKey := grokSafetyTestAPIKey()
			apiKey.GroupID = &apiKey.Group.ID
			enableGrokSafetyBillingForReviewTest(svc, target)
			cards := map[string]*ChannelModelPricing{}
			for model, price := range map[string]float64{
				"grok-review-requested": 1e-6,
				"grok-review-mapped":    5e-6,
				"grok-review-upstream":  1e-5,
			} {
				price := price
				cards[model] = &ChannelModelPricing{
					BillingMode: BillingModeToken,
					InputPrice:  &price, CacheReadPrice: &price, OutputPrice: &price,
				}
			}
			setGrokSafetyReviewPricing(svc, apiKey.Group.ID, cards)
			svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{
				APIKey:  apiKey,
				Account: &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth},
				Model:   tt.requested, UpstreamModel: "grok-review-upstream", RequestID: tt.name,
				ChannelUsageFields: ChannelUsageFields{
					OriginalModel: tt.requested, ChannelMappedModel: "grok-review-mapped",
					BillingModelSource: tt.source,
				},
			})
			require.Equal(t, 1, usageRepo.calls)
			row := usageRepo.lastLog
			require.NotNil(t, row)
			require.InDelta(t, target, row.ActualCost, 1e-6)
			totalTokens := row.InputTokens + row.CacheReadTokens + row.OutputTokens
			require.InDelta(t, target/tt.price, totalTokens, 1,
				"the synthesized tokens must use the selected card before RecordUsage recalculates them")
		})
	}
}

func TestRecordGrokSafetyRejectionUsage_RejectsNonTokenPricing(t *testing.T) {
	for _, mode := range []BillingMode{BillingModePerRequest, BillingModeImage, BillingModeVideo} {
		t.Run(string(mode), func(t *testing.T) {
			svc, usageRepo, userRepo := newGrokSafetyBillingTestService()
			apiKey := grokSafetyTestAPIKey()
			apiKey.GroupID = &apiKey.Group.ID
			enableGrokSafetyBillingForReviewTest(svc, 0.05)
			fixedPrice := 0.201
			setGrokSafetyReviewPricing(svc, apiKey.Group.ID, map[string]*ChannelModelPricing{
				"grok-review-fixed": {BillingMode: mode, PerRequestPrice: &fixedPrice},
			})
			account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}
			plan, err := svc.BuildGrokSafetyBillingPlan(context.Background(), apiKey, account,
				"grok-review-fixed", grokSafetyTestParams(0.05), time.Now(), rand.New(rand.NewSource(1)))
			require.ErrorContains(t, err, "not token pricing")
			require.Nil(t, plan)
			svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{
				APIKey: apiKey, Account: account, Model: "grok-review-fixed", RequestID: "fixed-" + string(mode),
			})
			require.Zero(t, usageRepo.calls, "must not charge the channel's fixed price as the configured safety fee")
			require.Zero(t, userRepo.deductCalls)
		})
	}
}

func TestRecordGrokSafetyRejectionUsage_RejectsUnreachableAmount(t *testing.T) {
	for _, tt := range []struct {
		name       string
		price      float64
		inputLimit int
	}{
		{"input_limit_below_target", 1e-6, 10},
		{"minimum_token_above_target", 0.1, 500_000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc, usageRepo, userRepo := newGrokSafetyBillingTestService()
			apiKey := grokSafetyTestAPIKey()
			apiKey.GroupID = &apiKey.Group.ID
			enableGrokSafetyBillingForReviewTest(svc, 0.05)
			svc.cfg.Gateway.Grok.SafetyRejectionBilling.MaxInputTokens = tt.inputLimit
			price := tt.price
			setGrokSafetyReviewPricing(svc, apiKey.Group.ID, map[string]*ChannelModelPricing{
				"grok-review-unreachable": {
					BillingMode: BillingModeToken,
					InputPrice:  &price, CacheReadPrice: &price, OutputPrice: &price,
				},
			})
			account := &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeOAuth}
			params := GrokSafetyBillingParametersFromConfig(svc.cfg.Gateway.Grok.SafetyRejectionBilling)
			plan, err := svc.BuildGrokSafetyBillingPlan(context.Background(), apiKey, account,
				"grok-review-unreachable", params, time.Now(), rand.New(rand.NewSource(1)))
			require.ErrorContains(t, err, "cannot be reached within tolerance")
			require.Nil(t, plan)

			svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{
				APIKey: apiKey, Account: account, Model: "grok-review-unreachable", RequestID: tt.name,
			})
			require.Zero(t, usageRepo.calls, "unreachable fees must not write misleading usage")
			require.Zero(t, userRepo.deductCalls, "unreachable fees must not charge a different amount")
		})
	}
}

func enableGrokSafetyBillingForReviewTest(svc *OpenAIGatewayService, amount float64) {
	svc.cfg.Gateway.Grok.SafetyRejectionBilling = config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled: true, AmountUSD: amount,
		CacheReadRatioMin: 1, CacheReadRatioMax: 1,
		OutputTokensMin: 32, OutputTokensMax: 32, MaxInputTokens: 500_000,
	}
}

func setGrokSafetyReviewPricing(svc *OpenAIGatewayService, groupID int64, cards map[string]*ChannelModelPricing) {
	cache := newEmptyChannelCache()
	for model, card := range cards {
		cache.pricingByGroupModel[channelModelKey{groupID: groupID, platform: PlatformGrok, model: model}] = card
	}
	cache.channelByGroupID[groupID] = &Channel{ID: groupID, Status: StatusActive}
	cache.groupPlatform[groupID] = PlatformGrok
	cache.loadedAt = time.Now()
	channels := &ChannelService{}
	channels.cache.Store(cache)
	svc.resolver = NewModelPricingResolver(channels, svc.billingService)
}
