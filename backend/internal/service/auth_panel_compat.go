package service

import (
	"context"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const panelCompatLifetime = 15 * 24 * time.Hour

func (s *AuthService) panelCompatEnabled(userID int64) bool {
	if s.cfg == nil || userID <= 0 {
		return false
	}
	for _, id := range s.cfg.JWT.PanelCompatUserIDs {
		if id == userID {
			return true
		}
	}
	return false
}

func panelRefreshDeadline(data *RefreshTokenData) time.Time {
	if data == nil || data.CreatedAt.IsZero() || data.ExpiresAt.IsZero() {
		return time.Time{}
	}
	deadline := data.CreatedAt.Add(panelCompatLifetime)
	if data.ExpiresAt.Before(deadline) {
		deadline = data.ExpiresAt
	}
	return deadline
}

func (s *AuthService) refreshPanelTokenPair(user *User, data *RefreshTokenData, rawToken string) (*TokenPairWithUser, error) {
	deadline := panelRefreshDeadline(data)
	expiresIn := s.GetAccessTokenExpiresIn()
	if remaining := int(time.Until(deadline).Seconds()); remaining < expiresIn {
		expiresIn = remaining
	}
	if expiresIn <= 0 {
		return nil, ErrRefreshTokenExpired
	}
	accessToken, err := s.generateAccessTokenWithDeadline(user, data.FamilyID, data.BindingHash, deadline)
	if err != nil {
		return nil, err
	}
	return &TokenPairWithUser{
		TokenPair: TokenPair{AccessToken: accessToken, RefreshToken: rawToken, ExpiresIn: expiresIn},
		UserRole:  user.Role,
	}, nil
}

// ValidatePanelRefreshToken supports legacy dashboards that send the RT as
// Bearer on explicitly permitted read endpoints. The caller MUST still perform
// the normal current-user, password-version, and session-binding checks.
// It neither consumes the RT nor extends its original 15-day deadline.
func (s *AuthService) ValidatePanelRefreshToken(ctx context.Context, rawToken string) (*JWTClaims, error) {
	if s.refreshTokenCache == nil || len(rawToken) > 4096 || !strings.HasPrefix(rawToken, refreshTokenPrefix) {
		return nil, ErrRefreshTokenInvalid
	}
	data, err := s.refreshTokenCache.GetRefreshToken(ctx, hashToken(rawToken))
	if err != nil || data == nil || !s.panelCompatEnabled(data.UserID) {
		return nil, ErrRefreshTokenInvalid
	}
	deadline := panelRefreshDeadline(data)
	if !deadline.After(time.Now()) {
		return nil, ErrTokenExpired
	}
	return &JWTClaims{
		UserID: data.UserID, TokenVersion: data.TokenVersion,
		SessionID: data.FamilyID, BindingHash: data.BindingHash,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(data.CreatedAt),
			ExpiresAt: jwt.NewNumericDate(deadline),
		},
	}, nil
}
