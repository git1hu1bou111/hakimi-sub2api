package service

import "strings"

// An absent or unfamiliar reason is not evidence of a transient failure.
// Keep upstream/admin cooldowns effective while retaining Hakimi's legacy
// transport-failure recovery behaviour.
func isLegacyGrokTransientQuarantine(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "legacy grok temporary quarantine", "grok upstream temporary error", "grok empty model output":
		return true
	default:
		return false
	}
}
