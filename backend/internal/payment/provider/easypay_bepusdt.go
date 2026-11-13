package provider

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

type bepusdtOrder struct {
	OrderID   string `json:"order_id"`
	TradeID   string `json:"trade_id"`
	Money     string `json:"money"`
	Fiat      string `json:"fiat"`
	TradeType string `json:"trade_type"`
	Status    int    `json:"status"`
}

// Submit server-side but keep the EasyPay notification format. Returning only
// the checkout URL prevents a cancelled browser URL from recreating its order.
func (e *EasyPay) createBEpusdtPayment(ctx context.Context, in payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	if in.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("USDT payment deadline is required")
	}
	redirect, err := e.createRedirectPayment(in)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(redirect.PayURL)
	if err != nil {
		return nil, err
	}
	form := u.RawQuery
	u.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := *e.httpClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("USDT order creation failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusSeeOther {
		return nil, fmt.Errorf("USDT gateway did not return a checkout (HTTP %d)", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		return nil, fmt.Errorf("invalid USDT checkout")
	}
	if location.Host != "" && !strings.EqualFold(location.Host, u.Host) {
		return nil, fmt.Errorf("USDT checkout host mismatch")
	}
	parts := strings.Split(strings.Trim(location.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "pay" || parts[1] != "checkout" || parts[2] == "" {
		return nil, fmt.Errorf("invalid USDT checkout path")
	}
	tradeID := parts[2]
	order, err := e.bepusdtOrderInfo(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	amount, err := strconv.ParseFloat(in.Amount, 64)
	actual, aerr := strconv.ParseFloat(order.Money, 64)
	if err != nil || aerr != nil || !validBEpusdtAmount(actual) || math.Abs(actual-amount) > 1e-8 || order.OrderID != in.OrderID || order.TradeType != e.upstreamPaymentType(in.PaymentType) || order.Status != 1 {
		return nil, fmt.Errorf("USDT created order binding mismatch")
	}
	// Prefer the configured HTTPS origin even when the gateway sits behind TLS termination.
	u.Path = location.Path
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return &payment.CreatePaymentResponse{TradeNo: tradeID, PayURL: u.String()}, nil
}

func validBEpusdtAmount(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func (e *EasyPay) bepusdtJSON(ctx context.Context, endpoint string, payload map[string]string, dest any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("USDT gateway request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("USDT gateway HTTP %d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxEasypayResponseSize+1))
	if err != nil {
		return err
	}
	if len(data) > maxEasypayResponseSize {
		return fmt.Errorf("USDT gateway response too large")
	}
	var envelope struct {
		StatusCode int             `json:"status_code"`
		Data       json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("invalid USDT gateway response")
	}
	if envelope.StatusCode != 200 {
		return fmt.Errorf("USDT gateway rejected operation (%d)", envelope.StatusCode)
	}
	if dest != nil {
		return json.Unmarshal(envelope.Data, dest)
	}
	return nil
}

func (e *EasyPay) bepusdtOrderInfo(ctx context.Context, tradeID string) (*bepusdtOrder, error) {
	if strings.TrimSpace(tradeID) == "" {
		return nil, fmt.Errorf("USDT gateway trade ID is missing")
	}
	var o bepusdtOrder
	if err := e.bepusdtJSON(ctx, e.config["upstreamVerifyURL"], map[string]string{"trade_id": tradeID}, &o); err != nil {
		return nil, err
	}
	if o.TradeID != tradeID || o.OrderID == "" || o.Fiat != "USD" || !strings.HasPrefix(o.TradeType, "usdt.") {
		return nil, fmt.Errorf("USDT queried order binding mismatch")
	}
	return &o, nil
}

func (e *EasyPay) queryBEpusdtOrder(ctx context.Context, tradeID string) (*payment.QueryOrderResponse, error) {
	o, err := e.bepusdtOrderInfo(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	amount, err := strconv.ParseFloat(o.Money, 64)
	if err != nil || !validBEpusdtAmount(amount) {
		return nil, fmt.Errorf("invalid USDT order amount")
	}
	status := payment.ProviderStatusPending
	switch o.Status {
	case 1, 5:
	case 2:
		status = payment.ProviderStatusPaid
	case 3, 4:
		status = payment.ProviderStatusFailed
	default:
		return nil, fmt.Errorf("unknown USDT order status")
	}
	metadata := e.MerchantIdentityMetadata()
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["bepusdt_order_id"] = o.OrderID
	metadata["bepusdt_status"] = strconv.Itoa(o.Status)
	return &payment.QueryOrderResponse{TradeNo: o.TradeID, Status: status, Amount: amount, Metadata: metadata}, nil
}

func (e *EasyPay) CancelPayment(ctx context.Context, tradeID string) error {
	if e.config["usdtRecharge"] != "true" {
		return fmt.Errorf("EasyPay cancellation unsupported")
	}
	o, err := e.bepusdtOrderInfo(ctx, tradeID)
	if err != nil {
		return err
	}
	if o.Status == 3 || o.Status == 4 {
		return nil
	}
	if o.Status != 1 {
		return fmt.Errorf("USDT order is paid or confirming; cannot cancel")
	}
	sum := md5.Sum([]byte("trade_id=" + tradeID + e.config["pkey"]))
	if err = e.bepusdtJSON(ctx, e.apiBase()+"/api/v1/order/cancel-transaction", map[string]string{"trade_id": tradeID, "signature": hex.EncodeToString(sum[:])}, nil); err != nil {
		return err
	}
	o, err = e.bepusdtOrderInfo(ctx, tradeID)
	if err != nil {
		return err
	}
	if o.Status != 3 && o.Status != 4 {
		return fmt.Errorf("USDT cancellation was not confirmed")
	}
	return nil
}
