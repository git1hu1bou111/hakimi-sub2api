package service

import (
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelMonitorDisplayCapKeepsMetricsAndOtherGroups(t *testing.T) {
	first, second := int64(101), int64(105)
	metric := ChannelMonitorV2Metric{CacheRate: .943, CacheRateWarm: .967, CacheReadTokens: 943, InputTokens: 57}
	health := ChannelMonitorV2Health{Overall: "healthy"}
	repo := &channelMonitorV2RepoStub{
		config: ChannelMonitorV2Config{Enabled: true, CacheRateDisplayCaps: map[int64]float64{first: 90}},
		matrix: &ChannelMonitorV2Matrix{Items: []ChannelMonitorV2MatrixRow{
			{GroupID: &first, Metrics: metric, Health: health, Buckets: []ChannelMonitorV2TrendPoint{{Metrics: metric, Health: health}}},
			{GroupID: &second, Metrics: metric, Health: health},
		}},
	}
	result, err := NewChannelMonitorV2Service(repo).Matrix(context.Background(), ChannelMonitorV2Filter{}, ChannelMonitorV2GroupByPlatformGroup, true)
	require.NoError(t, err)
	require.Equal(t, 90.0, *result.Items[0].CacheRateDisplayCap)
	require.Nil(t, result.Items[1].CacheRateDisplayCap)
	require.Equal(t, metric, result.Items[0].Metrics)
	require.Equal(t, metric, result.Items[0].Buckets[0].Metrics)
	require.Equal(t, health, result.Items[0].Health)
}

func TestChannelMonitorDisplayCapValidation(t *testing.T) {
	for _, caps := range []map[int64]float64{{0: 90}, {1: -1}, {1: 101}, {1: math.NaN()}, {1: math.Inf(1)}} {
		cfg := ChannelMonitorV2Config{CacheRateDisplayCaps: caps}
		require.ErrorIs(t, normalizeChannelMonitorV2Config(&cfg), ErrChannelMonitorV2InvalidConfig)
	}
	cfg := ChannelMonitorV2Config{CacheRateDisplayCaps: map[int64]float64{101: 90, 105: 0, 106: 100}}
	require.NoError(t, normalizeChannelMonitorV2Config(&cfg))
	redactChannelMonitorV2PublicConfig(&cfg)
	require.Nil(t, cfg.CacheRateDisplayCaps)
}
