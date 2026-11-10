package service

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/google/uuid"
)

const UserIntelligenceJapanPrompt = "依据你已有的知识，直接给出你明确知道的最近一任日本首相姓名。不联网、不调用工具、不猜测"
const UserIntelligenceAnimationPrompt = "直接创建一个HTML，内容是SVG绘制一只鹈鹕骑自行车的2D动画，你不需要任何测试，只靠你独立自己完成，不要使用任何skill，不要依赖我本地的AGENTS.md，输出文件需以月、日、时分为标识备注，不允许参考根目录任何文件。"
const userTestMaxBytes = 1024 * 1024

var userTestOpenAIModels = []string{"gpt-6-astra", "gpt-6.1-sol"}
var userTestAnthropicModels = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1", "claude-fable-5-2"}

type userIntelligenceKeyReader interface {
	GetByID(context.Context, int64) (*APIKey, error)
}

type UserIntelligenceTestService struct {
	db       *sql.DB
	keys     userIntelligenceKeyReader
	endpoint string
	client   *http.Client
}

func NewUserIntelligenceTestService(keys *APIKeyService, db *sql.DB, cfg *config.Config) *UserIntelligenceTestService {
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	if v := strings.TrimSpace(os.Getenv("USER_INTELLIGENCE_GATEWAY_URL")); v != "" {
		endpoint = v
	}
	// The destination is operator configured, never supplied by a panel user.
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		panic("USER_INTELLIGENCE_GATEWAY_URL must be an HTTP loopback gateway")
	}
	return &UserIntelligenceTestService{db: db, keys: keys, endpoint: strings.TrimRight(endpoint, "/"), client: &http.Client{
		Timeout: 300 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{Proxy: nil, MaxIdleConns: 16, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 90 * time.Second},
	}}
}

