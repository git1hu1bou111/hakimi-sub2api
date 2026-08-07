// Package provider contains concrete payment provider implementations.
package provider

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/payment"
)

// EasyPay constants.
const (
	easypayCodeSuccess     = 1
	easypayV2CodeSuccess   = 0
	easypayStatusPaid      = 1
	easypayStatusRefunded  = 2
	easypayHTTPTimeout     = 10 * time.Second
	maxEasypayResponseSize = 1 << 20 // 1MB
	maxEasypayErrorSummary = 512
	tradeStatusSuccess     = "TRADE_SUCCESS"
	signTypeMD5            = "MD5"
	signTypeRSA            = "RSA"
	signTypeRSA2           = "RSA2"
	paymentModePopup       = "popup"
	deviceMobile           = "mobile"

	easyPayAutomaticReconcileHost = "pay.v8jisu.cn"
	easypayV2QueryPath            = "/api/pay/query"
)

// EasyPay implements payment.Provider for the EasyPay aggregation platform.
type EasyPay struct {
	instanceID string
	config     map[string]string
	httpClient *http.Client
}

type easyPayCustomMethod struct {
	Type         string `json:"type"`
	UpstreamType string `json:"upstreamType"`
	DisplayName  string `json:"displayName"`
}

// NewEasyPay creates a new EasyPay provider.
// config keys: pid, pkey, apiBase, notifyUrl, returnUrl, cid, cidAlipay,
// cidWxpay. Optional query* keys enable the newer form POST order query for
// EasyPay-compatible gateways that support /api/pay/query:
// queryEndpoint/queryApiPath, querySignType (MD5/RSA/RSA2), queryPrivateKey,
// queryPublicKey, queryApiVersion.
func NewEasyPay(instanceID string, config map[string]string) (*EasyPay, error) {
	for _, k := range []string{"pid", "pkey", "apiBase", "notifyUrl", "returnUrl"} {
		if strings.TrimSpace(config[k]) == "" {
			return nil, fmt.Errorf("easypay config missing required key: %s", k)
		}
	}
	cfg := make(map[string]string, len(config))
	for k, v := range config {
		cfg[k] = v
	}
	cfg["apiBase"] = normalizeEasyPayAPIBase(cfg["apiBase"])
	return &EasyPay{
		instanceID: instanceID,
		config:     cfg,
		httpClient: &http.Client{Timeout: easypayHTTPTimeout},
	}, nil
}

func normalizeEasyPayAPIBase(apiBase string) string {
	base := strings.TrimSpace(apiBase)
	if base == "" {
		return ""
	}
	if parsed, err := url.Parse(base); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.RawPath = ""
		parsed.Path = trimEasyPayEndpointPath(parsed.Path)
		return strings.TrimRight(parsed.String(), "/")
	}
	return strings.TrimRight(trimEasyPayEndpointPath(base), "/")
}

func trimEasyPayEndpointPath(path string) string {
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	lower := strings.ToLower(path)
	for _, endpoint := range []string{"/submit.php", "/mapi.php", "/api.php"} {
		if strings.HasSuffix(lower, endpoint) {
			return strings.TrimRight(path[:len(path)-len(endpoint)], "/")
		}
	}
	return path
}

func (e *EasyPay) apiBase() string {
	if e == nil {
		return ""
	}
	return normalizeEasyPayAPIBase(e.config["apiBase"])
}

// AutomaticReconciliationEnabled limits frequent upstream polling to the
// EasyPay instance whose query behavior is verified for this deployment.
func (e *EasyPay) AutomaticReconciliationEnabled() bool {
	if e == nil {
		return false
	}
	parsed, err := url.Parse(e.apiBase())
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	host := strings.TrimSuffix(strings.TrimSpace(parsed.Hostname()), ".")
	return strings.EqualFold(host, easyPayAutomaticReconcileHost)
}

func (e *EasyPay) Name() string        { return "EasyPay" }
func (e *EasyPay) ProviderKey() string { return payment.TypeEasyPay }
func (e *EasyPay) SupportedTypes() []payment.PaymentType {
	types := []payment.PaymentType{payment.TypeAlipay, payment.TypeWxpay}
	for _, method := range e.customMethods() {
		if method.Type != "" {
			types = append(types, method.Type)
		}
	}
	return types
}

