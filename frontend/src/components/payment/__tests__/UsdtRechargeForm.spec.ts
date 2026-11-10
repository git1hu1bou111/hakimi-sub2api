import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UsdtRechargeForm from '../UsdtRechargeForm.vue'
import { paymentAPI } from '@/api/payment'
vi.mock('@/api/payment', () => ({ paymentAPI: { getUsdtQuote: vi.fn() } }))
const quote = {
  amount: '10', rate: '6.73', base_credit: '67.3', bonus_credit: '0.673', total_credit: '67.973',
  source: 'Coinbase USDT/CNY', quoted_at: '2026-11-10T07:00:00Z', expires_at: '2026-11-10T07:05:00Z', token: 'signed-quote',
}
const methods = {
  usdt_bep20: { usdt_recharge: true, display_name: 'USDT-BEP20', available: true, single_min: 3, single_max: 0, fee_rate: 0, daily_limit: 0, daily_used: 0, daily_remaining: 0 },
  usdt_trc20: { usdt_recharge: true, display_name: 'USDT-TRC20', available: true, single_min: 3, single_max: 0, fee_rate: 0, daily_limit: 0, daily_used: 0, daily_remaining: 0 },
}
let wrappers: ReturnType<typeof mount>[] = []
function render() { const wrapper = mount(UsdtRechargeForm, { props: { methods, busy: false } }); wrappers.push(wrapper); return wrapper }
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-11-10T07:00:00Z')); vi.mocked(paymentAPI.getUsdtQuote).mockReset(); vi.mocked(paymentAPI.getUsdtQuote).mockResolvedValue({ data: quote } as never) })
afterEach(() => { wrappers.forEach(w => w.unmount()); wrappers = []; vi.useRealTimers() })
describe('USDT recharge', () => {
  it('keeps the exact gift and submits the server quote', async () => {
    const w = render(); await vi.advanceTimersByTimeAsync(350); await flushPromises()
    expect(w.get('[data-testid="usdt-bonus"]').text()).toBe('+0.673')
    expect(w.get('[data-testid="usdt-total"]').text()).toBe('67.973')
    await w.get('button.btn-primary').trigger('click')
    expect(w.emitted('submit')?.[0]).toEqual([10, 'usdt_bep20', 'signed-quote'])
  })
  it('invalidates the old quote immediately when switching networks', async () => {
    const w = render(); await vi.advanceTimersByTimeAsync(350); await flushPromises()
    await w.findAll('button').find(b => b.text().includes('USDT-TRC20'))!.trigger('click')
    expect(w.get('button.btn-primary').attributes('disabled')).toBeDefined()
    await vi.advanceTimersByTimeAsync(350); await flushPromises()
    expect(paymentAPI.getUsdtQuote).toHaveBeenLastCalledWith({ amount: 10, payment_type: 'usdt_trc20' })
  })
  it('does not allow expired quotes or invalid input to create orders', async () => {
    const w = render(); await vi.advanceTimersByTimeAsync(350); await flushPromises()
    await vi.advanceTimersByTimeAsync(300000)
    expect(w.get('button.btn-primary').attributes('disabled')).toBeDefined()
    await w.get('input').setValue('10.001'); await vi.advanceTimersByTimeAsync(350)
    expect(w.text()).toContain('最多两位小数')
    expect(w.emitted('submit')).toBeUndefined()
  })
  it('fails closed when the exchange quote is unavailable', async () => {
    vi.mocked(paymentAPI.getUsdtQuote).mockRejectedValue(new Error('offline'))
    const w = render(); await vi.advanceTimersByTimeAsync(350); await flushPromises()
    expect(w.get('[role="alert"]').text()).toContain('暂时无法获取报价')
    expect(w.get('button.btn-primary').attributes('disabled')).toBeDefined()
  })
})
