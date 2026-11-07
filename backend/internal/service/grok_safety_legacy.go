package service

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// legacyGrokSafetyFeeReader lets a rolling upgrade retain the original group
// fees without requiring the retired column in the generated schema. It is
// deliberately read at billing time, so changes made by an older admin process
// take effect without invalidating the newer process's auth cache.
type legacyGrokSafetyFeeReader interface {
	GetLegacyGrokSafetyRejectionFee(context.Context, int64) (float64, error)
}

func (s *OpenAIGatewayService) grokSafetyBillingPolicy(ctx context.Context, apiKey *APIKey) (GrokSafetyBillingParameters, float64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s == nil || s.settingService == nil || apiKey == nil {
		return s.GrokSafetyRejectionBillingParameters(ctx), 0, nil
	}
	// A saved global setting, including an explicitly disabled setting, is an
	// intentional policy choice. Only an unconfigured global policy falls back
	// to the pre-upgrade group fees.
	settings, override, err := s.settingService.getGrokSafetyRejectionBillingPolicyState(ctx)
	params := settings.toGrokSafetyBillingParameters()
	if err != nil {
		return params, 0, err
	}
	if override || params.Enabled {
		return params, 0, nil
	}
	reader, ok := s.settingService.settingRepo.(legacyGrokSafetyFeeReader)
	if !ok {
		return params, 0, nil
	}
	var groupID int64
	if apiKey.Group != nil {
		groupID = apiKey.Group.ID
	} else if apiKey.GroupID != nil {
		groupID = *apiKey.GroupID
	}
	if groupID <= 0 {
		return params, 0, nil
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grokSafetyBillingDBTimeout)
	defer cancel()
	fee, err := reader.GetLegacyGrokSafetyRejectionFee(dbCtx, groupID)
	if err != nil {
		return params, 0, fmt.Errorf("read legacy Grok group fee: %w", err)
	}
	if math.IsNaN(fee) || math.IsInf(fee, 0) || fee < 0 {
		return params, 0, fmt.Errorf("invalid legacy Grok group fee")
	}
	return params, QuantizeUsageBillingAmount(fee), nil
}

func (s *OpenAIGatewayService) recordLegacyGrokSafetyRejectionUsage(ctx context.Context, in GrokSafetyRejectionBillingInput, fee float64) error {
	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = strings.TrimSpace(in.UpstreamModel)
	}
	if model == "" {
		model = "grok-safety-review"
	}
	return s.RecordUsage(ctx, &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: in.RequestID, Model: model, UpstreamModel: in.UpstreamModel, Stream: in.Stream},
		APIKey: in.APIKey, User: in.APIKey.User, Account: in.Account, Subscription: in.Subscription,
		InboundEndpoint: in.InboundEndpoint, UpstreamEndpoint: in.UpstreamEndpoint,
		UserAgent: in.UserAgent, IPAddress: in.IPAddress, SessionID: in.SessionID,
		RequestPayloadHash: in.RequestPayloadHash, APIKeyService: in.APIKeyService,
		ChannelUsageFields: in.ChannelUsageFields, NativeCompactionV2: in.NativeCompactionV2,
		PricingAt: in.PricingAt, legacyGrokSafetyFeeUSD: &fee,
	})
}