func (e *EasyPay) MerchantIdentityMetadata() map[string]string {
	if e == nil {
		return nil
	}
	pid := strings.TrimSpace(e.config["pid"])
	if pid == "" {
		return nil
	}
	return map[string]string{"pid": pid}
}

func (e *EasyPay) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	// Payment mode determined by instance config, not payment type.
	// "popup" → hosted page (submit.php); "qrcode"/default → API call (mapi.php).
	mode := e.config["paymentMode"]
	if mode == paymentModePopup {
		return e.createRedirectPayment(req)
	}
	return e.createAPIPayment(ctx, req)
}

// createRedirectPayment builds a submit.php URL for browser redirect.
// No server-side API call — the user is redirected to EasyPay's hosted page.
// TradeNo is empty; it arrives via the notify callback after payment.
func (e *EasyPay) createRedirectPayment(req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount,
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	payURL := e.apiBase() + "/submit.php?" + q.Encode()
	return &payment.CreatePaymentResponse{PayURL: payURL}, nil
}

// createAPIPayment calls mapi.php to get payurl/qrcode (existing behavior).
func (e *EasyPay) createAPIPayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResponse, error) {
	notifyURL, returnURL := e.resolveURLs(req)
	paymentType := e.upstreamPaymentType(req.PaymentType)
	params := map[string]string{
		"pid": e.config["pid"], "type": paymentType,
		"out_trade_no": req.OrderID, "notify_url": notifyURL,
		"return_url": returnURL, "name": req.Subject,
		"money": req.Amount, "clientip": req.ClientIP,
	}
	if cid := e.resolveCID(paymentType); cid != "" {
		params["cid"] = cid
	}
	if req.IsMobile {
		params["device"] = deviceMobile
	}
	params["sign"] = easyPaySign(params, e.config["pkey"])
	params["sign_type"] = signTypeMD5

	body, err := e.post(ctx, e.apiBase()+"/mapi.php", params)
	if err != nil {
		return nil, fmt.Errorf("easypay create: %w", err)
	}
	var resp struct {
		Code    int    `json:"code"`
		Msg     string `json:"msg"`
		TradeNo string `json:"trade_no"`
		PayURL  string `json:"payurl"`
		PayURL2 string `json:"payurl2"` // H5 mobile payment URL
		QRCode  string `json:"qrcode"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("easypay parse: %w", err)
	}
	if resp.Code != easypayCodeSuccess {
		return nil, fmt.Errorf("easypay error: %s", resp.Msg)
	}
	payURL := resp.PayURL
	if req.IsMobile && resp.PayURL2 != "" {
		payURL = resp.PayURL2
	}
	return &payment.CreatePaymentResponse{TradeNo: resp.TradeNo, PayURL: payURL, QRCode: resp.QRCode}, nil
}

// resolveURLs returns (notifyURL, returnURL) preferring request values,
// falling back to instance config.
func (e *EasyPay) resolveURLs(req payment.CreatePaymentRequest) (string, string) {
	notifyURL := req.NotifyURL
	if notifyURL == "" {
		notifyURL = e.config["notifyUrl"]
	}
	returnURL := req.ReturnURL
	if returnURL == "" {
		returnURL = e.config["returnUrl"]
	}
	return notifyURL, returnURL
}

func (e *EasyPay) customMethods() []easyPayCustomMethod {
	if e == nil {
		return nil
	}
	raw := strings.TrimSpace(e.config["customMethods"])
	if raw == "" {
		return nil
	}
	var methods []easyPayCustomMethod
	if err := json.Unmarshal([]byte(raw), &methods); err != nil {
		return nil
	}
	result := make([]easyPayCustomMethod, 0, len(methods))
	for _, method := range methods {
		method.Type = strings.TrimSpace(method.Type)
		method.UpstreamType = strings.TrimSpace(method.UpstreamType)
		method.DisplayName = strings.TrimSpace(method.DisplayName)
		if method.Type == "" || method.UpstreamType == "" {
			continue
		}
		result = append(result, method)
	}
	return result
}

func (e *EasyPay) upstreamPaymentType(paymentType string) string {
	paymentType = strings.TrimSpace(paymentType)
	for _, method := range e.customMethods() {
		if paymentType == method.Type {
			return method.UpstreamType
		}
	}
	return paymentType
}

func (e *EasyPay) QueryOrder(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	var v2Err error
	if e.shouldUseV2Query() {
		resp, err := e.queryOrderV2(ctx, tradeNo)
		if err == nil {
			return resp, nil
		}
		if isEasyPayNoFallbackQueryError(err) {
			return nil, err
		}
		v2Err = err
	}
	resp, err := e.queryOrderLegacy(ctx, tradeNo)
	if err != nil && v2Err != nil {
		return nil, fmt.Errorf("easypay query: v2 failed: %v; legacy failed: %w", v2Err, err)
	}
	return resp, err
}

func (e *EasyPay) queryOrderLegacy(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	params := map[string]string{
		"act": "order", "pid": e.config["pid"],
		"key": e.config["pkey"], "out_trade_no": tradeNo,
	}
	endpoint := e.apiBase() + "/api.php"
	body, err := e.get(ctx, endpoint, params)
	var getResp *payment.QueryOrderResponse
	if err == nil {
		resp, parseErr := parseEasyPayQueryOrderResponse(body, tradeNo, e.MerchantIdentityMetadata())
		if parseErr == nil {
			if resp.Status == payment.ProviderStatusPaid || easyPayQueryResponseCodeAllowsStatusFromBody(body) {
				return resp, nil
			}
			getResp = resp
			// A few EasyPay-compatible gateways document GET but return a
			// generic JSON failure for GET while POST still works. Fall back to
			// POST on a non-success GET response; if POST is also unavailable,
			// keep the safe pending result from GET.
			err = fmt.Errorf("non-success response")
		} else {
			err = parseErr
		}
	}
	// Some EasyPay-compatible gateways accept only form POST despite
	// documenting api.php?act=order as a URL query. Keep a compatibility
	// fallback for those variants while preferring the documented GET shape.
	firstErr := err
	body, err = e.post(ctx, endpoint, params)
	if err != nil {
		if getResp != nil {
			return getResp, nil
		}
		if firstErr != nil {
			return nil, fmt.Errorf("easypay query: GET failed: %v; POST failed: %w", firstErr, err)
		}
		return nil, fmt.Errorf("easypay query: %w", err)
	}
	resp, err := parseEasyPayQueryOrderResponse(body, tradeNo, e.MerchantIdentityMetadata())
	if err != nil {
		if getResp != nil {
			return getResp, nil
		}
		if firstErr != nil {
			return nil, fmt.Errorf("easypay parse query: GET failed: %v; POST parse failed: %w", firstErr, err)
		}
		return nil, fmt.Errorf("easypay parse query: %w", err)
	}
	return resp, nil
}

func (e *EasyPay) shouldUseV2Query() bool {
	if e == nil {
		return false
	}
	queryAPI := strings.ToLower(strings.TrimSpace(e.config["queryApiVersion"]))
	if queryAPI == "legacy" || queryAPI == "v1" {
		return false
	}
	if queryAPI != "" {
		return true
	}
	for _, key := range []string{"queryEndpoint", "queryApiPath", "querySignType", "queryPrivateKey", "queryPublicKey", "rsaPrivateKey"} {
		if strings.TrimSpace(e.config[key]) != "" {
			return true
		}
	}
	return false
}

func (e *EasyPay) queryOrderV2(ctx context.Context, tradeNo string) (*payment.QueryOrderResponse, error) {
	params, err := e.buildV2QueryParams(tradeNo, time.Now())
	if err != nil {
		return nil, newEasyPayNoFallbackQueryError(err)
	}
	body, status, err := e.postRaw(ctx, e.v2QueryEndpoint(), params)
	if err != nil {
		return nil, fmt.Errorf("v2 pay query request: %w", err)
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("v2 pay query HTTP %d: %s", status, summarizeEasyPayResponse(body))
	}
	resp, successCode, err := parseEasyPayV2QueryOrderResponse(body, tradeNo, e.MerchantIdentityMetadata())
	if err != nil {
		if successCode {
			return nil, newEasyPayNoFallbackQueryError(err)
		}
		return nil, err
	}
	if !successCode {
		return nil, fmt.Errorf("v2 pay query non-success response: %s", summarizeEasyPayResponse(body))
	}
	if err := e.verifyV2QueryResponseSignature(body); err != nil {
		return nil, newEasyPayNoFallbackQueryError(err)
	}
	return resp, nil
}

func (e *EasyPay) v2QueryEndpoint() string {
	if e == nil {
		return ""
	}
	if endpoint := strings.TrimSpace(e.config["queryEndpoint"]); endpoint != "" {
		return endpoint
	}
	path := strings.TrimSpace(e.config["queryApiPath"])
	if path == "" {
		path = easypayV2QueryPath
	}
	if strings.HasPrefix(strings.ToLower(path), "http://") || strings.HasPrefix(strings.ToLower(path), "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return e.apiBase() + path
}

func (e *EasyPay) buildV2QueryParams(tradeNo string, now time.Time) (map[string]string, error) {
	signType := e.querySignType()
	params := map[string]string{
		"pid":          e.config["pid"],
		"out_trade_no": tradeNo,
		"timestamp":    strconv.FormatInt(now.Unix(), 10),
		"sign_type":    signType,
	}
	sign, err := e.signV2Params(params, signType)
	if err != nil {
		return nil, err
	}
	params["sign"] = sign
	return params, nil
}

func (e *EasyPay) querySignType() string {
	for _, key := range []string{"querySignType", "signType"} {
		if value := normalizeEasyPaySignType(e.config[key]); value != "" {
			return value
		}
	}
	if e.queryPrivateKey() != "" {
		return signTypeRSA
	}
	return signTypeMD5
}

func normalizeEasyPaySignType(signType string) string {
	switch strings.ToUpper(strings.TrimSpace(signType)) {
	case signTypeMD5:
		return signTypeMD5
	case signTypeRSA:
		return signTypeRSA
	case signTypeRSA2:
		return signTypeRSA2
	default:
		return ""
	}
}

func (e *EasyPay) signV2Params(params map[string]string, signType string) (string, error) {
	switch signType {
	case signTypeMD5:
		return easyPaySign(params, e.config["pkey"]), nil
	case signTypeRSA, signTypeRSA2:
		privateKeyPEM := e.queryPrivateKey()
		if privateKeyPEM == "" {
			return "", fmt.Errorf("easypay %s query signing requires queryPrivateKey", signType)
		}
		privateKey, err := parseEasyPayRSAPrivateKey(privateKeyPEM)
		if err != nil {
			return "", fmt.Errorf("parse easypay query private key: %w", err)
		}
		return signEasyPayRSA(easyPayCanonicalString(params), privateKey, signType)
	default:
		return "", fmt.Errorf("unsupported easypay query sign_type: %s", signType)
	}
}

func (e *EasyPay) queryPrivateKey() string {
	if e == nil {
		return ""
	}
	for _, key := range []string{"queryPrivateKey", "rsaPrivateKey", "privateKey"} {
		if value := strings.TrimSpace(e.config[key]); value != "" {
			return value
		}
	}
	pkey := strings.TrimSpace(e.config["pkey"])
	if strings.Contains(pkey, "PRIVATE KEY") {
		return pkey
	}
	return ""
}

func (e *EasyPay) verifyV2QueryResponseSignature(body []byte) error {
	publicKeyPEM := strings.TrimSpace(e.config["queryPublicKey"])
	if publicKeyPEM == "" {
		return nil
	}
	publicKey, err := parseEasyPayRSAPublicKey(publicKeyPEM)
	if err != nil {
		return fmt.Errorf("parse easypay query public key: %w", err)
	}
	params, sign, signType, err := easyPayResponseSignatureParams(body)
	if err != nil {
		return err
	}
	return verifyEasyPayRSA(easyPayCanonicalString(params), sign, publicKey, signType)
}

type easyPayNoFallbackQueryError struct {
	err error
}

func (e easyPayNoFallbackQueryError) Error() string {
	return e.err.Error()
}

func (e easyPayNoFallbackQueryError) Unwrap() error {
	return e.err
}

func newEasyPayNoFallbackQueryError(err error) error {
	if err == nil {
		return nil
	}
	return easyPayNoFallbackQueryError{err: err}
}

func isEasyPayNoFallbackQueryError(err error) bool {
	var noFallback easyPayNoFallbackQueryError
	return errors.As(err, &noFallback)
}

func easyPayQueryResponseCodeAllowsStatusFromBody(body []byte) bool {
	var resp struct {
		Code any `json:"code"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	return easyPayQueryResponseCodeAllowsStatus(resp.Code)
}

func parseEasyPayQueryOrderResponse(body []byte, tradeNo string, metadata map[string]string) (*payment.QueryOrderResponse, error) {
	type easyPayQueryData struct {
		TradeStatus any `json:"trade_status"`
		Status      any `json:"status"`
		Money       any `json:"money"`
		TradeNo     any `json:"trade_no"`
		OutTradeNo  any `json:"out_trade_no"`
	}
	var resp struct {
		Code        any              `json:"code"`
		Msg         string           `json:"msg"`
		TradeStatus any              `json:"trade_status"`
		Status      any              `json:"status"`
		Money       any              `json:"money"`
		TradeNo     any              `json:"trade_no"`
		OutTradeNo  any              `json:"out_trade_no"`
		Data        easyPayQueryData `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if err := validateEasyPayReturnedOutTradeNo(tradeNo, resp.OutTradeNo, resp.Data.OutTradeNo); err != nil {
		return nil, err
	}
	status := payment.ProviderStatusPending
	if easyPayQueryResponseCodeAllowsStatus(resp.Code) {
		if tradeStatus, ok := easyPayAnyString(resp.TradeStatus); ok {
			if easyPayTradeStatusIsPaid(tradeStatus) {
				status = payment.ProviderStatusPaid
			}
		} else if tradeStatus, ok := easyPayAnyString(resp.Data.TradeStatus); ok {
			if easyPayTradeStatusIsPaid(tradeStatus) {
				status = payment.ProviderStatusPaid
			}
		} else if numericStatus, ok := easyPayAnyInt(resp.Status); ok {
			if numericStatus == easypayStatusPaid {
				status = payment.ProviderStatusPaid
			}
		} else if numericStatus, ok := easyPayAnyInt(resp.Data.Status); ok && numericStatus == easypayStatusPaid {
			status = payment.ProviderStatusPaid
		}
	}

	money := ""
	if value, ok := easyPayAnyString(resp.Money); ok {
		money = value
	} else if value, ok := easyPayAnyString(resp.Data.Money); ok {
		money = value
	}
	responseTradeNo := tradeNo
	if value, ok := easyPayAnyString(resp.TradeNo); ok && strings.TrimSpace(value) != "" {
		responseTradeNo = strings.TrimSpace(value)
	} else if value, ok := easyPayAnyString(resp.Data.TradeNo); ok && strings.TrimSpace(value) != "" {
		responseTradeNo = strings.TrimSpace(value)
	}

	amount, _ := strconv.ParseFloat(money, 64)
	return &payment.QueryOrderResponse{
		TradeNo:  responseTradeNo,
		Status:   status,
		Amount:   amount,
		Metadata: metadata,
	}, nil
}

func parseEasyPayV2QueryOrderResponse(body []byte, tradeNo string, metadata map[string]string) (*payment.QueryOrderResponse, bool, error) {
	var resp struct {
		Code       any    `json:"code"`
		Msg        string `json:"msg"`
		TradeNo    any    `json:"trade_no"`
		OutTradeNo any    `json:"out_trade_no"`
		APITradeNo any    `json:"api_trade_no"`
		Type       any    `json:"type"`
		Status     any    `json:"status"`
		PID        any    `json:"pid"`
		EndTime    any    `json:"endtime"`
		Money      any    `json:"money"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, false, err
	}
	successCode := easyPayV2ResponseCodeIsSuccess(resp.Code)
	if !successCode {
		return &payment.QueryOrderResponse{
			TradeNo:  tradeNo,
			Status:   payment.ProviderStatusPending,
			Metadata: withEasyPayPIDMetadata(metadata, resp.PID),
		}, false, nil
	}
	if err := validateEasyPayReturnedOutTradeNo(tradeNo, resp.OutTradeNo); err != nil {
		return nil, true, err
	}

	status := payment.ProviderStatusPending
	if numericStatus, ok := easyPayAnyInt(resp.Status); ok {
		switch numericStatus {
		case easypayStatusPaid:
			status = payment.ProviderStatusPaid
		case easypayStatusRefunded:
			status = payment.ProviderStatusRefunded
		}
	}

	money, _ := easyPayAnyString(resp.Money)
	amount, _ := strconv.ParseFloat(strings.TrimSpace(money), 64)
	responseTradeNo := tradeNo
	for _, candidate := range []any{resp.APITradeNo, resp.TradeNo} {
		if value, ok := easyPayAnyString(candidate); ok && strings.TrimSpace(value) != "" {
			responseTradeNo = strings.TrimSpace(value)
			break
		}
	}

	return &payment.QueryOrderResponse{
		TradeNo:  responseTradeNo,
		Status:   status,
		Amount:   amount,
		PaidAt:   easyPayAnyStringOrEmpty(resp.EndTime),
		Metadata: withEasyPayPIDMetadata(metadata, resp.PID),
	}, true, nil
}

func validateEasyPayReturnedOutTradeNo(expected string, values ...any) error {
	expected = strings.TrimSpace(expected)
	for _, value := range values {
		actual, ok := easyPayAnyString(value)
		if !ok {
			continue
		}
		actual = strings.TrimSpace(actual)
		if actual == "" {
			continue
		}
		if expected != "" && actual != expected {
			return fmt.Errorf("easypay query out_trade_no mismatch: expected %s, got %s", expected, actual)
		}
	}
	return nil
}

func withEasyPayPIDMetadata(metadata map[string]string, pid any) map[string]string {
	value, ok := easyPayAnyString(pid)
	if !ok || strings.TrimSpace(value) == "" {
		return metadata
	}
	out := cloneStringMap(metadata)
	out["pid"] = strings.TrimSpace(value)
	return out
}

func easyPayAnyStringOrEmpty(value any) string {
	if s, ok := easyPayAnyString(value); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func easyPayQueryResponseCodeAllowsStatus(code any) bool {
	if code == nil {
		return true
	}
	return easyPayResponseCodeIsSuccess(code)
}

func easyPayV2ResponseCodeIsSuccess(code any) bool {
	switch v := code.(type) {
	case float64:
		return int(v) == easypayV2CodeSuccess
	case int:
		return v == easypayV2CodeSuccess
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return err == nil && n == easypayV2CodeSuccess
	case json.Number:
		n, err := strconv.Atoi(strings.TrimSpace(v.String()))
		return err == nil && n == easypayV2CodeSuccess
	default:
		return false
	}
}

func easyPayTradeStatusIsPaid(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), tradeStatusSuccess)
}

