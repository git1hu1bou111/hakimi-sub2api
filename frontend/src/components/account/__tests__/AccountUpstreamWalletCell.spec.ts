import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { UpstreamWalletSnapshot } from '@/types'
import AccountUpstreamWalletCell from '../AccountUpstreamWalletCell.vue'
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: { time: string }) => [key, params?.time].join(' '), locale: { value: 'en' } })
}))
vi.mock('@/utils/format', () => ({ formatDateTimeToMinute: (time: string) => time }))

function render(wallet?: UpstreamWalletSnapshot) {
  return mount(AccountUpstreamWalletCell, { props: { wallet } })
}

describe('AccountUpstreamWalletCell', () => {
  it('distinguishes pending and unsupported balances', () => {
    expect(render().text()).toContain('admin.accounts.upstreamWallet.pending')
    expect(render({ status: 'unsupported' }).text()).toContain('admin.accounts.upstreamWallet.unsupported')
  })

  it('displays both zero and negative balances with explicit currencies', () => {
    const text = render({ status: 'ok', balances: [{ amount: 0, currency: 'USD' }, { amount: -12.5, currency: 'CNY' }] }).text()
    expect(text).toContain('0 USD')
    expect(text).toContain('-12.5 CNY')
  })

  it('displays the previous successful amount and timestamp when a refresh fails', () => {
    const text = render({ status: 'error', balances: [{ amount: 9, currency: 'USD' }], updated_at: '2026-11-18T02:00:00Z', checked_at: '2026-11-18T03:00:00Z' }).text()
    expect(text).toContain('9 USD')
    expect(text).toContain('admin.accounts.upstreamWallet.error')
    expect(text).toContain('2026-11-18T02:00:00Z')
    expect(text).toContain('2026-11-18T03:00:00Z')
  })
})