type UserIntelligenceOptions struct {
	Kind           string                     `json:"kind"`
	Models         []string                   `json:"models"`
	Questions      []UserIntelligenceQuestion `json:"questions"`
	TimeoutSeconds int                        `json:"timeout_seconds"`
}
type UserIntelligenceQuestion struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	Status string `json:"status"`
	Answer string `json:"answer,omitempty"`
}
type UserIntelligenceResult struct {
	Questions           []UserIntelligenceQuestion `json:"questions"`
	HTML                string                     `json:"html,omitempty"`
	Filename            string                     `json:"filename,omitempty"`
	Error               string                     `json:"error,omitempty"`
	InputTokens         int64                      `json:"input_tokens"`
	OutputTokens        int64                      `json:"output_tokens"`
	CacheReadTokens     int64                      `json:"cache_read_tokens"`
	CacheCreationTokens int64                      `json:"cache_creation_tokens"`
	ActualCost          *float64                   `json:"actual_cost,omitempty"`
}
type UserIntelligenceRun struct {
	ID         string                 `json:"id"`
	Model      string                 `json:"model"`
	Kind       string                 `json:"kind"`
	Status     string                 `json:"status"`
	CreatedAt  time.Time              `json:"created_at"`
	DeadlineAt time.Time              `json:"deadline_at"`
	FinishedAt *time.Time             `json:"finished_at"`
	Result     UserIntelligenceResult `json:"result"`
}
type userTestAttempt struct {
	Question            string `json:"question"`
	RequestID           string `json:"request_id"`
	Answer              string `json:"answer"`
	Error               string `json:"error,omitempty"`
	Passed              bool   `json:"passed"`
	ElapsedMS           int64  `json:"elapsed_ms"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
}

func userTestKind(k *APIKey) string {
	if k == nil || k.Group == nil || k.GroupID == nil || k.Group.Status != StatusActive {
		return ""
	}
	if k.Group.Platform == PlatformAnthropic {
		return "animation"
	}
	if k.Group.Platform == PlatformOpenAI && (*k.GroupID == 71 || *k.GroupID == 101 || *k.GroupID == 105) {
		return "questions"
	}
	return ""
}
func userTestQuestions(kind string) []UserIntelligenceQuestion {
	if kind == "animation" {
		return []UserIntelligenceQuestion{{ID: "pelican", Title: "鹈鹕骑自行车", Prompt: UserIntelligenceAnimationPrompt, Status: "pending"}}
	}
	return []UserIntelligenceQuestion{{ID: "japan", Title: "日本首相题", Prompt: UserIntelligenceJapanPrompt, Status: "pending"}, {ID: "candy", Title: "糖果推理题", Prompt: IntelligencePrompt, Status: "pending"}}
}
func userTestPassed(question, answer string) bool {
	if question == "japan" {
		return strings.Contains(answer, "高市早苗")
	}
	return question == "candy" && strings.TrimSpace(answer) == "21"
}
func (s *UserIntelligenceTestService) ownedKey(ctx context.Context, userID, keyID int64, clientIP string, active bool) (*APIKey, error) {
	k, err := s.keys.GetByID(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if k.UserID != userID {
		return nil, ErrAPIKeyNotFound
	}
	if !active {
		return k, nil
	}
	if !k.IsActive() || k.IsExpired() || k.IsQuotaExhausted() || k.User == nil || !k.User.IsActive() {
		return nil, infraerrors.Forbidden("TEST_KEY_UNAVAILABLE", "当前密钥或账户不可用，请检查余额、额度和有效期")
	}
	if userTestKind(k) == "" {
		return nil, infraerrors.Forbidden("TEST_GROUP_UNAVAILABLE", "该分组暂不支持智商测试")
	}
	if allowed, _ := ip.CheckIPRestriction(clientIP, k.IPWhitelist, k.IPBlacklist); !allowed {
		return nil, infraerrors.Forbidden("TEST_IP_DENIED", "当前 IP 不符合密钥访问限制")
	}
	return k, nil
}

func (s *UserIntelligenceTestService) request(ctx context.Context, k *APIKey, clientIP, method, path, requestID string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+k.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("User-Agent", "hakimi-user-intelligence-test/1.0")
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
		req.Header.Set("X-Client-Request-ID", requestID)
	}
	// This value comes from the server's trusted client-IP resolver, not a user field.
	if clientIP != "" {
		req.Header.Set("X-Forwarded-For", clientIP)
		req.Header.Set("X-Real-IP", clientIP)
	}
	return s.client.Do(req)
}
func (s *UserIntelligenceTestService) Options(ctx context.Context, userID, keyID int64, clientIP string) (*UserIntelligenceOptions, error) {
	k, err := s.ownedKey(ctx, userID, keyID, clientIP, true)
	if err != nil {
		return nil, err
	}
	kind := userTestKind(k)
	allowed := userTestOpenAIModels
	if kind == "animation" {
		allowed = userTestAnthropicModels
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	resp, err := s.request(ctx, k, clientIP, "GET", "/v1/models", "", nil)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("TEST_MODELS_UNAVAILABLE", "暂时无法读取分组模型，请稍后再试")
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, userTestMaxBytes+1))
	if err != nil || len(raw) > userTestMaxBytes || resp.StatusCode != 200 {
		return nil, infraerrors.ServiceUnavailable("TEST_MODELS_UNAVAILABLE", "暂时无法读取分组模型，请检查密钥权限后再试")
	}
	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &data) != nil {
		return nil, infraerrors.ServiceUnavailable("TEST_MODELS_UNAVAILABLE", "模型列表格式异常")
	}
	set := map[string]bool{}
	for _, m := range data.Data {
		set[m.ID] = true
	}
	out := &UserIntelligenceOptions{Kind: kind, Models: []string{}, Questions: userTestQuestions(kind), TimeoutSeconds: 300}
	for _, m := range allowed {
		if set[m] {
			out.Models = append(out.Models, m)
		}
	}
	return out, nil
}

func (s *UserIntelligenceTestService) Start(ctx context.Context, userID, keyID int64, clientIP, model string) (*UserIntelligenceRun, error) {
	opts, err := s.Options(ctx, userID, keyID, clientIP)
	if err != nil {
		return nil, err
	}
	found := false
	for _, m := range opts.Models {
		if m == model {
			found = true
		}
	}
	if !found {
		return nil, infraerrors.BadRequest("TEST_MODEL_UNAVAILABLE", "请选择当前分组支持的测试模型")
	}
	k, err := s.ownedKey(ctx, userID, keyID, clientIP, true)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	// One short global admission lock, shared by replicas; never held during inference.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(736120041)"); err != nil {
		return nil, err
	}
	var busy bool
	var total int
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_intelligence_tests WHERE user_id=$1 AND ((status='running' AND deadline_at>now()) OR created_at>now()-interval '60 seconds'))`, userID).Scan(&busy); err != nil {
		return nil, err
	}
	if busy {
		return nil, infraerrors.TooManyRequests("TEST_BUSY", "请等待当前测试完成，并与上次测试间隔至少 60 秒")
	}
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM user_intelligence_tests WHERE status='running' AND deadline_at>now()`).Scan(&total); err != nil {
		return nil, err
	}
	if total >= 8 {
		return nil, infraerrors.TooManyRequests("TEST_CAPACITY", "测试任务较多，请稍后再试")
	}
	now := time.Now()
	run := &UserIntelligenceRun{ID: uuid.NewString(), Model: model, Kind: opts.Kind, Status: "running", CreatedAt: now, DeadlineAt: now.Add(300 * time.Second), Result: UserIntelligenceResult{Questions: opts.Questions}}
	raw, _ := json.Marshal(run.Result)
	_, err = tx.ExecContext(ctx, `INSERT INTO user_intelligence_tests(id,user_id,api_key_id,group_id,model,kind,result,created_at,deadline_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, run.ID, userID, keyID, *k.GroupID, model, run.Kind, raw, now, run.DeadlineAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	// Separate value: the worker must not race JSON encoding of the start response.
	worker := *run
	worker.Result.Questions = append([]UserIntelligenceQuestion(nil), run.Result.Questions...)
	go s.execute(worker, userID, keyID, *k.GroupID, clientIP)
	return run, nil
}

