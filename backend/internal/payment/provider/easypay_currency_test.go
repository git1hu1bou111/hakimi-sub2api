package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestEasyPayCurrencySignedForEachNetwork(t *testing.T) {
	for _, network := range []string{"bep20", "trc20", "erc20", "polygon"} {
		t.Run(network, func(t *testing.T) {
			p := newTestEasyPay(t, "https://pay.example.com")
			p.config["paymentMode"] = paymentModePopup
			p.config["currency"] = "USD"
			p.config["customMethods"] = `[{"type":"usdt_` + network + `","upstreamType":"usdt.` + network + `"}]`
			resp, err := p.CreatePayment(context.Background(), payment.CreatePaymentRequest{
				OrderID: "usd-order", Amount: "100.00", PaymentType: "usdt_" + network, Subject: "Balance recharge",
			})
			require.NoError(t, err)
			u, err := url.Parse(resp.PayURL)
			require.NoError(t, err)
			q := u.Query()
			require.Equal(t, "USD", q.Get("fiat"))
			require.Equal(t, "100.00", q.Get("money"))
			require.Equal(t, "usdt."+network, q.Get("type"))
			params := map[string]string{}
			for k := range q {
				params[k] = q.Get(k)
			}
			require.Equal(t, q.Get("sign"), easyPaySign(params, p.config["pkey"]))
			params["fiat"] = "CNY"
			require.NotEqual(t, q.Get("sign"), easyPaySign(params, p.config["pkey"]), "currency cannot be changed without invalidating the signature")
		})
	}
}

func TestEasyPayLegacyRedirectOmitsFiat(t *testing.T) {
	for _, currency := range []string{"", "CNY"} {
		t.Run("currency="+currency, func(t *testing.T) {
			p := newTestEasyPay(t, "https://pay.example.com")
			p.config["paymentMode"] = paymentModePopup
			p.config["currency"] = currency
			resp, err := p.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "old-order", Amount: "100.00", PaymentType: "alipay"})
			require.NoError(t, err)
			u, err := url.Parse(resp.PayURL)
			require.NoError(t, err)
			require.False(t, u.Query().Has("fiat"))
		})
	}
}

func TestEasyPayAPICurrencyAndNormalization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		require.Equal(t, "USD", r.Form.Get("fiat"))
		require.Equal(t, "100.00", r.Form.Get("money"))
		params := map[string]string{}
		for k := range r.Form {
			params[k] = r.Form.Get(k)
		}
		require.Equal(t, r.Form.Get("sign"), easyPaySign(params, "test-secret"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"trade_no":"test-trade","payurl":"https://pay.example.com/checkout"}`))
	}))
	defer server.Close()
	cfg := map[string]string{"pid": "test", "pkey": "test-secret", "apiBase": server.URL,
		"notifyUrl": "https://example.com/notify", "returnUrl": "https://example.com/return", "currency": " usd "}
	p, err := NewEasyPay("usd", cfg)
	require.NoError(t, err)
	_, err = p.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "api-order", Amount: "100.00", PaymentType: "usdt"})
	require.NoError(t, err)
	cfg["currency"] = "USDT"
	_, err = NewEasyPay("invalid", cfg)
	require.Error(t, err, "token ticker must not be accepted as fiat currency")
}

func TestUSDTFixedRateSignedAndEnforced(t *testing.T) {
	p := newTestEasyPay(t, "https://pay.example.com")
	p.config["paymentMode"] = "popup"
	p.config["currency"] = "USD"
	p.config["usdtRecharge"] = "true"
	p.config["upstreamVerifyMode"] = "dry_run"
	p.config["customMethods"] = `[{"type":"usdt_bep20","upstreamType":"usdt.bep20"}]`
	require.Equal(t, easyPayUpstreamVerifyEnforce, p.upstreamVerifyMode())
	result, err := p.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "usdt", Amount: "10.00", PaymentType: "usdt_bep20", CryptoRate: "1"})
	require.NoError(t, err)
	u, err := url.Parse(result.PayURL)
	require.NoError(t, err)
	params := map[string]string{}
	for k := range u.Query() {
		params[k] = u.Query().Get(k)
	}
	require.Equal(t, "1", params["rate"])
	require.Equal(t, "USD", params["fiat"])
	require.Equal(t, params["sign"], easyPaySign(params, p.config["pkey"]))
	params["rate"] = "0.1"
	require.NotEqual(t, params["sign"], easyPaySign(params, p.config["pkey"]))
	_, err = p.CreatePayment(context.Background(), payment.CreatePaymentRequest{Amount: "10.00", PaymentType: "alipay", CryptoRate: "1"})
	require.Error(t, err)
	_, err = p.CreatePayment(context.Background(), payment.CreatePaymentRequest{Amount: "10.00", PaymentType: "usdt_bep20", CryptoRate: "0.1"})
	require.Error(t, err)
}

func TestUSDTConfirmationUsesNativePaidOrderAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(map[string]any)
		httpStatus int
		wantErr    bool
	}{
		{name: "paid", httpStatus: 200},
		{name: "unpaid", httpStatus: 200, mutate: func(d map[string]any) { d["status"] = 1 }, wantErr: true},
		{name: "different order", httpStatus: 200, mutate: func(d map[string]any) { d["order_id"] = "other" }, wantErr: true},
		{name: "missing amount", httpStatus: 200, mutate: func(d map[string]any) { delete(d, "money") }, wantErr: true},
		{name: "different amount", httpStatus: 200, mutate: func(d map[string]any) { d["money"] = "1" }, wantErr: true},
		{name: "wrong fiat", httpStatus: 200, mutate: func(d map[string]any) { d["fiat"] = "CNY" }, wantErr: true},
		{name: "wrong crypto", httpStatus: 200, mutate: func(d map[string]any) { d["trade_type"] = "ethereum.eth" }, wantErr: true},
		{name: "unreachable", httpStatus: 503, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := map[string]any{"order_id": "order", "trade_id": "trade", "money": "10", "status": 2, "fiat": "USD", "trade_type": "usdt.polygon"}
			if tc.mutate != nil {
				tc.mutate(data)
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodPost, r.Method)
				require.Equal(t, "/info", r.URL.Path)
				w.WriteHeader(tc.httpStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": data})
			}))
			defer srv.Close()
			p := newTestEasyPay(t, srv.URL)
			p.config["usdtRecharge"] = "true"
			p.config["upstreamVerifyMode"] = "dry_run"
			p.config["upstreamVerifyURL"] = srv.URL + "/info"
			err := p.ConfirmNotification(context.Background(), &payment.PaymentNotification{OrderID: "order", TradeNo: "trade", Amount: 10, Status: payment.NotificationStatusSuccess})
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, calls)
		})
	}
}
