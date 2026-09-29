package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

const (
	IntelligenceModel    = "gpt-6-astra"
	IntelligenceInterval = 60 * time.Second
	IntelligenceTimeout  = 30 * time.Second
	// Retries share one budget shorter than a minute, including backoff. A slow
	// attempt must not push the next natural-minute probe into a later minute.
	IntelligenceProbeBudget = 55 * time.Second
	IntelligenceMaxAttempts = 3
	IntelligenceSlots       = 60
	// A single round cannot promise one-minute coverage beyond this capacity.
	// Oldest-first selection rotates remaining accounts into subsequent rounds.
	accountIntelligenceConcurrency = 16
	// Keep the current six OpenAI groups in the same minute even if every
	// upstream times out, while retaining an explicit concurrency bound.
	intelligenceConcurrency = 8
	IntelligencePrompt      = "在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）  形状 | 苹果味 | 桃子味 | 西瓜味 圆形 | 7 | 9 | 8 五角星形 | 7 | 6 | 4  不许联网，自己计算。只回答最少取出的糖果总数，使用一个整数，不要解释。"
)

type IntelligencePoint struct {
	CheckedAt time.Time `json:"checked_at"`
	Status    string    `json:"status"`
	LatencyMs int       `json:"latency_ms"`
	Detail    string    `json:"detail,omitempty"`
}

type IntelligenceUptime struct {
	Model              string              `json:"model"`
	ReasoningEffort    string              `json:"reasoning_effort"`
	IntervalSeconds    int                 `json:"interval_seconds"`
	TimeoutSeconds     int                 `json:"timeout_seconds"`
	RetryMaxAttempts   int                 `json:"retry_max_attempts"`
	RetryBudgetSeconds int                 `json:"retry_budget_seconds"`
	Status             string              `json:"status"`
	CheckedAt          *time.Time          `json:"checked_at,omitempty"`
	PassRate           *float64            `json:"pass_rate,omitempty"`
	Points             []IntelligencePoint `json:"points"`
	WindowStart        time.Time           `json:"window_start"`
	BucketSeconds      int                 `json:"bucket_seconds"`
	WindowPoints       int                 `json:"window_points"`
}

type IntelligenceGroup struct {
	ID   int64
	Name string
}

type IntelligenceRepository interface {
	ListGroups(context.Context, []int64) ([]IntelligenceGroup, error)
	KeyCandidates(context.Context, int64) ([]int64, error)
	AdminIDs(context.Context) ([]int64, error)
	Latest(context.Context, int64) (*IntelligencePoint, error)
	Save(context.Context, int64, IntelligencePoint) error
	History(context.Context, []int64, time.Time, time.Time, time.Duration) (map[int64][]IntelligencePoint, error)
	Prune(context.Context, time.Time) error
}

// This worker is separate from the retired V1 probes and passive V2 scorer.
// Reads never trigger paid requests, and only configured OpenAI groups run.
type ChannelMonitorIntelligence struct {
	repo        IntelligenceRepository
	v2          ChannelMonitorV2Repository
	keys        *APIKeyService
	settings    channelMonitorRuntimeReader
	db          *sql.DB
	endpoint    string
	client      *http.Client
	owner       string
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	startOnce   sync.Once
	stopOnce    sync.Once
	accountTest *AccountTestService
	groupSweeps sync.Map // group ID -> *intelligenceGroupSweep; tick never overlaps
}

func NewChannelMonitorIntelligence(repo IntelligenceRepository, v2 ChannelMonitorV2Repository, keys *APIKeyService, settings channelMonitorRuntimeReader, db *sql.DB, endpoint string) *ChannelMonitorIntelligence {
	ctx, cancel := context.WithCancel(context.Background())
	return &ChannelMonitorIntelligence{
		repo: repo, v2: v2, keys: keys, settings: settings, db: db,
		endpoint: strings.TrimRight(endpoint, "/") + "/v1/responses",
		client: &http.Client{
			Timeout: IntelligenceTimeout,
			// Never forward a group credential through an HTTP redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport:     &http.Transport{MaxIdleConns: 4, MaxIdleConnsPerHost: 2, IdleConnTimeout: 90 * time.Second},
		},
		owner: uuid.NewString(), ctx: ctx, cancel: cancel,
	}
}

