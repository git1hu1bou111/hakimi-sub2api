//go:build unit

package service

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type intelligenceTestKeys struct{ key *APIKey }

func (k intelligenceTestKeys) GetByID(context.Context, int64) (*APIKey, error) { return k.key, nil }
func intelligenceFixture() *APIKey {
	id := int64(71)
	return &APIKey{ID: 12, UserID: 7, Key: "test-only-key", Status: StatusActive, GroupID: &id, Group: &Group{ID: id, Status: StatusActive, Platform: PlatformOpenAI}, User: &User{ID: 7, Status: StatusActive}}
}

type intelligenceCapture struct {
	results *[]UserIntelligenceResult
	audits  *[][]userTestAttempt
	audit   bool
}

func (c intelligenceCapture) Match(v driver.Value) bool {
	b, ok := v.([]byte)
	if !ok {
		return false
	}
	if c.audit {
		var a []userTestAttempt
		if json.Unmarshal(b, &a) != nil {
			return false
		}
		*c.audits = append(*c.audits, a)
	} else {
		var r UserIntelligenceResult
		if json.Unmarshal(b, &r) != nil {
			return false
		}
		*c.results = append(*c.results, r)
	}
	return true
}

func TestUserIntelligenceScopeAndOwnership(t *testing.T) {
	k := intelligenceFixture()
	s := &UserIntelligenceTestService{keys: intelligenceTestKeys{k}}
	_, err := s.ownedKey(context.Background(), 99, k.ID, "", false)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	for _, id := range []int64{71, 101, 105} {
		*k.GroupID = id
		require.Equal(t, "questions", userTestKind(k))
	}
	*k.GroupID = 999
	require.Empty(t, userTestKind(k))
	k.Group.Platform = PlatformAnthropic
	require.Equal(t, "animation", userTestKind(k))
	k.IPWhitelist = []string{"192.0.2.1"}
	_, err = s.ownedKey(context.Background(), 7, 12, "192.0.2.2", true)
	require.Error(t, err)
	k.IPWhitelist = nil
	k.Status = "inactive"
	_, err = s.ownedKey(context.Background(), 7, 12, "192.0.2.1", true)
	require.Error(t, err)
	k.Status = StatusActive
	k.Group.Status = "inactive"
	require.Empty(t, userTestKind(k))
}
func TestUserIntelligenceAnswerRules(t *testing.T) {
	require.True(t, userTestPassed("japan", "我明确知道的是高市早苗。"))
	require.False(t, userTestPassed("japan", "高市 早苗"))
	require.True(t, userTestPassed("candy", " 21\n"))
	for _, answer := range []string{"29", "最少21个", "211", ""} {
		require.False(t, userTestPassed("candy", answer))
	}
	require.False(t, userTestPassed("pelican", "21"))
}
func TestUserIntelligenceOptionsOrderAndPayload(t *testing.T) {
	k := intelligenceFixture()
	k.Group.Platform = PlatformAnthropic
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/models", r.URL.Path)
		require.Equal(t, "Bearer test-only-key", r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"data":[{"id":"claude-fable-5-2"},{"id":"unapproved"},{"id":"claude-opus-5-5"},{"id":"claude-sonnet-5-5"}]}`)
	}))
	defer upstream.Close()
	s := &UserIntelligenceTestService{keys: intelligenceTestKeys{k}, endpoint: upstream.URL, client: upstream.Client()}
	opts, err := s.Options(context.Background(), 7, 12, "192.0.2.1")
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-2"}, opts.Models)
	_, err = s.Start(context.Background(), 7, 12, "192.0.2.1", "unapproved")
	require.Error(t, err) // rejected before touching DB
}
func TestUserIntelligenceRetriesAndPrivateAudit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		answers  []string
		expected []string
	}{
		{"eventual pass", []string{"不知道", "高市早苗", "29", "20", "21"}, []string{"passed", "passed"}},
		{"exhausted", []string{"不知道", "不知道", "不知道", "29", "29", "29"}, []string{"failed", "failed"}},
		{"first pass", []string{"高市早苗", "21"}, []string{"passed", "passed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			results := []UserIntelligenceResult{}
			audits := [][]userTestAttempt{}
			for i := 0; i < 5+len(tc.answers)-2; i++ {
				mock.ExpectExec("UPDATE user_intelligence_tests").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), intelligenceCapture{results: &results}, intelligenceCapture{audits: &audits, audit: true}, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			requests := 0
			ids := map[string]bool{}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/v1/responses", r.URL.Path)
				id := r.Header.Get("X-Client-Request-ID")
				require.NotEmpty(t, id)
				require.False(t, ids[id])
				ids[id] = true
				var body map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Equal(t, map[string]any{"effort": "low"}, body["reasoning"])
				require.Equal(t, "none", body["tool_choice"])
				require.Equal(t, false, body["store"])
				require.Equal(t, []any{}, body["tools"])
				require.IsType(t, "", body["input"])
				if requests >= len(tc.answers) {
					t.Error("too many requests")
					w.WriteHeader(500)
					return
				}
				answer := tc.answers[requests]
				requests++
				json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output_text": answer, "usage": map[string]int{"input_tokens": 10, "output_tokens": 2}})
			}))
			defer upstream.Close()
			s := &UserIntelligenceTestService{db: db, keys: intelligenceTestKeys{intelligenceFixture()}, endpoint: upstream.URL, client: upstream.Client()}
			s.execute(UserIntelligenceRun{ID: uuid.NewString(), Kind: "questions", Model: "gpt-6-astra", Status: "running", CreatedAt: time.Now(), DeadlineAt: time.Now().Add(time.Minute), Result: UserIntelligenceResult{Questions: userTestQuestions("questions")}}, 7, 12, 71, "192.0.2.1")
			require.NoError(t, mock.ExpectationsWereMet())
			require.Equal(t, len(tc.answers), requests)
			final := results[len(results)-1]
			for i, status := range tc.expected {
				require.Equal(t, status, final.Questions[i].Status)
			}
			require.Equal(t, int64(10*requests), final.InputTokens)
			require.Len(t, audits[len(audits)-1], requests)
			for _, r := range results {
				for _, q := range r.Questions {
					if q.Status == "running" {
						require.Empty(t, q.Answer)
					}
				}
			}
			public, _ := json.Marshal(final)
			require.NotContains(t, string(public), "request_id")
			require.NotContains(t, string(public), "attempt")
		})
	}
}
func TestUserIntelligenceNetworkErrorDoesNotRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	for i := 0; i < 2; i++ {
		mock.ExpectExec("UPDATE user_intelligence_tests").WillReturnResult(sqlmock.NewResult(0, 1))
	}
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(502)
		fmt.Fprint(w, "secret upstream credential")
	}))
	defer upstream.Close()
	s := &UserIntelligenceTestService{db: db, keys: intelligenceTestKeys{intelligenceFixture()}, endpoint: upstream.URL, client: upstream.Client()}
	s.execute(UserIntelligenceRun{ID: uuid.NewString(), Kind: "questions", Status: "running", DeadlineAt: time.Now().Add(time.Minute), Result: UserIntelligenceResult{Questions: userTestQuestions("questions")}}, 7, 12, 71, "192.0.2.1")
	require.Equal(t, 1, calls)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestUserIntelligenceStreamCompletion(t *testing.T) {
	tests := []struct {
		name, kind, body     string
		ok                   bool
		answer               string
		input, output, cache int64
	}{
		{"responses", "questions", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output_text\":\"21\",\"usage\":{\"input_tokens\":10,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":4}}}}\n\n", true, "21", 10, 2, 4},
		{"truncated", "questions", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\n", false, "", 0, 0, 0},
		{"anthropic", "animation", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7,\"cache_read_input_tokens\":3}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"<html></html>\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":8}}\n\ndata: {\"type\":\"message_stop\"}\n\n", true, "<html></html>", 7, 8, 3},
		{"anthropic cut off", "animation", "data: {\"type\":\"message_start\",\"message\":{}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", false, "", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, err := readIntelligenceResponse(strings.NewReader(tc.body), tc.kind, "text/event-stream")
			if !tc.ok {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.answer, a.Answer)
			require.Equal(t, tc.input, a.InputTokens)
			require.Equal(t, tc.output, a.OutputTokens)
			require.Equal(t, tc.cache, a.CacheReadTokens)
		})
	}
	_, err := readIntelligenceResponse(strings.NewReader(strings.Repeat("x", userTestMaxBytes+1)), "questions", "application/json")
	require.Error(t, err)
}
func TestUserIntelligenceHistoryIsolation(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	s := &UserIntelligenceTestService{db: db, keys: intelligenceTestKeys{intelligenceFixture()}}
	_, err = s.History(context.Background(), 99, 12)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	id := uuid.NewString()
	mock.ExpectQuery("SELECT id,model,kind,status,created_at,deadline_at,finished_at,result FROM user_intelligence_tests WHERE id=\\$1 AND user_id=\\$2 AND api_key_id=\\$3").WithArgs(id, int64(7), int64(12)).WillReturnRows(sqlmock.NewRows([]string{"id", "model", "kind", "status", "created_at", "deadline_at", "finished_at", "result"}))
	_, err = s.Get(context.Background(), 7, 12, id)
	require.ErrorIs(t, err, ErrAPIKeyNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}
func TestUserIntelligenceHTMLExtraction(t *testing.T) {
	require.Equal(t, "<!doctype html><html><body>动画</body></html>", extractIntelligenceHTML("```html\n<!doctype html><html><body>动画</body></html>\n```"))
	require.Empty(t, extractIntelligenceHTML("<svg></svg>"))
	require.Empty(t, extractIntelligenceHTML("<html>partial"))
}

func TestUserIntelligenceAnimationIsManualAndSingleCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	results := []UserIntelligenceResult{}
	for i := 0; i < 3; i++ {
		mock.ExpectExec("UPDATE user_intelligence_tests").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), intelligenceCapture{results: &results}, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "/v1/messages", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "claude-opus-5-5", body["model"])
		require.NotContains(t, body, "tools")
		fmt.Fprint(w, `{"type":"message","stop_reason":"end_turn","content":[{"type":"text","text":"<!doctype html><html><body><svg></svg></body></html>"}],"usage":{"input_tokens":50,"output_tokens":100}}`)
	}))
	defer upstream.Close()
	k := intelligenceFixture()
	k.Group.Platform = PlatformAnthropic
	s := &UserIntelligenceTestService{db: db, keys: intelligenceTestKeys{k}, endpoint: upstream.URL, client: upstream.Client()}
	s.execute(UserIntelligenceRun{ID: uuid.NewString(), Kind: "animation", Model: "claude-opus-5-5", Status: "running", CreatedAt: time.Date(2026, 11, 10, 21, 9, 0, 0, time.UTC), DeadlineAt: time.Now().Add(time.Minute), Result: UserIntelligenceResult{Questions: userTestQuestions("animation")}}, 7, 12, 71, "192.0.2.1")
	require.Equal(t, 1, calls)
	require.NoError(t, mock.ExpectationsWereMet())
	r := results[len(results)-1]
	require.Equal(t, "manual", r.Questions[0].Status)
	require.Contains(t, r.Filename, "pelican-11-11-0509-")
	require.NotEmpty(t, r.HTML)
}

func TestUserIntelligenceAdmissionLimits(t *testing.T) {
	for _, busy := range []bool{true, false} {
		t.Run(fmt.Sprint(busy), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"data":[{"id":"gpt-6-astra"}]}`) }))
			defer upstream.Close()
			s := &UserIntelligenceTestService{db: db, keys: intelligenceTestKeys{intelligenceFixture()}, endpoint: upstream.URL, client: upstream.Client()}
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT EXISTS").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"busy"}).AddRow(busy))
			if !busy {
				mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(8))
			}
			mock.ExpectRollback()
			_, err = s.Start(context.Background(), 7, 12, "192.0.2.1", "gpt-6-astra")
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserIntelligenceGatewayBillingCorrelation(t *testing.T) {
	gatewayID := uuid.NewString()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEqual(t, gatewayID, r.Header.Get("X-Client-Request-ID"))
		w.Header().Set("X-Client-Request-ID", gatewayID)
		fmt.Fprint(w, `{"status":"completed","output_text":"21","usage":{"input_tokens":10,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	s := &UserIntelligenceTestService{endpoint: upstream.URL, client: upstream.Client()}
	answer := s.infer(context.Background(), intelligenceFixture(), "192.0.2.1", "gpt-6-astra", "questions", UserIntelligenceQuestion{ID: "candy", Prompt: "test"})
	require.Empty(t, answer.Error)
	require.Equal(t, gatewayID, answer.RequestID)
}
