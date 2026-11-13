package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func isUSDTOrder(o *dbent.PaymentOrder) bool {
	return o != nil && strings.HasPrefix(o.PaymentType, "usdt_")
}

func usdtNeedsPaymentReview(o *dbent.PaymentOrder, now time.Time) bool {
	return isUSDTOrder(o) && (o.Status == OrderStatusCancelled || o.Status == OrderStatusExpired || (o.PaidAt == nil && (o.Status == OrderStatusPending || o.Status == OrderStatusFailed) && !o.ExpiresAt.After(now)))
}

func (s *PaymentService) requireUSDTReview(ctx context.Context, o *dbent.PaymentOrder, tradeNo string) error {
	s.writeAuditLog(ctx, o.ID, "PAYMENT_REVIEW_REQUIRED", "system", map[string]any{"detail": "USDT payment on a closed/expired order requires ownership review; automatic credit blocked", "status": o.Status, "tradeNo": tradeNo})
	return fmt.Errorf("USDT payment requires manual review: order is cancelled or expired")
}

// Do not tell the customer cancellation succeeded while the shared-address
// gateway can still match another customer's payment to this order.
func (s *PaymentService) closeUSDTOrder(ctx context.Context, o *dbent.PaymentOrder) (bool, error) {
	prov, err := s.getOrderProvider(ctx, o)
	if err != nil {
		return false, err
	}
	ref := paymentOrderQueryReference(o, prov)
	if ref == "" {
		return false, fmt.Errorf("USDT legacy order has no gateway trade ID; manual reconciliation required")
	}
	resp, err := prov.QueryOrder(ctx, ref)
	if err != nil {
		return false, fmt.Errorf("cannot confirm USDT payment state; retry cancellation: %w", err)
	}
	if err = validateUSDTQueryBinding(o, resp); err != nil {
		return false, err
	}
	if resp.Status == payment.ProviderStatusPaid {
		return s.fulfillQueriedUSDT(ctx, o, prov, resp)
	}
	if resp.Metadata["bepusdt_status"] == "3" || resp.Metadata["bepusdt_status"] == "4" {
		return false, nil
	}
	cp, ok := prov.(payment.CancelableProvider)
	if !ok {
		return false, fmt.Errorf("USDT gateway cancellation unsupported")
	}
	if err = cp.CancelPayment(ctx, ref); err != nil {
		// Payment may have won the race. Only a verified paid response can
		// settle it; uncertainty leaves the local order unchanged.
		again, qerr := prov.QueryOrder(ctx, ref)
		if qerr == nil && validateUSDTQueryBinding(o, again) == nil && again.Status == payment.ProviderStatusPaid {
			return s.fulfillQueriedUSDT(ctx, o, prov, again)
		}
		return false, fmt.Errorf("USDT gateway did not confirm cancellation: %w", err)
	}
	return false, nil
}

func validateUSDTQueryBinding(o *dbent.PaymentOrder, r *payment.QueryOrderResponse) error {
	if r == nil || r.TradeNo != o.PaymentTradeNo || r.Metadata["bepusdt_order_id"] != o.OutTradeNo {
		return fmt.Errorf("USDT upstream order binding mismatch")
	}
	return nil
}

func (s *PaymentService) fulfillQueriedUSDT(ctx context.Context, o *dbent.PaymentOrder, p payment.Provider, r *payment.QueryOrderResponse) (bool, error) {
	err := s.HandlePaymentNotification(ctx, &payment.PaymentNotification{TradeNo: r.TradeNo, OrderID: o.OutTradeNo, Amount: r.Amount, Status: payment.NotificationStatusSuccess, Metadata: r.Metadata}, p.ProviderKey())
	return err == nil, err
}
