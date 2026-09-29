package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// A sweep can span minute rounds. An interrupted attempt is not counted as a
// failure, and no yellow result is published until every eligible account was
// tried. Only failures are remembered: any success immediately ends the sweep.
type intelligenceGroupSweep struct {
	started time.Time
	failed  map[int64]struct{}
}

func (s *intelligenceGroupSweep) run(ctx context.Context, accounts []Account, timeout time.Duration, probe func(context.Context, *Account) IntelligencePoint) (IntelligencePoint, bool) {
	now := time.Now()
	if s.failed == nil || now.Sub(s.started) >= IntelligenceSlots*IntelligenceInterval {
		s.started, s.failed = now, make(map[int64]struct{})
	}
	current := make(map[int64]struct{}, len(accounts))
	for _, a := range accounts {
		current[a.ID] = struct{}{}
	}
	for id := range s.failed {
		if _, ok := current[id]; !ok {
			delete(s.failed, id)
		}
	}
	finish := func(status, detail string) (IntelligencePoint, bool) {
		point := IntelligencePoint{CheckedAt: time.Now().UTC(), Status: status, Detail: detail,
			LatencyMs: int(time.Since(s.started).Milliseconds())}
		s.failed = nil
		return point, true
	}
	for i := range accounts {
		if ctx.Err() != nil {
			return IntelligencePoint{}, false
		}
		if _, done := s.failed[accounts[i].ID]; done {
			continue
		}
		attempt, cancel := context.WithTimeout(ctx, timeout)
		point := probe(attempt, &accounts[i])
		attemptErr := attempt.Err()
		cancel()
		if ctx.Err() != nil {
			return IntelligencePoint{}, false
		}
		if attemptErr != nil {
			point.Status = "red"
		}
		if point.Status == "green" {
			return finish("green", fmt.Sprintf("account_%d_answer_21_after_%d_accounts", accounts[i].ID, len(s.failed)+1))
		}
		if point.Status == "red" || point.Status == "yellow" {
			s.failed[accounts[i].ID] = struct{}{}
		}
		// Busy, removed or newly disabled accounts return unknown, not a failed
		// candy answer. Other candidates can still establish a green result.
	}
	if len(current) > 0 && len(s.failed) == len(current) {
		return finish("yellow", fmt.Sprintf("all_%d_schedulable_accounts_failed", len(current)))
	}
	return IntelligencePoint{}, false
}

func intelligenceAccountInGroup(account *Account, groupID int64) bool {
	if account == nil || account.Platform != PlatformOpenAI || !account.IsSchedulable() || !account.IsModelSupported(IntelligenceModel) {
		return false
	}
	for _, id := range account.GroupIDs {
		if id == groupID {
			return true
		}
	}
	return false
}

func (s *ChannelMonitorIntelligence) probeGroupAccounts(parent context.Context, group IntelligenceGroup) (IntelligencePoint, bool) {
	if s.accountTest == nil || s.accountTest.accountRepo == nil || s.accountTest.openaiGatewayService == nil || s.keys == nil {
		return IntelligencePoint{}, false
	}
	ctx, cancel := context.WithTimeout(parent, IntelligenceProbeBudget)
	defer cancel()
	credential, err := s.resolveKey(ctx, group)
	if err != nil {
		slog.Warn("intelligence group sweep: credential unavailable", "group_id", group.ID)
		return IntelligencePoint{}, false
	}
	key, err := s.keys.apiKeyRepo.GetByKey(ctx, credential)
	if err != nil || key == nil || key.GroupID == nil || *key.GroupID != group.ID || key.Group == nil || key.Group.Platform != PlatformOpenAI {
		return IntelligencePoint{}, false
	}
	// Query the same group's schedulable pool, preserving group/account priority.
	// Never route through a fallback group or randomly reselect a failed account.
	accounts, err := s.accountTest.accountRepo.ListSchedulableByGroupIDAndPlatform(ctx, group.ID, PlatformOpenAI)
	if err != nil {
		slog.Warn("intelligence group sweep: list failed", "group_id", group.ID)
		return IntelligencePoint{}, false
	}
	gateway := s.accountTest.openaiGatewayService
	eligible := make([]Account, 0, len(accounts))
	for i := range accounts {
		if !intelligenceAccountInGroup(&accounts[i], group.ID) {
			continue
		}
		fresh := gateway.recheckSelectedOpenAIAccountFromDB(ctx, &accounts[i], &group.ID, PlatformOpenAI, IntelligenceModel, false, "")
		if intelligenceAccountInGroup(fresh, group.ID) {
			eligible = append(eligible, *fresh)
		}
	}
	state, _ := s.groupSweeps.LoadOrStore(group.ID, &intelligenceGroupSweep{})
	sweep := state.(*intelligenceGroupSweep)
	point, complete := sweep.run(ctx, eligible, IntelligenceTimeout, func(attempt context.Context, account *Account) IntelligencePoint {
		fresh, err := s.accountTest.accountRepo.GetByID(attempt, account.ID)
		if err != nil || !intelligenceAccountInGroup(fresh, group.ID) {
			return IntelligencePoint{Status: "unknown"}
		}
		fresh = gateway.recheckSelectedOpenAIAccountFromDB(attempt, fresh, &group.ID, PlatformOpenAI, IntelligenceModel, false, "")
		if !intelligenceAccountInGroup(fresh, group.ID) {
			return IntelligencePoint{Status: "unknown"}
		}
		if gateway.concurrencyService != nil {
			slot, err := gateway.concurrencyService.AcquireAccountSlot(attempt, fresh.ID, fresh.Concurrency)
			if err != nil || slot == nil || !slot.Acquired {
				return IntelligencePoint{Status: "unknown"}
			}
			defer slot.ReleaseFunc()
		}
		result, _ := s.probeAccountWithKeyOnce(attempt, fresh, key)
		slog.Info("intelligence group sweep: account result", "group_id", group.ID, "account_id", fresh.ID, "status", result.Status, "detail", result.Detail)
		return result
	})
	if complete {
		s.groupSweeps.Delete(group.ID)
		slog.Info("intelligence group sweep: completed", "group_id", group.ID, "status", point.Status, "detail", point.Detail)
	}
	return point, complete
}
