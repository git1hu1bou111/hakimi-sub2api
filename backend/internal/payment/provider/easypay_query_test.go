package provider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

func TestEasyPayQueryOrderStatusMapping(t *testing.T) {
	t.Parallel()

	const orderID = "order-123"
	tests := []struct {
		name        string
		body        string
		wantStatus  string
		wantTradeNo string
		wantAmount  float64
	}{
		{
			name:        "top level trade success is paid",
			body:        `{"code":1,"trade_status":"TRADE_SUCCESS","status":0,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "waiting trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"WAITING","status":1,"money":"12.34","trade_no":"gateway-123"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: "gateway-123",
			wantAmount:  12.34,
		},
		{
			name:        "empty trade status with paid numeric status stays pending",
			body:        `{"code":1,"trade_status":"","status":1,"money":"12.34"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  12.34,
		},
		{
			name:        "nested data trade success is paid",
			body:        `{"code":1,"data":{"trade_status":"TRADE_SUCCESS","status":0,"money":"9.99","trade_no":"data-456"}}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "data-456",
			wantAmount:  9.99,
		},
		{
			name:        "legacy numeric paid status remains compatible",
			body:        `{"code":1,"status":1,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "legacy string paid status remains compatible",
			body:        `{"code":"1","status":"1","money":6.66,"trade_no":"gateway-string-status"}`,
			wantStatus:  payment.ProviderStatusPaid,
			wantTradeNo: "gateway-string-status",
			wantAmount:  6.66,
		},
		{
			name:        "non success code does not trust paid numeric status",
			body:        `{"code":0,"status":1,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "legacy numeric non paid status is pending",
			body:        `{"code":1,"status":0,"money":"3.21"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
			wantAmount:  3.21,
		},
		{
			name:        "query failure with missing status is pending",
			body:        `{"code":0,"msg":"订单不存在"}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
		{
			name:        "missing fields are pending",
			body:        `{}`,
			wantStatus:  payment.ProviderStatusPending,
			wantTradeNo: orderID,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotForm url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet && r.Method != http.MethodPost {
					t.Errorf("method = %q, want GET or POST", r.Method)
				}
				if r.URL.Path != "/api.php" {
					t.Errorf("path = %q, want /api.php", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				gotForm = make(url.Values, len(r.Form))
				for key, values := range r.Form {
					gotForm[key] = append([]string(nil), values...)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			provider := newTestEasyPay(t, server.URL)
			resp, err := provider.QueryOrder(context.Background(), orderID)
			if err != nil {
				t.Fatalf("QueryOrder returned error: %v", err)
			}
			if resp.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (response=%+v)", resp.Status, tt.wantStatus, resp)
			}
			if resp.TradeNo != tt.wantTradeNo {
				t.Fatalf("trade_no = %q, want %q", resp.TradeNo, tt.wantTradeNo)
			}
			if resp.Amount != tt.wantAmount {
				t.Fatalf("amount = %v, want %v", resp.Amount, tt.wantAmount)
			}
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := gotForm.Get(key); got != want {
					t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, gotForm)
				}
			}
		})
	}
}

func TestEasyPayQueryOrderFallsBackToPostWhenDocumentedGetIsNotJSON(t *testing.T) {
	t.Parallel()

	const orderID = "order-post-fallback"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.URL.Path != "/api.php" {
			t.Errorf("path = %q, want /api.php", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html>method not allowed</html>"))
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				t.Errorf("ParseForm: %v", err)
			}
			for key, want := range map[string]string{
				"act":          "order",
				"pid":          "pid-1",
				"key":          "pkey-1",
				"out_trade_no": orderID,
			} {
				if got := r.PostForm.Get(key); got != want {
					t.Fatalf("form[%s] = %q, want %q (form=%v)", key, got, want, r.PostForm)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":1,"status":1,"money":"1.23","trade_no":"fallback-trade"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL)
	resp, err := provider.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("status = %q, want %q", resp.Status, payment.ProviderStatusPaid)
	}
	if resp.TradeNo != "fallback-trade" {
		t.Fatalf("trade_no = %q, want fallback-trade", resp.TradeNo)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Fatalf("methods = %v, want [GET POST]", methods)
	}
}

func TestEasyPayQueryOrderFallsBackToPostWhenDocumentedGetReturnsJSONFailure(t *testing.T) {
	t.Parallel()

	const orderID = "order-json-fallback"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"code":0,"msg":"order not found on GET"}`))
		case http.MethodPost:
			_, _ = w.Write([]byte(`{"code":1,"status":1,"money":"4.56","trade_no":"json-fallback-trade"}`))
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	provider := newTestEasyPay(t, server.URL)
	resp, err := provider.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("status = %q, want %q", resp.Status, payment.ProviderStatusPaid)
	}
	if resp.TradeNo != "json-fallback-trade" {
		t.Fatalf("trade_no = %q, want json-fallback-trade", resp.TradeNo)
	}
	if len(methods) != 2 || methods[0] != http.MethodGet || methods[1] != http.MethodPost {
		t.Fatalf("methods = %v, want [GET POST]", methods)
	}
}

func TestEasyPayV2QueryIsConfigurationDrivenNotDomainDriven(t *testing.T) {
	t.Parallel()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":       "pid-1",
		"pkey":      "pkey-1",
		"apiBase":   "https://pay.v8jisu.cn",
		"notifyUrl": "https://example.com/notify",
		"returnUrl": "https://example.com/return",
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}
	if provider.shouldUseV2Query() {
		t.Fatal("V2 query should be enabled by query config, not by a hardcoded EasyPay domain")
	}

	provider.config["queryApiPath"] = "/api/pay/query"
	if !provider.shouldUseV2Query() {
		t.Fatal("V2 query should be enabled when queryApiPath is configured")
	}
}

func TestEasyPayAutomaticReconciliationIsLimitedToV8Jisu(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		apiBase string
		want    bool
	}{
		{name: "exact https host", apiBase: "https://pay.v8jisu.cn/", want: true},
		{name: "host with explicit port", apiBase: "https://pay.v8jisu.cn:443/mapi.php", want: true},
		{name: "trailing root dot", apiBase: "https://pay.v8jisu.cn./", want: true},
		{name: "http is rejected", apiBase: "http://pay.v8jisu.cn/", want: false},
		{name: "subdomain is rejected", apiBase: "https://api.pay.v8jisu.cn/", want: false},
		{name: "suffix lookalike is rejected", apiBase: "https://pay.v8jisu.cn.example.com/", want: false},
		{name: "different gateway is rejected", apiBase: "https://pay.example.com/", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider, err := NewEasyPay("test-instance", map[string]string{
				"pid":       "pid-1",
				"pkey":      "pkey-1",
				"apiBase":   tt.apiBase,
				"notifyUrl": "https://example.com/notify",
				"returnUrl": "https://example.com/return",
			})
			if err != nil {
				t.Fatalf("NewEasyPay: %v", err)
			}
			if got := provider.AutomaticReconciliationEnabled(); got != tt.want {
				t.Fatalf("AutomaticReconciliationEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEasyPayQueryOrderUsesV2PayQueryEndpointWithMD5(t *testing.T) {
	t.Parallel()

	const orderID = "order-v2-md5"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/pay/query" {
			t.Errorf("path = %q, want /api/pay/query", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("pid"); got != "pid-1" {
			t.Fatalf("pid = %q, want pid-1", got)
		}
		if got := r.PostForm.Get("out_trade_no"); got != orderID {
			t.Fatalf("out_trade_no = %q, want %q", got, orderID)
		}
		if got := r.PostForm.Get("sign_type"); got != signTypeMD5 {
			t.Fatalf("sign_type = %q, want MD5", got)
		}
		params := formToStringMap(r.PostForm)
		if got, want := r.PostForm.Get("sign"), easyPaySign(params, "pkey-1"); got != want {
			t.Fatalf("sign = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"trade_no":"platform-123","out_trade_no":"order-v2-md5","api_trade_no":"api-456","status":1,"pid":1001,"money":"7.89"}`))
	}))
	defer server.Close()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       server.URL,
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"queryApiPath":  "/api/pay/query",
		"querySignType": signTypeMD5,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPaid {
		t.Fatalf("status = %q, want %q", resp.Status, payment.ProviderStatusPaid)
	}
	if resp.TradeNo != "api-456" {
		t.Fatalf("trade_no = %q, want api-456", resp.TradeNo)
	}
	if resp.Amount != 7.89 {
		t.Fatalf("amount = %v, want 7.89", resp.Amount)
	}
	if resp.Metadata["pid"] != "1001" {
		t.Fatalf("metadata pid = %q, want 1001", resp.Metadata["pid"])
	}
}

func TestEasyPayQueryOrderUsesV2PayQueryEndpointWithRSA(t *testing.T) {
	t.Parallel()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))

	const orderID = "order-v2-rsa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pay/query" {
			t.Errorf("path = %q, want /api/pay/query", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		params := formToStringMap(r.PostForm)
		if got := r.PostForm.Get("sign_type"); got != signTypeRSA {
			t.Fatalf("sign_type = %q, want RSA", got)
		}
		if err := verifyEasyPayRSA(easyPayCanonicalString(params), r.PostForm.Get("sign"), &key.PublicKey, signTypeRSA); err != nil {
			t.Fatalf("invalid RSA query signature: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"trade_no":"platform-rsa","out_trade_no":"order-v2-rsa","status":0,"pid":"pid-1","money":"0.00"}`))
	}))
	defer server.Close()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":             "pid-1",
		"pkey":            "pkey-1",
		"apiBase":         server.URL,
		"notifyUrl":       "https://example.com/notify",
		"returnUrl":       "https://example.com/return",
		"queryApiPath":    "/api/pay/query",
		"querySignType":   signTypeRSA,
		"queryPrivateKey": privatePEM,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	resp, err := provider.QueryOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("QueryOrder returned error: %v", err)
	}
	if resp.Status != payment.ProviderStatusPending {
		t.Fatalf("status = %q, want %q", resp.Status, payment.ProviderStatusPending)
	}
	if resp.TradeNo != "platform-rsa" {
		t.Fatalf("trade_no = %q, want platform-rsa", resp.TradeNo)
	}
}

func TestEasyPayV2QueryRejectsMismatchedOutTradeNo(t *testing.T) {
	t.Parallel()

	const orderID = "order-v2-safe"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"trade_no":"platform-other","out_trade_no":"another-order","status":1,"pid":"pid-1","money":"7.89"}`))
	}))
	defer server.Close()

	provider, err := NewEasyPay("test-instance", map[string]string{
		"pid":           "pid-1",
		"pkey":          "pkey-1",
		"apiBase":       server.URL,
		"notifyUrl":     "https://example.com/notify",
		"returnUrl":     "https://example.com/return",
		"queryApiPath":  "/api/pay/query",
		"querySignType": signTypeMD5,
	})
	if err != nil {
		t.Fatalf("NewEasyPay: %v", err)
	}

	_, err = provider.QueryOrder(context.Background(), orderID)
	if err == nil {
		t.Fatal("QueryOrder returned nil error for mismatched out_trade_no")
	}
}

func formToStringMap(form url.Values) map[string]string {
	params := make(map[string]string, len(form))
	for key := range form {
		params[key] = form.Get(key)
	}
	return params
}
