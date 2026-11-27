package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Deployment-specific opt-in for the two customer groups that share the local 9l9 relay.
// Only its scoped read-only receipt view is queried. Never correlate by token counts or timestamps.
const nineTierDSNFile = "HAKIMI_9L9_TIER_DSN_FILE"

type nineTierReceipt struct{ id, tier string }
type nineTierLookup func(context.Context, string, string) (nineTierReceipt, string)

var configuredNineTierLookup = sync.OnceValue(func() nineTierLookup {
	path := os.Getenv(nineTierDSNFile)
	if path == "" {
		return nil
	}
	contents, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(contents))) == 0 {
		return nineTierUnavailable
	}
	connector, err := pq.NewConnector(strings.TrimSpace(string(contents)))
	if err != nil {
		return nineTierUnavailable
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(2 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	store := &nineTierLedger{db: db, slots: make(chan struct{}, 96)}
	return store.lookup
})

func nineTierUnavailable(context.Context, string, string) (nineTierReceipt, string) {
	return nineTierReceipt{}, "configuration"
}

type nineTierLedger struct {
	db           *sql.DB
	slots        chan struct{}
	backoffUntil atomic.Int64
}

var errAmbiguousNineTier = errors.New("ambiguous relay receipt")

func (s *nineTierLedger) find(ctx context.Context, httpID, responseID string) (nineTierReceipt, error) {
	// The database role may SELECT only this view. Its predicate fixes the upstream tenant/key.
	rows, err := s.db.QueryContext(ctx, "SELECT request_id, COALESCE(service_tier,'') FROM public.hakimi_billed_tier_receipts WHERE request_id IN (NULLIF($1,''),NULLIF($2,'')) LIMIT 2", httpID, responseID)
	if err != nil {
		return nineTierReceipt{}, err
	}
	defer rows.Close()
	var rec nineTierReceipt
	for rows.Next() {
		if rec.id != "" {
			return nineTierReceipt{}, errAmbiguousNineTier
		}
		if err := rows.Scan(&rec.id, &rec.tier); err != nil {
			return nineTierReceipt{}, err
		}
	}
	if err := rows.Err(); err != nil {
		return nineTierReceipt{}, err
	}
	if rec.id == "" {
		return nineTierReceipt{}, sql.ErrNoRows
	}
	return rec, nil
}

func (s *nineTierLedger) lookup(ctx context.Context, httpID, responseID string) (nineTierReceipt, string) {
	if time.Now().UnixMilli() < s.backoffUntil.Load() {
		return nineTierReceipt{}, "database_backoff"
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		return nineTierReceipt{}, "capacity"
	}
	// The peer also writes usage asynchronously. Do not block the customer stream or
	// allow an unavailable peer database to occupy usage workers without a deadline.
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for _, wait := range [...]time.Duration{0, 20 * time.Millisecond, 60 * time.Millisecond, 120 * time.Millisecond, 240 * time.Millisecond, 400 * time.Millisecond} {
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nineTierReceipt{}, "deadline"
			case <-timer.C:
			}
		}
		rec, err := s.find(ctx, httpID, responseID)
		switch {
		case err == nil:
			return rec, ""
		case errors.Is(err, sql.ErrNoRows):
			continue
		case errors.Is(err, errAmbiguousNineTier):
			return nineTierReceipt{}, "ambiguous"
		default:
			if ctx.Err() != nil {
				return nineTierReceipt{}, "deadline"
			}
			s.backoffUntil.Store(time.Now().Add(2 * time.Second).UnixMilli())
			return nineTierReceipt{}, "database"
		}
	}
	return nineTierReceipt{}, "missing"
}