func easyPayAnyString(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case int:
		return strconv.Itoa(v), true
	case json.Number:
		return v.String(), true
	default:
		return fmt.Sprint(v), true
	}
}

func easyPayAnyInt(value any) (int, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case int:
		return v, true
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	case json.Number:
		n, err := strconv.Atoi(strings.TrimSpace(v.String()))
		return n, err == nil
	default:
		return 0, false
	}
}

func (e *EasyPay) VerifyNotification(_ context.Context, rawBody string, _ map[string]string) (*payment.PaymentNotification, error) {
	values, err := url.ParseQuery(rawBody)
	if err != nil {
		return nil, fmt.Errorf("parse notify: %w", err)
	}
	// url.ParseQuery already decodes values — no additional decode needed.
	params := make(map[string]string)
	for k := range values {
		params[k] = values.Get(k)
	}
	sign := params["sign"]
	if sign == "" {
		return nil, fmt.Errorf("missing sign")
	}
	if !easyPayVerifySign(params, e.config["pkey"], sign) {
		return nil, fmt.Errorf("invalid signature")
	}
	status := payment.ProviderStatusFailed
	if params["trade_status"] == tradeStatusSuccess {
		status = payment.ProviderStatusSuccess
	}
	amount, _ := strconv.ParseFloat(params["money"], 64)

	metadata := e.MerchantIdentityMetadata()
	if pid := strings.TrimSpace(params["pid"]); pid != "" {
		if metadata == nil {
			metadata = map[string]string{}
		}
		metadata["pid"] = pid
	}
	return &payment.PaymentNotification{
		TradeNo: params["trade_no"], OrderID: params["out_trade_no"],
		Amount: amount, Status: status, RawData: rawBody, Metadata: metadata,
	}, nil
}

