//go:build unit

package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyMonitorFailureThresholdRequiresThreeConsecutiveFailures(t *testing.T) {
	first := &CheckResult{Status: MonitorStatusError, Message: "upstream HTTP 502"}
	applyMonitorFailureThreshold(first, nil)
	require.Equal(t, MonitorStatusDegraded, first.Status)
	require.True(t, strings.HasPrefix(first.Message, "[failure-streak=1/3 raw=error]"))

	second := &CheckResult{Status: MonitorStatusError, Message: "upstream HTTP 503"}
	applyMonitorFailureThreshold(second, []*ChannelMonitorHistoryEntry{
		{Status: first.Status, Message: first.Message},
	})
	require.Equal(t, MonitorStatusDegraded, second.Status)
	require.True(t, strings.HasPrefix(second.Message, "[failure-streak=2/3 raw=error]"))

	third := &CheckResult{Status: MonitorStatusError, Message: "upstream HTTP 504"}
	applyMonitorFailureThreshold(third, []*ChannelMonitorHistoryEntry{
		{Status: second.Status, Message: second.Message},
		{Status: first.Status, Message: first.Message},
	})
	require.Equal(t, MonitorStatusError, third.Status)
	require.True(t, strings.HasPrefix(third.Message, "[failure-streak=3/3 raw=error]"))
}

func TestApplyMonitorFailureThresholdTreatsExistingHardFailuresAsStreak(t *testing.T) {
	result := &CheckResult{Status: MonitorStatusFailed, Message: "challenge mismatch"}
	applyMonitorFailureThreshold(result, []*ChannelMonitorHistoryEntry{
		{Status: MonitorStatusError, Message: "older hard failure"},
		{Status: MonitorStatusFailed, Message: "oldest hard failure"},
	})

	require.Equal(t, MonitorStatusFailed, result.Status)
	require.True(t, strings.HasPrefix(result.Message, "[failure-streak=3/3 raw=failed]"))
}

func TestApplyMonitorFailureThresholdResetsAfterNonFailure(t *testing.T) {
	result := &CheckResult{Status: MonitorStatusError, Message: "upstream HTTP 502"}
	applyMonitorFailureThreshold(result, []*ChannelMonitorHistoryEntry{
		{Status: MonitorStatusDegraded, Message: "recovered after 2 attempts from upstream HTTP 502"},
		{Status: MonitorStatusError, Message: "older failure"},
	})

	require.Equal(t, MonitorStatusDegraded, result.Status)
	require.True(t, strings.HasPrefix(result.Message, "[failure-streak=1/3 raw=error]"))
}

func TestApplyMonitorFailureThresholdLeavesHealthyResultsUntouched(t *testing.T) {
	result := &CheckResult{Status: MonitorStatusOperational, Message: "ok"}
	applyMonitorFailureThreshold(result, []*ChannelMonitorHistoryEntry{
		{Status: MonitorStatusError, Message: "failure"},
	})

	require.Equal(t, MonitorStatusOperational, result.Status)
	require.Equal(t, "ok", result.Message)
}
