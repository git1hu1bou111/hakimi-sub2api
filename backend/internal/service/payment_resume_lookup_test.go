//go:build unit

package service

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type paymentResumeLookupProvider struct {
	queryCount int
}

func (p *paymentResumeLookupProvider) Name() string { return "resume-lookup-provider" }

func (p *paymentResumeLookupProvider) ProviderKey() string { return payment.TypeAlipay }

func (p *paymentResumeLookupProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay}
}

func (p *paymentResumeLookupProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}

func (p *paymentResumeLookupProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	p.queryCount++
	return &payment.QueryOrderResponse{Status: payment.ProviderStatusPending}, nil
}

func (p *paymentResumeLookupProvider) VerifyNotification(context.Context, string, map[string]string) (*payment.PaymentNotification, error) {
	panic("unexpected call")
}

func (p *paymentResumeLookupProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}

type paymentPublicReturnLookupProvider struct {
	queryCount  int
	verifyCount int
	lastRaw     string
	verifyErr   error
	queryResp   *payment.QueryOrderResponse
}

func (p *paymentPublicReturnLookupProvider) Name() string { return "public-return-provider" }

func (p *paymentPublicReturnLookupProvider) ProviderKey() string { return payment.TypeEasyPay }

func (p *paymentPublicReturnLookupProvider) SupportedTypes() []payment.PaymentType {
	return []payment.PaymentType{payment.TypeAlipay}
}

func (p *paymentPublicReturnLookupProvider) CreatePayment(context.Context, payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	panic("unexpected call")
}

func (p *paymentPublicReturnLookupProvider) QueryOrder(context.Context, string) (*payment.QueryOrderResponse, error) {
	p.queryCount++
	if p.queryResp != nil {
		return p.queryResp, nil
	}
	return &payment.QueryOrderResponse{Status: payment.ProviderStatusPending}, nil
}

func (p *paymentPublicReturnLookupProvider) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	p.verifyCount++
	p.lastRaw = rawBody
	if p.verifyErr != nil {
		return nil, p.verifyErr
	}
	values, err := url.ParseQuery(rawBody)
	if err != nil {
		return nil, err
	}
	if values.Get("status") != "" || values.Get("resume_token") != "" || values.Get("order_id") != "" {
		return nil, fmt.Errorf("local unsigned return params must be filtered before signature verification")
	}
	if values.Get("sign") != "valid-sign" {
		return nil, fmt.Errorf("invalid signature")
	}
	return &payment.PaymentNotification{
		OrderID: values.Get("out_trade_no"),
		TradeNo: values.Get("trade_no"),
		Status:  payment.ProviderStatusSuccess,
	}, nil
}

func (p *paymentPublicReturnLookupProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResponse, error) {
	panic("unexpected call")
}

func TestGetPublicOrderByResumeTokenReturnsMatchingOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("resume@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-user").
		Save(ctx)
	require.NoError(t, err)

	instanceID := "12"
	providerKey := payment.TypeEasyPay
	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-ORDER").
		SetOutTradeNo("sub2_resume_lookup").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-1").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID(instanceID).
		SetProviderKey(providerKey).
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		ProviderInstanceID: instanceID,
		ProviderKey:        providerKey,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:     client,
		resumeService: resumeSvc,
	}

	got, err := svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
}

func TestGetPublicOrderByResumeTokenRejectsSnapshotMismatch(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("resume-mismatch@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-mismatch-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-MISMATCH").
		SetOutTradeNo("sub2_resume_lookup_mismatch").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-2").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID("12").
		SetProviderKey(payment.TypeEasyPay).
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		ProviderInstanceID: "99",
		ProviderKey:        payment.TypeEasyPay,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:     client,
		resumeService: resumeSvc,
	}

	_, err = svc.GetPublicOrderByResumeToken(ctx, token)
	require.Error(t, err)
	require.Equal(t, "INVALID_RESUME_TOKEN", infraerrors.Reason(err))
}