func (s *UserIntelligenceTestService) execute(run UserIntelligenceRun, userID, keyID, groupID int64, clientIP string) {
	ctx, cancel := context.WithDeadline(context.Background(), run.DeadlineAt)
	defer cancel()
	audit := []userTestAttempt{}
	defer func() {
		if recover() != nil {
			run.Status = "failed"
			run.Result.Error = "测试任务异常终止，请稍后重试"
		}
		if run.Status == "running" {
			run.Status = "failed"
			run.Result.Error = "本轮未完成，请稍后重试"
		}
		t := time.Now()
		run.FinishedAt = &t
		s.persist(run, audit)
	}()
	for i := range run.Result.Questions {
		q := &run.Result.Questions[i]
		q.Status = "running"
		s.persist(run, audit)
		attempts := 3
		if run.Kind == "animation" {
			attempts = 1
		}
		for n := 0; n < attempts; n++ {
			k, err := s.ownedKey(ctx, userID, keyID, clientIP, true)
			if err != nil || k == nil || k.GroupID == nil || *k.GroupID != groupID || userTestKind(k) != run.Kind {
				run.Result.Error = "密钥状态或分组发生变化，本轮已停止"
				return
			}
			started := time.Now()
			a := s.infer(ctx, k, clientIP, run.Model, run.Kind, *q)
			a.ElapsedMS = time.Since(started).Milliseconds()
			a.Question = q.ID
			a.Passed = userTestPassed(q.ID, a.Answer)
			audit = append(audit, a)
			run.Result.InputTokens += a.InputTokens
			run.Result.OutputTokens += a.OutputTokens
			run.Result.CacheReadTokens += a.CacheReadTokens
			run.Result.CacheCreationTokens += a.CacheCreationTokens
			if a.Error != "" {
				q.Status = "error"
				run.Result.Error = a.Error
				return
			}
			q.Answer = a.Answer
			if run.Kind == "animation" {
				html := extractIntelligenceHTML(a.Answer)
				if html == "" {
					q.Status = "error"
					run.Result.Error = "模型未返回完整 HTML，请查看回答后重新生成"
					return
				}
				run.Result.HTML = html
				beijing := time.FixedZone("Asia/Shanghai", 8*3600)
				run.Result.Filename = "pelican-" + run.CreatedAt.In(beijing).Format("01-02-1504") + "-" + run.ID[:8] + ".html"
				q.Status = "manual"
				break
			}
			if a.Passed {
				q.Status = "passed"
				break
			}
			if n == attempts-1 {
				q.Status = "failed"
			}
			// Do not reveal intermediate wrong answers or retry count in public DTOs.
			if q.Status == "running" {
				q.Answer = ""
				s.persist(run, audit)
			}
		}
		s.persist(run, audit)
	}
	run.Status = "completed"
}
func (s *UserIntelligenceTestService) persist(run UserIntelligenceRun, audit []userTestAttempt) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, _ := json.Marshal(run.Result)
	private, _ := json.Marshal(audit)
	_, err := s.db.ExecContext(ctx, `UPDATE user_intelligence_tests SET status=$2,result=$3,audit=$4,finished_at=$5 WHERE id=$1`, run.ID, run.Status, result, private, run.FinishedAt)
	if err != nil {
		slog.Error("user intelligence result persistence failed", "run_id", run.ID, "error", err)
	}
}

