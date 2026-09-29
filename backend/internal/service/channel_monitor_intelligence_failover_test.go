//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntelligenceGroupFailoverLegacyBaseline(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		answer := "29"
		if calls > 1 {
			answer = "21"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "completed", "output_text": answer})
	}))
	defer server.Close()
	svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, server.URL)
	defer svc.Stop()
	point := svc.probe(context.Background(), "test-only")
	require.Equal(t, "yellow", point.Status)
	require.Equal(t, 1, calls)
	t.Log("BASELINE input=wrong_answer_then_healthy_account result=yellow upstream_calls=1")
}

func TestIntelligenceGroupFailoverPinnedGateway(t *testing.T) {
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(200, `{"id":"resp_first","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"29"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`),
		newOpenAIRejectedFieldTestResponse(200, `{"id":"resp_second","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"21"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`),
	}}
	svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, "http://127.0.0.1")
	defer svc.Stop()
	svc.accountTest = &AccountTestService{openaiGatewayService: newOpenAIRejectedFieldTestService(upstream)}
	a, b := newOpenAIRejectedFieldTestAccount(), newOpenAIRejectedFieldTestAccount()
	a.ID, b.ID = 1, 2
	a.GroupIDs, b.GroupIDs = []int64{105}, []int64{105}
	a.Credentials["api_key"], b.Credentials["api_key"] = "account-one-test-key", "account-two-test-key"
	groupID := int64(105)
	key := &APIKey{Key: "group-test-key", GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI}}
	state := &intelligenceGroupSweep{}
	point, complete := state.run(context.Background(), []Account{*a, *b}, time.Second, func(ctx context.Context, account *Account) IntelligencePoint {
		result, _ := svc.probeAccountWithKeyOnce(ctx, account, key)
		t.Logf("account=%d result=%+v upstream_calls=%d", account.ID, result, len(upstream.requests))
		return result
	})
	require.True(t, complete)
	require.Equal(t, "green", point.Status)
	require.Len(t, upstream.requests, 2)
	require.Equal(t, "Bearer account-one-test-key", upstream.requests[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer account-two-test-key", upstream.requests[1].Header.Get("Authorization"))
	for _, raw := range upstream.bodies {
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		require.Equal(t, IntelligenceModel, body["model"])
		require.Equal(t, map[string]any{"effort": "medium"}, body["reasoning"])
		require.Equal(t, IntelligencePrompt, body["input"])
	}
	t.Log("MODIFIED input=account1:29,account2:21 result=green distinct_upstream_accounts=2")
}

func TestIntelligenceGroupFailover(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []string
		want     string
		calls    int
	}{
		{"first_pass_stops", []string{"green", "yellow"}, "green", 1},
		{"wrong_then_pass", []string{"yellow", "green"}, "green", 2},
		{"error_wrong_then_pass", []string{"red", "yellow", "green"}, "green", 3},
		{"does_not_stop_at_old_three_retry_limit", []string{"red", "yellow", "red", "yellow", "green"}, "green", 5},
		{"all_wrong", []string{"yellow", "yellow", "yellow"}, "yellow", 3},
		{"all_transport_errors", []string{"red", "red"}, "yellow", 2},
		{"mixed_failures", []string{"red", "yellow", "red"}, "yellow", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := make([]Account, len(tc.statuses))
			for i := range accounts {
				accounts[i].ID = int64(i + 1)
			}
			var got []int64
			state := &intelligenceGroupSweep{}
			point, complete := state.run(context.Background(), accounts, time.Second, func(_ context.Context, a *Account) IntelligencePoint {
				got = append(got, a.ID)
				return IntelligencePoint{Status: tc.statuses[a.ID-1]}
			})
			require.True(t, complete)
			require.Equal(t, tc.want, point.Status)
			require.Len(t, got, tc.calls)
			for i, id := range got {
				require.Equal(t, int64(i+1), id)
			}
			require.Nil(t, state.failed, "completed rounds must restart with the first account")
			require.False(t, point.CheckedAt.IsZero())
		})
	}
}

