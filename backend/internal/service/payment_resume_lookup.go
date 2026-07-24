package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var paymentPublicReturnUnsignedParams = map[string]struct{}{
	"order_id":            {},
	"resume_token":        {},
	"status":              {},
	"wechat_resume_token": {},
}

func (s *PaymentService) GetPublicOrderByResumeToken(ctx context.Context, token string) (*dbent.PaymentOrder, error) {
	claims, err := s.paymentResume().ParseToken(strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}

	order, err := s.entClient.PaymentOrder.Get(ctx, claims.OrderID)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, infraerrors.NotFound("NOT_FOUND", "order not found")
		}
		return nil, fmt.Errorf("get order by resume token: %w", err)
	}
	if claims.UserID > 0 && order.UserID != claims.UserID {
		return nil, invalidResumeTokenMatchError()
	}
	snapshot := psOrderProviderSnapshot(order)
	orderProviderInstanceID := strings.TrimSpace(psStringValue(order.ProviderInstanceID))
	orderProviderKey := strings.TrimSpace(psStringValue(order.ProviderKey))
	if snapshot != nil {
		if snapshot.ProviderInstanceID != "" {
			orderProviderInstanceID = snapshot.ProviderInstanceID
		}
		if snapshot.ProviderKey != "" {
			orderProviderKey = snapshot.ProviderKey
		}
	}
	if claims.ProviderInstanceID != "" && orderProviderInstanceID != claims.ProviderInstanceID {
		return nil, invalidResumeTokenMatchError()
	}
	if claims.ProviderKey != "" && !strings.EqualFold(orderProviderKey, claims.ProviderKey) {
		return nil, invalidResumeTokenMatchError()
	}
	if claims.PaymentType != "" && NormalizeVisibleMethod(order.PaymentType) != NormalizeVisibleMethod(claims.PaymentType) {
		return nil, invalidResumeTokenMatchError()
	}
	if paymentOrderCanRecoverFromUpstreamPaid(order.Status) {
		result := s.reconcilePaid(ctx, order)
		if result == checkPaidResultAlreadyPaid {
			order, err = s.entClient.PaymentOrder.Get(ctx, order.ID)
			if err != nil {
				return nil, fmt.Errorf("reload order by resume token: %w", err)
			}
		}
	}

	return order, nil
}

// VerifyOrderPublicWithReturnParams keeps the legacy public result-page lookup
// safe while allowing EasyPay-compatible signed return_url payloads to recover
// missed async notify callbacks. The return_url payload is used only as proof
// that the browser came back from the gateway; the persisted payment state is
// still updated through an authoritative upstream QueryOrder call.
func (s *PaymentService) VerifyOrderPublicWithReturnParams(ctx context.Context, outTradeNo string, returnParams map[string]string) (*dbent.PaymentOrder, error) {
	order, err := s.VerifyOrderPublic(ctx, outTradeNo)
	if err != nil {
		return nil, err
	}
	if !paymentOrderCanRecoverFromUpstreamPaid(order.Status) {
		return order, nil
	}
	if !s.verifySignedEasyPayReturnParams(ctx, order, returnParams) {
		return order, nil
	}
	if result := s.reconcilePaid(ctx, order); result == checkPaidResultAlreadyPaid {
		reloaded, reloadErr := s.entClient.PaymentOrder.Get(ctx, order.ID)
		if reloadErr != nil {
			return nil, fmt.Errorf("reload public verified order: %w", reloadErr)
		}
		return reloaded, nil
	}
	return order, nil
}

func (s *PaymentService) verifySignedEasyPayReturnParams(ctx context.Context, order *dbent.PaymentOrder, returnParams map[string]string) bool {
	if order == nil || len(returnParams) == 0 {
		return false
	}
	params := normalizePaymentPublicReturnParams(returnParams)
	if params["sign"] == "" || params["out_trade_no"] == "" {
		return false
	}
	if params["out_trade_no"] != order.OutTradeNo {
		return false
	}

	prov, err := s.getOrderProvider(ctx, order)
	if err != nil {
		slog.Warn("public payment return provider lookup failed", "orderID", order.ID, "error", err)
		return false
	}
	if prov == nil || prov.ProviderKey() != payment.TypeEasyPay {
		return false
	}

	notification, err := prov.VerifyNotification(ctx, encodePaymentPublicReturnParams(params), nil)
	if err != nil {
		slog.Warn("public EasyPay return signature verification failed", "orderID", order.ID, "error", err)
		return false
	}
	if notification == nil {
		return false
	}
	if strings.TrimSpace(notification.OrderID) != order.OutTradeNo {
		return false
	}
	return notification.Status == payment.ProviderStatusSuccess || notification.Status == payment.NotificationStatusSuccess
}

func normalizePaymentPublicReturnParams(returnParams map[string]string) map[string]string {
	params := make(map[string]string, len(returnParams))
	for key, value := range returnParams {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := paymentPublicReturnUnsignedParams[key]; ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		params[key] = value
	}
	return params
}

func encodePaymentPublicReturnParams(params map[string]string) string {
	values := url.Values{}
	for key, value := range params {
		values.Set(key, value)
	}
	return values.Encode()
}

func invalidResumeTokenMatchError() error {
	return infraerrors.BadRequest("INVALID_RESUME_TOKEN", "resume token does not match the payment order")
}

func (s *PaymentService) ParseWeChatPaymentResumeToken(token string) (*WeChatPaymentResumeClaims, error) {
	return s.paymentResume().ParseWeChatPaymentResumeToken(strings.TrimSpace(token))
}
