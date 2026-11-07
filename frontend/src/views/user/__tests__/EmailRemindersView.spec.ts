import { reactive } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import EmailRemindersView from '@/views/user/EmailRemindersView.vue'
import type { NotifyEmailEntry } from '@/types'

const { fetchPublicSettingsMock, refreshUserMock } = vi.hoisted(() => ({
  fetchPublicSettingsMock: vi.fn(),
  refreshUserMock: vi.fn()
}))

let authState: ReturnType<typeof makeAuthState>
function makeAuthState() {
  return reactive({
    user: {
      id: 7,
      email: 'test@example.com',
      balance_notify_enabled: true,
      balance_notify_threshold: null as number | null,
      balance_notify_extra_emails: [] as NotifyEmailEntry[]
    },
    refreshUser: refreshUserMock
  })
}

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ fetchPublicSettings: fetchPublicSettingsMock })
}))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))

enableAutoUnmount(afterEach)

const render = () => mount(EmailRemindersView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      ProfileBalanceNotifyCard: {
        name: 'ProfileBalanceNotifyCard',
        props: ['enabled', 'threshold', 'extraEmails', 'systemDefaultThreshold', 'userEmail'],
        template: '<div data-testid="balance-form" />'
      }
    }
  }
})

describe('EmailRemindersView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    authState = makeAuthState()
    refreshUserMock.mockResolvedValue(authState.user)
    fetchPublicSettingsMock.mockResolvedValue({
      balance_low_notify_enabled: true,
      balance_low_notify_threshold: 50
    })
  })

  it('loads the existing account settings and distinguishes an enabled switch from a configured recipient', async () => {
    const wrapper = render()
    expect(wrapper.find('[data-testid="balance-form"]').exists()).toBe(false)
    await flushPromises()

    expect(refreshUserMock).toHaveBeenCalledOnce()
    expect(fetchPublicSettingsMock).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-testid="reminder-status"]').text()).toContain('emailReminders.statusNeedsEmail')
    expect(wrapper.getComponent({ name: 'ProfileBalanceNotifyCard' }).props()).toEqual({
      enabled: true,
      threshold: null,
      extraEmails: [],
      systemDefaultThreshold: 50,
      userEmail: 'test@example.com'
    })
  })

  it('updates the status when verification, recipient toggles, or the reminder switch change', async () => {
    const wrapper = render()
    await flushPromises()
    authState.user.balance_notify_extra_emails = [{ email: 'test@example.com', verified: true, disabled: true }]
    await flushPromises()
    expect(wrapper.get('[data-testid="reminder-status"]').text()).toContain('emailReminders.statusNeedsEmail')

    authState.user.balance_notify_extra_emails[0]!.disabled = false
    await flushPromises()
    expect(wrapper.get('[data-testid="reminder-status"]').text()).toContain('emailReminders.statusReady')

    authState.user.balance_notify_enabled = false
    await flushPromises()
    expect(wrapper.get('[data-testid="reminder-status"]').text()).toContain('emailReminders.statusPaused')
  })

  it('keeps the site switch authoritative without discarding saved user settings', async () => {
    fetchPublicSettingsMock.mockResolvedValue({ balance_low_notify_enabled: false, balance_low_notify_threshold: 50 })
    authState.user.balance_notify_threshold = 8
    const wrapper = render()
    await flushPromises()

    expect(wrapper.find('[data-testid="balance-form"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="reminders-unavailable"]').exists()).toBe(true)
    expect(authState.user.balance_notify_threshold).toBe(8)
  })

  it('does not report a usable reminder when neither a custom nor a default threshold is set', async () => {
    authState.user.balance_notify_extra_emails = [{ email: 'test@example.com', verified: true, disabled: false }]
    fetchPublicSettingsMock.mockResolvedValue({ balance_low_notify_enabled: true, balance_low_notify_threshold: 0 })
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="reminder-status"]').text()).toContain('emailReminders.statusNeedsThreshold')
  })

  it('shows a retryable load error instead of exposing stale editable settings', async () => {
    refreshUserMock.mockRejectedValueOnce(new Error('network unavailable'))
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('emailReminders.loadFailed')
    expect(wrapper.find('[data-testid="balance-form"]').exists()).toBe(false)

    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="balance-form"]').exists()).toBe(true)
  })
})
