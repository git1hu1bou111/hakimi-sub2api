package handler

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHakimi8586FailoverAccounting(t *testing.T) {
	metered := func(id string) *service.UpstreamFailoverError {
		return &service.UpstreamFailoverError{StatusCode: 502, PartialResult: &service.ForwardResult{RequestID: id, Usage: service.ClaudeUsage{InputTokens: 11}}}
	}
	var rows []string
	submit := func(account string) func(*service.ForwardResult) {
		return func(r *service.ForwardResult) { rows = append(rows, account+":"+r.RequestID) }
	}
	ctx := context.Background()
	t.Run("selection exhausted after last metered account; no double submission", func(t *testing.T) {
		var p pendingFailedStreamUsage
		rows = nil
		fs := NewFailoverState(2, false)
		e := metered("r1")
		p.remember(e, submit("original"))
		require.Equal(t, FailoverContinue, fs.HandleFailoverError(ctx, nil, 1, service.PlatformAnthropic, 0, e))
		require.Equal(t, FailoverExhausted, fs.HandleSelectionExhausted(ctx))
		p.exhaust(nil)
		p.exhaust(nil)
		require.Equal(t, []string{"original:r1"}, rows)
	})
	t.Run("only last metered attempt uses its own account", func(t *testing.T) {
		var p pendingFailedStreamUsage
		rows = nil
		p.remember(metered("old"), submit("A"))
		p.remember(metered("new"), submit("B"))
		p.remember(&service.UpstreamFailoverError{}, submit("C"))
		p.exhaust(nil)
		require.Equal(t, []string{"B:new"}, rows)
	})
	t.Run("retry success only submits success", func(t *testing.T) {
		rows = nil
		func() {
			var p pendingFailedStreamUsage
			p.remember(metered("old"), submit("A"))
			submit("B")(&service.ForwardResult{RequestID: "success"})
		}()
		require.Equal(t, []string{"B:success"}, rows)
	})
	t.Run("no observed usage never submits", func(t *testing.T) {
		rows = nil
		var p pendingFailedStreamUsage
		p.remember(&service.UpstreamFailoverError{StatusCode: 502}, submit("A"))
		p.exhaust(nil)
		require.Empty(t, rows)
	})
}
