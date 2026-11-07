package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"golang.org/x/sync/singleflight"
)

// GrokSafetyRejectionBillingSettings 是 Grok 安全拒绝计费的运行时配置（后台可改）。
// config.yaml 的 gateway.grok.safety_rejection_billing 仍是默认值来源：
// 数据库没有该 key 时直接沿用配置文件，保存后以数据库为准。
type GrokSafetyRejectionBillingSettings struct {
	Enabled           bool    `json:"enabled"`
	AmountUSD         float64 `json:"amount_usd"`
	CacheReadRatioMin float64 `json:"cache_read_ratio_min"`
	CacheReadRatioMax float64 `json:"cache_read_ratio_max"`
	OutputTokensMin   int     `json:"output_tokens_min"`
	OutputTokensMax   int     `json:"output_tokens_max"`
	MaxInputTokens    int     `json:"max_input_tokens"`
}

// 运行时改配置的合法区间（与 config.Validate 保持一致，另加宽松上限防止误填）。
const (
	grokSafetyAmountMaxUSD    = 1000.0
	grokSafetyTokensMax       = 1_000_000
	grokSafetyInputTokensMax  = 10_000_000
	grokSafetyDefaultMaxInput = 500_000
)

// cachedGrokSafetyBilling 缓存生效配置（进程内，30s TTL）。安全拒绝是低频事件，
// 但 handler 的防双扣短路会读它，故仍走缓存，写设置时立即失效。
type cachedGrokSafetyBilling struct {
	value     GrokSafetyRejectionBillingSettings
	override  bool  // true = 数据库有覆盖值，false = 沿用 config.yaml 默认
	err       error // billing must distinguish unavailable settings from confirmed absence
	expiresAt int64 // unix nano
}

var grokSafetyBillingCache atomic.Value // *cachedGrokSafetyBilling
var grokSafetyBillingSF singleflight.Group

const grokSafetyBillingCacheTTL = 30 * time.Second
const grokSafetyBillingErrorTTL = 5 * time.Second
const grokSafetyBillingDBTimeout = 3 * time.Second

// grokSafetyBillingConfigDefaults 把 config.yaml 的默认值转成设置结构。
func grokSafetyBillingConfigDefaults(cfg *config.Config) GrokSafetyRejectionBillingSettings {
	params := GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{})
	if cfg != nil {
		params = GrokSafetyBillingParametersFromConfig(cfg.Gateway.Grok.SafetyRejectionBilling)
	}
	return GrokSafetyRejectionBillingSettings{
		Enabled:           params.Enabled,
		AmountUSD:         params.AmountUSD,
		CacheReadRatioMin: params.CacheReadRatioMin,
		CacheReadRatioMax: params.CacheReadRatioMax,
		OutputTokensMin:   params.OutputTokensMin,
		OutputTokensMax:   params.OutputTokensMax,
		MaxInputTokens:    params.MaxInputTokens,
	}
}

// GrokSafetyRejectionBillingDefaults 返回配置文件里的默认值（后台展示 fallback）。
func (s *SettingService) GrokSafetyRejectionBillingDefaults() GrokSafetyRejectionBillingSettings {
	if s == nil {
		return grokSafetyBillingConfigDefaults(nil)
	}
	return grokSafetyBillingConfigDefaults(s.cfg)
}

func normalizeGrokSafetyBillingSettings(in GrokSafetyRejectionBillingSettings) GrokSafetyRejectionBillingSettings {
	if math.IsNaN(in.AmountUSD) || math.IsInf(in.AmountUSD, 0) || in.AmountUSD < 0 {
		in.AmountUSD = 0
	}
	if in.CacheReadRatioMin < minGrokSafetyCacheReadRatio || in.CacheReadRatioMin > 1 {
		in.CacheReadRatioMin = minGrokSafetyCacheReadRatio
	}
	if in.CacheReadRatioMax < in.CacheReadRatioMin || in.CacheReadRatioMax > 1 {
		in.CacheReadRatioMax = math.Min(1, in.CacheReadRatioMin+0.06)
	}
	if in.OutputTokensMin < 0 {
		in.OutputTokensMin = 0
	}
	if in.OutputTokensMax < in.OutputTokensMin {
		in.OutputTokensMax = in.OutputTokensMin
	}
	if in.MaxInputTokens <= 0 {
		in.MaxInputTokens = grokSafetyDefaultMaxInput
	}
	return in
}

