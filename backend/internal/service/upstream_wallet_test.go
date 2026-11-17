package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamWalletBalances(t *testing.T) {
	for _, tc := range []struct {
		name, kind, body, status string
		entries                  []UpstreamWalletAmount
	}{
		{"real zero", "relay", `{"mode":"unrestricted","isValid":true,"remaining":12,"unit":"USD","balance":0}`, "ok", []UpstreamWalletAmount{{0, "USD"}}},
		{"negative", "relay", `{"mode":"unrestricted","unit":"USD","balance":-0.25}`, "ok", []UpstreamWalletAmount{{-0.25, "USD"}}},
		{"quota only", "relay", `{"mode":"quota_limited","unit":"USD","remaining":200}`, "unsupported", nil},
		{"subscription", "relay", `{"mode":"unrestricted","subscription":{},"remaining":200,"unit":"USD"}`, "unsupported", nil},
		{"auth failed", "relay", `{"mode":"unrestricted","isValid":false,"balance":0,"unit":"USD"}`, "error", nil},
		{"missing currency", "relay", `{"mode":"unrestricted","balance":50}`, "error", nil},
		{"bad decimal", "relay", `{"mode":"unrestricted","balance":"NaN","unit":"USD"}`, "error", nil},
		{"two currencies", "deepseek", `{"is_available":true,"balance_infos":[{"total_balance":"0.00","currency":"USD"},{"total_balance":"4.20","currency":"CNY"}]}`, "ok", []UpstreamWalletAmount{{0, "USD"}, {4.2, "CNY"}}},
		{"missing amount", "kimi", `{"code":0,"data":{}}`, "error", nil},
		{"explicit zero", "kimi", `{"code":0,"data":{"available_balance":0}}`, "ok", []UpstreamWalletAmount{{0, "CNY"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, status := parseWallet(tc.kind, []byte(tc.body))
			require.Equal(t, tc.status, status)
			require.Equal(t, tc.entries, got)
		})
	}
}
func TestUpstreamWalletKeepsLastSuccessfulValueOnFailure(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	old := makeWalletSnapshot(nil, "ok", []UpstreamWalletAmount{{12.5, "USD"}}, now)
	for _, state := range []string{"error", "unsupported"} {
		next := makeWalletSnapshot(map[string]any{UpstreamWalletExtraKey: old}, state, nil, now.Add(time.Hour))
		require.Equal(t, "error", next.Status)
		require.Equal(t, old.Balances, next.Balances)
		require.Equal(t, old.UpdatedAt, next.UpdatedAt)
		require.True(t, next.CheckedAt.After(now))
	}
}

func TestUpstreamWalletRelayUsesConfiguredOriginOnly(t *testing.T) {
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"mode":"unrestricted","balance":15.5,"unit":"USD"}`))}}
	svc := newUpstreamBillingProbeTestService(nil, upstream, &upstreamBillingProbeSettingRepo{})
	svc.now = func() time.Time { return time.Date(2026, time.November, 18, 18, 0, 0, 0, time.UTC) }
	a := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://upstream.example/relay/v1", "api_key": "sample"}}
	balances, status := svc.readUpstreamWallet(context.Background(), a)
	require.Equal(t, "ok", status)
	require.Equal(t, []UpstreamWalletAmount{{15.5, "USD"}}, balances)
	require.Equal(t, "https://upstream.example/relay/v1/usage?end_date=2026-11-19&start_date=2026-11-19", upstream.lastReq.URL.String())
	require.True(t, HTTPUpstreamRedirectsDisabled(upstream.lastReq.Context()))
	require.True(t, HTTPUpstreamPublicHostsOnly(upstream.lastReq.Context()))
	for _, host := range []string{"https://api.openai.com", "http://relay.example"} {
		upstream.lastReq = nil
		a.Credentials["base_url"] = host
		_, status := svc.readUpstreamWallet(context.Background(), a)
		require.Equal(t, "unsupported", status)
		require.Nil(t, upstream.lastReq)
	}
}