func (e *EasyPay) Refund(ctx context.Context, req payment.RefundRequest) (*payment.RefundResponse, error) {
	attempts := e.refundAttempts(req)
	if len(attempts) == 0 {
		return nil, fmt.Errorf("easypay refund missing order identifier")
	}
	var firstErr error
	for i, attempt := range attempts {
		body, status, err := e.postRaw(ctx, e.apiBase()+"/api.php?act=refund", attempt.params)
		if err != nil {
			return nil, fmt.Errorf("easypay refund request: %w", err)
		}
		if err := parseEasyPayRefundResponse(status, body); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if i+1 < len(attempts) && isEasyPayRefundOrderNotFound(err) {
				continue
			}
			return nil, err
		}
		return &payment.RefundResponse{RefundID: attempt.refundID, Status: payment.ProviderStatusSuccess}, nil
	}
	return nil, firstErr
}

type easyPayRefundAttempt struct {
	params   map[string]string
	refundID string
}

func (e *EasyPay) refundAttempts(req payment.RefundRequest) []easyPayRefundAttempt {
	base := map[string]string{
		"pid": e.config["pid"], "key": e.config["pkey"], "money": req.Amount,
	}
	var attempts []easyPayRefundAttempt
	if orderID := strings.TrimSpace(req.OrderID); orderID != "" {
		params := cloneStringMap(base)
		params["out_trade_no"] = orderID
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: orderID})
	}
	if tradeNo := strings.TrimSpace(req.TradeNo); tradeNo != "" {
		params := cloneStringMap(base)
		params["trade_no"] = tradeNo
		attempts = append(attempts, easyPayRefundAttempt{params: params, refundID: tradeNo})
	}
	return attempts
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func isEasyPayRefundOrderNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	return strings.Contains(msg, "订单编号不存在") ||
		strings.Contains(msg, "订单不存在") ||
		strings.Contains(lower, "order not found") ||
		strings.Contains(lower, "not exist")
}

