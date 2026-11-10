//go:build unit

package service

import (
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/stretchr/testify/require"
)

func TestEasyPayUSDOrderCurrencySnapshotAndBonus(t *testing.T) {
	sel := &payment.InstanceSelection{InstanceID: "6", ProviderKey: payment.TypeEasyPay, Config: map[string]string{"currency": "usd", "pid": "merchant"}}
	require.Equal(t, "USD", paymentProviderConfigCurrency(sel.ProviderKey, sel.Config))
	snapshot := buildPaymentOrderProviderSnapshot(sel, CreateOrderRequest{PaymentType: "usdt_trc20"})
	require.Equal(t, "USD", snapshot["currency"])
	order := &dbent.PaymentOrder{ProviderSnapshot: snapshot}
	sel.Config["currency"] = "CNY"
	require.Equal(t, "USD", PaymentOrderCurrency(order), "existing order retains the currency selected at creation")
	require.Equal(t, "CNY", PaymentOrderCurrency(&dbent.PaymentOrder{}), "historical orders without currency retain the old meaning")

	for _, multiplier := range []float64{1, 6.67} {
		cfg := &PaymentConfig{BalanceRechargeMultiplier: multiplier, RechargeBonusMode: RechargeBonusModeBonus,
			RechargeBonusTiers: []RechargeBonusTier{{MinAmount: 0, BonusPercent: 1}}}
		quote := quoteRechargeBonus(cfg, 100, "USD")
		require.Equal(t, 100.0, quote.PayBase)
		require.InDelta(t, multiplier, quote.Bonus, 1e-9)
		require.InDelta(t, 101*multiplier, quote.Credited, 1e-9)
	}
}

func TestEasyPayCurrencyCannotChangeWithPendingOrders(t *testing.T) {
	require.True(t, hasPendingOrderProtectedConfigChange(payment.TypeEasyPay, map[string]string{}, map[string]string{"currency": "USD"}))
	require.False(t, hasPendingOrderProtectedConfigChange(payment.TypeEasyPay, map[string]string{}, map[string]string{"currency": "CNY"}))
	require.False(t, hasPendingOrderProtectedConfigChange(payment.TypeEasyPay, map[string]string{"currency": "usd"}, map[string]string{"currency": "USD"}))
}
