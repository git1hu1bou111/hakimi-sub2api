package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

const usdtQuoteTTL = 5 * time.Minute
const usdtRateURL = "https://api.coinbase.com/v2/exchange-rates?currency=USDT"

// This flag is opt-in per gateway. Legacy CNY orders keep their original semantics.
func isUSDTRechargeProvider(key string, config map[string]string) bool {
	return key == payment.TypeEasyPay && config["usdtRecharge"] == "true" && paymentProviderConfigCurrency(key, config) == "USD"
}

func (s *PaymentConfigService) pcUSDTMethod(instances []*dbent.PaymentProviderInstance) (bool, bool) {
	var mode bool
	for i, inst := range instances {
		cfg, err := s.decryptConfig(inst.Config)
		if err != nil {
			return false, false
		}
		next := isUSDTRechargeProvider(inst.ProviderKey, cfg)
		if i > 0 && next != mode {
			return false, false
		}
		mode = next
	}
	return mode, true
}

type USDTRechargeQuote struct {
	Amount      string    `json:"amount"`
	Rate        string    `json:"rate"`
	BaseCredit  string    `json:"base_credit"`
	BonusCredit string    `json:"bonus_credit"`
	TotalCredit string    `json:"total_credit"`
	Source      string    `json:"source"`
	QuotedAt    time.Time `json:"quoted_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Token       string    `json:"token,omitempty"`
}
type usdtQuoteClaims struct {
	Kind   string            `json:"kind"`
	UserID int64             `json:"user_id"`
	Method string            `json:"method"`
	Quote  USDTRechargeQuote `json:"quote"`
}
type usdtRateCache struct {
	mu     sync.Mutex
	rate   decimal.Decimal
	at     time.Time
	client *http.Client
}

var usdtRates = &usdtRateCache{client: &http.Client{Timeout: 5 * time.Second}}

func (c *usdtRateCache) get(ctx context.Context) (decimal.Decimal, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	if c.rate.IsPositive() && now.Sub(c.at) < time.Minute {
		return c.rate, c.at, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, usdtRateURL, nil)
	if err != nil {
		return decimal.Zero, time.Time{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return decimal.Zero, time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return decimal.Zero, time.Time{}, fmt.Errorf("exchange rate HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data struct {
			Currency string            `json:"currency"`
			Rates    map[string]string `json:"rates"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128*1024)).Decode(&result); err != nil {
		return decimal.Zero, time.Time{}, err
	}
	rate, err := decimal.NewFromString(result.Data.Rates["CNY"])
	if err != nil || result.Data.Currency != "USDT" || !rate.IsPositive() || rate.GreaterThan(decimal.NewFromInt(100)) {
		return decimal.Zero, time.Time{}, fmt.Errorf("invalid USDT/CNY exchange rate")
	}
	c.rate, c.at = rate.Round(6), time.Now().UTC()
	return c.rate, c.at, nil
}

func makeUSDTQuote(amount, rate decimal.Decimal, at, now time.Time) USDTRechargeQuote {
	base := amount.Mul(rate).Round(8)
	bonus := base.Div(decimal.NewFromInt(100)).Round(8)
	return USDTRechargeQuote{Amount: amount.String(), Rate: rate.String(), BaseCredit: base.String(), BonusCredit: bonus.String(), TotalCredit: base.Add(bonus).String(), Source: "Coinbase USDT/CNY", QuotedAt: at, ExpiresAt: now.Add(usdtQuoteTTL)}
}

