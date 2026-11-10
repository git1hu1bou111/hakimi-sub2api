//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestUSDTQuotePrecisionAndBinding(t *testing.T) {
	now := time.Now().UTC()
	quote := makeUSDTQuote(decimal.RequireFromString("10"), decimal.RequireFromString("6.73"), now, now)
	require.Equal(t, "67.3", quote.BaseCredit)
	require.Equal(t, "0.673", quote.BonusCredit)
	require.Equal(t, "67.973", quote.TotalCredit)
	svc := &PaymentService{resumeService: NewPaymentResumeService([]byte("test-usdt-signing-key"))}
	token, err := svc.signUSDTQuote(usdtQuoteClaims{Kind: "usdt_recharge_v1", UserID: 42, Method: "usdt_bep20", Quote: quote})
	require.NoError(t, err)
	valid := CreateOrderRequest{UserID: 42, PaymentType: "usdt_bep20", OrderType: payment.OrderTypeBalance, Amount: 10, USDTQuoteToken: token}
	q, err := svc.parseUSDTQuote(valid, now)
	require.NoError(t, err)
	require.Equal(t, quote.TotalCredit, q.TotalCredit)
	for _, mutate := range []func(*CreateOrderRequest){
		func(r *CreateOrderRequest) { r.UserID++ }, func(r *CreateOrderRequest) { r.Amount = 100 },
		func(r *CreateOrderRequest) { r.PaymentType = "usdt_trc20" }, func(r *CreateOrderRequest) { r.OrderType = payment.OrderTypeSubscription },
		func(r *CreateOrderRequest) { r.USDTQuoteToken = "x" + r.USDTQuoteToken },
	} {
		bad := valid
		mutate(&bad)
		_, err = svc.parseUSDTQuote(bad, now)
		require.Error(t, err)
	}
	_, err = svc.parseUSDTQuote(valid, now.Add(usdtQuoteTTL))
	require.Error(t, err)
	quote.BonusCredit = "1000"
	token, err = svc.signUSDTQuote(usdtQuoteClaims{Kind: "usdt_recharge_v1", UserID: 42, Method: "usdt_bep20", Quote: quote})
	require.NoError(t, err)
	valid.USDTQuoteToken = token
	_, err = svc.parseUSDTQuote(valid, now)
	require.Error(t, err)
}

type usdtRoundTripper func(*http.Request) (*http.Response, error)

func (f usdtRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestUSDTRateCacheAndFailClosed(t *testing.T) {
	calls := 0
	body := `{"data":{"currency":"USDT","rates":{"CNY":"6.7300001"}}}`
	cache := &usdtRateCache{client: &http.Client{Transport: usdtRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, usdtRateURL, r.URL.String())
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})}}
	rate, _, err := cache.get(context.Background())
	require.NoError(t, err)
	require.Equal(t, "6.73", rate.String())
	_, _, err = cache.get(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	for _, bad := range []string{`{"data":{"currency":"USD","rates":{"CNY":"6.73"}}}`, `{"data":{"currency":"USDT","rates":{"CNY":"0"}}}`, `garbage`} {
		cache.at = time.Now().Add(-2 * time.Minute)
		body = bad
		_, _, err = cache.get(context.Background())
		require.Error(t, err, "must not reuse a stale quote on failure")
	}
}

