package service

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const downstreamTierGroupEnv = "HAKIMI_DEFAULT_RESPONSE_TIER_GROUP_ID"

func useDefaultDownstreamTier(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	return group != nil && group.Platform == PlatformOpenAI && group.ID > 0 &&
		os.Getenv(downstreamTierGroupEnv) == strconv.FormatInt(group.ID, 10)
}

var firstDownstreamTierRewrite sync.Once

// rewriteDownstreamUltraFastTier operates on the final client copy. Callers use
// the original payload for accounting and protocol state.
func rewriteDownstreamUltraFastTier(payload []byte) []byte {
	if !bytes.Contains(payload, []byte(`"service_tier"`)) {
		return payload
	}
	root := gjson.ParseBytes(payload)
	event := root.Get("type").String()
	var path string
	switch event {
	case "response.created", "response.queued", "response.in_progress", "response.completed",
		"response.done", "response.incomplete", "response.failed", "response.cancelled", "response.canceled":
		path = "response.service_tier"
	case "":
		switch root.Get("object").String() {
		case "response", "response.compaction", "chat.completion", "chat.completion.chunk":
		case "":
			// Some compatible providers omit object from otherwise complete responses.
			compactResponse := strings.HasPrefix(root.Get("id").String(), "resp_") &&
				(root.Get("status").Type == gjson.String || root.Get("usage").IsObject())
			if !root.Get("model").Exists() || !(root.Get("choices").IsArray() || root.Get("output").IsArray() || compactResponse) {
				return payload
			}
		default:
			return payload
		}
		path = "service_tier"
	default:
		return payload
	}
	if root.Get(path).String() != OpenAIFastTierUltrafast || !gjson.ValidBytes(payload) {
		return payload
	}
	out, err := sjson.SetBytes(payload, path, "default")
	if err != nil {
		return payload
	}
	if event == "" || event == "response.completed" || event == "response.done" {
		firstDownstreamTierRewrite.Do(func() {
			slog.Info("openai.downstream_service_tier_rewritten", "group_id", os.Getenv(downstreamTierGroupEnv),
				"event", event, "original_tier", OpenAIFastTierUltrafast, "client_tier", "default")
		})
	}
	return out
}