// validateGrokSafetyBillingSettings 是后台写入前的唯一校验来源。
func validateGrokSafetyBillingSettings(in GrokSafetyRejectionBillingSettings) error {
	if math.IsNaN(in.AmountUSD) || math.IsInf(in.AmountUSD, 0) {
		return errors.New("amount_usd must be a finite number")
	}
	if in.AmountUSD < 0 || in.AmountUSD > grokSafetyAmountMaxUSD {
		return fmt.Errorf("amount_usd must be between 0 and %.0f", grokSafetyAmountMaxUSD)
	}
	if in.CacheReadRatioMin < minGrokSafetyCacheReadRatio || in.CacheReadRatioMin > 1 {
		return fmt.Errorf("cache_read_ratio_min must be between %.2f and 1", minGrokSafetyCacheReadRatio)
	}
	if in.CacheReadRatioMax < in.CacheReadRatioMin || in.CacheReadRatioMax > 1 {
		return errors.New("cache_read_ratio_max must be between cache_read_ratio_min and 1")
	}
	if in.OutputTokensMin < 0 || in.OutputTokensMax < in.OutputTokensMin || in.OutputTokensMax > grokSafetyTokensMax {
		return fmt.Errorf("output token range must satisfy 0 <= min <= max <= %d", grokSafetyTokensMax)
	}
	if in.MaxInputTokens < 0 || in.MaxInputTokens > grokSafetyInputTokensMax {
		return fmt.Errorf("max_input_tokens must be between 0 and %d", grokSafetyInputTokensMax)
	}
	return nil
}

// GetGrokSafetyRejectionBillingSettings 返回当前生效的 Grok 安全拒绝计费设置。
// 数据库无记录时回退到 config.yaml 默认值；读取失败时同样回退（fail-safe）。
func (s *SettingService) GetGrokSafetyRejectionBillingSettings(ctx context.Context) GrokSafetyRejectionBillingSettings {
	settings, _ := s.GetGrokSafetyRejectionBillingState(ctx)
	return settings
}

// GetGrokSafetyRejectionBillingState 额外返回是否由数据库覆盖（供后台展示来源）。
func (s *SettingService) GetGrokSafetyRejectionBillingState(ctx context.Context) (GrokSafetyRejectionBillingSettings, bool) {
	settings, override, _ := s.getGrokSafetyRejectionBillingPolicyState(ctx)
	return settings, override
}

// Unlike the display/defaults API, billing may not interpret a failed read as
// confirmation that no explicit global policy exists.
func (s *SettingService) getGrokSafetyRejectionBillingPolicyState(ctx context.Context) (GrokSafetyRejectionBillingSettings, bool, error) {
	defaults := s.GrokSafetyRejectionBillingDefaults()
	if s == nil || s.settingRepo == nil {
		return defaults, false, nil
	}
	if cached, ok := grokSafetyBillingCache.Load().(*cachedGrokSafetyBilling); ok && cached != nil &&
		time.Now().UnixNano() < cached.expiresAt {
		return cached.value, cached.override, cached.err
	}

	value, err, _ := grokSafetyBillingSF.Do("grok_safety_rejection_billing", func() (any, error) {
		dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grokSafetyBillingDBTimeout)
		defer cancel()
		raw, getErr := s.settingRepo.GetValue(dbCtx, SettingKeyGrokSafetyRejectionBilling)
		if getErr != nil || strings.TrimSpace(raw) == "" {
			ttl := grokSafetyBillingCacheTTL
			var policyErr error
			if getErr != nil && !errors.Is(getErr, ErrSettingNotFound) {
				ttl = grokSafetyBillingErrorTTL
				policyErr = fmt.Errorf("read Grok safety billing settings: %w", getErr)
			}
			grokSafetyBillingCache.Store(&cachedGrokSafetyBilling{value: defaults, override: false, err: policyErr, expiresAt: time.Now().Add(ttl).UnixNano()})
			return grokSafetyBillingCachedState{settings: defaults, override: false, err: policyErr}, nil
		}
		var stored GrokSafetyRejectionBillingSettings
		if jsonErr := json.Unmarshal([]byte(raw), &stored); jsonErr != nil {
			policyErr := fmt.Errorf("parse Grok safety billing settings: %w", jsonErr)
			grokSafetyBillingCache.Store(&cachedGrokSafetyBilling{value: defaults, override: false, err: policyErr, expiresAt: time.Now().Add(grokSafetyBillingErrorTTL).UnixNano()})
			return grokSafetyBillingCachedState{settings: defaults, override: false, err: policyErr}, nil
		}
		normalized := normalizeGrokSafetyBillingSettings(stored)
		grokSafetyBillingCache.Store(&cachedGrokSafetyBilling{value: normalized, override: true, expiresAt: time.Now().Add(grokSafetyBillingCacheTTL).UnixNano()})
		return grokSafetyBillingCachedState{settings: normalized, override: true}, nil
	})
	if err != nil {
		return defaults, false, err
	}
	if state, ok := value.(grokSafetyBillingCachedState); ok {
		return state.settings, state.override, state.err
	}
	return defaults, false, errors.New("invalid Grok safety billing cache state")
}

