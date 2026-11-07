package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

type legacyGrokSafetySettingRepo struct {
	grokSafetySettingRepo
	fees          map[int64]float64
	feeErr        error
	feeReads      int
	feeContextErr error
}

func (r *legacyGrokSafetySettingRepo) GetLegacyGrokSafetyRejectionFee(ctx context.Context, id int64) (float64, error) {
	r.feeReads++
	r.feeContextErr = ctx.Err()
	return r.fees[id], r.feeErr
}

func TestRecordGrokSafetyRejectionUsage_LegacyGroupFixedFee(t *testing.T) {
	for _, multiplier := range []float64{0, 0.09, 1, 2.5} {
		t.Run(fmt.Sprintf("group-multiplier-%g", multiplier), func(t *testing.T) {
			resetGrokSafetyBillingCacheForTest()
			t.Cleanup(resetGrokSafetyBillingCacheForTest)
			usage := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
			svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usage, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			repo := &legacyGrokSafetySettingRepo{fees: map[int64]float64{85: 0.10}}
			svc.settingService = NewSettingService(repo, svc.cfg)
			id := int64(85)
			key := &APIKey{ID: 91, User: &User{ID: 51}, GroupID: &id, Group: &Group{ID: id, Platform: PlatformGrok, RateMultiplier: multiplier}}
			require.True(t, svc.GrokSafetyRejectionBillingEnabled(context.Background(), key))
			accountMultiplier := 3.0
			svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{APIKey: key, Account: &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeAPIKey, RateMultiplier: &accountMultiplier}, Model: "grok-5", RequestID: "legacy-fixed-fee"})
			require.Equal(t, 1, billing.calls)
			require.Equal(t, 1, usage.calls)
			require.Equal(t, 0.10, billing.lastCmd.BalanceCost)
			require.Zero(t, billing.lastCmd.AccountQuotaCost)
			require.Equal(t, 0.10, usage.lastLog.ActualCost)
			require.Equal(t, 0.10, usage.lastLog.TotalCost)
			require.Equal(t, 1.0, usage.lastLog.RateMultiplier)
			require.NotNil(t, usage.lastLog.AccountRateMultiplier)
			require.Zero(t, *usage.lastLog.AccountRateMultiplier)
			require.Equal(t, RequestTypeGrokSafetyRejected, usage.lastLog.RequestType)
			require.Zero(t, usage.lastLog.InputTokens+usage.lastLog.OutputTokens+usage.lastLog.CacheReadTokens)
		})
	}
}

func TestGrokSafetyBillingLegacyPolicy_PreservesScopeAndExplicitOverride(t *testing.T) {
	tests := []struct {
		name        string
		fee         float64
		stored      string
		settingErr  error
		feeErr      error
		wantFee     float64
		wantEnabled bool
		wantErr     bool
		wantRead    bool
	}{
		{name: "positive old fee", fee: 0.10, wantFee: 0.10, wantEnabled: true, wantRead: true},
		{name: "zero remains free", fee: 0, wantRead: true},
		{name: "explicit disabled wins", fee: 0.10, stored: `{"enabled":false,"amount_usd":0.05}`},
		{name: "explicit global wins", fee: 0.10, stored: `{"enabled":true,"amount_usd":0.05}`, wantEnabled: true},
		{name: "explicit disabled unreadable", fee: 0.10, stored: `{"enabled":false,"amount_usd":0.05}`, settingErr: errors.New("settings unavailable"), wantErr: true},
		{name: "invalid settings fail closed", fee: 0.10, stored: `{bad json`, wantErr: true},
		{name: "read error fails closed", fee: 0.10, feeErr: errors.New("unavailable"), wantErr: true, wantRead: true},
		{name: "invalid fee fails closed", fee: math.NaN(), wantErr: true, wantRead: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGrokSafetyBillingCacheForTest()
			t.Cleanup(resetGrokSafetyBillingCacheForTest)
			svc, usage, user := newGrokSafetyBillingTestService()
			repo := &legacyGrokSafetySettingRepo{fees: map[int64]float64{71: tt.fee}, feeErr: tt.feeErr}
			repo.getErr = tt.settingErr
			if tt.stored != "" {
				repo.values = map[string]string{SettingKeyGrokSafetyRejectionBilling: tt.stored}
			}
			svc.settingService = NewSettingService(repo, svc.cfg)
			key := grokSafetyTestAPIKey()
			params, fee, err := svc.grokSafetyBillingPolicy(context.Background(), key)
			require.Equal(t, tt.wantErr, err != nil)
			require.Equal(t, tt.wantFee, fee)
			require.Equal(t, tt.wantRead, repo.feeReads > 0)
			require.Equal(t, tt.wantEnabled, err == nil && (fee > 0 || params.Enabled && params.AmountUSD > 0))
			if !tt.wantEnabled {
				svc.RecordGrokSafetyRejectionUsage(context.Background(), GrokSafetyRejectionBillingInput{APIKey: key, Account: &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeAPIKey}, Model: "grok-4.6", RequestID: "legacy-disabled"})
				require.Zero(t, usage.calls)
				require.Zero(t, user.deductCalls)
			}
		})
	}
}