func parseEasyPayRefundResponse(status int, body []byte) error {
	summary := summarizeEasyPayResponse(body)
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return fmt.Errorf("easypay refund HTTP %d: %s", status, summary)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("easypay refund empty response (HTTP %d): %s", status, summary)
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "<!doctype html") || strings.HasPrefix(lower, "<html") ||
		(strings.HasPrefix(lower, "<") && strings.Contains(lower, "html")) {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}

	var resp struct {
		Code any    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("easypay refund non-JSON response (HTTP %d): %s", status, summary)
	}
	if !easyPayResponseCodeIsSuccess(resp.Code) {
		msg := strings.TrimSpace(resp.Msg)
		if msg == "" {
			msg = summary
		}
		return fmt.Errorf("easypay refund failed (HTTP %d): %s", status, msg)
	}
	return nil
}

func easyPayResponseCodeIsSuccess(code any) bool {
	switch v := code.(type) {
	case float64:
		return int(v) == easypayCodeSuccess
	case int:
		return v == easypayCodeSuccess
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return err == nil && n == easypayCodeSuccess
	case json.Number:
		n, err := strconv.Atoi(strings.TrimSpace(v.String()))
		return err == nil && n == easypayCodeSuccess
	default:
		return false
	}
}

func summarizeEasyPayResponse(body []byte) string {
	summary := strings.Join(strings.Fields(string(body)), " ")
	if summary == "" {
		return "<empty>"
	}
	if len(summary) > maxEasypayErrorSummary {
		truncated := summary[:maxEasypayErrorSummary]
		for len(truncated) > 0 && !utf8.ValidString(truncated) {
			truncated = truncated[:len(truncated)-1]
		}
		return truncated + "..."
	}
	return summary
}

