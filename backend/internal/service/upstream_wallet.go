package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"golang.org/x/sync/errgroup"
)

const UpstreamWalletExtraKey = "upstream_wallet"

type UpstreamWalletAmount struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

type UpstreamWalletSnapshot struct {
	Status    string                 `json:"status"`
	Balances  []UpstreamWalletAmount `json:"balances,omitempty"`
	UpdatedAt *time.Time             `json:"updated_at,omitempty"`
	CheckedAt time.Time              `json:"checked_at"`
}

type upstreamWalletRepository interface {
	ListDueUpstreamWalletAccounts(context.Context, time.Time, int) ([]Account, error)
	SaveUpstreamWallet(context.Context, *Account, *UpstreamWalletSnapshot) error
}

func (s *UpstreamBillingProbeService) walletLoop() {
	defer s.wg.Done()
	repo, ok := s.accountRepo.(upstreamWalletRepository)
	if !ok {
		return
	}
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for s.parentCtx.Err() == nil {
		if err := s.walletBatch(s.parentCtx, repo); err != nil && s.parentCtx.Err() == nil {
			logger.LegacyPrintf("service.upstream_wallet", "wallet batch failed: %v", err)
		}
		select {
		case <-s.parentCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *UpstreamBillingProbeService) walletBatch(ctx context.Context, repo upstreamWalletRepository) error {
	// Three waves of four checks with 27s per wave fit within the leader lease.
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	release, ok, err := s.tryAcquireLeaderLock(ctx, "upstream:wallet:hourly")
	if err != nil || !ok {
		return err
	}
	defer release()
	rows, err := repo.ListDueUpstreamWalletAccounts(ctx, s.currentTime().UTC(), 12)
	if err != nil {
		return err
	}
	var group errgroup.Group
	group.SetLimit(4)
	for i := range rows {
		a := &rows[i]
		group.Go(func() error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			pctx, done := context.WithTimeout(ctx, 27*time.Second)
			balances, status := s.readUpstreamWallet(pctx, a)
			done()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			snap := makeWalletSnapshot(a.Extra, status, balances, s.currentTime().UTC())
			err := repo.SaveUpstreamWallet(ctx, a, &snap)
			if errors.Is(err, ErrUpstreamBillingProbeIdentityChanged) {
				return nil
			}
			return err
		})
	}
	return group.Wait()
}

func makeWalletSnapshot(extra map[string]any, status string, balances []UpstreamWalletAmount, now time.Time) UpstreamWalletSnapshot {
	snap := UpstreamWalletSnapshot{Status: status, CheckedAt: now}
	if status == "ok" {
		snap.Balances = balances
		snap.UpdatedAt = &now
	} else if previous, ok := extra[UpstreamWalletExtraKey]; ok {
		var old UpstreamWalletSnapshot
		b, err := json.Marshal(previous)
		if err == nil && json.Unmarshal(b, &old) == nil && old.UpdatedAt != nil && len(old.Balances) > 0 {
			snap.Status = "error"
			snap.Balances = old.Balances
			snap.UpdatedAt = old.UpdatedAt
		}
	}
	return snap
}

func (s *UpstreamBillingProbeService) readUpstreamWallet(ctx context.Context, a *Account) ([]UpstreamWalletAmount, string) {
	if a.Type != AccountTypeAPIKey {
		return nil, "unsupported"
	}
	base := strings.TrimSpace(a.GetCredential("base_url"))
	if a.RoutesProtocolByInbound() && a.IsAdaptiveAPIProtocol() {
		base = a.GetCNProtocolBaseURL(APIProtocolChatCompletions)
	}
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, "error"
	}
	host := strings.TrimSuffix(strings.ToLower(baseURL.Hostname()), ".")
	kind := "relay"
	target := ""
	switch {
	case a.clineAccountAPISupported():
		kind = "cline"
	case a.commandCodeUsageSupported():
		kind = "commandcode"
	case a.Platform == PlatformDeepseek && !a.IsCodingPlan() && (base == "" || host == "api.deepseek.com"):
		kind = "deepseek"
		target = "https://api.deepseek.com/user/balance"
	case a.Platform == PlatformKimi && !a.IsCodingPlan() && (base == "" || host == "api.moonshot.cn"):
		kind = "kimi"
		target = "https://api.moonshot.cn/v1/users/me/balance"
	case upstreamBillingProbeTargetIsOfficialAPI(base):
		return nil, "unsupported"
	default:
		target = buildOpenAIEndpointURL(base, "/v1/usage")
	}
	t := s.accountTestService
	if t == nil || t.httpUpstream == nil || strings.TrimSpace(a.GetCredential("api_key")) == "" {
		return nil, "error"
	}
	proxy := ""
	if a.ProxyID != nil {
		if a.Proxy == nil || a.Proxy.ID != *a.ProxyID {
			return nil, "error"
		}
		proxy = a.Proxy.URL()
	}
	ctx = WithHTTPUpstreamPublicHostsOnly(WithHTTPUpstreamRedirectsDisabled(ctx))
	switch kind {
	case "cline":
		amount, err := newClineAccountClient(t.cfg, t.httpUpstream, a, proxy, "UPSTREAM_WALLET").balance(ctx)
		if err != nil {
			return nil, "error"
		}
		return []UpstreamWalletAmount{{Amount: amount, Currency: "USD"}}, "ok"
	case "commandcode":
		var payload struct {
			Credits struct {
				Purchased json.Number `json:"purchasedCredits"`
			} `json:"credits"`
		}
		_, err := newCommandCodeUsageClient(t.cfg, t.httpUpstream, a, proxy).getCredits(ctx, &payload)
		if err != nil {
			return nil, "error"
		}
		amount, err := payload.Credits.Purchased.Float64()
		if err != nil || math.IsInf(amount, 0) || math.IsNaN(amount) {
			return nil, "error"
		}
		// Purchased dollars exclude monthly plan and promotional credits.
		return []UpstreamWalletAmount{{Amount: amount, Currency: "USD"}}, "ok"
	}
	if !strings.HasPrefix(target, "https://") {
		return nil, "unsupported"
	}
	target, err = t.validateUpstreamBaseURL(target)
	if err != nil {
		return nil, "error"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "error"
	}
	if kind == "relay" {
		day := s.currentTime().In(time.FixedZone("UTC+8", 8*60*60)).Format("2006-01-02")
		query := req.URL.Query()
		query.Set("start_date", day)
		query.Set("end_date", day)
		req.URL.RawQuery = query.Encode()
	}
	req.Header.Set("Authorization", "Bearer "+a.GetCredential("api_key"))
	req.Header.Set("Accept", "application/json")
	a.ApplyHeaderOverrides(req.Header)
	resp, err := t.httpUpstream.Do(req, proxy, a.ID, maxInt(a.Concurrency, 1))
	if err != nil || resp == nil || resp.Body == nil {
		return nil, "error"
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 || resp.StatusCode == 405 {
		return nil, "unsupported"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "error"
	}
	const maxBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || len(body) > maxBytes {
		return nil, "error"
	}
	return parseWallet(kind, body)
}

func parseWallet(kind string, body []byte) ([]UpstreamWalletAmount, string) {
	var data struct {
		Mode    string      `json:"mode"`
		Valid   *bool       `json:"isValid"`
		Balance json.Number `json:"balance"`
		Unit    string      `json:"unit"`
		Code    *int        `json:"code"`
		Data    struct {
			Available json.Number `json:"available_balance"`
		} `json:"data"`
		Infos []struct {
			Total    json.Number `json:"total_balance"`
			Currency string      `json:"currency"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, "error"
	}
	var entries []UpstreamWalletAmount
	add := func(value json.Number, currency string) bool {
		amount, err := value.Float64()
		currency = strings.ToUpper(strings.TrimSpace(currency))
		if err != nil || math.IsInf(amount, 0) || math.IsNaN(amount) || len(currency) != 3 {
			return false
		}
		for _, c := range currency {
			if c < 'A' || c > 'Z' {
				return false
			}
		}
		entries = append(entries, UpstreamWalletAmount{Amount: amount, Currency: currency})
		return true
	}
	switch kind {
	case "relay":
		if data.Valid != nil && !*data.Valid {
			return nil, "error"
		}
		if data.Mode != "unrestricted" || data.Balance == "" {
			return nil, "unsupported"
		}
		if !add(data.Balance, data.Unit) {
			return nil, "error"
		}
	case "deepseek":
		for _, info := range data.Infos {
			if !add(info.Total, info.Currency) {
				return nil, "error"
			}
		}
	case "kimi":
		if (data.Code != nil && *data.Code != 0) || !add(data.Data.Available, "CNY") {
			return nil, "error"
		}
	}
	if len(entries) == 0 {
		return nil, "error"
	}
	return entries, "ok"
}
