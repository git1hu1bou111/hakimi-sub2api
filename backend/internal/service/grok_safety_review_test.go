//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIsGrokSafetyReviewRejection(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "documented error code",
			body: `{"error":{"code":"safety_review_rejected","type":"invalid_request_error","message":"I'm sorry, I can't help with that request."}}`,
			want: true,
		},
		{
			name: "nested response error code",
			body: `{"type":"response.failed","response":{"error":{"code":"safety_review_rejected"}}}`,
			want: true,
		},
		{
			name: "bare forbidden is not enough",
			body: `{"error":{"type":"invalid_request_error","message":"I'm sorry, I can't help with that request."}}`,
			want: false,
		},
		{
			name: "old content policy code is not this fee",
			body: `{"error":{"code":"new_sensitive","message":"text is sensitive"}}`,
			want: false,
		},
		{
			name: "account entitlement is not this fee",
			body: `{"error":{"code":"subscription_required","message":"subscription required"}}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsGrokSafetyReviewRejection(http.StatusForbidden, []byte(tt.body)))
		})
	}
}

func TestGrokSafetyReviewMarkIsFirstWins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	first := GrokSafetyReviewMark{AttemptID: "attempt-1", RequestID: "req-1", ErrorCode: grokSafetyReviewRejectedCode}
	second := GrokSafetyReviewMark{AttemptID: "attempt-2", RequestID: "req-2", ErrorCode: grokSafetyReviewRejectedCode}

	MarkGrokSafetyReview(c, first)
	MarkGrokSafetyReview(c, second)
	got := GetGrokSafetyReviewMark(c)
	require.NotNil(t, got)
	require.Equal(t, "attempt-1", got.AttemptID)
	require.Equal(t, "req-1", got.RequestID)

	ClearGrokSafetyReviewMark(c)
	require.Nil(t, GetGrokSafetyReviewMark(c))
}