func (e *EasyPay) resolveCID(paymentType string) string {
	if strings.HasPrefix(paymentType, "alipay") {
		if v := e.config["cidAlipay"]; v != "" {
			return v
		}
		return e.config["cid"]
	}
	if v := e.config["cidWxpay"]; v != "" {
		return v
	}
	return e.config["cid"]
}

func (e *EasyPay) post(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	body, _, err := e.postRaw(ctx, endpoint, params)
	return body, err
}

func (e *EasyPay) get(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	body, _, err := e.getRaw(ctx, endpoint, params)
	return body, err
}

func (e *EasyPay) postRaw(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := e.httpClient
	if client == nil {
		client = &http.Client{Timeout: easypayHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEasypayResponseSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (e *EasyPay) getRaw(ctx context.Context, endpoint string, params map[string]string) ([]byte, int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, 0, err
	}
	query := parsed.Query()
	for k, v := range params {
		query.Set(k, v)
	}
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	client := e.httpClient
	if client == nil {
		client = &http.Client{Timeout: easypayHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEasypayResponseSize))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func easyPayCanonicalString(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k, v := range params {
		if k == "sign" || k == "sign_type" || v == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf strings.Builder
	for i, k := range keys {
		if i > 0 {
			_ = buf.WriteByte('&')
		}
		_, _ = buf.WriteString(k + "=" + params[k])
	}
	return buf.String()
}

func easyPaySign(params map[string]string, pkey string) string {
	payload := easyPayCanonicalString(params) + pkey
	hash := md5.Sum([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func signEasyPayRSA(payload string, privateKey *rsa.PrivateKey, signType string) (string, error) {
	hash, cryptoHash := hashEasyPayRSAPayload(payload, signType)
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, cryptoHash, hash)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

func verifyEasyPayRSA(payload, signature string, publicKey *rsa.PublicKey, signType string) error {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return fmt.Errorf("decode RSA signature: %w", err)
	}
	hash, cryptoHash := hashEasyPayRSAPayload(payload, signType)
	if err := rsa.VerifyPKCS1v15(publicKey, cryptoHash, hash, decoded); err != nil {
		return fmt.Errorf("invalid RSA signature: %w", err)
	}
	return nil
}

func hashEasyPayRSAPayload(payload string, signType string) ([]byte, crypto.Hash) {
	switch normalizeEasyPaySignType(signType) {
	case signTypeRSA2:
		sum := sha256.Sum256([]byte(payload))
		return sum[:], crypto.SHA256
	default:
		sum := sha1.Sum([]byte(payload))
		return sum[:], crypto.SHA1
	}
}

func parseEasyPayRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	der, err := decodeEasyPayPEMOrDER(raw, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is %T, want *rsa.PrivateKey", parsed)
	}
	return key, nil
}

func parseEasyPayRSAPublicKey(raw string) (*rsa.PublicKey, error) {
	der, err := decodeEasyPayPEMOrDER(raw, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS1PublicKey(der); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		if cert, certErr := x509.ParseCertificate(der); certErr == nil {
			if key, ok := cert.PublicKey.(*rsa.PublicKey); ok {
				return key, nil
			}
		}
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is %T, want *rsa.PublicKey", parsed)
	}
	return key, nil
}

func decodeEasyPayPEMOrDER(raw string, blockHint string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty %s", strings.ToLower(blockHint))
	}
	if block, _ := pem.Decode([]byte(trimmed)); block != nil {
		return block.Bytes, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(stripEasyPayKeyWhitespace(trimmed))
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", strings.ToLower(blockHint), err)
	}
	return decoded, nil
}

func stripEasyPayKeyWhitespace(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func easyPayResponseSignatureParams(body []byte) (map[string]string, string, string, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", "", err
	}
	params := make(map[string]string, len(raw))
	for key, value := range raw {
		if s, ok := easyPayAnyString(value); ok {
			params[key] = strings.TrimSpace(s)
		}
	}
	sign := strings.TrimSpace(params["sign"])
	if sign == "" {
		return nil, "", "", fmt.Errorf("easypay query response missing sign")
	}
	signType := normalizeEasyPaySignType(params["sign_type"])
	if signType == "" {
		signType = signTypeRSA
	}
	if signType != signTypeRSA && signType != signTypeRSA2 {
		return nil, "", "", fmt.Errorf("unsupported easypay query response sign_type: %s", params["sign_type"])
	}
	return params, sign, signType, nil
}

func easyPayVerifySign(params map[string]string, pkey string, sign string) bool {
	return hmac.Equal([]byte(easyPaySign(params, pkey)), []byte(sign))
}
