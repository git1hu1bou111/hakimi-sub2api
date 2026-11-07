package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

func grokSafetyBillingDefaultsDTO(settings service.GrokSafetyRejectionBillingSettings) *dto.GrokSafetyBillingDefaults {
	return &dto.GrokSafetyBillingDefaults{
		Enabled:           settings.Enabled,
		AmountUSD:         settings.AmountUSD,
		CacheReadRatioMin: settings.CacheReadRatioMin,
		CacheReadRatioMax: settings.CacheReadRatioMax,
		OutputTokensMin:   settings.OutputTokensMin,
		OutputTokensMax:   settings.OutputTokensMax,
		MaxInputTokens:    settings.MaxInputTokens,
	}
}

func grokSafetyBillingDTO(settings service.GrokSafetyRejectionBillingSettings, override bool, defaults service.GrokSafetyRejectionBillingSettings) dto.GrokSafetyBillingSettings {
	return dto.GrokSafetyBillingSettings{
		Enabled:           settings.Enabled,
		AmountUSD:         settings.AmountUSD,
		CacheReadRatioMin: settings.CacheReadRatioMin,
		CacheReadRatioMax: settings.CacheReadRatioMax,
		OutputTokensMin:   settings.OutputTokensMin,
		OutputTokensMax:   settings.OutputTokensMax,
		MaxInputTokens:    settings.MaxInputTokens,
		Override:          override,
		Defaults:          grokSafetyBillingDefaultsDTO(defaults),
	}
}

// GetGrokSafetyBillingSettings 获取 Grok 安全审查拒绝计费配置
// GET /api/v1/admin/settings/grok-safety-billing
func (h *SettingHandler) GetGrokSafetyBillingSettings(c *gin.Context) {
	settings, override := h.settingService.GetGrokSafetyRejectionBillingState(c.Request.Context())
	response.Success(c, grokSafetyBillingDTO(settings, override, h.settingService.GrokSafetyRejectionBillingDefaults()))
}

// UpdateGrokSafetyBillingSettings 保存 Grok 安全审查拒绝计费配置
// PUT /api/v1/admin/settings/grok-safety-billing
func (h *SettingHandler) UpdateGrokSafetyBillingSettings(c *gin.Context) {
	var req dto.GrokSafetyBillingSettingsUpdate
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	saved, err := h.settingService.SetGrokSafetyRejectionBillingSettings(c.Request.Context(), service.GrokSafetyRejectionBillingSettings{
		Enabled:           req.Enabled,
		AmountUSD:         req.AmountUSD,
		CacheReadRatioMin: req.CacheReadRatioMin,
		CacheReadRatioMax: req.CacheReadRatioMax,
		OutputTokensMin:   req.OutputTokensMin,
		OutputTokensMax:   req.OutputTokensMax,
		MaxInputTokens:    req.MaxInputTokens,
	})
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, grokSafetyBillingDTO(saved, true, h.settingService.GrokSafetyRejectionBillingDefaults()))
}

// ResetGrokSafetyBillingSettings 删除数据库覆盖值，回退到 config.yaml 默认
// DELETE /api/v1/admin/settings/grok-safety-billing
func (h *SettingHandler) ResetGrokSafetyBillingSettings(c *gin.Context) {
	settings, err := h.settingService.ResetGrokSafetyRejectionBillingSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, grokSafetyBillingDTO(settings, false, settings))
}