func (s *PaymentService) GetUSDTQuote(ctx context.Context, uid int64, amount float64, method string) (*USDTRechargeQuote, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 || amount > 1000000 || !decimal.NewFromFloat(amount).Equal(decimal.NewFromFloat(amount).Round(2)) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "USDT 金额必须大于 0，最多两位小数")
	}
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || cfg.BalanceDisabled {
		return nil, infraerrors.Forbidden("PAYMENT_DISABLED", "充值暂未开放")
	}
	if cfg.RechargeFeeRate != 0 {
		return nil, infraerrors.ServiceUnavailable("USDT_FEE_CONFLICT", "USDT 充值手续费配置需要检查")
	}
	if _, err := s.validateOrderInput(ctx, CreateOrderRequest{Amount: amount, OrderType: payment.OrderTypeBalance}, cfg); err != nil {
		return nil, err
	}
	limits, err := s.configService.GetAvailableMethodLimits(ctx)
	if err != nil {
		return nil, err
	}
	method = NormalizeVisibleMethod(method)
	ml, ok := limits.Methods[method]
	if !ok || !ml.USDTRecharge {
		return nil, infraerrors.BadRequest("USDT_NETWORK_UNAVAILABLE", "该 USDT 网络暂不可用")
	}
	if (ml.SingleMin > 0 && amount < ml.SingleMin) || (ml.SingleMax > 0 && amount > ml.SingleMax) {
		return nil, infraerrors.BadRequest("INVALID_AMOUNT", "充值金额超出该网络限额")
	}
	rate, at, err := usdtRates.get(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("USDT_RATE_UNAVAILABLE", "实时汇率暂不可用，请稍后重试")
	}
	quote := makeUSDTQuote(decimal.NewFromFloat(amount), rate, at, time.Now().UTC())
	token, err := s.signUSDTQuote(usdtQuoteClaims{Kind: "usdt_recharge_v1", UserID: uid, Method: method, Quote: quote})
	if err != nil {
		return nil, err
	}
	quote.Token = token
	return &quote, nil
}

func (s *PaymentService) signUSDTQuote(claims usdtQuoteClaims) (string, error) {
	if err := s.resumeService.ensureSigningKey(); err != nil {
		return "", err
	}
	data, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.resumeService.signingKey)
	mac.Write([]byte("usdt_quote:v1:" + payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *PaymentService) parseUSDTQuote(req CreateOrderRequest, now time.Time) (*USDTRechargeQuote, error) {
	invalid := func() (*USDTRechargeQuote, error) {
		return nil, infraerrors.BadRequest("USDT_QUOTE_INVALID", "报价已过期或与订单不匹配，请刷新报价")
	}
	if err := s.resumeService.ensureSigningKey(); err != nil {
		return nil, err
	}
	if len(req.USDTQuoteToken) > 4096 {
		return invalid()
	}
	parts := strings.Split(req.USDTQuoteToken, ".")
	if len(parts) != 2 {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalid()
	}
	mac := hmac.New(sha256.New, s.resumeService.signingKey)
	mac.Write([]byte("usdt_quote:v1:" + parts[0]))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return invalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return invalid()
	}
	var claims usdtQuoteClaims
	if json.Unmarshal(data, &claims) != nil {
		return invalid()
	}
	q := claims.Quote
	if claims.Kind != "usdt_recharge_v1" || claims.UserID != req.UserID || claims.Method != req.PaymentType || req.OrderType != payment.OrderTypeBalance || !now.Before(q.ExpiresAt) || q.ExpiresAt.Sub(now) > usdtQuoteTTL || now.Sub(q.QuotedAt) > usdtQuoteTTL+time.Minute || now.Before(q.QuotedAt) {
		return invalid()
	}
	amount, err := decimal.NewFromString(q.Amount)
	if err != nil || math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) || !amount.Equal(decimal.NewFromFloat(req.Amount)) {
		return invalid()
	}
	rate, err := decimal.NewFromString(q.Rate)
	if err != nil || !rate.IsPositive() {
		return invalid()
	}
	expected := makeUSDTQuote(amount, rate, q.QuotedAt, now)
	if q.BaseCredit != expected.BaseCredit || q.BonusCredit != expected.BonusCredit || q.TotalCredit != expected.TotalCredit {
		return invalid()
	}
	q.Token = ""
	return &q, nil
}

// Only expose the immutable, non-secret quote, never the provider credentials.
func PaymentOrderUSDTQuote(order *dbent.PaymentOrder) *USDTRechargeQuote {
	if order == nil {
		return nil
	}
	raw, ok := order.ProviderSnapshot["usdt_quote"]
	if !ok {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var q USDTRechargeQuote
	if json.Unmarshal(data, &q) != nil || q.Amount == "" {
		return nil
	}
	q.Token = ""
	return &q
}