func TestIntelligenceGroupFailoverResumesWithoutFalseYellow(t *testing.T) {
	accounts := []Account{{ID: 1}, {ID: 2}, {ID: 3}}
	state := &intelligenceGroupSweep{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var first []int64
	_, complete := state.run(ctx, accounts, time.Second, func(_ context.Context, a *Account) IntelligencePoint {
		first = append(first, a.ID)
		if a.ID == 2 {
			cancel()
		}
		return IntelligencePoint{Status: "yellow"}
	})
	require.False(t, complete, "budget exhaustion is not an all-failed verdict")
	require.Equal(t, []int64{1, 2}, first)
	var second []int64
	point, complete := state.run(context.Background(), accounts, time.Second, func(_ context.Context, a *Account) IntelligencePoint {
		second = append(second, a.ID)
		if a.ID == 3 {
			return IntelligencePoint{Status: "green"}
		}
		return IntelligencePoint{Status: "yellow"}
	})
	require.True(t, complete)
	require.Equal(t, "green", point.Status)
	require.Equal(t, []int64{2, 3}, second, "failed account 1 is not selected again; interrupted account 2 is retried")
}

func TestIntelligenceGroupFailoverTimeoutMovesToNext(t *testing.T) {
	state := &intelligenceGroupSweep{}
	point, complete := state.run(context.Background(), []Account{{ID: 1}, {ID: 2}}, time.Millisecond, func(ctx context.Context, a *Account) IntelligencePoint {
		if a.ID == 1 {
			<-ctx.Done()
			return IntelligencePoint{Status: "red"}
		}
		return IntelligencePoint{Status: "green"}
	})
	require.True(t, complete)
	require.Equal(t, "green", point.Status)
	require.Contains(t, point.Detail, "account_2")
}

func TestIntelligenceGroupFailoverEmptyBusyAndMembershipChanges(t *testing.T) {
	state := &intelligenceGroupSweep{}
	probe := func(_ context.Context, a *Account) IntelligencePoint {
		if a.ID == 2 {
			return IntelligencePoint{Status: "unknown"}
		}
		return IntelligencePoint{Status: "yellow"}
	}
	_, complete := state.run(context.Background(), nil, time.Second, probe)
	require.False(t, complete)
	_, complete = state.run(context.Background(), []Account{{ID: 1}, {ID: 2}}, time.Second, probe)
	require.False(t, complete, "busy is not a failed candy answer")
	point, complete := state.run(context.Background(), []Account{{ID: 1}, {ID: 3}}, time.Second, probe)
	require.True(t, complete)
	require.Equal(t, "yellow", point.Status)
	require.Equal(t, "all_2_schedulable_accounts_failed", point.Detail)
}

func TestIntelligenceGroupFailoverEligibility(t *testing.T) {
	base := Account{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true, GroupIDs: []int64{105}}
	require.True(t, intelligenceAccountInGroup(&base, 105))
	require.False(t, intelligenceAccountInGroup(&base, 106))
	require.False(t, intelligenceAccountInGroup(nil, 105))
	for _, mutate := range []func(*Account){
		func(a *Account) { a.Schedulable = false },
		func(a *Account) { a.Status = "disabled" },
		func(a *Account) { a.Platform = PlatformAnthropic },
		func(a *Account) { future := time.Now().Add(time.Hour); a.RateLimitResetAt = &future },
		func(a *Account) { future := time.Now().Add(time.Hour); a.TempUnschedulableUntil = &future },
		func(a *Account) { future := time.Now().Add(time.Hour); a.OverloadUntil = &future },
		func(a *Account) { past := time.Now().Add(-time.Hour); a.ExpiresAt = &past; a.AutoPauseOnExpired = true },
	} {
		a := base
		mutate(&a)
		require.False(t, intelligenceAccountInGroup(&a, 105))
	}
}