func (s *ChannelMonitorIntelligence) Start() {
	s.startOnce.Do(func() {
		s.wg.Add(2)
		go func() {
			defer s.wg.Done()
			// Allow the local gateway to start accepting requests before probing.
			timer := time.NewTimer(15 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-timer.C:
					s.tick()
					// Schedule on minute boundaries, not "60s after all requests
					// finish". Slow responses must not lengthen the cadence.
					now := time.Now()
					timer.Reset(time.Until(now.Truncate(IntelligenceInterval).Add(IntelligenceInterval)))
				}
			}
		}()
		go func() {
			defer s.wg.Done()
			timer := time.NewTimer(20 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-s.ctx.Done():
					return
				case <-timer.C:
					s.tickAccounts()
					timer.Reset(time.Until(time.Now().Truncate(IntelligenceInterval).Add(IntelligenceInterval)))
				}
			}
		}()
	})
}

func (s *ChannelMonitorIntelligence) Stop() {
	s.stopOnce.Do(func() { s.cancel(); s.wg.Wait(); s.client.CloseIdleConnections() })
}

func (s *ChannelMonitorIntelligence) tick() {
	if s.settings == nil || !s.settings.GetChannelMonitorRuntime(s.ctx).PassiveAggregationAllowed() {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Minute)
	defer cancel()
	cfg, err := s.v2.GetConfig(ctx)
	if err != nil || cfg == nil || !cfg.Enabled {
		return
	}
	enabled := false
	for _, p := range cfg.Platforms {
		if p.Platform == PlatformOpenAI && p.Enabled {
			enabled = true
		}
	}
	if !enabled {
		return
	}
	release, acquired := tryAcquireSingletonLeaderLock(ctx, nil, s.db, "channel-monitor-intelligence", s.owner, 3*time.Minute)
	if !acquired {
		return
	}
	if release != nil {
		defer release()
	}
	groups, err := s.repo.ListGroups(ctx, cfg.GroupIDs)
	if err != nil {
		slog.Warn("intelligence monitor: list groups failed")
		return
	}
	// Bounded concurrency; no overlap per group, including across replicas.
	sem := make(chan struct{}, intelligenceConcurrency)
	var wg sync.WaitGroup
	round := time.Now().UTC()
	for _, group := range groups {
		last, err := s.repo.Latest(ctx, group.ID)
		if err != nil {
			continue
		}
		if !intelligenceProbeDue(last, round) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func(g IntelligenceGroup) {
			defer wg.Done()
			defer func() { <-sem }()
			point, complete := s.probeGroupAccounts(ctx, g)
			if !complete {
				return // A partial sweep is not evidence that every account failed.
			}
			if s.ctx.Err() != nil {
				return
			} // shutdown is not a channel failure
			if strings.Contains(point.Detail, "_after_retry_") {
				slog.Info("intelligence monitor: retry completed", "group_id", g.ID, "status", point.Status, "detail", point.Detail, "latency_ms", point.LatencyMs)
			}
			// The HTTP timeout must not cancel persistence of a red result.
			saveCtx, done := context.WithTimeout(s.ctx, 5*time.Second)
			defer done()
			if err := s.repo.Save(saveCtx, g.ID, point); err != nil {
				slog.Warn("intelligence monitor: save failed", "group_id", g.ID)
			}
		}(group)
	}
	wg.Wait()
	if err := s.repo.Prune(ctx, time.Now().Add(-30*24*time.Hour)); err != nil && ctx.Err() == nil {
		slog.Warn("intelligence monitor: history cleanup failed")
	}
}

// A group is probed at most once per natural minute, including after a restart.
// Minute slots avoid skipping a cycle because of sub-second scheduling jitter.
func intelligenceProbeDue(last *IntelligencePoint, now time.Time) bool {
	return last == nil || last.CheckedAt.Truncate(IntelligenceInterval).Before(now.Truncate(IntelligenceInterval))
}

