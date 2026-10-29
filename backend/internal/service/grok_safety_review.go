package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const grokSafetyReviewRejectedCode = "safety_review_rejected"

// GrokSafetyReviewMark records one confirmed upstream Grok safety rejection.
// It is deliberately separate from CyberPolicyMark: the fee is opt-in per group
// and must not be inferred from a generic 403.
type GrokSafetyReviewMark struct {
	AttemptID         string // server-generated idempotency key for this incoming call
	RequestID         string // platform X-Request-ID
	ClientRequestID   string // platform X-Client-Request-ID
	UpstreamRequestID string // upstream X-Request-ID (if any)
	Model             string
	HTTPStatus        int
	ErrorCode         string
	Body              string
}

const grokSafetyReviewMarkKey = "ops_grok_safety_review"

// IsGrokSafetyReviewRejection matches only the documented structured code.
// A bare 403 / invalid_request_error / message is intentionally not enough.
func IsGrokSafetyReviewRejection(_ int, body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return grokSafetyReviewCodeFromPayload(body) != ""
}

func grokSafetyReviewCodeFromPayload(body []byte) string {
	for _, path := range []string{"error.code", "response.error.code"} {
		if code := strings.TrimSpace(gjson.GetBytes(body, path).String()); strings.EqualFold(code, grokSafetyReviewRejectedCode) {
			return grokSafetyReviewRejectedCode
		}
	}
	return ""
}

// MarkGrokSafetyReview stores the first confirmed rejection for this request.
// Multiple SSE error frames therefore only produce one fee.
func MarkGrokSafetyReview(c *gin.Context, mark GrokSafetyReviewMark) {
	if c == nil {
		return
	}
	if GetGrokSafetyReviewMark(c) != nil {
		return
	}
	if strings.TrimSpace(mark.AttemptID) == "" {
		mark.AttemptID = uuid.NewString()
	}
	mark.ErrorCode = strings.TrimSpace(mark.ErrorCode)
	if mark.ErrorCode == "" {
		mark.ErrorCode = grokSafetyReviewRejectedCode
	}
	mark.Body = truncateString(strings.TrimSpace(mark.Body), 4096)
	c.Set(grokSafetyReviewMarkKey, &mark)
}

func GetGrokSafetyReviewMark(c *gin.Context) *GrokSafetyReviewMark {
	if c == nil {
		return nil
	}
	if v, ok := c.Get(grokSafetyReviewMarkKey); ok {
		if mark, ok := v.(*GrokSafetyReviewMark); ok {
			return mark
		}
	}
	return nil
}

func ClearGrokSafetyReviewMark(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(grokSafetyReviewMarkKey, (*GrokSafetyReviewMark)(nil))
}

// markGrokSafetyReviewFromUpstream records the exact documented rejection and
// returns true when this response is fee-eligible. It never infers the fee from
// a bare 403 or a generic content-policy message.
func markGrokSafetyReviewFromUpstream(c *gin.Context, upstreamRequestID, model string, status int, body []byte) bool {
	if c == nil || !IsGrokSafetyReviewRejection(status, body) {
		return false
	}
	mark := GrokSafetyReviewMark{
		RequestID:         strings.TrimSpace(c.Writer.Header().Get("X-Request-ID")),
		UpstreamRequestID: strings.TrimSpace(upstreamRequestID),
		Model:             strings.TrimSpace(model),
		HTTPStatus:        status,
		ErrorCode:         grokSafetyReviewRejectedCode,
		Body:              string(body),
	}
	if c.Request != nil {
		mark.ClientRequestID, _ = c.Request.Context().Value(ctxkey.ClientRequestID).(string)
	}
	MarkGrokSafetyReview(c, mark)
	return true
}

// grokSafetyReviewStatus reports the client-facing status for a confirmed
// rejection. Streaming rejections arrive after HTTP 200; keep 200 in that case.
func grokSafetyReviewStatus(stream bool, upstreamStatus int) int {
	if stream {
		return http.StatusOK
	}
	if upstreamStatus == http.StatusForbidden {
		return http.StatusForbidden
	}
	return upstreamStatus
}