func TestGetPublicOrderByResumeTokenUsesSnapshotAuthorityWhenColumnsDiffer(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("resume-snapshot-authority@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-snapshot-authority-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-SNAPSHOT-AUTHORITY").
		SetOutTradeNo("sub2_resume_snapshot_authority").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-snapshot-authority").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		SetProviderInstanceID("legacy-column-instance").
		SetProviderKey(payment.TypeAlipay).
		SetProviderSnapshot(map[string]any{
			"schema_version":       2,
			"provider_instance_id": "snapshot-instance",
			"provider_key":         payment.TypeEasyPay,
		}).
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		ProviderInstanceID: "snapshot-instance",
		ProviderKey:        payment.TypeEasyPay,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	svc := &PaymentService{
		entClient:     client,
		resumeService: resumeSvc,
	}

	got, err := svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
}

func TestGetPublicOrderByResumeTokenChecksUpstreamForPendingOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("resume-refresh@example.com").
		SetPasswordHash("hash").
		SetUsername("resume-refresh-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("RESUME-PENDING").
		SetOutTradeNo("sub2_resume_lookup_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-pending").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	resumeSvc := NewPaymentResumeService([]byte("0123456789abcdef0123456789abcdef"))
	token, err := resumeSvc.CreateToken(ResumeTokenClaims{
		OrderID:            order.ID,
		UserID:             user.ID,
		PaymentType:        payment.TypeAlipay,
		CanonicalReturnURL: "https://app.example.com/payment/result",
	})
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentResumeLookupProvider{}
	registry.Register(provider)

	svc := &PaymentService{
		entClient:       client,
		registry:        registry,
		resumeService:   resumeSvc,
		providersLoaded: true,
	}

	got, err := svc.GetPublicOrderByResumeToken(ctx, token)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
	require.Equal(t, 1, provider.queryCount)
}

