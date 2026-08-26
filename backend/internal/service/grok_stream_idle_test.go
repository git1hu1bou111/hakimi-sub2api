//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResolveGrokStreamIdleTimeout(t *testing.T) {
	require.Equal(t, 90*time.Second, resolveGrokStreamIdleTimeout(90))
	require.Equal(t, defaultGrokStreamIdleTimeout, resolveGrokStreamIdleTimeout(0))
	require.Equal(t, defaultGrokStreamIdleTimeout, resolveGrokStreamIdleTimeout(-1))
}

func TestIsGrokAPIKeyAccount(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{name: "grok api key", account: &Account{Platform: PlatformGrok, Type: AccountTypeAPIKey}, want: true},
		{name: "grok oauth", account: &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}, want: false},
		{name: "openai api key", account: &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, want: false},
		{name: "nil", account: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokAPIKeyAccount(tt.account))
		})
	}
}

func TestGrokStreamIdleFailoverError(t *testing.T) {
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeOAuth}
	err := grokStreamIdleFailoverError(account, 180*time.Second)
	require.NotNil(t, err)
	require.Equal(t, 502, err.StatusCode)
	require.True(t, err.SafeToFailoverAfterWrite)
	require.True(t, err.RetryableOnSameAccount)
	require.True(t, err.RequestScopedTransient)
	require.Equal(t, 1, err.SameAccountRetryMax)
	require.Contains(t, string(err.ResponseBody), "empty_upstream")
	require.WithinDuration(t, time.Now().Add(180*time.Second), err.SameAccountRetryDeadline, 2*time.Second)
}

func TestGrokStreamIdleFailoverErrorRequiresGrokAccount(t *testing.T) {
	openAI := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	err := grokStreamIdleFailoverError(openAI, time.Second)
	require.False(t, err.RetryableOnSameAccount)
	require.True(t, err.RequestScopedTransient)
}
