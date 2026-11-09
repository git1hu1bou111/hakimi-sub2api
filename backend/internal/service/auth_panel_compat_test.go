//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type panelTestUsers struct {
	UserRepository
	users map[int64]*User
}

func (r *panelTestUsers) GetByID(_ context.Context, id int64) (*User, error) {
	if u := r.users[id]; u != nil {
		return u, nil
	}
	return nil, ErrUserNotFound
}

type panelTestCache struct {
	refreshTokenCacheStub
	mu   sync.Mutex
	data map[string]*RefreshTokenData
	ttls map[string]time.Duration
}

func (c *panelTestCache) StoreRefreshToken(_ context.Context, key string, d *RefreshTokenData, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := *d
	c.data[key] = &copy
	c.ttls[key] = ttl
	return nil
}
func (c *panelTestCache) GetRefreshToken(_ context.Context, key string) (*RefreshTokenData, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := c.data[key]; d != nil {
		copy := *d
		return &copy, nil
	}
	return nil, ErrRefreshTokenNotFound
}
func (c *panelTestCache) DeleteRefreshToken(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
	return nil
}
func (c *panelTestCache) DeleteTokenFamily(_ context.Context, family string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, d := range c.data {
		if d.FamilyID == family {
			delete(c.data, k)
		}
	}
	return nil
}

func newPanelAuthTest() (*AuthService, *panelTestCache, *User, *User) {
	users := &panelTestUsers{users: map[int64]*User{}}
	for _, id := range []int64{145, 146} {
		users.users[id] = &User{ID: id, Role: "user", Status: StatusActive, TokenVersion: 7, TokenVersionResolved: true}
	}
	cache := &panelTestCache{data: map[string]*RefreshTokenData{}, ttls: map[string]time.Duration{}}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "panel-compat-test-secret-32bytes", ExpireHour: 24, RefreshTokenExpireDays: 30, PanelCompatUserIDs: []int64{145}}}
	return NewAuthService(nil, users, nil, cache, cfg, nil, nil, nil, nil, nil, nil, nil, nil), cache, users.users[145], users.users[146]
}

func TestPanelCompatStableRefreshAndNormalRotation(t *testing.T) {
	s, c, u, other := newPanelAuthTest()
	ctx := context.Background()
	p, err := s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	d, err := c.GetRefreshToken(ctx, hashToken(p.RefreshToken))
	require.NoError(t, err)
	require.Equal(t, 15*24*time.Hour, d.ExpiresAt.Sub(d.CreatedAt))
	for i := 0; i < 3; i++ {
		next, err := s.RefreshTokenPair(ctx, p.RefreshToken)
		require.NoError(t, err)
		require.Equal(t, p.RefreshToken, next.RefreshToken)
		claims, err := s.ValidateToken(next.AccessToken)
		require.NoError(t, err)
		require.Equal(t, u.ID, claims.UserID)
	}
	after, _ := c.GetRefreshToken(ctx, hashToken(p.RefreshToken))
	require.Equal(t, d.ExpiresAt, after.ExpiresAt)
	ordinary, err := s.GenerateTokenPair(ctx, other, "")
	require.NoError(t, err)
	od, _ := c.GetRefreshToken(ctx, hashToken(ordinary.RefreshToken))
	require.Equal(t, 30*24*time.Hour, od.ExpiresAt.Sub(od.CreatedAt))
	_, err = s.ValidatePanelRefreshToken(ctx, ordinary.RefreshToken)
	require.Error(t, err)
	next, err := s.RefreshTokenPair(ctx, ordinary.RefreshToken)
	require.NoError(t, err)
	require.NotEqual(t, ordinary.RefreshToken, next.RefreshToken)
	_, err = s.RefreshTokenPair(ctx, ordinary.RefreshToken)
	require.Error(t, err)
}

func TestPanelCompatFixedDeadlineAndAccessClamp(t *testing.T) {
	s, c, u, _ := newPanelAuthTest()
	ctx := context.Background()
	p, err := s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	k := hashToken(p.RefreshToken)
	d, _ := c.GetRefreshToken(ctx, k)
	d.CreatedAt = time.Now().Add(-15*24*time.Hour + 90*time.Second)
	d.ExpiresAt = time.Now().Add(20 * 24 * time.Hour)
	require.NoError(t, c.StoreRefreshToken(ctx, k, d, 20*24*time.Hour))
	next, err := s.RefreshTokenPair(ctx, p.RefreshToken)
	require.NoError(t, err)
	require.LessOrEqual(t, next.ExpiresIn, 90)
	claims, err := s.ValidateToken(next.AccessToken)
	require.NoError(t, err)
	require.False(t, claims.ExpiresAt.Time.After(d.CreatedAt.Add(panelCompatLifetime)))
	d.CreatedAt = time.Now().Add(-16 * 24 * time.Hour)
	require.NoError(t, c.StoreRefreshToken(ctx, k, d, 20*24*time.Hour))
	_, err = s.RefreshTokenPair(ctx, p.RefreshToken)
	require.ErrorIs(t, err, ErrRefreshTokenExpired)
	_, err = s.ValidatePanelRefreshToken(ctx, p.RefreshToken)
	require.ErrorIs(t, err, ErrTokenExpired)
}

func TestPanelCompatRevocationAndDisabledAccount(t *testing.T) {
	s, _, u, _ := newPanelAuthTest()
	ctx := context.Background()
	p, err := s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	require.NoError(t, s.RevokeRefreshToken(ctx, p.RefreshToken))
	_, err = s.RefreshTokenPair(ctx, p.RefreshToken)
	require.Error(t, err)
	_, err = s.ValidatePanelRefreshToken(ctx, p.RefreshToken)
	require.Error(t, err)
	p, err = s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	u.TokenVersion++
	_, err = s.RefreshTokenPair(ctx, p.RefreshToken)
	require.ErrorIs(t, err, ErrTokenRevoked)
	p, err = s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	u.Status = "disabled"
	_, err = s.RefreshTokenPair(ctx, p.RefreshToken)
	require.ErrorIs(t, err, ErrUserNotActive)
}

func TestPanelCompatConcurrentRefreshDoesNotConsumeToken(t *testing.T) {
	s, _, u, _ := newPanelAuthTest()
	ctx := context.Background()
	p, err := s.GenerateTokenPair(ctx, u, "")
	require.NoError(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.RefreshTokenPair(ctx, p.RefreshToken); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	_, err = s.ValidatePanelRefreshToken(ctx, p.RefreshToken)
	require.NoError(t, err)
}
