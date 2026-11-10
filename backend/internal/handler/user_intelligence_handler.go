package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"strconv"
)

func (h *APIKeyHandler) intelligenceIdentity(c *gin.Context) (int64, int64, string, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "请先登录")
		return 0, 0, "", false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "无效密钥")
		return 0, 0, "", false
	}
	clientIP := ip.GetSecurityClientIP(c, h.config.TrustForwardedIPForAPIKeyACL())
	c.Header("Cache-Control", "no-store")
	return subject.UserID, id, clientIP, true
}
func (h *APIKeyHandler) IntelligenceOptions(c *gin.Context) {
	user, key, clientIP, ok := h.intelligenceIdentity(c)
	if !ok {
		return
	}
	result, err := h.intelligence.Options(c.Request.Context(), user, key, clientIP)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *APIKeyHandler) StartIntelligence(c *gin.Context) {
	user, key, clientIP, ok := h.intelligenceIdentity(c)
	if !ok {
		return
	}
	var req struct {
		Model string `json:"model" binding:"required,max=100"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "请选择测试模型")
		return
	}
	result, err := h.intelligence.Start(c.Request.Context(), user, key, clientIP, req.Model)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *APIKeyHandler) GetIntelligence(c *gin.Context) {
	user, key, _, ok := h.intelligenceIdentity(c)
	if !ok {
		return
	}
	result, err := h.intelligence.Get(c.Request.Context(), user, key, c.Param("runID"))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *APIKeyHandler) IntelligenceHistory(c *gin.Context) {
	user, key, _, ok := h.intelligenceIdentity(c)
	if !ok {
		return
	}
	result, err := h.intelligence.History(c.Request.Context(), user, key)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
