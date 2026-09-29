//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntelligenceProbe(t *testing.T) {
	for _, tc := range []struct {
		name, body, expected string
		code                 int
	}{
		{"correct", `{"status":"completed","output_text":"21"}`, "green", 200},
		{"trim", `{"output_text":" 21\n"}`, "green", 200},
		{"wrong", `{"output_text":"22"}`, "yellow", 200},
		{"explanation", `{"output_text":"21 because..."}`, "yellow", 200},
		{"decimal", `{"output_text":"21.0"}`, "yellow", 200},
		{"brackets", `{"output_text":"【21】"}`, "yellow", 200},
		{"empty", `{"output_text":""}`, "yellow", 200},
		{"error_envelope", `{"error":{"message":"secret"}}`, "red", 200},
		{"incomplete", `{"status":"incomplete","output_text":"21"}`, "red", 200},
		{"invalid_json", `<html>21</html>`, "red", 200},
		{"http_error", `{"output_text":"21"}`, "red", 503},
		{"reasoning_before_message", `{"output":[{"type":"reasoning","summary":[{"text":"22"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"21"}]}]}`, "green", 200},
		{"reasoning_only", `{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"21"}]}]}`, "yellow", 200},
		{"multiple_text", `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"2"},{"type":"output_text","text":"1"}]}]}`, "green", 200},
		{"oversize", `{"output_text":"` + strings.Repeat("x", 1<<20) + `"}`, "red", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/responses", r.URL.Path)
				require.Equal(t, "Bearer test-only", r.Header.Get("Authorization"))
				var request map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, IntelligenceModel, request["model"])
				require.Equal(t, IntelligencePrompt, request["input"])
				require.Equal(t, map[string]any{"effort": "medium"}, request["reasoning"])
				require.Equal(t, "none", request["tool_choice"])
				require.Empty(t, request["tools"])
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, server.URL)
			defer svc.Stop()
			point := svc.probe(context.Background(), "test-only")
			require.Equal(t, tc.expected, point.Status)
			require.NotContains(t, point.Detail, "secret")
		})
	}
}

func TestIntelligenceTimeoutAndRedirect(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
		}))
		defer server.Close()
		svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, server.URL)
		defer svc.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		result := svc.probe(ctx, "test")
		require.Equal(t, "red", result.Status)
		require.Equal(t, "timeout", result.Detail)
	})
	t.Run("redirect", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/credential-leak", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		svc := NewChannelMonitorIntelligence(nil, nil, nil, nil, nil, server.URL)
		defer svc.Stop()
		result := svc.probe(context.Background(), "test")
		require.Equal(t, "red", result.Status)
		require.Equal(t, "http_307", result.Detail)
	})
}

type intelligenceReadStub struct {
	IntelligenceRepository
	ids    []int64
	latest *IntelligencePoint
	start  time.Time
	end    time.Time
	bucket time.Duration
}

func (r *intelligenceReadStub) History(_ context.Context, ids []int64, start, end time.Time, bucket time.Duration) (map[int64][]IntelligencePoint, error) {
	r.ids = ids
	r.start, r.end, r.bucket = start, end, bucket
	return map[int64][]IntelligencePoint{7: {{CheckedAt: time.Now(), Status: "yellow"}}}, nil
}
func (r *intelligenceReadStub) Latest(context.Context, int64) (*IntelligencePoint, error) {
	return r.latest, nil
}
func TestIntelligenceAttachOnlyAuthorizedOpenAI(t *testing.T) {
	id1, id2 := int64(7), int64(8)
	repo := &intelligenceReadStub{latest: &IntelligencePoint{CheckedAt: time.Now(), Status: "green"}}
	svc := NewChannelMonitorIntelligence(repo, nil, nil, nil, nil, "http://127.0.0.1")
	defer svc.Stop()
	matrix := &ChannelMonitorV2Matrix{Items: []ChannelMonitorV2MatrixRow{
		{Platform: "openai", GroupID: &id1}, {Platform: "anthropic", GroupID: &id2}, {Platform: "openai"},
	}}
	require.NoError(t, svc.Attach(context.Background(), matrix, ChannelMonitorV2Filter{Bucket: 5 * time.Minute}))
	require.Equal(t, []int64{7}, repo.ids)
	require.Equal(t, "green", matrix.Items[0].Intelligence.Status)
	require.Equal(t, "yellow", matrix.Items[0].Intelligence.Points[0].Status)
	require.Equal(t, time.Minute, repo.bucket)
	require.Equal(t, 60*time.Minute, repo.end.Sub(repo.start))
	require.Equal(t, repo.start, matrix.Items[0].Intelligence.WindowStart)
	require.Equal(t, 60, matrix.Items[0].Intelligence.BucketSeconds)
	require.Equal(t, 60, matrix.Items[0].Intelligence.WindowPoints)
	require.Nil(t, matrix.Items[1].Intelligence)
	require.Nil(t, matrix.Items[2].Intelligence)
	repo.latest.CheckedAt = time.Now().Add(-5 * time.Minute)
	require.NoError(t, svc.Attach(context.Background(), matrix, ChannelMonitorV2Filter{Bucket: 5 * time.Minute}))
	require.Equal(t, "stale", matrix.Items[0].Intelligence.Status)
}

func TestIntelligenceMinuteCadence(t *testing.T) {
	now := time.Date(2026, 9, 25, 4, 30, 0, 0, time.UTC)
	require.Equal(t, time.Minute, IntelligenceInterval)
	require.Equal(t, 30*time.Second, IntelligenceTimeout)
	require.GreaterOrEqual(t, intelligenceConcurrency, 6)
	require.True(t, intelligenceProbeDue(nil, now))
	require.False(t, intelligenceProbeDue(&IntelligencePoint{CheckedAt: now}, now.Add(59*time.Second)))
	require.True(t, intelligenceProbeDue(&IntelligencePoint{CheckedAt: now.Add(time.Millisecond)}, now.Add(time.Minute)))
	require.True(t, intelligenceProbeDue(&IntelligencePoint{CheckedAt: now.Add(59 * time.Second)}, now.Add(time.Minute)))
	require.False(t, intelligenceProbeDue(&IntelligencePoint{CheckedAt: now.Add(time.Minute)}, now))
}