func (s *ChannelMonitorIntelligence) resolveKey(ctx context.Context, group IntelligenceGroup) (string, error) {
	ids, err := s.repo.KeyCandidates(ctx, group.ID)
	if err != nil {
		return "", err
	}
	for _, id := range ids {
		key, err := s.keys.apiKeyRepo.GetByID(ctx, id)
		if err != nil || key == nil || key.GroupID == nil || *key.GroupID != group.ID ||
			!key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() ||
			key.User == nil || key.User.Status != StatusActive || key.Group == nil || key.Group.Platform != PlatformOpenAI ||
			len(key.IPWhitelist) != 0 || len(key.IPBlacklist) != 0 {
			continue
		}
		user, err := s.keys.userRepo.GetByID(ctx, key.UserID)
		if err != nil || !s.keys.canUserBindGroup(ctx, user, key.Group) {
			continue
		}
		return key.Key, nil
	}
	// Creation uses the ordinary service validation. Never grant a group, balance
	// or subscription to an administrator just to make a probe succeed.
	admins, err := s.repo.AdminIDs(ctx)
	if err != nil {
		return "", err
	}
	for _, id := range admins {
		key, err := s.keys.Create(ctx, id, CreateAPIKeyRequest{Name: "勿删！" + group.Name + "uptime专用", GroupID: &group.ID})
		if err == nil {
			return key.Key, nil
		}
	}
	return "", errors.New("no authorized group credential")
}

func (s *ChannelMonitorIntelligence) probe(parent context.Context, key string) IntelligencePoint {
	return s.probeWithRetry(parent, key, intelligenceRetryPolicy{
		attempts: IntelligenceMaxAttempts, timeout: IntelligenceTimeout,
		budget: IntelligenceProbeBudget, backoff: time.Second,
	})
}

type intelligenceRetryPolicy struct {
	attempts                 int
	timeout, budget, backoff time.Duration
}

// Exactly one final point is persisted by tick. Red intermediate attempts must
// not poison the minute bucket when a later attempt recovers. Valid answers,
// including yellow mismatches, are final and never retried to chase a green.
func (s *ChannelMonitorIntelligence) probeWithRetry(parent context.Context, key string, policy intelligenceRetryPolicy) IntelligencePoint {
	return intelligenceRetry(parent, policy, func(ctx context.Context) (IntelligencePoint, time.Duration) {
		return s.probeOnce(ctx, key)
	})
}

