//go:build unit

package middleware

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type panelMiddlewareCache struct {
	service.RefreshTokenCache
	data map[string]*service.RefreshTokenData
}

func (c *panelMiddlewareCache) GetRefreshToken(_ context.Context, key string) (*service.RefreshTokenData, error) {
	if d := c.data[key]; d != nil {
		return d, nil
	}
	return nil, service.ErrRefreshTokenNotFound
}

func TestJWTPanelCompatReadOnlyAndUserChecks(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := "rt_panel-compat-fixture"
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	user := &service.User{ID: 145, Role: "user", Status: service.StatusActive, TokenVersion: 7, TokenVersionResolved: true}
	repo := &stubJWTUserRepo{users: map[int64]*service.User{145: user}}
	data := &service.RefreshTokenData{UserID: 145, TokenVersion: 7, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(15 * 24 * time.Hour)}
	cache := &panelMiddlewareCache{data: map[string]*service.RefreshTokenData{key: data}}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "middleware-test-secret", ExpireHour: 24, PanelCompatUserIDs: []int64{145}}}
	s := service.NewAuthService(nil, repo, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	r := gin.New()
	r.Use(jwtAuth(s, repo, nil, nil, nil))
	for _, path := range []string{"/api/v1/auth/me", "/api/v1/usage/stats", "/api/v1/keys", "/api/v1/admin/users"} {
		r.GET(path, func(c *gin.Context) { c.Status(200) })
	}
	r.PUT("/api/v1/user/password", func(c *gin.Context) { c.Status(200) })
	request := func(method, path, token string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, 200, request("GET", "/api/v1/auth/me", raw))
	require.Equal(t, 200, request("GET", "/api/v1/usage/stats", raw))
	for _, path := range []string{"/api/v1/keys", "/api/v1/admin/users"} {
		require.Equal(t, 401, request("GET", path, raw))
	}
	require.Equal(t, 401, request("PUT", "/api/v1/user/password", raw))
	user.TokenVersion++
	require.Equal(t, 401, request("GET", "/api/v1/auth/me", raw))
	user.TokenVersion--
	user.Status = "disabled"
	require.Equal(t, 401, request("GET", "/api/v1/auth/me", raw))
	user.Status = service.StatusActive
	data.UserID = 146
	require.Equal(t, 401, request("GET", "/api/v1/auth/me", raw))
	data.UserID = 145
	data.CreatedAt = time.Now().Add(-16 * 24 * time.Hour)
	require.Equal(t, 401, request("GET", "/api/v1/auth/me", raw))
	data.CreatedAt = time.Now()
	cfg.JWT.PanelCompatUserIDs = nil
	require.Equal(t, 401, request("GET", "/api/v1/auth/me", raw))
	access, err := s.GenerateToken(context.Background(), user)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, request("GET", "/api/v1/auth/me", access))
}
