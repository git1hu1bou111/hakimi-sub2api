package provider

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestBEpusdtCreatesBoundCheckoutAndClosesNativeOrder(t *testing.T) {
	for _, network := range []string{"bep20", "polygon", "erc20", "trc20"} {
		t.Run(network, func(t *testing.T) {
			status := 1
			submitCalls := 0
			cancelCalls := 0
			var p *EasyPay
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/submit.php":
					submitCalls++
					require.Equal(t, http.MethodPost, r.Method)
					require.NoError(t, r.ParseForm())
					params := map[string]string{}
					for k := range r.PostForm {
						params[k] = r.PostForm.Get(k)
					}
					require.Equal(t, easyPaySign(params, p.config["pkey"]), params["sign"])
					require.Equal(t, "USD", params["fiat"])
					require.Equal(t, "1", params["rate"])
					require.Equal(t, "usdt."+network, params["type"])
					seconds, err := strconv.Atoi(params["timeout"])
					require.NoError(t, err)
					require.GreaterOrEqual(t, seconds, 120)
					require.LessOrEqual(t, seconds, 300)
					http.Redirect(w, r, "/pay/checkout/native-trade", http.StatusFound)
				case "/api/v1/pay/info":
					var body map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					require.Equal(t, "native-trade", body["trade_id"])
					fmt.Fprintf(w, `{"status_code":200,"data":{"order_id":"site-order","trade_id":"native-trade","money":"50","fiat":"USD","trade_type":"usdt.%s","status":%d}}`, network, status)
				case "/api/v1/order/cancel-transaction":
					cancelCalls++
					var body map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					sum := md5.Sum([]byte("trade_id=native-trade" + p.config["pkey"]))
					require.Equal(t, hex.EncodeToString(sum[:]), body["signature"])
					status = 4
					fmt.Fprint(w, `{"status_code":200,"data":{}}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			p = newTestEasyPay(t, srv.URL)
			p.config["currency"] = "USD"
			p.config["usdtRecharge"] = "true"
			p.config["paymentMode"] = "popup"
			p.config["upstreamVerifyURL"] = srv.URL + "/api/v1/pay/info"
			p.config["customMethods"] = fmt.Sprintf(`[{"type":"usdt_%s","upstreamType":"usdt.%s"}]`, network, network)
			result, err := p.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "site-order", Amount: "50", CryptoRate: "1", PaymentType: "usdt_" + network, ExpiresAt: time.Now().Add(5 * time.Minute)})
			require.NoError(t, err)
			require.Equal(t, "native-trade", result.TradeNo)
			require.Equal(t, srv.URL+"/pay/checkout/native-trade", result.PayURL)
			require.Equal(t, 1, submitCalls)
			queried, err := p.QueryOrder(context.Background(), result.TradeNo)
			require.NoError(t, err)
			require.Equal(t, "site-order", queried.Metadata["bepusdt_order_id"])
			require.NoError(t, p.CancelPayment(context.Background(), result.TradeNo))
			require.NoError(t, p.CancelPayment(context.Background(), result.TradeNo))
			require.Equal(t, 1, cancelCalls)
			status = 2
			queried, err = p.QueryOrder(context.Background(), result.TradeNo)
			require.NoError(t, err)
			require.Equal(t, payment.ProviderStatusPaid, queried.Status)
			require.Error(t, p.CancelPayment(context.Background(), result.TradeNo))
			require.Equal(t, 1, cancelCalls)
		})
	}
}

func TestBEpusdtRejectsUnconfirmedCancellationAndBindingMismatch(t *testing.T) {
	for _, mode := range []string{"wrong_order", "wrong_trade", "wrong_currency", "unknown_status", "cancel_failure", "still_waiting"} {
		t.Run(mode, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/order/cancel-transaction" {
					if mode == "cancel_failure" {
						fmt.Fprint(w, `{"status_code":400}`)
					} else {
						fmt.Fprint(w, `{"status_code":200}`)
					}
					return
				}
				if r.URL.Path == "/submit.php" {
					http.Redirect(w, r, "/pay/checkout/trade", 302)
					return
				}
				o := map[string]any{"order_id": "site", "trade_id": "trade", "money": "50", "fiat": "USD", "trade_type": "usdt.bep20", "status": 1}
				switch mode {
				case "wrong_order":
					o["order_id"] = "other"
				case "wrong_trade":
					o["trade_id"] = "other"
				case "wrong_currency":
					o["fiat"] = "CNY"
				case "unknown_status":
					o["status"] = 99
				}
				json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "data": o})
			}))
			defer srv.Close()
			p := newTestEasyPay(t, srv.URL)
			p.config["currency"] = "USD"
			p.config["usdtRecharge"] = "true"
			p.config["upstreamVerifyURL"] = srv.URL + "/api/v1/pay/info"
			p.config["customMethods"] = `[{"type":"usdt_bep20","upstreamType":"usdt.bep20"}]`
			if mode == "wrong_order" {
				_, err := p.CreatePayment(context.Background(), payment.CreatePaymentRequest{OrderID: "site", Amount: "50", CryptoRate: "1", PaymentType: "usdt_bep20", ExpiresAt: time.Now().Add(5 * time.Minute)})
				require.Error(t, err)
			} else {
				require.Error(t, p.CancelPayment(context.Background(), "trade"))
			}
		})
	}
}
