package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type grokSafetySettingRepo struct {
	values map[string]string
	getErr error
	setErr error
}

func (r *grokSafetySettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *grokSafetySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", ErrSettingNotFound
}

func (r *grokSafetySettingRepo) Set(_ context.Context, key, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *grokSafetySettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}

func (r *grokSafetySettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }

func (r *grokSafetySettingRepo) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}

func (r *grokSafetySettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func resetGrokSafetyBillingCacheForTest() {
	grokSafetyBillingCache.Store(&cachedGrokSafetyBilling{})
}

func newGrokSafetyBillingTestSettingService(repo SettingRepository) *SettingService {
	cfg := &config.Config{}
	cfg.Gateway.Grok.SafetyRejectionBilling = config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:           true,
		AmountUSD:         0.07,
		CacheReadRatioMin: 0.90,
		CacheReadRatioMax: 0.95,
		OutputTokensMin:   16,
		OutputTokensMax:   128,
		MaxInputTokens:    123456,
	}
	return NewSettingService(repo, cfg)
}

// 数据库没有记录时沿用 config.yaml 默认值，并标记 override=false。
func TestGrokSafetyBillingSettings_DefaultsFromConfig(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	svc := newGrokSafetyBillingTestSettingService(&grokSafetySettingRepo{})

	settings, override := svc.GetGrokSafetyRejectionBillingState(context.Background())
	require.False(t, override)
	require.True(t, settings.Enabled)
	require.Equal(t, 0.07, settings.AmountUSD)
	require.Equal(t, 0.95, settings.CacheReadRatioMax)
	require.Equal(t, 123456, settings.MaxInputTokens)
}

// 保存后立即生效（缓存失效），并返回 override=true。
func TestGrokSafetyBillingSettings_SetGetRoundTrip(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	repo := &grokSafetySettingRepo{values: map[string]string{}}
	svc := newGrokSafetyBillingTestSettingService(repo)

	saved, err := svc.SetGrokSafetyRejectionBillingSettings(context.Background(), GrokSafetyRejectionBillingSettings{
		Enabled:           true,
		AmountUSD:         0.12,
		CacheReadRatioMin: 0.90,
		CacheReadRatioMax: 0.93,
		OutputTokensMin:   24,
		OutputTokensMax:   240,
		MaxInputTokens:    250000,
	})
	require.NoError(t, err)
	require.Equal(t, 0.12, saved.AmountUSD)
	require.Contains(t, repo.values[SettingKeyGrokSafetyRejectionBilling], `"amount_usd":0.12`)

	settings, override := svc.GetGrokSafetyRejectionBillingState(context.Background())
	require.True(t, override)
	require.Equal(t, 0.12, settings.AmountUSD)
	require.Equal(t, 0.93, settings.CacheReadRatioMax)
	require.Equal(t, 240, settings.OutputTokensMax)

	// 另一个进程视角（重新读库）同样拿到保存值。
	resetGrokSafetyBillingCacheForTest()
	again, overrideAgain := svc.GetGrokSafetyRejectionBillingState(context.Background())
	require.True(t, overrideAgain)
	require.Equal(t, 0.12, again.AmountUSD)
}

// 非法值必须在写入前被拒绝。
func TestGrokSafetyBillingSettings_Validation(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	repo := &grokSafetySettingRepo{values: map[string]string{}}
	svc := newGrokSafetyBillingTestSettingService(repo)

	cases := []GrokSafetyRejectionBillingSettings{
		{Enabled: true, AmountUSD: -1, CacheReadRatioMin: 0.9, CacheReadRatioMax: 0.96},
		{Enabled: true, AmountUSD: 0.05, CacheReadRatioMin: 0.5, CacheReadRatioMax: 0.96},
		{Enabled: true, AmountUSD: 0.05, CacheReadRatioMin: 0.9, CacheReadRatioMax: 0.8},
		{Enabled: true, AmountUSD: 0.05, CacheReadRatioMin: 0.9, CacheReadRatioMax: 0.96, OutputTokensMin: 10, OutputTokensMax: 5},
		{Enabled: true, AmountUSD: 0.05, CacheReadRatioMin: 0.9, CacheReadRatioMax: 0.96, MaxInputTokens: -5},
	}
	for i, in := range cases {
		_, err := svc.SetGrokSafetyRejectionBillingSettings(context.Background(), in)
		require.Error(t, err, "case %d must be rejected", i)
	}
	require.Empty(t, repo.values)
}

// 重置删除覆盖值后回退到 config.yaml 默认。
func TestGrokSafetyBillingSettings_Reset(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	repo := &grokSafetySettingRepo{values: map[string]string{}}
	svc := newGrokSafetyBillingTestSettingService(repo)

	_, err := svc.SetGrokSafetyRejectionBillingSettings(context.Background(), GrokSafetyRejectionBillingSettings{
		Enabled:           false,
		AmountUSD:         0.5,
		CacheReadRatioMin: 0.90,
		CacheReadRatioMax: 0.96,
	})
	require.NoError(t, err)

	settings, err := svc.ResetGrokSafetyRejectionBillingSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0.07, settings.AmountUSD, "reset returns config default")

	effective, override := svc.GetGrokSafetyRejectionBillingState(context.Background())
	require.False(t, override)
	require.Equal(t, 0.07, effective.AmountUSD)
}

// 读库失败时 fail-safe 回退默认值，不阻塞计费判定。
func TestGrokSafetyBillingSettings_ReadErrorFallsBack(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	svc := newGrokSafetyBillingTestSettingService(&grokSafetySettingRepo{getErr: errors.New("db down")})
	settings, override := svc.GetGrokSafetyRejectionBillingState(context.Background())
	require.False(t, override)
	require.Equal(t, 0.07, settings.AmountUSD)
}
