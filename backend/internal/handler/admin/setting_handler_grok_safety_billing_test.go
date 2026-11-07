package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// grokSafetyBillingRepoStub 支持读写的设置仓储桩（handler 层用例）。
type grokSafetyBillingRepoStub struct {
	values map[string]string
	setErr error
}

func (s *grokSafetyBillingRepoStub) Get(context.Context, string) (*service.Setting, error) {
	panic("unexpected Get call")
}

func (s *grokSafetyBillingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := s.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func (s *grokSafetyBillingRepoStub) Set(_ context.Context, key, value string) error {
	if s.setErr != nil {
		return s.setErr
	}
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func (s *grokSafetyBillingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}

func (s *grokSafetyBillingRepoStub) SetMultiple(context.Context, map[string]string) error { return nil }

func (s *grokSafetyBillingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}

func (s *grokSafetyBillingRepoStub) Delete(_ context.Context, key string) error {
	delete(s.values, key)
	return nil
}

func newGrokSafetyBillingHandler(t *testing.T) (*SettingHandler, *grokSafetyBillingRepoStub) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	repo := &grokSafetyBillingRepoStub{values: map[string]string{}}
	cfg := &config.Config{}
	cfg.Gateway.Grok.SafetyRejectionBilling = config.GatewayGrokSafetyRejectionBillingConfig{
		Enabled:           false,
		AmountUSD:         0.05,
		CacheReadRatioMin: 0.90,
		CacheReadRatioMax: 0.96,
		OutputTokensMin:   32,
		OutputTokensMax:   256,
		MaxInputTokens:    500000,
	}
	svc := service.NewSettingService(repo, cfg)
	return &SettingHandler{settingService: svc}, repo
}

func doGrokSafetyBillingRequest(t *testing.T, h *SettingHandler, method string, body any, handler func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, "/api/v1/admin/settings/grok-safety-billing", reader)
	c.Request.Header.Set("Content-Type", "application/json")
	handler(c)
	return rec
}

// 未保存过时返回 config.yaml 默认值，override=false。
func TestGrokSafetyBillingHandler_GetDefaults(t *testing.T) {
	h, _ := newGrokSafetyBillingHandler(t)
	rec := doGrokSafetyBillingRequest(t, h, http.MethodGet, nil, h.GetGrokSafetyBillingSettings)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Enabled           bool    `json:"enabled"`
			AmountUSD         float64 `json:"amount_usd"`
			CacheReadRatioMin float64 `json:"cache_read_ratio_min"`
			MaxInputTokens    int     `json:"max_input_tokens"`
			Override          bool    `json:"override"`
			Defaults          *struct {
				AmountUSD float64 `json:"amount_usd"`
			} `json:"defaults"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.False(t, payload.Data.Enabled)
	require.Equal(t, 0.05, payload.Data.AmountUSD)
	require.Equal(t, 0.90, payload.Data.CacheReadRatioMin)
	require.Equal(t, 500000, payload.Data.MaxInputTokens)
	require.False(t, payload.Data.Override)
	require.NotNil(t, payload.Data.Defaults)
}

// 保存后 override=true，且再读返回保存值。
func TestGrokSafetyBillingHandler_UpdateThenGet(t *testing.T) {
	h, repo := newGrokSafetyBillingHandler(t)
	rec := doGrokSafetyBillingRequest(t, h, http.MethodPut, map[string]any{
		"enabled":              true,
		"amount_usd":           0.2,
		"cache_read_ratio_min": 0.92,
		"cache_read_ratio_max": 0.99,
		"output_tokens_min":    10,
		"output_tokens_max":    120,
		"max_input_tokens":     200000,
	}, h.UpdateGrokSafetyBillingSettings)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, repo.values[service.SettingKeyGrokSafetyRejectionBilling], `"amount_usd":0.2`)

	var payload struct {
		Data struct {
			Enabled  bool    `json:"enabled"`
			Amount   float64 `json:"amount_usd"`
			Override bool    `json:"override"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.True(t, payload.Data.Enabled)
	require.Equal(t, 0.2, payload.Data.Amount)
	require.True(t, payload.Data.Override)
}

// 非法参数返回 400，且不落库。
func TestGrokSafetyBillingHandler_UpdateRejectsInvalid(t *testing.T) {
	h, repo := newGrokSafetyBillingHandler(t)
	rec := doGrokSafetyBillingRequest(t, h, http.MethodPut, map[string]any{
		"enabled":              true,
		"amount_usd":           0.05,
		"cache_read_ratio_min": 0.5,
		"cache_read_ratio_max": 0.96,
	}, h.UpdateGrokSafetyBillingSettings)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Empty(t, repo.values)
}

// 重置删除覆盖值，回到默认。
func TestGrokSafetyBillingHandler_Reset(t *testing.T) {
	h, repo := newGrokSafetyBillingHandler(t)
	repo.values[service.SettingKeyGrokSafetyRejectionBilling] = `{"enabled":true,"amount_usd":0.9,"cache_read_ratio_min":0.9,"cache_read_ratio_max":0.96,"output_tokens_min":32,"output_tokens_max":256,"max_input_tokens":500000}`

	rec := doGrokSafetyBillingRequest(t, h, http.MethodDelete, nil, h.ResetGrokSafetyBillingSettings)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, repo.values, service.SettingKeyGrokSafetyRejectionBilling)

	var payload struct {
		Data struct {
			Amount   float64 `json:"amount_usd"`
			Override bool    `json:"override"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, 0.05, payload.Data.Amount)
	require.False(t, payload.Data.Override)
}
