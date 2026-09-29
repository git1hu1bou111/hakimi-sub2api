package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGrokLegacyQuarantineKeepsExplicitCooldowns(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, reason := range []string{"", "manual pause", "grok payment required", "grok credentials unauthorized", "grok access or entitlement denied", string(GrokCredentialReasonRefreshTransient)} {
		a := &Account{Platform: PlatformGrok, Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &future, TempUnschedulableReason: reason}
		require.False(t, a.IsSchedulable(), reason)
	}
	for _, reason := range []string{"grok upstream temporary error", "grok empty model output"} {
		a := &Account{Platform: PlatformGrok, Status: StatusActive, Schedulable: true, TempUnschedulableUntil: &future, TempUnschedulableReason: reason}
		require.True(t, a.IsSchedulable(), reason)
		a.RateLimitResetAt = &future
		require.False(t, a.IsSchedulable(), "explicit quota limit must still apply")
	}
}
