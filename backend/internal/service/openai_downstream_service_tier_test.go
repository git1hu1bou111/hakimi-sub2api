package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestDownstreamDefaultTierScope(t *testing.T) {
	group := &Group{ID: 138, Platform: PlatformOpenAI}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)
	t.Setenv(downstreamTierGroupEnv, "")
	require.False(t, useDefaultDownstreamTier(ctx))
	t.Setenv(downstreamTierGroupEnv, "138")
	require.True(t, useDefaultDownstreamTier(ctx))
	require.False(t, useDefaultDownstreamTier(nil))
	require.False(t, useDefaultDownstreamTier(context.Background()))
	require.False(t, useDefaultDownstreamTier(context.WithValue(ctx, ctxkey.Group, &Group{ID: 139, Platform: PlatformOpenAI})))
	require.False(t, useDefaultDownstreamTier(context.WithValue(ctx, ctxkey.Group, &Group{ID: 138, Platform: PlatformAnthropic})))
}

func TestDownstreamDefaultTierWireFormat(t *testing.T) {
	for _, tc := range []struct {
		wire   string
		change bool
	}{
		{`{"object":"response","service_tier":"ultrafast","usage":{"input_tokens":100}}`, true},
		{`{"object":"chat.completion.chunk","service_tier":"ultrafast","choices":[]}`, true},
		{`{"model":"gpt-6.1-sol","service_tier":"ultrafast","output":[]}`, true},
		{`{"model":"gpt-6.1-sol","id":"resp_1","service_tier":"ultrafast","usage":{"input_tokens":1}}`, true},
		{`{"type":"response.created","response":{"model":"gpt-6.2-sol","service_tier":"ultrafast"}}`, true},
		{`{"type":"response.completed","response":{"service_tier":"ultrafast","output":[{"service_tier":"ultrafast"}]}}`, true},
		{`{"object":"response","service_tier":"default"}`, false},
		{`{"object":"response","service_tier":"priority"}`, false},
		{`{"object":"response","output":[{"service_tier":"ultrafast"}]}`, false},
		{`{"type":"response.output_text.delta","response":{"service_tier":"ultrafast"}}`, false},
		{`{"type":"response.create","response":{"service_tier":"ultrafast"}}`, false},
		{`{"object":"response","service_tier":"ultrafast"`, false},
		{`[DONE]`, false},
	} {
		input := []byte(tc.wire)
		out := rewriteDownstreamUltraFastTier(input)
		want := tc.wire
		if tc.change {
			want = string(bytes.Replace(input, []byte(`"ultrafast"`), []byte(`"default"`), 1))
		}
		require.Equal(t, want, string(out))
		require.Equal(t, tc.wire, string(input), "internal observer still reads the original")
	}
}
