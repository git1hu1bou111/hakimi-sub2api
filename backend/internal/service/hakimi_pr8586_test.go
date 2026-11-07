package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHakimi8586MeteredSSEFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_h\",\"type\":\"message\",\"model\":\"claude-3-5-sonnet-latest\",\"usage\":{\"input_tokens\":11,\"cache_read_input_tokens\":7}}}\n\n"
	const fail = "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n"
	for _, tc := range []struct {
		name                   string
		enabled, metered, want bool
	}{
		{"on-metered", true, true, true}, {"off-metered", false, true, false}, {"on-no-usage", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte("{\"model\":\"claude-3-5-sonnet-latest\",\"stream\":true,\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}]}")), PlatformAnthropic)
			require.NoError(t, err)
			frame := fail
			if tc.metered {
				frame = start + fail
			}
			up := &anthropicHTTPUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"rid-8586"}}, Body: io.NopCloser(strings.NewReader(frame))}}
			svc := newForwardPartialUsageServiceForTest(up)
			svc.rateLimitService = NewRateLimitService(&gatewayForwardErrorPolicyRepoStub{}, nil, svc.cfg, nil, nil)
			a := newAnthropicOAuthAccountForPartialUsageTest()
			a.Extra = map[string]any{"record_failed_stream_usage": tc.enabled}
			r, err := svc.Forward(context.Background(), c, a, parsed)
			var e *UpstreamFailoverError
			require.ErrorAs(t, err, &e)
			require.Nil(t, r, "partial usage is never submitted as a normal success")
			if !tc.want {
				require.Nil(t, e.PartialResult)
				return
			}
			require.NotNil(t, e.PartialResult)
			require.Equal(t, 11, e.PartialResult.Usage.InputTokens)
			require.Equal(t, 7, e.PartialResult.Usage.CacheReadInputTokens)
			require.Equal(t, "rid-8586", e.PartialResult.RequestID)
			require.True(t, e.PartialResult.Stream)
		})
	}
}

func TestHakimi8586MeteredSSEFailoverPreservesFastTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, observedSpeed, billingTier string
	}{
		{"fast honoured", "fast", "fast"},
		{"no upstream speed", "", "fast"},
		{"upstream downgraded", "standard", "standard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-opus-5","speed":"fast","stream":true,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
			require.NoError(t, err)
			frame := fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fast\",\"type\":\"message\",\"model\":\"claude-opus-5\",\"usage\":{\"input_tokens\":11,\"speed\":%q}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n", tc.observedSpeed)
			upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"rid-fast-failover"}},
				Body:       io.NopCloser(strings.NewReader(frame)),
			}}
			svc := newForwardPartialUsageServiceForTest(upstream)
			svc.rateLimitService = NewRateLimitService(&gatewayForwardErrorPolicyRepoStub{}, nil, svc.cfg, nil, nil)
			account := newAnthropicOAuthAccountForPartialUsageTest()
			account.Extra = map[string]any{"record_failed_stream_usage": true}

			result, err := svc.Forward(context.Background(), c, account, parsed)
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.Nil(t, result, "failed usage must remain separate from the normal success result")
			require.NotNil(t, failoverErr.PartialResult)
			require.NotNil(t, failoverErr.PartialResult.ServiceTier)
			require.Equal(t, "fast", *failoverErr.PartialResult.ServiceTier)
			ApplyForwardServiceTierBillingResolution(failoverErr.PartialResult)
			require.Equal(t, tc.billingTier, *failoverErr.PartialResult.ServiceTier)
		})
	}
}
