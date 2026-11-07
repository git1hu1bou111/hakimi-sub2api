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
