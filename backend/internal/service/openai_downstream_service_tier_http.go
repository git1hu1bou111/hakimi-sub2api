package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func defaultHTTPDownstreamTier(c *gin.Context) bool {
	return c != nil && c.Request != nil && useDefaultDownstreamTier(c.Request.Context())
}

// The upstream scanners supply complete lines. Rewriting at this point keeps
// network packet boundaries and downstream flush/keepalive behavior independent.
func downstreamTierSSELine(line string) string {
	if !strings.Contains(line, `"service_tier"`) {
		return line
	}
	data, ok := extractOpenAISSEDataLine(line)
	if !ok {
		return line
	}
	rewritten := rewriteDownstreamUltraFastTier([]byte(data))
	if len(rewritten) == len(data) {
		return line
	}
	return line[:len(line)-len(data)] + string(rewritten)
}

// This also handles buffered fallback responses for which the gateway could
// not extract a final JSON object and sends the original series of SSE frames.
func downstreamTierSSEBody(body string) string {
	if !strings.Contains(body, `"service_tier"`) {
		return body
	}
	var out strings.Builder
	last := 0
	for start := 0; start < len(body); {
		end := strings.IndexByte(body[start:], '\n')
		if end < 0 {
			end = len(body)
		} else {
			end += start
		}
		line := body[start:end]
		if rewritten := downstreamTierSSELine(line); len(rewritten) != len(line) {
			out.WriteString(body[last:start])
			out.WriteString(rewritten)
			last = end
		}
		start = end + 1
	}
	if last == 0 {
		return body
	}
	out.WriteString(body[last:])
	return out.String()
}

func downstreamHTTPResponseBody(c *gin.Context, body []byte, sse bool) []byte {
	if !defaultHTTPDownstreamTier(c) {
		return body
	}
	if sse {
		return []byte(downstreamTierSSEBody(string(body)))
	}
	return rewriteDownstreamUltraFastTier(body)
}
