package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDefaultDownstreamTierHTTP(t *testing.T) {
	t.Setenv(downstreamTierGroupEnv, "138")
	const model = "gpt-6.1-sol"
	const priceTier = "ultrafast"
	const output = `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`
	const responseJSON = `{"object":"response","id":"resp_1","model":"gpt-6.1-sol","status":"completed","service_tier":"ultrafast","output":[` + output + `],"usage":{"input_tokens":100,"output_tokens":4,"input_tokens_details":{"cached_tokens":64}}}`
	const responseSSE = "event: response.created\n" +
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol","service_tier":"ultrafast"}}` + "\n\n" +
		`data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n" +
		`data: {"type":"response.completed","response":` + responseJSON + `}` + "\n\n"
	const chatJSON = `{"object":"chat.completion","id":"chatcmpl_1","model":"gpt-6.1-sol","service_tier":"ultrafast","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":64}}}`
	const chatSSE = `data: {"object":"chat.completion.chunk","id":"chatcmpl_1","model":"gpt-6.1-sol","service_tier":"ultrafast","choices":[{"index":0,"delta":{"content":"hello"}}]}` + "\n\n" +
		`data: {"object":"chat.completion.chunk","id":"chatcmpl_1","model":"gpt-6.1-sol","service_tier":"ultrafast","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":64}}}` + "\n\ndata: [DONE]\n\n"

	for _, target := range []struct{ name, body string }{
		{"responses-json", responseJSON}, {"responses-sse", responseSSE},
		{"passthrough-json", responseJSON}, {"passthrough-sse", responseSSE},
		{"responses-aggregate", responseSSE}, {"passthrough-aggregate", responseSSE},
		{"chat-converted-json", responseSSE}, {"chat-converted-sse", responseSSE},
		{"chat-raw-json", chatJSON}, {"chat-raw-sse", chatSSE},
		{"responses-from-chat-json", chatJSON}, {"responses-from-chat-sse", chatSSE},
	} {
		t.Run(target.name, func(t *testing.T) {
			var baselineObserved string
			for _, groupID := range []int64{139, 138} {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				ctx := context.WithValue(c.Request.Context(), ctxkey.Group, &Group{ID: groupID, Platform: PlatformOpenAI})
				c.Request = c.Request.WithContext(ctx)
				upstream := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(iotest.OneByteReader(strings.NewReader(target.body)))}
				if strings.HasPrefix(target.body, "{") {
					upstream.Header.Set("Content-Type", "application/json")
				} else {
					upstream.Header.Set("Content-Type", "text/event-stream")
				}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
				var usage OpenAIUsage
				var err error
				start := time.Now()
				switch target.name {
				case "responses-json", "responses-aggregate":
					r, e := svc.handleNonStreamingResponse(ctx, upstream, c, account, model, model)
					err = e
					if r != nil {
						usage = *r.usage
					}
				case "responses-sse":
					r, e := svc.handleStreamingResponse(ctx, upstream, c, account, start, model, model)
					err = e
					if r != nil {
						usage = *r.usage
					}
				case "passthrough-json", "passthrough-aggregate":
					r, e := svc.handleNonStreamingResponsePassthrough(ctx, upstream, c, account, model, model)
					err = e
					if r != nil {
						usage = *r.usage
					}
				case "passthrough-sse":
					r, e := svc.handleStreamingResponsePassthrough(ctx, upstream, c, account, start, model, model)
					err = e
					if r != nil {
						usage = *r.usage
					}
				default:
					var r *OpenAIForwardResult
					switch target.name {
					case "chat-converted-json":
						r, err = svc.handleChatBufferedStreamingResponse(upstream, c, account, model, model, model, start)
					case "chat-converted-sse":
						r, err = svc.handleChatStreamingResponse(upstream, c, account, model, model, model, start, 0)
					case "chat-raw-json":
						r, err = svc.bufferRawChatCompletions(c, upstream, account, model, model, model, nil, nil, start)
					case "chat-raw-sse":
						r, err = svc.streamRawChatCompletions(c, upstream, account, model, model, model, nil, nil, start, 0)
					case "responses-from-chat-json":
						r, err = svc.bufferChatCompletionsAsResponses(c, upstream, model, nil, nil, false, nil, model, model, nil, nil, start)
					case "responses-from-chat-sse":
						r, err = svc.streamChatCompletionsAsResponses(c, upstream, model, nil, nil, false, nil, model, model, nil, nil, start)
					}
					if r != nil {
						usage = r.Usage
					}
				}
				require.NoError(t, err)
				require.Equal(t, 100, usage.InputTokens)
				require.Equal(t, 4, usage.OutputTokens)
				require.Equal(t, 64, usage.CacheReadInputTokens)
				if groupID == 139 {
					baselineObserved = observedUpstreamResponseServiceTier(c)
				} else {
					require.Equal(t, baselineObserved, observedUpstreamResponseServiceTier(c))
				}
				clientTier := priceTier
				if groupID == 138 {
					clientTier = "default"
				}
				require.Contains(t, rec.Body.String(), `"service_tier":"`+clientTier+`"`)
				if groupID == 138 {
					require.NotContains(t, rec.Body.String(), `"service_tier":"ultrafast"`)
				}
				require.Contains(t, rec.Body.String(), "hello")
			}
		})
	}
}

func TestDefaultDownstreamTierHTTPFallbackSSEFraming(t *testing.T) {
	wire := ": keepalive\r\nevent: response.completed\r\ndata:\t" +
		`{"type":"response.completed","response":{"service_tier":"ultrafast","metadata":{"service_tier":"ultrafast"}}}` +
		"\r\n\r\ndata: [DONE]"
	want := strings.Replace(wire, `"response":{"service_tier":"ultrafast"`, `"response":{"service_tier":"default"`, 1)
	require.Equal(t, want, downstreamTierSSEBody(wire))
}