func intelligenceRetry(parent context.Context, policy intelligenceRetryPolicy, probe func(context.Context) (IntelligencePoint, time.Duration)) IntelligencePoint {
	start := time.Now()
	ctx, cancel := context.WithTimeout(parent, policy.budget)
	defer cancel()
	point := IntelligencePoint{CheckedAt: start.UTC(), Status: "red", Detail: "timeout"}
	attempts := 0
	for attempts < policy.attempts && ctx.Err() == nil {
		attempts++
		attemptCtx, done := context.WithTimeout(ctx, policy.timeout)
		result, retryAfter := probe(attemptCtx)
		done()
		point.Status, point.Detail = result.Status, result.Detail
		if attempts == policy.attempts || ctx.Err() != nil || !intelligenceRetryable(result) {
			break
		}
		delay := policy.backoff * time.Duration(attempts)
		if retryAfter > delay {
			delay = retryAfter
		}
		// Respect Retry-After, but never sleep beyond the round budget.
		if deadline, ok := ctx.Deadline(); ok && delay >= time.Until(deadline) {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	if attempts > 1 {
		point.Detail += fmt.Sprintf("_after_retry_%d", attempts)
	}
	point.LatencyMs = int(time.Since(start).Milliseconds())
	return point
}

func intelligenceRetryable(point IntelligencePoint) bool {
	if point.Status != "red" {
		return false
	}
	switch point.Detail {
	case "network_error", "timeout", "timeout_or_read_error", "invalid_response", "response_error":
		return true
	}
	code, err := strconv.Atoi(strings.TrimPrefix(point.Detail, "http_"))
	return err == nil && (code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || (code >= 500 && code <= 599))
}

func intelligenceRetryAfter(header string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds > 0 {
		// Saturate before duration multiplication (untrusted upstream header).
		if seconds >= int64(IntelligenceProbeBudget/time.Second) {
			return IntelligenceProbeBudget
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(header); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

func (s *ChannelMonitorIntelligence) probeOnce(ctx context.Context, key string) (IntelligencePoint, time.Duration) {
	point := IntelligencePoint{Status: "red"}
	payload, _ := json.Marshal(map[string]any{
		"model": IntelligenceModel, "input": IntelligencePrompt,
		"reasoning": map[string]string{"effort": "medium"},
		"stream":    false, "store": false, "tools": []any{}, "tool_choice": "none",
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(payload))
	if err != nil {
		point.Detail = "request_error"
		return point, 0
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "sub2api-intelligence-uptime/1.0")
	resp, err := s.client.Do(req)
	if err != nil {
		point.Detail = "network_error"
		if ctx.Err() != nil {
			point.Detail = "timeout"
		}
		return point, 0
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if readErr != nil || ctx.Err() != nil {
		point.Detail = "timeout_or_read_error"
		return point, 0
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		point.Detail = fmt.Sprintf("http_%d", resp.StatusCode)
		return point, intelligenceRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	}
	if len(raw) > 1<<20 {
		point.Detail = "response_too_large"
		return point, 0
	}
	return classifyIntelligenceAnswer(raw), 0
}

func classifyIntelligenceAnswer(raw []byte) IntelligencePoint {
	point := IntelligencePoint{Status: "red"}
	var body struct {
		Error      json.RawMessage `json:"error"`
		Status     string          `json:"status"`
		OutputText string          `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &body) != nil {
		point.Detail = "invalid_response"
		return point
	}
	if (len(body.Error) > 0 && string(body.Error) != "null") || (body.Status != "" && body.Status != "completed") {
		point.Detail = "response_error"
		return point
	}
	text := body.OutputText
	if text == "" {
		for _, output := range body.Output {
			if output.Type != "message" || output.Role != "assistant" {
				continue
			}
			for _, content := range output.Content {
				if content.Type == "output_text" {
					text += content.Text
				}
			}
		}
	}
	point.Status = "yellow"
	point.Detail = "answer_mismatch"
	if strings.TrimSpace(text) == "21" {
		point.Status, point.Detail = "green", "answer_21"
	}
	return point
}

// The gateway owns OAuth refresh, upstream protocol mapping, proxy routing and
// plugin transport. This internal-only call pins the account without exposing a
// client-controlled account selection header or charging an unrelated API key.
func (s *ChannelMonitorIntelligence) probeAccountOnce(ctx context.Context, account *Account) (IntelligencePoint, time.Duration) {
	return s.probeAccountWithKeyOnce(ctx, account, nil)
}

func (s *ChannelMonitorIntelligence) probeAccountWithKeyOnce(ctx context.Context, account *Account, key *APIKey) (IntelligencePoint, time.Duration) {
	point := IntelligencePoint{Status: "red", Detail: "response_error"}
	if s.accountTest == nil || s.accountTest.openaiGatewayService == nil {
		point.Detail = "request_error"
		return point, 0
	}
	payload, _ := json.Marshal(map[string]any{
		"model": IntelligenceModel, "input": IntelligencePrompt,
		"reasoning": map[string]string{"effort": "medium"},
		"stream":    false, "store": false, "tools": []any{}, "tool_choice": "none",
	})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(payload)).WithContext(ctx)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "sub2api-intelligence-uptime/1.0")
	if key != nil {
		c.Set("api_key", key)
		c.Request.Header.Set("Authorization", "Bearer "+key.Key)
	}
	_, err := s.accountTest.openaiGatewayService.Forward(ctx, c, account, payload)
	if ctx.Err() != nil {
		point.Detail = "timeout"
		return point, 0
	}
	if err != nil || recorder.Code < 200 || recorder.Code >= 300 {
		point.Detail = fmt.Sprintf("http_%d", recorder.Code)
		if recorder.Code == http.StatusOK {
			point.Detail = "response_error"
		}
		return point, intelligenceRetryAfter(recorder.Header().Get("Retry-After"), time.Now())
	}
	if recorder.Body.Len() > 1<<20 {
		point.Detail = "response_too_large"
		return point, 0
	}
	return classifyIntelligenceAnswer(recorder.Body.Bytes()), 0
}

func (s *ChannelMonitorIntelligence) tickAccounts() {
	if s.db == nil || s.accountTest == nil || s.settings == nil ||
		!s.settings.GetChannelMonitorRuntime(s.ctx).PassiveAggregationAllowed() {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, IntelligenceProbeBudget+10*time.Second)
	defer cancel()
	release, acquired := tryAcquireSingletonLeaderLock(ctx, nil, s.db, "channel-monitor-account-intelligence", s.owner, 2*time.Minute)
	if !acquired {
		return
	}
	if release != nil {
		defer release()
	}
	// Oldest due account first. Limit one round to its actual concurrency budget
	// so slow requests do not spill into the next cycle.
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, array_agg(g.id ORDER BY g.id), MAX(h.checked_at) AS last_check
		FROM accounts a JOIN account_groups ag ON ag.account_id=a.id
		JOIN groups g ON g.id=ag.group_id AND g.deleted_at IS NULL AND g.platform='openai'
			AND g.status='active' AND g.account_intelligence_enabled=TRUE
		LEFT JOIN LATERAL (SELECT MAX(checked_at) checked_at FROM channel_monitor_account_intelligence_history
			WHERE account_id=a.id AND group_id=g.id) h ON TRUE
		WHERE a.deleted_at IS NULL AND a.platform='openai' AND a.status='active'
		GROUP BY a.id HAVING MAX(h.checked_at) IS NULL OR MAX(h.checked_at)<$1
		ORDER BY MAX(h.checked_at) NULLS FIRST, a.id
		LIMIT $2`, time.Now().UTC().Truncate(IntelligenceInterval), accountIntelligenceConcurrency)
	if err != nil {
		slog.Warn("account intelligence: select failed", "error", err)
		return
	}
	type target struct {
		id     int64
		groups []int64
	}
	var targets []target
	for rows.Next() {
		var target target
		var last sql.NullTime
		if err := rows.Scan(&target.id, pq.Array(&target.groups), &last); err != nil {
			slog.Warn("account intelligence: scan failed", "error", err)
			rows.Close()
			return
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		slog.Warn("account intelligence: rows failed", "error", err)
		return
	}
	rows.Close()
	var wg sync.WaitGroup
	for _, item := range targets {
		wg.Add(1)
		go func(target target) {
			defer wg.Done()
			account, err := s.accountTest.accountRepo.GetByID(ctx, target.id)
			if err != nil || account == nil || account.Platform != PlatformOpenAI || !account.IsActive() {
				return
			}
			point := intelligenceRetry(ctx, intelligenceRetryPolicy{attempts: IntelligenceMaxAttempts, timeout: IntelligenceTimeout,
				budget: IntelligenceProbeBudget, backoff: time.Second}, func(attempt context.Context) (IntelligencePoint, time.Duration) {
				return s.probeAccountOnce(attempt, account)
			})
			if s.ctx.Err() != nil {
				return
			}
			saveCtx, done := context.WithTimeout(s.ctx, 5*time.Second)
			defer done()
			for _, groupID := range target.groups {
				_, err := s.db.ExecContext(saveCtx, `INSERT INTO channel_monitor_account_intelligence_history(account_id,group_id,checked_at,status,latency_ms,detail)
					SELECT $1,g.id,$3,$4,$5,$6 FROM groups g JOIN account_groups ag ON ag.group_id=g.id AND ag.account_id=$1
					JOIN accounts a ON a.id=$1
					WHERE g.id=$2 AND g.deleted_at IS NULL AND g.status='active' AND g.platform='openai' AND g.account_intelligence_enabled=TRUE
						AND a.deleted_at IS NULL AND a.status='active' AND a.platform='openai'
					ON CONFLICT DO NOTHING`, target.id, groupID, point.CheckedAt, point.Status, point.LatencyMs, point.Detail)
				if err != nil {
					slog.Warn("account intelligence: save failed", "account_id", target.id, "error", err)
				}
			}
		}(item)
	}
	wg.Wait()
	_, _ = s.db.ExecContext(ctx, `DELETE FROM channel_monitor_account_intelligence_history WHERE checked_at<$1`, time.Now().Add(-30*24*time.Hour))
}

type AccountIntelligenceView struct {
	AccountID int64              `json:"account_id"`
	GroupID   int64              `json:"group_id"`
	GroupName string             `json:"group_name"`
	Uptime    IntelligenceUptime `json:"uptime"`
}

func (s *ChannelMonitorIntelligence) AccountHistory(ctx context.Context, ids []int64) ([]AccountIntelligenceView, error) {
	end := time.Now().UTC().Truncate(IntelligenceInterval).Add(IntelligenceInterval)
	start := end.Add(-IntelligenceSlots * IntelligenceInterval)
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,g.id,g.name,h.checked_at,h.status,h.latency_ms
		FROM accounts a JOIN account_groups ag ON ag.account_id=a.id
		JOIN groups g ON g.id=ag.group_id AND g.deleted_at IS NULL AND g.platform='openai'
			AND g.status='active' AND g.account_intelligence_enabled=TRUE
		LEFT JOIN channel_monitor_account_intelligence_history h ON h.account_id=a.id AND h.group_id=g.id
			AND h.checked_at>=$2 AND h.checked_at<$3
		WHERE a.id=ANY($1) AND a.deleted_at IS NULL AND a.platform='openai' AND a.status='active'
		ORDER BY a.id,g.id,h.checked_at`, pq.Array(ids), start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountIntelligenceView{}
	for rows.Next() {
		var id, groupID int64
		var name string
		var checked sql.NullTime
		var status sql.NullString
		var latency sql.NullInt64
		if err := rows.Scan(&id, &groupID, &name, &checked, &status, &latency); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].AccountID != id || out[len(out)-1].GroupID != groupID {
			out = append(out, AccountIntelligenceView{AccountID: id, GroupID: groupID, GroupName: name, Uptime: IntelligenceUptime{
				Model: IntelligenceModel, ReasoningEffort: "medium", IntervalSeconds: 60, TimeoutSeconds: 30,
				RetryMaxAttempts: IntelligenceMaxAttempts, RetryBudgetSeconds: 55, Status: "unknown",
				Points: []IntelligencePoint{}, WindowStart: start, BucketSeconds: 60, WindowPoints: IntelligenceSlots,
			}})
		}
		if checked.Valid {
			view := &out[len(out)-1].Uptime
			point := IntelligencePoint{CheckedAt: checked.Time, Status: status.String, LatencyMs: int(latency.Int64)}
			view.CheckedAt = &point.CheckedAt
			view.Status = point.Status
			if point.Status == "green" || point.Status == "yellow" {
				view.Points = append(view.Points, point)
			}
		}
	}
	return out, rows.Err()
}

// Attach uses only already-authorized matrix rows. No key, raw answer or
// upstream error is exposed. A stale latest success never remains green.
func (s *ChannelMonitorIntelligence) Attach(ctx context.Context, matrix *ChannelMonitorV2Matrix, _ ChannelMonitorV2Filter) error {
	if matrix == nil {
		return nil
	}
	var ids []int64
	for _, row := range matrix.Items {
		if row.Platform == PlatformOpenAI && row.GroupID != nil {
			ids = append(ids, *row.GroupID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	// This is a live probe timeline, independent of the passive monitor range.
	// One bar is one minute; never blend it into the V2 five-minute/hour buckets.
	end := time.Now().UTC().Truncate(IntelligenceInterval).Add(IntelligenceInterval)
	start := end.Add(-IntelligenceSlots * IntelligenceInterval)
	points, err := s.repo.History(ctx, ids, start, end, IntelligenceInterval)
	if err != nil {
		return err
	}
	latest := make(map[int64]*IntelligencePoint)
	for _, id := range ids {
		if _, ok := latest[id]; ok {
			continue
		}
		last, err := s.repo.Latest(ctx, id)
		if err != nil {
			return err
		}
		latest[id] = last
	}
	for i := range matrix.Items {
		row := &matrix.Items[i]
		if row.Platform != PlatformOpenAI || row.GroupID == nil {
			continue
		}
		history := points[*row.GroupID]
		if history == nil {
			history = []IntelligencePoint{}
		}
		view := &IntelligenceUptime{
			Model: IntelligenceModel, ReasoningEffort: "medium", IntervalSeconds: int(IntelligenceInterval.Seconds()),
			TimeoutSeconds: int(IntelligenceTimeout.Seconds()), Status: "unknown", Points: history,
			RetryMaxAttempts: 1, RetryBudgetSeconds: int(IntelligenceProbeBudget.Seconds()), // one attempt per account, then switch
			WindowStart: start, BucketSeconds: int(IntelligenceInterval.Seconds()), WindowPoints: IntelligenceSlots,
		}
		if last := latest[*row.GroupID]; last != nil {
			view.CheckedAt = &last.CheckedAt
			view.Status = last.Status
			if time.Since(last.CheckedAt) > 3*IntelligenceInterval {
				view.Status = "stale"
			}
		}
		row.Intelligence = view
	}
	return nil
}