func TestGrokSafetyBillingLegacyPolicy_ReadsCurrentGroupFee(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	t.Cleanup(resetGrokSafetyBillingCacheForTest)
	svc, _, _ := newGrokSafetyBillingTestService()
	repo := &legacyGrokSafetySettingRepo{fees: map[int64]float64{71: 0.10}}
	svc.settingService = NewSettingService(repo, svc.cfg)
	key := grokSafetyTestAPIKey()
	_, fee, err := svc.grokSafetyBillingPolicy(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, 0.10, fee)
	repo.fees[71] = 0
	_, fee, err = svc.grokSafetyBillingPolicy(context.Background(), key)
	require.NoError(t, err)
	require.Zero(t, fee)
	key.Group.ID = 72
	_, fee, err = svc.grokSafetyBillingPolicy(context.Background(), key)
	require.NoError(t, err)
	require.Zero(t, fee)
}

func TestGrokSafetyBillingLegacyPolicy_CanceledRequestStillResolvesFee(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	t.Cleanup(resetGrokSafetyBillingCacheForTest)
	svc, _, _ := newGrokSafetyBillingTestService()
	repo := &legacyGrokSafetySettingRepo{fees: map[int64]float64{71: 0.10}}
	svc.settingService = NewSettingService(repo, svc.cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.True(t, svc.GrokSafetyRejectionBillingEnabled(ctx, grokSafetyTestAPIKey()))
	require.NoError(t, repo.feeContextErr)
}

func TestRecordGrokSafetyRejectionUsage_LegacyPreservesBillingIdempotencyKey(t *testing.T) {
	resetGrokSafetyBillingCacheForTest()
	t.Cleanup(resetGrokSafetyBillingCacheForTest)
	usage := &openAIRecordUsageLogRepoStub{inserted: true}
	billing := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usage, billing, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.settingService = NewSettingService(&legacyGrokSafetySettingRepo{fees: map[int64]float64{71: 0.10}}, svc.cfg)
	in := GrokSafetyRejectionBillingInput{APIKey: grokSafetyTestAPIKey(), Account: &Account{ID: 61, Platform: PlatformGrok, Type: AccountTypeAPIKey}, Model: "grok-5", RequestID: "same-upstream-attempt"}
	svc.RecordGrokSafetyRejectionUsage(context.Background(), in)
	require.NotNil(t, billing.lastCmd)
	first := *billing.lastCmd
	billing.result = &UsageBillingApplyResult{Applied: false}
	svc.RecordGrokSafetyRejectionUsage(context.Background(), in)
	require.Equal(t, 2, billing.calls)
	require.Equal(t, "same-upstream-attempt", billing.lastCmd.RequestID)
	require.Equal(t, first.RequestID, billing.lastCmd.RequestID)
	require.Equal(t, first.RequestFingerprint, billing.lastCmd.RequestFingerprint)
	require.Equal(t, first.APIKeyID, billing.lastCmd.APIKeyID)
	require.Equal(t, first.AccountID, billing.lastCmd.AccountID)
	require.Equal(t, 0.10, billing.lastCmd.BalanceCost)
}
