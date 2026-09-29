package service

import "testing"

// Guards against upstream priority/fast promoting an untiered outbound request.
func TestScreenshotBillingRegressionProbe(t *testing.T) {
	for _, observed := range []string{"priority", "fast"} {
		t.Run("untiered_request_upstream_"+observed, func(t *testing.T) {
			observer := &upstreamResponseModelObserver{}
			observer.ObserveOpenAI([]byte(`{"model":"gpt-5.4","service_tier":"`+observed+`"}`), "")
			result := &OpenAIForwardResult{
				ServiceTier: resolvedOpenAIUpstreamServiceTierFromObserver(observer, nil),
				UpstreamResponseServiceTier: observer.ServiceTier(),
			}
			resolution := ApplyOpenAIServiceTierBillingResolution(nil, result)
			tier := optionalStringValue(result.ServiceTier)
			multiplier := serviceTierCostMultiplier(tier)
			t.Logf("outbound=<omitted> observed=%s billed=%q fallback_multiplier=%.1f downgraded=%t", observed, tier, multiplier, resolution.Downgraded)
			if tier != "" || multiplier != 1 {
				t.Errorf("response promoted an untiered request: want omitted tier and 1.0 multiplier")
			}
		})
	}
}
