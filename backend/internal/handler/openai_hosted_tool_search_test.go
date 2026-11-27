package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestOpenAIResponsesHTTPHostedSearchHistoryBeforeToolResult(t *testing.T) {
	const input = `{"input":[{"type":"tool_search_call","execution":"server","call_id":null,"status":"completed","arguments":{"paths":["crm"]}},{"type":"tool_search_output","execution":"server","call_id":null,"status":"completed","tools":[]},{"type":"item_reference","id":"call_order_auth"},`
	h := &OpenAIGatewayHandler{}
	for _, tc := range []struct {
		name   string
		output string
		valid  bool
	}{
		{"referenced", `{"type":"function_call_output","call_id":"call_order_auth","output":"ok"}`, true},
		{"missing id", `{"type":"function_call_output","output":"ok"}`, false},
		{"unrelated id", `{"type":"function_call_output","call_id":"call_other","output":"ok"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			require.Equal(t, tc.valid, h.validateFunctionCallOutputRequest(c, []byte(input+tc.output+`]}`), zap.NewNop()))
			if tc.valid {
				require.False(t, c.Writer.Written())
			} else {
				require.Equal(t, 400, w.Code)
			}
		})
	}
}