func TestUSDTOrderSnapshotKeepsCreditAndGift(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().SetEmail("usdt@example.com").SetPasswordHash("hash").SetUsername("usdt").Save(ctx)
	require.NoError(t, err)
	instance, err := client.PaymentProviderInstance.Create().SetProviderKey(payment.TypeEasyPay).SetName("USDT").SetConfig(`{}`).SetSupportedTypes("usdt_bep20").SetEnabled(true).Save(ctx)
	require.NoError(t, err)
	svc := &PaymentService{entClient: client}
	now := time.Now().UTC()
	q := makeUSDTQuote(decimal.NewFromInt(10), decimal.RequireFromString("6.73"), now, now)
	order, err := svc.createOrderInTx(ctx, CreateOrderRequest{UserID: user.ID, PaymentType: "usdt_bep20", OrderType: payment.OrderTypeBalance, usdtQuote: &q}, &User{ID: user.ID, Email: user.Email, Username: user.Username}, nil, &PaymentConfig{MaxPendingOrders: 3, OrderTimeoutMin: 30}, 67.973, 10, 0, 10, .673, &payment.InstanceSelection{InstanceID: strconv.FormatInt(instance.ID, 10), ProviderKey: payment.TypeEasyPay, Config: map[string]string{"currency": "USD", "usdtRecharge": "true", "pkey": "do-not-copy"}})
	require.NoError(t, err)
	persisted, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, 67.973, persisted.Amount)
	require.Equal(t, .673, persisted.BonusAmount)
	require.Equal(t, 10.0, persisted.PayAmount)
	require.Equal(t, "USD", PaymentOrderCurrency(persisted))
	require.Equal(t, "0.673", PaymentOrderUSDTQuote(persisted).BonusCredit)
	require.Equal(t, 67.3, paymentOrderAmountWithoutBonus(persisted))
	require.NotContains(t, persisted.ProviderSnapshot, "pkey")
	require.Equal(t, "CNY", PaymentOrderCurrency(&dbent.PaymentOrder{}))
	require.Nil(t, PaymentOrderUSDTQuote(&dbent.PaymentOrder{}))
	// Daily caps compare converted base credits, not USDT units, and exclude the gift.
	_, err = client.PaymentOrder.UpdateOneID(order.ID).SetStatus(OrderStatusCompleted).SetPaidAt(now).Save(ctx)
	require.NoError(t, err)
	tx, err := client.Tx(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	require.Error(t, svc.checkDailyLimit(ctx, tx, user.ID, 1, 68))
	require.NoError(t, svc.checkDailyLimit(ctx, tx, user.ID, .7, 68))
}

// Exercise the actual payment fulfillment path with a fractional 1% gift,
// including webhook retries; no production balances are used.
func TestUSDTFulfillmentCreditsExactGiftOnlyOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	ensurePaymentAuditOrderActionUniqueIndex(t, ctx, client)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	now := time.Now().UTC()
	quote := makeUSDTQuote(decimal.NewFromInt(10), decimal.RequireFromString("6.73"), now, now)
	snapshot := buildPaymentOrderProviderSnapshot(&payment.InstanceSelection{ProviderKey: payment.TypeEasyPay, Config: map[string]string{"currency": "USD", "usdtRecharge": "true"}}, CreateOrderRequest{usdtQuote: &quote})
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(payment.OrderTypeBalance).SetPaymentType("usdt_bep20").SetProviderKey(payment.TypeEasyPay).SetProviderSnapshot(snapshot).SetAmount(67.973).SetBonusAmount(.673).SetPayAmount(10).ClearPlanID().ClearSubscriptionGroupID().ClearSubscriptionDays().Save(ctx)
	require.NoError(t, err)
	redeemRepo := &paymentFulfillmentRedeemRepo{}
	credited := 0.0
	creditCalls := 0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, order.UserID, id)
		credited += amount
		creditCalls++
		return nil
	}
	cache := &paymentFulfillmentRedeemCacheStub{}
	svc := &PaymentService{entClient: client, userRepo: userRepo, redeemService: NewRedeemService(redeemRepo, userRepo, nil, cache, nil, client, nil, nil)}
	note := &payment.PaymentNotification{OrderID: order.OutTradeNo, TradeNo: "usdt-paid-trade", Amount: 10, Status: payment.NotificationStatusSuccess}
	require.NoError(t, svc.HandlePaymentNotification(ctx, note, payment.TypeEasyPay))
	require.NoError(t, svc.HandlePaymentNotification(ctx, note, payment.TypeEasyPay))
	require.NoError(t, svc.HandlePaymentNotification(ctx, note, payment.TypeEasyPay))
	require.Equal(t, 1, creditCalls)
	require.InDelta(t, 67.973, credited, 1e-8)
	require.Equal(t, 1, redeemRepo.createCalls)
	got, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, got.Status)
	require.Equal(t, .673, got.BonusAmount)
}
