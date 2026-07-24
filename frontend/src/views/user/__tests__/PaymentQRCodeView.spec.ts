import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const routerPush = vi.hoisted(() => vi.fn())
const pollOrderStatus = vi.hoisted(() => vi.fn())
const verifyOrder = vi.hoisted(() => vi.fn())
const cancelOrder = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const toCanvas = vi.hoisted(() => vi.fn())
const canvasContext = vi.hoisted(() => ({
  beginPath: vi.fn(),
  moveTo: vi.fn(),
  arcTo: vi.fn(),
  fill: vi.fn(),
  drawImage: vi.fn(),
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: routerPush }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({
    pollOrderStatus,
  }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
  }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    verifyOrder,
    cancelOrder,
  },
}))

vi.mock('qrcode', () => ({
  default: {
    toCanvas,
  },
}))

import PaymentQRCodeView from '../PaymentQRCodeView.vue'

const orderFactory = (status: string) => ({
  id: 42,
  user_id: 9,
  amount: 1,
  pay_amount: 1,
  fee_rate: 0,
  payment_type: 'alipay',
  out_trade_no: 'sub2_qr_easypay_42',
  status,
  order_type: 'balance',
  created_at: '2026-07-25T01:55:00Z',
  expires_at: '2099-01-01T10:30:00Z',
  refund_amount: 0,
})

describe('PaymentQRCodeView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    routeState.query = {
      order_id: '42',
      qr: 'https://pay.example.com/qr/42',
      payment_type: 'alipay',
      out_trade_no: 'sub2_qr_easypay_42',
      expires_at: '2099-01-01T10:30:00Z',
    }
    routerPush.mockReset()
    pollOrderStatus.mockReset()
    verifyOrder.mockReset()
    cancelOrder.mockReset()
    showError.mockReset()
    toCanvas.mockReset().mockResolvedValue(undefined)
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(canvasContext as unknown as CanvasRenderingContext2D)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  it('actively verifies alipay QR orders and redirects to result when upstream is paid', async () => {
    pollOrderStatus.mockResolvedValue(orderFactory('PENDING'))
    verifyOrder.mockResolvedValue({ data: orderFactory('COMPLETED') })

    mount(PaymentQRCodeView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
        },
      },
    })

    await flushPromises()
    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()

    expect(pollOrderStatus).toHaveBeenCalledWith(42)
    expect(verifyOrder).toHaveBeenCalledWith('sub2_qr_easypay_42')
    expect(routerPush).toHaveBeenCalledWith({
      path: '/payment/result',
      query: {
        order_id: '42',
        out_trade_no: 'sub2_qr_easypay_42',
        status: 'success',
      },
    })
  })
})
