package service

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/lib/pq"
)

// 账号实时指标窗口。
// 缓存率与首字用近 10 分钟的滚动窗口（反映“当下”），报错率用近 1 小时
// （10 分钟内绝大多数健康账号没有任何错误，样本不足以区分 0.1% 与 0.5%）。
const (
	AccountLiveMetricsWindow = 10 * time.Minute
	AccountLiveErrorWindow   = time.Hour
)

// AccountLiveMetrics 是账号维度的实时快照，直接读使用记录计算，不落库。
// 所有请求都没有流量时各字段为零值，前端据此显示“未使用”。
type AccountLiveMetrics struct {
	AccountID       int64   `json:"account_id"`
	Requests10m     int64   `json:"requests_10m"`
	PromptTokens10m int64   `json:"prompt_tokens_10m"`
	CacheRead10m    int64   `json:"cache_read_10m"`
	CacheHitPct     float64 `json:"cache_hit_pct"`
	TtftP50Ms       int64   `json:"ttft_p50_ms"`
	TtftSamples     int64   `json:"ttft_samples"`
	Requests1h      int64   `json:"requests_1h"`
	ErrorRequests1h int64   `json:"error_requests_1h"`
	ErrorRatePct    float64 `json:"error_rate_pct"`
	ComputedAt      string  `json:"computed_at"`
}

// AccountLiveMetrics 读取一批账号的实时快照。
//
// 口径说明：
//   - 缓存率 = cache_read / (input + cache_creation + cache_read)，分母为 0 时记 0；
//   - 首字取中位数（长尾会把均值拉偏），样本为有 first_token_ms 的请求数；
//   - 报错率按 request_id 去重后再除以同窗口请求数，避免同一请求多次重试被重复计数；
//   - 生图/视频类账号天然没有缓存命中与首字，样本为 0，不代表异常。
func (s *ChannelMonitorIntelligence) AccountLiveMetrics(ctx context.Context, accountIDs []int64) ([]AccountLiveMetrics, error) {
	if s == nil || s.db == nil || len(accountIDs) == 0 {
		return []AccountLiveMetrics{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	now := time.Now().UTC()
	startWindow := now.Add(-AccountLiveMetricsWindow)
	startError := now.Add(-AccountLiveErrorWindow)

	result := make([]AccountLiveMetrics, 0, len(accountIDs))
	index := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		index[id] = len(result)
		result = append(result, AccountLiveMetrics{AccountID: id, ComputedAt: now.Format(time.RFC3339)})
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT account_id,
		       COUNT(*) FILTER (WHERE created_at >= $2) AS requests_10m,
		       COALESCE(SUM(input_tokens + cache_creation_tokens + cache_read_tokens) FILTER (WHERE created_at >= $2), 0) AS prompt_tokens_10m,
		       COALESCE(SUM(cache_read_tokens) FILTER (WHERE created_at >= $2), 0) AS cache_read_10m,
		       COUNT(first_token_ms) FILTER (WHERE created_at >= $2) AS ttft_samples,
		       COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY first_token_ms) FILTER (WHERE created_at >= $2 AND first_token_ms IS NOT NULL), 0) AS ttft_p50
		FROM usage_logs
		WHERE account_id = ANY($1) AND created_at >= $3
		GROUP BY account_id`, pq.Array(accountIDs), startWindow, startError)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, requests10m, promptTokens, cacheRead, ttftSamples int64
		var ttftP50 float64
		if err := rows.Scan(&id, &requests10m, &promptTokens, &cacheRead, &ttftSamples, &ttftP50); err != nil {
			return nil, err
		}
		item := &result[index[id]]
		item.Requests10m = requests10m
		item.PromptTokens10m = promptTokens
		item.CacheRead10m = cacheRead
		item.TtftSamples = ttftSamples
		item.TtftP50Ms = int64(ttftP50 + 0.5)
		if promptTokens > 0 {
			item.CacheHitPct = accountLiveRound1(float64(cacheRead) / float64(promptTokens) * 100)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 报错率的分母：同窗口（近 1 小时）的请求数。
	durationRows, err := s.db.QueryContext(ctx, `
		SELECT account_id, COUNT(*) AS requests_1h
		FROM usage_logs
		WHERE account_id = ANY($1) AND created_at >= $2
		GROUP BY account_id`, pq.Array(accountIDs), startError)
	if err != nil {
		return nil, err
	}
	defer durationRows.Close()
	for durationRows.Next() {
		var id, requests1h int64
		if err := durationRows.Scan(&id, &requests1h); err != nil {
			return nil, err
		}
		result[index[id]].Requests1h = requests1h
	}
	if err := durationRows.Err(); err != nil {
		return nil, err
	}

	// 报错数：异常日志按账号去重统计。查询失败只降级（报错率留空），不影响其余指标。
	errorRows, err := s.db.QueryContext(ctx, `
		SELECT account_id, COUNT(DISTINCT request_id) AS error_requests
		FROM ops_error_logs
		WHERE account_id = ANY($1) AND created_at >= $2 AND request_id IS NOT NULL
		GROUP BY account_id`, pq.Array(accountIDs), startError)
	if err != nil {
		slog.Warn("account live metrics: error aggregation failed", "error", err)
	} else {
		defer errorRows.Close()
		for errorRows.Next() {
			var id, errorRequests int64
			if err := errorRows.Scan(&id, &errorRequests); err != nil {
				slog.Warn("account live metrics: scan failed", "error", err)
				break
			}
			item := &result[index[id]]
			item.ErrorRequests1h = errorRequests
			if item.Requests1h > 0 {
				item.ErrorRatePct = accountLiveRound1(float64(errorRequests) / float64(item.Requests1h) * 100)
			}
		}
	}

	return result, nil
}

func accountLiveRound1(value float64) float64 {
	return math.Round(value*10) / 10
}