func nineTierEligible(in *OpenAIRecordUsageInput) bool {
	if in == nil || in.Result == nil || in.APIKey == nil || in.APIKey.GroupID == nil || in.Account == nil ||
		in.Account.ID != 1014 || !in.Account.IsOpenAIApiKey() || strings.TrimSuffix(in.Account.GetOpenAIBaseURL(), "/") != "http://127.0.0.1:4947" {
		return false
	}
	gid := *in.APIKey.GroupID
	r := in.Result
	if (gid != 101 && gid != 105) || in.CyberBlocked || in.legacyGrokSafetyFeeUSD != nil || r.ImageEndpoint || r.VideoCount > 0 || r.AudioUsage != nil ||
		normalizeBillingServiceTier(optionalStringValue(r.ServiceTier)) == "ultrafast" {
		return false
	}
	endpoint := r.UpstreamEndpoint
	if endpoint == "" {
		endpoint = in.UpstreamEndpoint
	}
	switch endpoint {
	case "/v1/responses", "/v1/responses/compact", "/v1/chat/completions":
		return true
	default:
		return false
	}
}

// A WebSocket handshake can carry several turns. Its headers must never supply a turn's receipt.
func nineTierReceiptIDs(r *OpenAIForwardResult) (string, string) {
	httpID := ""
	if !r.OpenAIWSMode {
		ids := r.UpstreamHeaders.Values("X-Client-Request-Id")
		if len(ids) == 1 {
			s := strings.TrimSpace(ids[0])
			if id, err := uuid.Parse(s); err == nil && len(s) == 36 && id != uuid.Nil {
				httpID = "client:" + s
			}
		}
	}
	rid := strings.TrimSpace(r.ResponseID)
	if rid == "" && r.OpenAIWSMode {
		rid = strings.TrimSpace(r.RequestID)
	}
	if !strings.HasPrefix(rid, "resp_") || len(rid) <= 5 || len(rid) > 128 {
		return httpID, ""
	}
	for _, c := range rid[5:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return httpID, ""
		}
	}
	return httpID, rid
}

var nineTierNextWarning atomic.Int64
var nineTierFallbackCount atomic.Uint64

func noteNineTierFallback(reason string) {
	nineTierFallbackCount.Add(1)
	now := time.Now().Unix()
	until := nineTierNextWarning.Load()
	if now >= until && nineTierNextWarning.CompareAndSwap(until, now+60) {
		slog.Warn("billing.9l9_tier_receipt_fallback", "reason", reason, "fallbacks", nineTierFallbackCount.Swap(0))
	}
}

// A verified receipt selects the tier for the usual pricing and deduction code,
// including the customer's own group rate. An unconfirmed Fast charge is never inferred.
func reconcileNineTier(ctx context.Context, in *OpenAIRecordUsageInput, getLookup func() nineTierLookup) (string, bool) {
	if !nineTierEligible(in) {
		return "", false
	}
	lookup := getLookup()
	if lookup == nil {
		return "", false
	}
	r := in.Result
	httpID, responseID := nineTierReceiptIDs(r)
	var rec nineTierReceipt
	reason := "no_peer_id"
	if httpID != "" || responseID != "" {
		rec, reason = lookup(ctx, httpID, responseID)
	}
	if reason == "" {
		switch normalizeBillingServiceTier(rec.tier) {
		case "priority", "fast":
			tier := "priority"
			r.ServiceTier = &tier
			return rec.id, true
		case "", "default", "standard", "auto", "scale":
			if rec.tier == "" {
				r.ServiceTier = nil
			} else {
				tier := "default"
				r.ServiceTier = &tier
			}
			return rec.id, true
		case "flex", "ultrafast":
			// Preserve the ordinary resolver for peer tiers outside this two-tier policy.
			return rec.id, false
		default:
			reason = "unknown_tier"
		}
	}
	noteNineTierFallback(reason)
	baseline := ResolveOpenAIServiceTierBilling(in.Account, optionalStringValue(r.ServiceTier), r.UpstreamResponseServiceTier)
	if baseline.Billing == "flex" {
		tier := "flex"
		r.ServiceTier = &tier
	} else if r.ServiceTier != nil {
		tier := "default"
		r.ServiceTier = &tier
	}
	return "", true
}