func TestVerifyOrderPublicDoesNotCheckUpstreamForPendingOrder(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("public-verify@example.com").
		SetPasswordHash("hash").
		SetUsername("public-verify-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("PUBLIC-VERIFY").
		SetOutTradeNo("sub2_public_verify_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("trade-public-verify").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentResumeLookupProvider{}
	registry.Register(provider)

	svc := &PaymentService{
		entClient:       client,
		registry:        registry,
		providersLoaded: true,
	}

	got, err := svc.VerifyOrderPublic(ctx, order.OutTradeNo)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
	require.Equal(t, 0, provider.queryCount)
}

func TestVerifyOrderPublicWithSignedEasyPayReturnChecksUpstream(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("public-return@example.com").
		SetPasswordHash("hash").
		SetUsername("public-return-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("PUBLIC-RETURN").
		SetOutTradeNo("sub2_public_return_pending").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentPublicReturnLookupProvider{
		queryResp: &payment.QueryOrderResponse{
			TradeNo: "gateway-public-return",
			Status:  payment.ProviderStatusPaid,
			Amount:  88,
		},
	}
	registry.Register(provider)

	userRepo := &mockUserRepo{
		getByIDUser: &User{
			ID:       user.ID,
			Email:    user.Email,
			Username: user.Username,
			Balance:  0,
		},
	}
	userRepo.updateBalanceFn = func(ctx context.Context, id int64, amount float64) error {
		require.Equal(t, user.ID, id)
		if userRepo.getByIDUser != nil {
			userRepo.getByIDUser.Balance += amount
		}
		return nil
	}
	redeemRepo := &paymentOrderLifecycleRedeemRepo{
		codesByCode: map[string]*RedeemCode{
			order.RechargeCode: {
				ID:     1,
				Code:   order.RechargeCode,
				Type:   RedeemTypeBalance,
				Value:  order.Amount,
				Status: StatusUnused,
			},
		},
	}
	redeemService := NewRedeemService(
		redeemRepo,
		userRepo,
		nil,
		nil,
		nil,
		client,
		nil,
		nil,
	)

	svc := &PaymentService{
		entClient:       client,
		registry:        registry,
		redeemService:   redeemService,
		userRepo:        userRepo,
		providersLoaded: true,
	}

	returnParams := map[string]string{
		"pid":          "1001",
		"type":         "alipay",
		"out_trade_no": order.OutTradeNo,
		"trade_no":     "gateway-public-return",
		"money":        "88.00",
		"trade_status": "TRADE_SUCCESS",
		"sign":         "valid-sign",
		"sign_type":    "MD5",
		"status":       "success",
		"resume_token": "local-resume-token",
		"order_id":     "42",
	}
	got, err := svc.VerifyOrderPublicWithReturnParams(ctx, order.OutTradeNo, returnParams)
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
	require.Equal(t, OrderStatusCompleted, got.Status)
	require.Equal(t, 1, provider.verifyCount)
	require.Equal(t, 1, provider.queryCount)
	require.Equal(t, 88.0, userRepo.getByIDUser.Balance)
	require.Len(t, redeemRepo.useCalls, 1)
	require.NotContains(t, provider.lastRaw, "status=success")
	require.NotContains(t, provider.lastRaw, "resume_token=")
	require.NotContains(t, provider.lastRaw, "order_id=")

	got, err = svc.VerifyOrderPublicWithReturnParams(ctx, order.OutTradeNo, returnParams)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, got.Status)
	require.Equal(t, 1, provider.verifyCount, "completed public return lookup must not re-verify signed return")
	require.Equal(t, 1, provider.queryCount, "completed public return lookup must not query upstream again")
	require.Equal(t, 88.0, userRepo.getByIDUser.Balance, "repeat public return lookup must not credit again")
	require.Len(t, redeemRepo.useCalls, 1, "repeat public return lookup must not redeem again")
}

func TestVerifyOrderPublicWithInvalidEasyPayReturnDoesNotCheckUpstream(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	user, err := client.User.Create().
		SetEmail("public-return-invalid@example.com").
		SetPasswordHash("hash").
		SetUsername("public-return-invalid-user").
		Save(ctx)
	require.NoError(t, err)

	order, err := client.PaymentOrder.Create().
		SetUserID(user.ID).
		SetUserEmail(user.Email).
		SetUserName(user.Username).
		SetAmount(88).
		SetPayAmount(88).
		SetFeeRate(0).
		SetRechargeCode("PUBLIC-RETURN-INVALID").
		SetOutTradeNo("sub2_public_return_invalid").
		SetPaymentType(payment.TypeAlipay).
		SetPaymentTradeNo("").
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").
		SetSrcHost("api.example.com").
		Save(ctx)
	require.NoError(t, err)

	registry := payment.NewRegistry()
	provider := &paymentPublicReturnLookupProvider{}
	registry.Register(provider)

	svc := &PaymentService{
		entClient:       client,
		registry:        registry,
		providersLoaded: true,
	}

	got, err := svc.VerifyOrderPublicWithReturnParams(ctx, order.OutTradeNo, map[string]string{
		"out_trade_no": order.OutTradeNo,
		"trade_no":     "gateway-public-return",
		"trade_status": "TRADE_SUCCESS",
		"sign":         "bad-sign",
		"sign_type":    "MD5",
	})
	require.NoError(t, err)
	require.Equal(t, order.ID, got.ID)
	require.Equal(t, 1, provider.verifyCount)
	require.Zero(t, provider.queryCount)
}

func TestVerifyOrderPublicRejectsBlankOutTradeNo(t *testing.T) {
	svc := &PaymentService{
		entClient: newPaymentConfigServiceTestClient(t),
	}

	_, err := svc.VerifyOrderPublic(context.Background(), "   ")
	require.Error(t, err)
	require.Equal(t, "INVALID_OUT_TRADE_NO", infraerrors.Reason(err))
}
