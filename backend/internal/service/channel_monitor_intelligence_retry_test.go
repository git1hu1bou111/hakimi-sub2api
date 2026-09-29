//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntelligenceRetryTransientRecovery(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"output_text":"21"}`))
	}))
	defer server.Close()
	svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, server.URL)
	defer svc.Stop()
	result := svc.probe(context.Background(), "test-only")
	require.Equal(t, "green", result.Status)
	require.EqualValues(t, 2, calls.Load())
	require.Equal(t, "answer_21_after_retry_2", result.Detail)
}

type intelligenceRoundTripFunc func(*http.Request) (*http.Response, error)

func (f intelligenceRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestIntelligenceRetrySequences(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		codes                  []int
		bodies                 []string
		wantStatus, wantDetail string
		wantCalls              int
	}{
		{"503_then_correct", []int{503, 200}, []string{``, `{"output_text":"21"}`}, "green", "answer_21_after_retry_2", 2},
		{"twice_then_correct", []int{502, 504, 200}, []string{``, ``, `{"output_text":"21"}`}, "green", "answer_21_after_retry_3", 3},
		{"exhausted", []int{503, 503, 503}, nil, "red", "http_503_after_retry_3", 3},
		{"429_then_wrong", []int{429, 200}, []string{``, `{"output_text":"29"}`}, "yellow", "answer_mismatch_after_retry_2", 2},
		{"408_then_correct", []int{408, 200}, []string{``, `{"output_text":"21"}`}, "green", "answer_21_after_retry_2", 2},
		{"wrong_no_retry", []int{200}, []string{`{"output_text":"29"}`}, "yellow", "answer_mismatch", 1},
		{"right_no_retry", []int{200}, []string{`{"output_text":"21"}`}, "green", "answer_21", 1},
		{"empty_no_retry", []int{200}, []string{`{"output_text":""}`}, "yellow", "answer_mismatch", 1},
		{"invalid_then_correct", []int{200, 200}, []string{`<html>`, `{"output_text":"21"}`}, "green", "answer_21_after_retry_2", 2},
		{"envelope_then_correct", []int{200, 200}, []string{`{"error":{"message":"secret"}}`, `{"output_text":"21"}`}, "green", "answer_21_after_retry_2", 2},
		{"incomplete_then_correct", []int{200, 200}, []string{`{"status":"incomplete"}`, `{"output_text":"21"}`}, "green", "answer_21_after_retry_2", 2},
		{"400_no_retry", []int{400}, nil, "red", "http_400", 1},
		{"401_no_retry", []int{401}, nil, "red", "http_401", 1},
		{"403_no_retry", []int{403}, nil, "red", "http_403", 1},
		{"404_no_retry", []int{404}, nil, "red", "http_404", 1},
		{"redirect_no_retry", []int{307}, nil, "red", "http_307", 1},
		{"oversize_no_retry", []int{200}, []string{strings.Repeat("x", (1<<20)+1)}, "red", "response_too_large", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, "http://monitor.test")
			defer svc.Stop()
			var firstPayload string
			svc.client.Transport = intelligenceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Less(t, calls, len(tc.codes))
				require.Equal(t, "http://monitor.test/v1/responses", r.URL.String())
				require.Equal(t, "Bearer same-group-test-key", r.Header.Get("Authorization"))
				require.Equal(t, "sub2api-intelligence-uptime/1.0", r.Header.Get("User-Agent"))
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				if calls == 0 {
					firstPayload = string(raw)
				} else {
					require.Equal(t, firstPayload, string(raw))
				}
				body := ""
				if calls < len(tc.bodies) {
					body = tc.bodies[calls]
				}
				code := tc.codes[calls]
				calls++
				return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			start := time.Now()
			result := svc.probeWithRetry(context.Background(), "same-group-test-key", intelligenceRetryPolicy{3, time.Second, time.Second, time.Millisecond})
			require.Equal(t, tc.wantStatus, result.Status)
			require.Equal(t, tc.wantDetail, result.Detail)
			require.Equal(t, tc.wantCalls, calls)
			require.WithinDuration(t, start, result.CheckedAt, 50*time.Millisecond)
			require.NotContains(t, result.Detail, "secret")
		})
	}
}

func TestIntelligenceRetryTimeoutAndCancellation(t *testing.T) {
	for _, mode := range []string{"timeout_then_ok", "network_then_ok", "read_then_ok", "budget", "cancel_backoff", "already_cancelled", "retry_after_budget"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, "http://monitor.test")
			defer svc.Stop()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := intelligenceRetryPolicy{3, 20 * time.Millisecond, 70 * time.Millisecond, time.Millisecond}
			if mode == "already_cancelled" {
				cancel()
			}
			if mode == "budget" {
				policy.timeout = time.Second
				policy.budget = 30 * time.Millisecond
			}
			if mode == "cancel_backoff" {
				policy.backoff = time.Second
			}
			svc.client.Transport = intelligenceRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if mode == "budget" || (mode == "timeout_then_ok" && calls == 1) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				if mode == "network_then_ok" && calls == 1 {
					return nil, errors.New("test connection reset")
				}
				if mode == "read_then_ok" && calls == 1 {
					return &http.Response{StatusCode: 200, Body: intelligenceErrorReader{}}, nil
				}
				if mode == "cancel_backoff" {
					// Cancel while the backoff timer is pending (larger parent budget).
					time.AfterFunc(10*time.Millisecond, cancel)
					return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				if mode == "retry_after_budget" {
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"120"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"output_text":"21"}`))}, nil
			})
			if mode == "cancel_backoff" {
				policy.budget = 2 * time.Second
			}
			start := time.Now()
			result := svc.probeWithRetry(ctx, "test-key", policy)
			require.Less(t, time.Since(start), 500*time.Millisecond)
			if strings.HasSuffix(mode, "then_ok") {
				require.Equal(t, "green", result.Status)
				require.Equal(t, 2, calls)
				require.Equal(t, "answer_21_after_retry_2", result.Detail)
			} else {
				require.Equal(t, "red", result.Status)
				if mode == "already_cancelled" {
					require.Zero(t, calls)
				} else {
					require.Equal(t, 1, calls)
				}
			}
			if mode == "timeout_then_ok" {
				require.GreaterOrEqual(t, result.LatencyMs, 20)
			}
		})
	}
}

type intelligenceErrorReader struct{}

func (intelligenceErrorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (intelligenceErrorReader) Close() error             { return nil }

func TestIntelligenceRetryPolicyContract(t *testing.T) {
	require.Equal(t, 3, IntelligenceMaxAttempts)
	require.Equal(t, 30*time.Second, IntelligenceTimeout)
	require.Equal(t, 55*time.Second, IntelligenceProbeBudget)
	require.Less(t, IntelligenceProbeBudget, IntelligenceInterval)
	require.Equal(t, 60, IntelligenceSlots)
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{
		{"2", 2 * time.Second}, {"120", IntelligenceProbeBudget}, {"9223372036854775807", IntelligenceProbeBudget},
		{"-1", 0}, {"invalid", 0}, {now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{now.Add(-time.Second).Format(http.TimeFormat), 0},
	} {
		require.Equal(t, tc.want, intelligenceRetryAfter(tc.header, now), tc.header)
	}
}