// grokSafetyBillingCachedState 是 singleflight 的返回值（设置 + 来源）。
type grokSafetyBillingCachedState struct {
	settings GrokSafetyRejectionBillingSettings
	override bool
	err      error
}

// SetGrokSafetyRejectionBillingSettings 持久化后台修改，并立刻让进程内缓存失效。
func (s *SettingService) SetGrokSafetyRejectionBillingSettings(ctx context.Context, in GrokSafetyRejectionBillingSettings) (GrokSafetyRejectionBillingSettings, error) {
	if s == nil || s.settingRepo == nil {
		return GrokSafetyRejectionBillingSettings{}, errors.New("settings storage is not configured")
	}
	if err := validateGrokSafetyBillingSettings(in); err != nil {
		return GrokSafetyRejectionBillingSettings{}, err
	}
	normalized := normalizeGrokSafetyBillingSettings(in)
	data, err := json.Marshal(normalized)
	if err != nil {
		return GrokSafetyRejectionBillingSettings{}, fmt.Errorf("marshal grok safety billing settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGrokSafetyRejectionBilling, string(data)); err != nil {
		return GrokSafetyRejectionBillingSettings{}, fmt.Errorf("save grok safety billing settings: %w", err)
	}
	grokSafetyBillingCache.Store(&cachedGrokSafetyBilling{value: normalized, override: true, expiresAt: time.Now().Add(grokSafetyBillingCacheTTL).UnixNano()})
	return normalized, nil
}

// ResetGrokSafetyRejectionBillingSettings 删除数据库覆盖值，回到 config.yaml 默认。
func (s *SettingService) ResetGrokSafetyRejectionBillingSettings(ctx context.Context) (GrokSafetyRejectionBillingSettings, error) {
	if s == nil || s.settingRepo == nil {
		return GrokSafetyRejectionBillingSettings{}, errors.New("settings storage is not configured")
	}
	if err := s.settingRepo.Delete(ctx, SettingKeyGrokSafetyRejectionBilling); err != nil && !errors.Is(err, ErrSettingNotFound) {
		return GrokSafetyRejectionBillingSettings{}, fmt.Errorf("reset grok safety billing settings: %w", err)
	}
	defaults := s.GrokSafetyRejectionBillingDefaults()
	grokSafetyBillingCache.Store(&cachedGrokSafetyBilling{value: defaults, override: false, expiresAt: time.Now().Add(grokSafetyBillingCacheTTL).UnixNano()})
	return defaults, nil
}

// toGrokSafetyBillingParameters 把设置转换为运行时参数。
func (s GrokSafetyRejectionBillingSettings) toGrokSafetyBillingParameters() GrokSafetyBillingParameters {
	return GrokSafetyBillingParametersFromConfig(config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:           s.Enabled,
		AmountUSD:         s.AmountUSD,
		CacheReadRatioMin: s.CacheReadRatioMin,
		CacheReadRatioMax: s.CacheReadRatioMax,
		OutputTokensMin:   s.OutputTokensMin,
		OutputTokensMax:   s.OutputTokensMax,
		MaxInputTokens:    s.MaxInputTokens,
	})
}