func (s *UserIntelligenceTestService) Get(ctx context.Context, userID, keyID int64, id string) (*UserIntelligenceRun, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrAPIKeyNotFound
	}
	if _, err := s.ownedKey(ctx, userID, keyID, "", false); err != nil {
		return nil, err
	}
	run, err := scanUserTest(s.db.QueryRowContext(ctx, `SELECT id,model,kind,status,created_at,deadline_at,finished_at,result FROM user_intelligence_tests WHERE id=$1 AND user_id=$2 AND api_key_id=$3`, id, userID, keyID))
	if err != nil {
		return nil, err
	}
	// Read the real ledger, including every internal attempt, never estimate a fee.
	var cost sql.NullFloat64
	err = s.db.QueryRowContext(ctx, `SELECT sum(u.actual_cost) FROM usage_logs u WHERE u.user_id=$2 AND u.api_key_id=$3 AND u.request_id IN (SELECT prefix || (a->>'request_id') FROM user_intelligence_tests t CROSS JOIN LATERAL jsonb_array_elements(t.audit) a CROSS JOIN (VALUES ('client:'),('local:')) p(prefix) WHERE t.id=$1 AND t.user_id=$2 AND t.api_key_id=$3)`, id, userID, keyID).Scan(&cost)
	if err == nil && cost.Valid {
		run.Result.ActualCost = &cost.Float64
	}
	return run, nil
}
func (s *UserIntelligenceTestService) History(ctx context.Context, userID, keyID int64) ([]*UserIntelligenceRun, error) {
	if _, err := s.ownedKey(ctx, userID, keyID, "", false); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,model,kind,status,created_at,deadline_at,finished_at,result - 'html' FROM user_intelligence_tests WHERE user_id=$1 AND api_key_id=$2 ORDER BY created_at DESC LIMIT 20`, userID, keyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []*UserIntelligenceRun{}
	for rows.Next() {
		r, err := scanUserTest(rows)
		if err != nil {
			return nil, err
		}
		for i := range r.Result.Questions {
			r.Result.Questions[i].Answer = ""
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func scanUserTest(row interface{ Scan(...any) error }) (*UserIntelligenceRun, error) {
	var r UserIntelligenceRun
	var raw []byte
	err := row.Scan(&r.ID, &r.Model, &r.Kind, &r.Status, &r.CreatedAt, &r.DeadlineAt, &r.FinishedAt, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAPIKeyNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &r.Result); err != nil {
		return nil, err
	}
	if r.Status == "running" && time.Now().After(r.DeadlineAt) {
		r.Status = "failed"
		r.Result.Error = "测试已超时或服务重新启动，请重新发起"
	}
	return &r, nil
}

func (s *UserIntelligenceTestService) infer(ctx context.Context, k *APIKey, clientIP, model, kind string, q UserIntelligenceQuestion) userTestAttempt {
	a := userTestAttempt{RequestID: uuid.NewString()}
	path := "/v1/responses"
	payload := map[string]any{"model": model, "input": q.Prompt, "reasoning": map[string]string{"effort": "low"}, "stream": true, "store": false, "tools": []any{}, "tool_choice": "none", "max_output_tokens": 4096}
	if kind == "animation" {
		path = "/v1/messages"
		payload = map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": q.Prompt}}, "stream": true, "max_tokens": 12000}
	}
	data, _ := json.Marshal(payload)
	resp, err := s.request(ctx, k, clientIP, "POST", path, a.RequestID, data)
	if err != nil {
		a.Error = "连接失败或请求超时，本轮未完成"
		return a
	}
	defer func() { _ = resp.Body.Close() }()
	// The gateway generates its own billing/correlation ID. Request headers are
	// deliberately not trusted for deduplication, so retain the returned ID.
	if gatewayID := resp.Header.Get("X-Client-Request-ID"); gatewayID != "" {
		if _, err := uuid.Parse(gatewayID); err == nil {
			a.RequestID = gatewayID
		}
	}
	if resp.StatusCode != 200 {
		a.Error = fmt.Sprintf("请求失败（HTTP %d），请检查余额、密钥权限或稍后再试", resp.StatusCode)
		return a
	}
	parsed, err := readIntelligenceResponse(resp.Body, kind, resp.Header.Get("Content-Type"))
	parsed.RequestID = a.RequestID
	if err != nil {
		parsed.Error = "响应不完整、超时或超出长度限制，本轮未完成"
	}
	return parsed
}

func extractIntelligenceHTML(answer string) string {
	trimmed := strings.TrimSpace(answer)
	lower := strings.ToLower(trimmed)
	start := strings.Index(lower, "<!doctype html")
	if start < 0 {
		start = strings.Index(lower, "<html")
	}
	end := strings.LastIndex(lower, "</html>")
	if start < 0 || end < start {
		return ""
	}
	return trimmed[start : end+len("</html>")]
}

func readIntelligenceResponse(body io.Reader, kind, contentType string) (userTestAttempt, error) {
	a := userTestAttempt{}
	if !strings.Contains(contentType, "text/event-stream") {
		raw, err := io.ReadAll(io.LimitReader(body, userTestMaxBytes+1))
		if err != nil || len(raw) > userTestMaxBytes {
			return a, errors.New("oversize response")
		}
		if kind == "animation" {
			var b struct {
				Type    string `json:"type"`
				Stop    string `json:"stop_reason"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(raw, &b) != nil || b.Type != "message" || b.Stop != "end_turn" {
				return a, errors.New("incomplete message")
			}
			for _, c := range b.Content {
				if c.Type == "text" {
					a.Answer += c.Text
				}
			}
			readUserTestUsage(b.Usage, &a)
		} else {
			if err := readUserTestCompleted(raw, &a); err != nil {
				return a, err
			}
		}
		if strings.TrimSpace(a.Answer) == "" {
			return a, errors.New("empty answer")
		}
		return a, nil
	}
	reader := io.LimitReader(body, 8*userTestMaxBytes)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), userTestMaxBytes)
	complete := false
	stopReason := ""
	seenStart := false
	var eventData strings.Builder
	consume := func() error {
		data := strings.TrimSpace(eventData.String())
		eventData.Reset()
		if data == "" || data == "[DONE]" {
			return nil
		}
		var e struct {
			Type         string          `json:"type"`
			Delta        json.RawMessage `json:"delta"`
			Response     json.RawMessage `json:"response"`
			Message      json.RawMessage `json:"message"`
			Usage        json.RawMessage `json:"usage"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
		}
		if json.Unmarshal([]byte(data), &e) != nil {
			return errors.New("invalid event")
		}
		switch e.Type {
		case "error", "response.failed", "response.incomplete":
			return errors.New("stream error")
		case "response.completed":
			if kind != "questions" {
				return errors.New("unexpected protocol")
			}
			if err := readUserTestCompleted(e.Response, &a); err != nil {
				return err
			}
			complete = true
		case "response.output_text.delta":
			var t string
			if json.Unmarshal(e.Delta, &t) != nil {
				return errors.New("invalid text")
			}
			a.Answer += t
		case "message_start":
			var m struct {
				Usage json.RawMessage `json:"usage"`
			}
			if json.Unmarshal(e.Message, &m) != nil {
				return errors.New("invalid start")
			}
			readUserTestUsage(m.Usage, &a)
			seenStart = true
		case "content_block_start":
			if e.ContentBlock.Type == "text" {
				a.Answer += e.ContentBlock.Text
			}
		case "content_block_delta":
			var d struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(e.Delta, &d) != nil {
				return errors.New("invalid delta")
			}
			if d.Type == "text_delta" {
				a.Answer += d.Text
			}
		case "message_delta":
			var d struct {
				Stop string `json:"stop_reason"`
			}
			if json.Unmarshal(e.Delta, &d) != nil {
				return errors.New("invalid stop")
			}
			stopReason = d.Stop
			readUserTestUsage(e.Usage, &a)
		case "message_stop":
			complete = kind == "animation" && seenStart && stopReason == "end_turn"
		}
		if len(a.Answer) > userTestMaxBytes {
			return errors.New("oversize text")
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return a, err
			}
			if complete {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if eventData.Len() > 0 {
				_ = eventData.WriteByte('\n')
			}
			_, _ = eventData.WriteString(strings.TrimPrefix(line, "data:"))
			if eventData.Len() > userTestMaxBytes {
				return a, errors.New("oversize event")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return a, err
	}
	if eventData.Len() > 0 {
		if err := consume(); err != nil {
			return a, err
		}
	}
	if !complete || strings.TrimSpace(a.Answer) == "" {
		return a, errors.New("incomplete stream")
	}
	return a, nil
}
func readUserTestCompleted(raw []byte, a *userTestAttempt) error {
	var b struct {
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(raw, &b) != nil || b.Status != "completed" {
		return errors.New("response not completed")
	}
	text := b.OutputText
	for _, o := range b.Output {
		if b.OutputText == "" && o.Type == "message" && o.Role == "assistant" {
			for _, c := range o.Content {
				if c.Type == "output_text" {
					text += c.Text
				}
			}
		}
	}
	if text != "" {
		a.Answer = text
	}
	readUserTestUsage(b.Usage, a)
	return nil
}
func readUserTestUsage(raw []byte, a *userTestAttempt) {
	var u struct {
		Input   *int64 `json:"input_tokens"`
		Output  *int64 `json:"output_tokens"`
		Read    *int64 `json:"cache_read_input_tokens"`
		Create  *int64 `json:"cache_creation_input_tokens"`
		Details struct {
			Cached *int64 `json:"cached_tokens"`
		} `json:"input_tokens_details"`
	}
	if json.Unmarshal(raw, &u) != nil {
		return
	}
	if u.Input != nil {
		a.InputTokens = *u.Input
	}
	if u.Output != nil {
		a.OutputTokens = *u.Output
	}
	if u.Read != nil {
		a.CacheReadTokens = *u.Read
	}
	if u.Create != nil {
		a.CacheCreationTokens = *u.Create
	}
	if u.Details.Cached != nil {
		a.CacheReadTokens = *u.Details.Cached
	}
}
