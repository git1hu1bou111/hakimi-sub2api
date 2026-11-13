//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/paymentauditlog"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestUSDTClosedOrdersNeverAutoCreditEvenAfterConcurrentCancellation(t *testing.T) {
	for _, status := range []string{OrderStatusCancelled, OrderStatusExpired, OrderStatusPending} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentConfigServiceTestClient(t)
			o := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
			o, err := client.PaymentOrder.UpdateOneID(o.ID).SetPaymentType("usdt_bep20").ClearPaidAt().Save(ctx)
			require.NoError(t, err)
			// Keep o stale to exercise the database compare-and-set race guard.
			u := client.PaymentOrder.UpdateOneID(o.ID).SetStatus(status)
			if status == OrderStatusPending {
				u = u.SetExpiresAt(time.Now().Add(-time.Minute))
			}
			_, err = u.Save(ctx)
			require.NoError(t, err)
			s := &PaymentService{entClient: client}
			err = s.toPaid(ctx, o, "paid-trade", o.PayAmount, payment.TypeEasyPay)
			require.ErrorContains(t, err, "manual review")
			current, err := client.PaymentOrder.Get(ctx, o.ID)
			require.NoError(t, err)
			require.Equal(t, status, current.Status)
			require.Nil(t, current.PaidAt)
			count, err := client.PaymentAuditLog.Query().Where(paymentauditlog.ActionEQ("PAYMENT_REVIEW_REQUIRED")).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count)
		})
	}
}

func TestUSDTCancellationMustBeConfirmedByGateway(t *testing.T) {
	for _, mode := range []string{"success", "query_failure", "cancel_failure", "wrong_order", "missing_trade"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			client := newPaymentOrderLifecycleTestClient(t)
			o := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
			o, err := client.PaymentOrder.UpdateOneID(o.ID).SetPaymentType("usdt_bep20").ClearPaidAt().Save(ctx)
			require.NoError(t, err)
			p := &paymentOrderLifecycleQueryProvider{key: payment.TypeEasyPay, supportedTypes: []string{"usdt_bep20"}, resp: &payment.QueryOrderResponse{TradeNo: o.PaymentTradeNo, Status: payment.ProviderStatusPending, Amount: o.PayAmount, Metadata: map[string]string{"bepusdt_order_id": o.OutTradeNo, "bepusdt_status": "1"}}}
			switch mode {
			case "query_failure":
				p.queryErr = errors.New("offline")
			case "cancel_failure":
				p.cancelErr = errors.New("offline")
			case "wrong_order":
				p.resp.Metadata["bepusdt_order_id"] = "other"
			case "missing_trade":
				o, err = client.PaymentOrder.UpdateOneID(o.ID).SetPaymentTradeNo("").Save(ctx)
				require.NoError(t, err)
			}
			registry := payment.NewRegistry()
			registry.Register(p)
			s := &PaymentService{entClient: client, registry: registry, providersLoaded: true}
			_, err = s.CancelOrder(ctx, o.ID, o.UserID)
			current, getErr := client.PaymentOrder.Get(ctx, o.ID)
			require.NoError(t, getErr)
			if mode == "success" {
				require.NoError(t, err)
				require.Equal(t, OrderStatusCancelled, current.Status)
				require.Equal(t, o.PaymentTradeNo, p.lastCancelTradeNo)
			} else {
				require.Error(t, err)
				require.Equal(t, OrderStatusPending, current.Status)
			}
		})
	}
}

func TestUSDTPaymentWinsCancellationRaceAndCreditsOnlyOnce(t *testing.T) {
	ctx := context.Background()
	client := newPaymentOrderLifecycleTestClient(t)
	o := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPending, time.Now())
	o, err := client.PaymentOrder.UpdateOneID(o.ID).SetPaymentType("usdt_bep20").SetOrderType(payment.OrderTypeBalance).ClearPlanID().ClearSubscriptionGroupID().ClearSubscriptionDays().ClearPaidAt().Save(ctx)
	require.NoError(t, err)
	metadata := map[string]string{"bepusdt_order_id": o.OutTradeNo, "bepusdt_status": "1"}
	p := &paymentOrderLifecycleQueryProvider{key: payment.TypeEasyPay, supportedTypes: []string{"usdt_bep20"}, cancelErr: errors.New("payment won"), responses: []*payment.QueryOrderResponse{{TradeNo: o.PaymentTradeNo, Status: payment.ProviderStatusPending, Amount: o.PayAmount, Metadata: metadata}, {TradeNo: o.PaymentTradeNo, Status: payment.ProviderStatusPaid, Amount: o.PayAmount, Metadata: metadata}}}
	registry := payment.NewRegistry()
	registry.Register(p)
	repo := &paymentFulfillmentRedeemRepo{}
	credited := 0.0
	userRepo := &mockUserRepo{getByIDUser: &User{ID: o.UserID}}
	userRepo.updateBalanceFn = func(_ context.Context, id int64, amount float64) error {
		require.Equal(t, o.UserID, id)
		credited += amount
		return nil
	}
	redeem := NewRedeemService(repo, userRepo, nil, &paymentFulfillmentRedeemCacheStub{}, nil, client, nil, nil)
	s := &PaymentService{entClient: client, registry: registry, providersLoaded: true, redeemService: redeem, userRepo: userRepo}
	result, err := s.CancelOrder(ctx, o.ID, o.UserID)
	require.NoError(t, err)
	require.Equal(t, checkPaidResultAlreadyPaid, result)
	current, err := client.PaymentOrder.Get(ctx, o.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, current.Status)
	require.InDelta(t, o.Amount, credited, 1e-8)
	err = s.HandlePaymentNotification(ctx, &payment.PaymentNotification{TradeNo: o.PaymentTradeNo, OrderID: o.OutTradeNo, Amount: o.PayAmount, Status: payment.NotificationStatusSuccess}, payment.TypeEasyPay)
	require.NoError(t, err)
	require.InDelta(t, o.Amount, credited, 1e-8)
	require.Len(t, repo.useCalls, 1)
}
